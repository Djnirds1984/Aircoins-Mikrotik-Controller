package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
	"github.com/djnirds1984/aircoins-mikrotik-controller/handlers"
)

// appConfig groups everything run() needs from the environment.
type appConfig struct {
	DB      database.Config
	Handler handlers.Config
}

func loadConfig() (appConfig, string, error) {
	var cfg appConfig

	cfg.DB.Path = envOr("DB_PATH", "data/aircoins.db")
	cfg.DB.SecretKeyPath = envOr("SECRET_KEY_PATH", "data/secret.key")

	timeout, err := time.ParseDuration(envOr("API_TIMEOUT", "12s"))
	if err != nil || timeout <= 0 {
		return cfg, "", fmt.Errorf("invalid API_TIMEOUT: want a positive duration like \"12s\"")
	}

	cfg.Handler.APITimeout = timeout
	cfg.Handler.PortalName = envOr("PORTAL_NAME", "Aircoins Hotspot")
	cfg.Handler.DefaultRedirect = strings.TrimSpace(os.Getenv("DEFAULT_REDIRECT"))
	cfg.Handler.SecureCookies = envBool("SECURE_COOKIES")

	addr := envOr("ADDR", ":8080")
	if _, _, err := splitListenAddr(addr); err != nil {
		return cfg, "", fmt.Errorf("invalid ADDR %q: %w", addr, err)
	}
	return cfg, addr, nil
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// splitListenAddr validates host:port addresses, tolerating a bare port.
func splitListenAddr(addr string) (string, int, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", 0, fmt.Errorf("empty address")
	}
	if port, err := strconv.Atoi(addr); err == nil {
		if port <= 0 || port > 65535 {
			return "", 0, fmt.Errorf("port out of range")
		}
		return "", port, nil
	}
	host, portStr, found := strings.Cut(addr, ":")
	if !found || portStr == "" {
		return "", 0, fmt.Errorf("want \":port\" or \"host:port\"")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return "", 0, fmt.Errorf("invalid port %q", portStr)
	}
	return host, port, nil
}
