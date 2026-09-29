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
	cfg.Handler.PortalTagline = envOr("PORTAL_TAGLINE", "Connect to the Wi-Fi to get online")
	cfg.Handler.PortalSupport = envOr("PORTAL_SUPPORT", "Ask the front desk for a voucher code.")
	// The panel lives under ADMIN_PATH (default /admin) so a guest who opens
	// the IP of the board only gets the captive portal. DASHBOARD_AT_ROOT=true
	// restores the previous layout where / is the operator dashboard.
	cfg.Handler.AdminPath = envOr("ADMIN_PATH", "/admin")
	cfg.Handler.DashboardAtRoot = envBool("DASHBOARD_AT_ROOT")
	// Panel credentials are only used on the very first boot, when the
	// account table is still empty. They never overwrite an existing account,
	// so changing them later has no effect - use /admin/settings for that.
	cfg.Handler.AdminUser = envOr("ADMIN_USER", "admin")
	cfg.Handler.AdminPassword = os.Getenv("ADMIN_PASSWORD")
	cfg.Handler.DefaultRedirect = strings.TrimSpace(os.Getenv("DEFAULT_REDIRECT"))
	cfg.Handler.SecureCookies = envBool("SECURE_COOKIES")

	// How long a panel sign-in survives. A non-positive or unparsable value
	// is ignored rather than fatal: a typo here should not stop the hotspot
	// controller from booting.
	if raw := strings.TrimSpace(os.Getenv("ADMIN_SESSION_TTL")); raw != "" {
		if ttl, err := time.ParseDuration(raw); err == nil && ttl > 0 {
			cfg.Handler.AdminSessionTTL = ttl
		} else {
			fmt.Fprintf(os.Stderr, "aircoins-controller: ignoring invalid ADMIN_SESSION_TTL %q (want e.g. 12h)\n", raw)
		}
	}

	// The panel ships on port 80 so it is reachable as http://<board-ip>/
	// without a port suffix; install.sh grants the service user
	// CAP_NET_BIND_SERVICE. ADDR still takes any ":port" or "host:port".
	addr := envOr("ADDR", ":80")
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
