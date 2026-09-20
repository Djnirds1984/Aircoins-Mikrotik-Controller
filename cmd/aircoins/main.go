// Command aircoins runs the Aircoins MikroTik hotspot controller.
//
// It serves the administration panel. Captive portal serving is added in a later
// milestone; the probe command (cmd/aircoins-probe) shares this code base.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/admin"
	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/config"
	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/crypto"
	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/logging"
	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/routeros"
	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/routeros/faketos"
	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/storage"
	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/store"
	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/version"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "aircoins:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Args[1:])
	if err != nil {
		return err
	}

	logger := logging.New(cfg.LogLevel, cfg.LogFormat)
	slog.SetDefault(logger)
	logger.Info("starting aircoins", "version", version.Full())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	key, err := loadMasterKey(cfg, logger)
	if err != nil {
		return err
	}

	db, err := storage.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := storage.Migrate(ctx, db); err != nil {
		return err
	}
	logger.Info("database ready", "path", cfg.DBPath)

	// The simulated device keeps the panel usable without hardware, which is how
	// the router registry can be demonstrated and tested end to end.
	dial := routeros.Dial
	dialTCP := routeros.DialTCP
	if cfg.FakeRouter {
		device := faketos.New(faketos.Options{})
		dial = func(context.Context, routeros.Options) (routeros.Transport, error) {
			return device, nil
		}
		dialTCP = func(context.Context, string, time.Duration) error { return nil }
		logger.Warn("fake-router is enabled: connections go to a simulated device, never to real hardware")
	}

	panel, err := admin.NewServer(admin.Deps{
		Config:  cfg,
		Logger:  logger,
		DB:      db,
		Key:     key,
		Dial:    dial,
		DialTCP: dialTCP,
	})
	if err != nil {
		return err
	}

	server := &http.Server{
		Addr:              cfg.AdminAddr,
		Handler:           panel.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("admin panel listening",
			"addr", cfg.AdminAddr,
			"url", "http://"+displayAddr(cfg.AdminAddr)+"/admin",
		)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
	}()

	// Housekeeping: drop expired panel sessions.
	go housekeeping(ctx, logger, db)

	select {
	case err := <-serverErrors:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		logger.Info("shutdown requested")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	logger.Info("stopped")
	return nil
}

// loadMasterKey returns the master key from configuration or the key file.
func loadMasterKey(cfg *config.Config, logger *slog.Logger) ([]byte, error) {
	if strings.TrimSpace(cfg.SecretKey) != "" {
		key, err := crypto.ParseKey(cfg.SecretKey)
		if err != nil {
			return nil, fmt.Errorf("secret-key: %w", err)
		}
		return key, nil
	}

	key, err := crypto.LoadOrCreateKey(cfg.KeyFilePath())
	if err != nil {
		return nil, fmt.Errorf("master key: %w", err)
	}
	logger.Info("using generated master key", "path", cfg.KeyFilePath())
	return key, nil
}

// housekeeping periodically removes expired panel sessions.
func housekeeping(ctx context.Context, logger *slog.Logger, db *sql.DB) {
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()

	admins := store.NewAdmins(db)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			removed, err := admins.DeleteExpiredSessions(ctx)
			if err != nil {
				logger.Warn("clean expired sessions", "error", err)
				continue
			}
			if removed > 0 {
				logger.Debug("cleaned expired sessions", "removed", removed)
			}
		}
	}
}

// displayAddr turns ":8080" into "localhost:8080" for log output.
func displayAddr(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "localhost" + addr
	}
	if host, port, err := net.SplitHostPort(addr); err == nil {
		if host == "" || host == "0.0.0.0" || host == "::" {
			return net.JoinHostPort("localhost", port)
		}
	}
	return addr
}
