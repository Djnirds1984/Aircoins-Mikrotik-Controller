// Command aircoins-controller is the Phase 1 entry point: admin dashboard,
// router inventory, voucher engine and captive portal backed by SQLite.
//
// Configuration is environment based so it runs unchanged under systemd:
//
//	ADDR (default ":80"), DB_PATH, SECRET_KEY_PATH, AIRCOINS_SECRET_KEY,
//	PORTAL_NAME, DEFAULT_REDIRECT, API_TIMEOUT, SECURE_COOKIES, VERSION
package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base32"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
	"github.com/djnirds1984/aircoins-mikrotik-controller/handlers"
)

//go:embed templates/*.html
var templateFS embed.FS

// version is overridden at build time: go build -ldflags "-X main.version=1.0.0".
var version = "dev"

func main() {
	// --version is handled before run so install.sh can prove which binary was
	// installed without starting the server or opening the database.
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println(version)
		return
	}
	// passwd is the recovery path for an operator locked out of the panel. It
	// must work while the service is stopped, so it runs before the server and
	// before the account bootstrap (which would otherwise be a no-op anyway).
	if len(os.Args) > 1 && os.Args[1] == "passwd" {
		if err := runPasswd(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "aircoins-controller passwd:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "aircoins-controller:", err)
		os.Exit(1)
	}
}

// passwdUsage explains the subcommand, including the flags that keep the
// password off the process list.
const passwdUsage = `Set or reset the panel operator password.

Usage:
  aircoins-controller passwd                 generate a strong password
  aircoins-controller passwd <password>      set an explicit password

Options:
  --user <name>   also change the operator name (default: keep current)
  --show          print the current operator name and exit

Reading the password:
  * With no argument a strong password is generated and printed once.
  * Pass it as an argument only for scripting; it is visible in
    "ps" output for a moment. Prefer the generated form by hand.
  * ADMIN_PASSWORD in the environment is used when no argument is given,
    which keeps it out of the shell history and the process list.

Stop the service before running this, then start it again:
  systemctl stop aircoins && aircoins-controller passwd && systemctl start aircoins
`

// runPasswd implements the passwd subcommand.
func runPasswd(args []string) error {
	var (
		showUser    bool
		newUsername string
		password    string
		positional  []string
	)
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; arg {
		case "-h", "--help", "help":
			fmt.Print(passwdUsage)
			return nil
		case "--user", "-u":
			if i+1 >= len(args) {
				return errors.New("--user needs a value")
			}
			i++
			newUsername = args[i]
		case "--show":
			showUser = true
		default:
			if len(arg) > 1 && arg[0] == '-' {
				return fmt.Errorf("unknown option %q (try --help)", arg)
			}
			positional = append(positional, arg)
		}
	}
	if len(positional) > 1 {
		return errors.New("expected at most one password argument (try --help)")
	}
	if len(positional) == 1 {
		password = positional[0]
	}

	cfg, _, err := loadConfig()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := database.Open(ctx, cfg.DB)
	if err != nil {
		return err
	}
	defer db.Close()

	store := db.AdminUsers()
	user, err := store.Get(ctx)
	if err != nil {
		if !errors.Is(err, database.ErrNoAdminUser) {
			return err
		}
		// No account yet: create one so the operator is never left without a
		// way in.
		username := newUsername
		if username == "" {
			username = cfg.Handler.AdminUser
		}
		if password == "" {
			password = firstNonEmpty(os.Getenv("ADMIN_PASSWORD"), mustGeneratePassword())
		}
		if err := store.Create(ctx, username, password); err != nil {
			return err
		}
		fmt.Printf("Created operator %q.\n", database.NormalizeAdminUsername(username))
		fmt.Printf("Password: %s\n", password)
		fmt.Println("Sign in at the panel, then change it under Settings.")
		return nil
	}

	if showUser {
		fmt.Println(user.Username)
		return nil
	}

	// With no password given, prefer the environment (keeps it out of the
	// process list) and fall back to generating one.
	generated := false
	if password == "" {
		if fromEnv := strings.TrimSpace(os.Getenv("ADMIN_PASSWORD")); fromEnv != "" {
			password = fromEnv
		} else {
			password = mustGeneratePassword()
			generated = true
		}
	}

	if newUsername == "" {
		if err := store.SetPassword(ctx, user.ID, password); err != nil {
			return err
		}
	} else {
		if err := store.SetCredentials(ctx, user.ID, newUsername, password); err != nil {
			return err
		}
	}
	// Both store calls revoke every session of the account, so a cookie
	// stolen before this reset cannot outlive it.
	if newUsername == "" {
		fmt.Printf("Password updated for %q.\n", user.Username)
	} else {
		fmt.Printf("Operator is now %q.\n", database.NormalizeAdminUsername(newUsername))
	}
	if generated {
		fmt.Printf("Password: %s\n", password)
	}
	fmt.Println("All existing sessions were signed out.")
	return nil
}

