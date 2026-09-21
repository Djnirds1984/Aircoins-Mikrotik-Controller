package database

import (
	"context"
	"fmt"
)

// migration is one versioned, forward-only schema change.
type migration struct {
	version    int
	name       string
	statements []string
}

const schemaMigrationsDDL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    applied_at TEXT NOT NULL
)`

const routersDDL = `
CREATE TABLE IF NOT EXISTS routers (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    name              TEXT COLLATE NOCASE NOT NULL UNIQUE,
    host              TEXT NOT NULL,
    port              INTEGER NOT NULL DEFAULT 8728 CHECK (port BETWEEN 1 AND 65535),
    username          TEXT NOT NULL,
    password          TEXT NOT NULL DEFAULT '',
    use_tls           INTEGER NOT NULL DEFAULT 0,
    verify_tls        INTEGER NOT NULL DEFAULT 0,
    location          TEXT NOT NULL DEFAULT '',
    portal_tag        TEXT NOT NULL DEFAULT '',
    is_default_portal INTEGER NOT NULL DEFAULT 0,
    notes             TEXT NOT NULL DEFAULT '',
    last_status       TEXT NOT NULL DEFAULT 'unknown',
    last_error        TEXT NOT NULL DEFAULT '',
    last_latency_ms   INTEGER NOT NULL DEFAULT 0,
    last_seen_at      TEXT,
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL
)`

const routersIndexesDDL = `
CREATE INDEX IF NOT EXISTS routers_host_idx ON routers(host);
CREATE INDEX IF NOT EXISTS routers_portal_tag_idx ON routers(portal_tag)`

const sessionsDDL = `
CREATE TABLE IF NOT EXISTS active_sessions (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    router_id    INTEGER NOT NULL REFERENCES routers(id) ON DELETE CASCADE,
    session_key  TEXT NOT NULL,
    username     TEXT NOT NULL DEFAULT '',
    address      TEXT NOT NULL DEFAULT '',
    mac_address  TEXT NOT NULL DEFAULT '',
    login_by     TEXT NOT NULL DEFAULT '',
    server       TEXT NOT NULL DEFAULT '',
    uptime       TEXT NOT NULL DEFAULT '',
    bytes_in     INTEGER NOT NULL DEFAULT 0,
    bytes_out    INTEGER NOT NULL DEFAULT 0,
    started_at   TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    ended_at     TEXT,
    end_reason   TEXT NOT NULL DEFAULT '',
    UNIQUE (router_id, session_key)
)`

const sessionsIndexesDDL = `
CREATE INDEX IF NOT EXISTS sessions_open_idx ON active_sessions(router_id, ended_at);
CREATE INDEX IF NOT EXISTS sessions_mac_idx ON active_sessions(mac_address);
CREATE INDEX IF NOT EXISTS sessions_started_idx ON active_sessions(started_at DESC)`

const vouchersDDL = `
CREATE TABLE IF NOT EXISTS vouchers (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    code             TEXT NOT NULL UNIQUE,
    batch            TEXT NOT NULL DEFAULT '',
    router_id        INTEGER REFERENCES routers(id) ON DELETE SET NULL,
    profile          TEXT NOT NULL DEFAULT 'default',
    duration_minutes INTEGER NOT NULL DEFAULT 0 CHECK (duration_minutes >= 0),
    data_limit_mb    INTEGER NOT NULL DEFAULT 0 CHECK (data_limit_mb >= 0),
    device_limit     INTEGER NOT NULL DEFAULT 1 CHECK (device_limit >= 0),
    price_cents      INTEGER NOT NULL DEFAULT 0 CHECK (price_cents >= 0),
    status           TEXT NOT NULL DEFAULT 'unused',
    uses             INTEGER NOT NULL DEFAULT 0,
    max_uses         INTEGER NOT NULL DEFAULT 1,
    note             TEXT NOT NULL DEFAULT '',
    created_at       TEXT NOT NULL,
    pushed_at        TEXT,
    activated_at     TEXT,
    expires_at       TEXT,
    last_used_at     TEXT
)`

const vouchersIndexesDDL = `
CREATE INDEX IF NOT EXISTS vouchers_status_idx ON vouchers(status);
CREATE INDEX IF NOT EXISTS vouchers_batch_idx ON vouchers(batch);
CREATE INDEX IF NOT EXISTS vouchers_router_idx ON vouchers(router_id);
CREATE INDEX IF NOT EXISTS vouchers_expiry_idx ON vouchers(status, expires_at)`

// migrations is the ordered list of schema changes. Append a new entry instead
// of editing an existing one: applied versions are never replayed.
var migrations = []migration{
	{
		version: 1,
		name:    "init",
		statements: []string{
			routersDDL,
			routersIndexesDDL,
			sessionsDDL,
			sessionsIndexesDDL,
			vouchersDDL,
			vouchersIndexesDDL,
		},
	},
}

// migrate applies every pending migration inside its own transaction.
func (db *DB) migrate(ctx context.Context) error {
	if _, err := db.sql.ExecContext(ctx, schemaMigrationsDDL); err != nil {
		return fmt.Errorf("database: create schema_migrations: %w", err)
	}

	rows, err := db.sql.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("database: read schema_migrations: %w", err)
	}
	applied := make(map[int]bool)
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			rows.Close()
			return fmt.Errorf("database: scan schema_migrations: %w", err)
		}
		applied[version] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return fmt.Errorf("database: read schema_migrations: %w", err)
	}

	for _, m := range migrations {
		if applied[m.version] {
			continue
		}
		if err := db.applyMigration(ctx, m); err != nil {
			return err
		}
		db.log.Info("applied database migration", "version", m.version, "name", m.name)
	}
	return nil
}

func (db *DB) applyMigration(ctx context.Context, m migration) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("database: begin migration %d: %w", m.version, err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a harmless no-op

	for i, statement := range m.statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("database: migration %d (%s) statement %d: %w", m.version, m.name, i+1, err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
		m.version, m.name, stamp(now())); err != nil {
		return fmt.Errorf("database: record migration %d: %w", m.version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("database: commit migration %d: %w", m.version, err)
	}
	return nil
}
