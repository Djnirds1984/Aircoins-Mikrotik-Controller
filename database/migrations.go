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

const adminUsersDDL = `
CREATE TABLE IF NOT EXISTS admin_users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT COLLATE NOCASE NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    salt          TEXT NOT NULL,
    iterations    INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    password_changed_at TEXT
)`

const adminSessionsDDL = `
CREATE TABLE IF NOT EXISTS admin_sessions (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    last_seen  TEXT NOT NULL,
    remote     TEXT NOT NULL DEFAULT ''
)`

const adminSessionsIndexesDDL = `
CREATE INDEX IF NOT EXISTS admin_sessions_expiry_idx ON admin_sessions(expires_at);
CREATE INDEX IF NOT EXISTS admin_sessions_user_idx ON admin_sessions(user_id)`

// coinCreditsDDL backs the coin-slot credit tally fed by the NodeMCU.
//
// The tally is keyed by "subject" rather than by MAC alone for two reasons:
// a MikroTik hotspot only reports the MAC on clients it has already seen, so
// a guest standing at the coin box often has an address and nothing else; and
// a kiosk that never gets a MAC at all still has to be able to accumulate a
// credit. The subject is therefore "mac:<normalized>" when the MAC is known
// and "ip:<address>" otherwise, which keeps one row per paying client either
// way.
//
// granted_seconds is the running total of access time the inserted money has
// bought and used_seconds is how much of it has already been handed to the
// device. Keeping both (rather than a single "remaining" column that gets
// overwritten) means the audit trail survives a controller restart and the
// portal can always show what was paid for and what was consumed.
const coinCreditsDDL = `
    CREATE TABLE IF NOT EXISTS coin_credits (
        id             INTEGER PRIMARY KEY AUTOINCREMENT,
        subject        TEXT NOT NULL UNIQUE,
        mac_address    TEXT NOT NULL DEFAULT '',
        router_id      INTEGER REFERENCES routers(id) ON DELETE SET NULL,
        node_id        TEXT NOT NULL DEFAULT '',
        pulses         INTEGER NOT NULL DEFAULT 0 CHECK (pulses >= 0),
        amount_cents   INTEGER NOT NULL DEFAULT 0 CHECK (amount_cents >= 0),
        granted_seconds INTEGER NOT NULL DEFAULT 0 CHECK (granted_seconds >= 0),
        used_seconds   INTEGER NOT NULL DEFAULT 0 CHECK (used_seconds >= 0),
        status         TEXT NOT NULL DEFAULT 'active',
        last_event     TEXT NOT NULL DEFAULT '',
        last_pulse_at  TEXT,
        connected_at   TEXT,
        expires_at     TEXT,
        created_at     TEXT NOT NULL,
        updated_at     TEXT NOT NULL
    )`

// ratesDDL backs the admin RATES page: what a coin costs and how much Wi-Fi
// time it buys.
//
// The controller used to price a pulse from a single environment variable
// (COIN_SECONDS_PER_PULSE), which works until the operator wants more than one
// denomination or more than one plan - "5 pesos buys 15 minutes but 20 pesos
// buys an hour" is not expressible as one number. The table makes the pricing
// explicit, editable from the panel, and durable across a service restart.
//
// The time allowance is stored three ways on purpose:
//
//   - days / hours / minutes are what the operator picks in the form and what the
//     panel displays, so the value they typed is the value that is kept;
//   - granted_seconds is the same figure flattened for arithmetic, so the coin
//     path never has to re-derive it (and a future second-level granularity does
//     not need a schema change to be representable).
//
// amount_cents is what the operator pays. It is informational - the granted
// time is what reaches the customer - and exists so the end-of-day
// reconciliation against the acceptor's coin tube is possible.
//
// is_active is a soft delete rather than a hard one: a rate that priced a coin
// yesterday must still be able to explain a credit issued yesterday, and a
// hard-deleted row could not.
const ratesDDL = `
CREATE TABLE IF NOT EXISTS rates (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    label           TEXT NOT NULL DEFAULT '',
    pulses          INTEGER NOT NULL DEFAULT 1 CHECK (pulses BETWEEN 1 AND 1000),
    amount_cents    INTEGER NOT NULL DEFAULT 0 CHECK (amount_cents >= 0),
    days            INTEGER NOT NULL DEFAULT 0 CHECK (days BETWEEN 0 AND 30),
    hours           INTEGER NOT NULL DEFAULT 0 CHECK (hours BETWEEN 0 AND 23),
    minutes         INTEGER NOT NULL DEFAULT 0 CHECK (minutes BETWEEN 0 AND 59),
    granted_seconds INTEGER NOT NULL DEFAULT 0 CHECK (granted_seconds >= 0),
    is_active       INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0, 1)),
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
)`

