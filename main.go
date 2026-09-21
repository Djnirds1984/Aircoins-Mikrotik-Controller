// Command aircoins-controller is the Phase 1 entry point: admin dashboard,
// router inventory, voucher engine and captive portal backed by SQLite.
//
// Configuration is environment based so it runs unchanged under systemd:
//
//	ADDR, DB_PATH, SECRET_KEY_PATH, AIRCOINS_SECRET_KEY, PORTAL_NAME,
//	DEFAULT_REDIRECT, API_TIMEOUT, SECURE_COOKIES, VERSION
package main

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
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
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "aircoins-controller:", err)
		os.Exit(1)
	}
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
			errCh <- err
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
