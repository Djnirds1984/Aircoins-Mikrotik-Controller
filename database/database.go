// Package database owns the SQLite persistence layer for the Aircoins MikroTik
// controller: the router inventory, the observed hotspot sessions and the
// prepaid voucher ledger.
//
// The package uses the pure Go modernc.org/sqlite driver so the controller
// builds and runs on Ubuntu without cgo or a system libsqlite3.
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// driverName is the driver registered by modernc.org/sqlite.
const driverName = "sqlite"

// timeLayout is how every timestamp is stored. UTC RFC3339 sorts
// lexicographically, which makes range queries on TEXT columns trivial.
const timeLayout = time.RFC3339

// Config configures the SQLite connection and the credential secret box.
type Config struct {
	// Path is the database file. Use ":memory:" for tests.
	Path string
	// SecretKeyPath is the file holding the master key used to encrypt router
	// API passwords at rest. Created with 0600 permissions when missing.
	SecretKeyPath string
	// SecretKey overrides SecretKeyPath and AIRCOINS_SECRET_KEY when non-empty
	// (base64 or hex, 32 bytes).
	SecretKey string
	// BusyTimeout is how long a connection waits on a locked database.
	BusyTimeout time.Duration
	// MaxOpenConns caps the connection pool. SQLite serialises writers, so a
	// small pool is faster than a large one.
	MaxOpenConns int
	// Logger receives background information about the database. Optional.
	Logger *slog.Logger
}

func (c Config) withDefaults() Config {
	if c.BusyTimeout <= 0 {
		c.BusyTimeout = 5 * time.Second
	}
	if c.MaxOpenConns <= 0 {
		c.MaxOpenConns = 4
	}
	if c.Logger == nil {
		c.Logger = slog.New(slog.DiscardHandler)
	}
	return c
}

// DB is the opened SQLite database plus the credential cipher.
type DB struct {
	sql *sql.DB
	box *secretBox
	cfg Config
	log *slog.Logger
}

// Open creates (or opens) the database file, applies pending migrations and
// returns a ready to use handle. The returned DB is safe for concurrent use.
func Open(ctx context.Context, cfg Config) (*DB, error) {
	cfg = cfg.withDefaults()
	if strings.TrimSpace(cfg.Path) == "" {
		return nil, errors.New("database: path is required")
	}

	if cfg.Path != ":memory:" {
		if dir := filepath.Dir(cfg.Path); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o750); err != nil {
				return nil, fmt.Errorf("database: create directory %s: %w", dir, err)
			}
		}
	}

	key, err := loadOrCreateSecretKey(cfg)
	if err != nil {
		return nil, err
	}
	box, err := newSecretBox(key)
	if err != nil {
		return nil, err
	}

	sqlDB, err := sql.Open(driverName, buildDSN(cfg))
	if err != nil {
		return nil, fmt.Errorf("database: open %s: %w", cfg.Path, err)
	}
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxOpenConns)
	sqlDB.SetConnMaxLifetime(0)

	db := &DB{sql: sqlDB, box: box, cfg: cfg, log: cfg.Logger}

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("database: ping %s: %w (is the file writable?)", cfg.Path, err)
	}
	if err := db.migrate(ctx); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return db, nil
}

// buildDSN assembles the modernc.org/sqlite connection string. The pragmas are
// applied to every pooled connection.
func buildDSN(cfg Config) string {
	pragmas := []string{
		fmt.Sprintf("_pragma=busy_timeout(%d)", cfg.BusyTimeout.Milliseconds()),
		"_pragma=foreign_keys(1)",
		"_pragma=journal_mode(WAL)",
		"_pragma=synchronous(NORMAL)",
	}
	if cfg.Path == ":memory:" {
		// Shared cache keeps the in-memory database visible to every pooled
		// connection instead of handing each one a private database.
		return "file:aircoins-memory?mode=memory&cache=shared&" + strings.Join(pragmas, "&")
	}
	return "file:" + filepath.ToSlash(cfg.Path) + "?" + strings.Join(pragmas, "&")
}

// Close releases the connection pool.
func (db *DB) Close() error {
	if db == nil || db.sql == nil {
		return nil
	}
	return db.sql.Close()
}

// Ping verifies the database is still reachable.
func (db *DB) Ping(ctx context.Context) error {
	return db.sql.PingContext(ctx)
}

// SQL exposes the raw pool for the rare query that does not belong to a store.
func (db *DB) SQL() *sql.DB { return db.sql }

// Routers returns the router inventory store.
func (db *DB) Routers() *RouterStore { return &RouterStore{db: db} }

// Sessions returns the observed session store.
func (db *DB) Sessions() *SessionStore { return &SessionStore{db: db} }

// Vouchers returns the prepaid voucher store.
func (db *DB) Vouchers() *VoucherStore { return &VoucherStore{db: db} }