const ratesIndexesDDL = `
CREATE INDEX IF NOT EXISTS rates_active_idx ON rates(is_active, pulses)`

const coinCreditsIndexesDDL = `
    CREATE INDEX IF NOT EXISTS coin_credits_status_idx ON coin_credits(status, updated_at DESC);
    CREATE INDEX IF NOT EXISTS coin_credits_node_idx ON coin_credits(node_id)`

// devicesDDL backs the admin DEVICES page: the operator's own inventory of
// client devices.
//
// Only the durable identity of a device is stored here - which router it
// belongs to, its MAC address, a friendly name and free-form notes. The
// volatile facts (its current IP address, DHCP hostname and how much paid
// session time is left) are read live from the router on every page load and
// are deliberately NOT persisted, so the table can never show a stale address
// for a device that has since moved.
//
// MAC is stored normalised (see NormalizeMAC) and is unique per router, so the
// same physical device cannot be saved twice on one hotspot while still being
// trackable on two different routers.
const devicesDDL = `
CREATE TABLE IF NOT EXISTS devices (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    router_id  INTEGER NOT NULL REFERENCES routers(id) ON DELETE CASCADE,
    mac        TEXT NOT NULL,
    name       TEXT NOT NULL DEFAULT '',
    notes      TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (router_id, mac)
)`

const devicesIndexesDDL = `
CREATE INDEX IF NOT EXISTS devices_router_idx ON devices(router_id);
CREATE INDEX IF NOT EXISTS devices_mac_idx ON devices(mac)`

const (
	routersTransportDDL = `ALTER TABLE routers ADD COLUMN transport TEXT NOT NULL DEFAULT 'auto'`
	routersRestPortDDL  = `ALTER TABLE routers ADD COLUMN rest_port INTEGER NOT NULL DEFAULT 0`
	routersLastXDDL     = `ALTER TABLE routers ADD COLUMN last_transport TEXT NOT NULL DEFAULT ''`
)

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
	{
		version: 2,
		name:    "router-transport",
		statements: []string{
			routersTransportDDL,
			routersRestPortDDL,
			routersLastXDDL,
		},
	},
	{
		version: 3,
		name:    "admin-auth",
		statements: []string{
			adminUsersDDL,
			adminSessionsDDL,
			adminSessionsIndexesDDL,
		},
	},
	{
		version: 4,
		name:    "portal-appearance",
		statements: []string{
			portalSettingsDDL,
		},
	},
	{
		version: 5,
		name:    "portal-full-page",
		statements: []string{
			// "standard" renders the built-in captive layout; "full" serves
			// the operator's own document instead. The default keeps every
			// existing install on the built-in page.
			portalPageModeDDL,
			portalFullHTMLDDL,
		},
	},
	{
		version: 6,
		name:    "coin-credits",
		statements: []string{
			// The credit tally a coin-slot NodeMCU posts to. Purely additive:
			// an install that never wires up hardware is unaffected.
			coinCreditsDDL,
			coinCreditsIndexesDDL,
		},
	},
	{
		version: 7,
		name:    "coin-rates",
		statements: []string{
			// The pricing table behind the admin RATES page. Additive: an
			// install that never opens the page keeps pricing pulses from
			// COIN_SECONDS_PER_PULSE exactly as before.
			ratesDDL,
			ratesIndexesDDL,
		},
	},
	{
		version: 8,
		name:    "devices",
		statements: []string{
			// The operator-tracked device inventory behind the admin DEVICES
			// page, which replaces the retired session-history view. Additive:
			// an install that never saves a device is unaffected.
			devicesDDL,
			devicesIndexesDDL,
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
