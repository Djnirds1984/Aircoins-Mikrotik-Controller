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

	// Piso Wi-Fi coin slot. COIN_NODE_TOKEN is the shared secret a NodeMCU
	// presents on /api/coin-pulse; while it is empty that endpoint refuses
	// every report, so a controller exposed on a hotspot is never a free
	// internet vending machine.
	cfg.Handler.CoinNodeToken = strings.TrimSpace(os.Getenv("COIN_NODE_TOKEN"))
	cfg.Handler.CoinPulseSeconds = envInt("COIN_SECONDS_PER_PULSE", 300)
	cfg.Handler.CoinPulseCents = envInt("COIN_CENTS_PER_PULSE", 500)
	cfg.Handler.CoinIdleTTL = envDuration("COIN_IDLE_TTL", 20*time.Minute)
	cfg.Handler.CoinMaxSessionMinutes = envInt("COIN_MAX_SESSION_MINUTES", 240)

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

// envInt reads a positive integer, falling back on anything unparsable.
//
// The coin slot's money-to-time rate is the one number an operator will fat
// finger ("COIN_SECONDS_PER_PULSE=5m"), and a bad value must not stop the
// controller from booting or, worse, be silently read as zero - which would
// make every coin worth nothing.
func envInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		fmt.Fprintf(os.Stderr, "aircoins-controller: ignoring invalid %s %q (want a positive whole number)\n", key, raw)
		return fallback
	}
	return n
}

// envDuration reads a duration, falling back on anything unparsable.
func envDuration(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		fmt.Fprintf(os.Stderr, "aircoins-controller: ignoring invalid %s %q (want e.g. 20m)\n", key, raw)
		return fallback
	}
	return d
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