func mustGeneratePassword() string {
	password, err := randomPassword()
	if err != nil {
		// crypto/rand failing is fatal for a credential; there is no safe
		// weaker fallback, so stop rather than invent one.
		fmt.Fprintln(os.Stderr, "aircoins-controller: cannot generate a password:", err)
		os.Exit(1)
	}
	return password
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func run() error {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	cfg, httpAddr, err := loadConfig()
	if err != nil {
		return err
	}
	cfg.DB.Logger = logger
	cfg.Handler.Logger = logger
	cfg.Handler.Version = version

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db, err := database.Open(ctx, cfg.DB)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	tpl, err := template.New("").Funcs(handlers.TemplateFuncs()).ParseFS(templateFS, handlers.TemplatePattern)
	if err != nil {
		return fmt.Errorf("parse templates: %w", err)
	}

	// Seed the operator account before the server accepts a single request,
	// so the panel is never briefly open on a fresh install.
	if err := bootstrapAdmin(ctx, db, cfg, logger); err != nil {
		return err
	}

	h := handlers.New(db, tpl, cfg.Handler)
	server := &http.Server{
		Addr:              httpAddr,
		Handler:           h.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	done := make(chan struct{})
	go expirySweeper(db, logger, done)
	defer close(done)

	sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("aircoins controller listening", "addr", httpAddr, "portal", cfg.Handler.PortalName, "version", version)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- listenError(httpAddr, err)
			return
		}
		errCh <- nil
	}()

	select {
	case <-sigCtx.Done():
		logger.Info("shutting down")
	case err := <-errCh:
		return err
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	return <-errCh
}

// bootstrapAdmin creates the panel operator account on a fresh install.
//
// It is deliberately a no-op once an account exists, so restarting the service
// never resets a password the operator changed through /admin/settings. When
// no ADMIN_PASSWORD is configured a random one is generated and written to the
// log exactly once - a blank or well-known default would leave the whole
// router fleet one dictionary away from being exposed.
func bootstrapAdmin(ctx context.Context, db *database.DB, cfg appConfig, logger *slog.Logger) error {
	store := db.AdminUsers()
	count, err := store.Count(ctx)
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	username := cfg.Handler.AdminUser
	password := cfg.Handler.AdminPassword
	generated := false
	if password == "" {
		password, err = randomPassword()
		if err != nil {
			return err
		}
		generated = true
	}
	if err := store.Create(ctx, username, password); err != nil {
		// A too-short ADMIN_PASSWORD must stop the boot rather than fall back
		// to a random one the operator never sees.
		return fmt.Errorf("create admin account: %w", err)
	}

	if generated {
		logger.Warn("generated an initial panel password because ADMIN_PASSWORD was not set",
			"username", username, "password", password)
	} else {
		logger.Info("panel account created from ADMIN_USER/ADMIN_PASSWORD", "username", username)
	}
	return nil
}

// randomPassword returns a 20 character base32 password. base32 (not base64)
// keeps it free of characters that are easy to misread when copied off a log.
func randomPassword() (string, error) {
	buf := make([]byte, 20)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate initial admin password: %w", err)
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf)
	return strings.ToLower(encoded[:20]), nil
}

// listenError turns the privileged-port failure into an actionable message: a
// default ADDR of ":80" cannot be bound by a manual, unprivileged run.
func listenError(addr string, err error) error {
	if errors.Is(err, syscall.EACCES) {
		return fmt.Errorf("listen on %s: %w; ports below 1024 need root or "+
			"CAP_NET_BIND_SERVICE (install.sh sets AmbientCapabilities, or use "+
			"ADDR=:8080 - see INSTALLATION.md section F)", addr, err)
	}
	return err
}
