// Package config loads the controller configuration from flags and environment
// variables. Flags win over environment variables, which win over defaults.
package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config is the fully resolved runtime configuration.
type Config struct {
	// PortalAddr is where the captive portal (client facing) listens.
	PortalAddr string
	// AdminAddr is where the admin panel listens.
	AdminAddr string
	// PublicBaseURL is how hotspot clients reach this panel. It is embedded in
	// the router side login stub and in portal links, so it must be an address
	// clients can reach before they are authenticated.
	PublicBaseURL string
	// DataDir holds the SQLite database and the generated master key.
	DataDir string
	// DBPath is the SQLite file path.
	DBPath string
	// SecretKey is the base64/hex/raw master key. When empty a key file is
	// generated inside DataDir.
	SecretKey string
	// CookieSecure sets the Secure flag on admin session cookies.
	CookieSecure bool
	// LogLevel is debug|info|warn|error.
	LogLevel string
	// LogFormat is json|text.
	LogFormat string
	// ProbeTimeout bounds a single router probe run.
	ProbeTimeout time.Duration
	// HealthInterval is the background router health poll interval (0 disables).
	HealthInterval time.Duration
	// FakeRouter serves a simulated RouterOS device so the panel can be used
	// without hardware.
	FakeRouter bool
}

// Load parses args (without the program name) and returns a validated config.
func Load(args []string) (*Config, error) {
	cfg := &Config{}

	fs := flag.NewFlagSet("aircoins", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	fs.StringVar(&cfg.PortalAddr, "portal-addr", env("AIRCOINS_PORTAL_ADDR", ":8081"),
		"listen address for the captive portal")
	fs.StringVar(&cfg.AdminAddr, "admin-addr", env("AIRCOINS_ADMIN_ADDR", ":8080"),
		"listen address for the admin panel")
	fs.StringVar(&cfg.PublicBaseURL, "base-url", env("AIRCOINS_BASE_URL", ""),
		"public URL of this panel as reachable by hotspot clients, e.g. http://10.5.50.2:8081")
	fs.StringVar(&cfg.DataDir, "data-dir", env("AIRCOINS_DATA_DIR", "data"),
		"directory holding the database and master key")
	fs.StringVar(&cfg.DBPath, "db", env("AIRCOINS_DB", ""),
		"SQLite database path (default <data-dir>/aircoins.db)")
	fs.StringVar(&cfg.SecretKey, "secret-key", env("AIRCOINS_SECRET_KEY", ""),
		"master key (base64/hex/raw 32 bytes); generated into <data-dir>/secret.key when empty")
	fs.BoolVar(&cfg.CookieSecure, "cookie-secure", envBool("AIRCOINS_COOKIE_SECURE", false),
		"set Secure on admin session cookies (enable when the admin panel is served over TLS)")
	fs.StringVar(&cfg.LogLevel, "log-level", env("AIRCOINS_LOG_LEVEL", "info"),
		"log level: debug|info|warn|error")
	fs.StringVar(&cfg.LogFormat, "log-format", env("AIRCOINS_LOG_FORMAT", "json"),
		"log format: json|text")
	fs.DurationVar(&cfg.ProbeTimeout, "probe-timeout", 12*time.Second,
		"overall timeout for one router probe")
	fs.DurationVar(&cfg.HealthInterval, "health-interval", 5*time.Minute,
		"background router health poll interval (0 disables)")
	fs.BoolVar(&cfg.FakeRouter, "fake-router", envBool("AIRCOINS_FAKE_ROUTER", false),
		"serve a simulated RouterOS device instead of real hardware (demo/development)")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	if cfg.DBPath == "" {
		cfg.DBPath = filepath.Join(cfg.DataDir, "aircoins.db")
	}
	cfg.PublicBaseURL = strings.TrimRight(cfg.PublicBaseURL, "/")

	return cfg, cfg.Validate()
}

// Validate checks the configuration for obviously unusable combinations.
func (c *Config) Validate() error {
	if strings.TrimSpace(c.PortalAddr) == "" {
		return fmt.Errorf("portal-addr must not be empty")
	}
	if strings.TrimSpace(c.AdminAddr) == "" {
		return fmt.Errorf("admin-addr must not be empty")
	}
	if strings.TrimSpace(c.DBPath) == "" {
		return fmt.Errorf("db path must not be empty")
	}
	if c.ProbeTimeout <= 0 {
		return fmt.Errorf("probe-timeout must be positive")
	}
	if c.FakeRouter && c.PublicBaseURL == "" {
		c.PublicBaseURL = "http://localhost" + c.PortalAddr
	}
	return nil
}

// KeyFilePath is where the generated master key lives when no key is supplied.
func (c *Config) KeyFilePath() string {
	return filepath.Join(c.DataDir, "secret.key")
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}
