-- 0001_init: panel settings, admins, router registry, probe history and the
-- cached hotspot inventory.
--
-- This schema is API-only: there is intentionally no RADIUS/NAS table. RouterOS
-- authenticates hotspot users from its own local user database, which this
-- panel provisions through the RouterOS API.

CREATE TABLE IF NOT EXISTS settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS admins (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role          TEXT NOT NULL DEFAULT 'superadmin',
    disabled      INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL DEFAULT (datetime('now')),
    last_login_at TEXT
);

CREATE TABLE IF NOT EXISTS admin_sessions (
    token_hash TEXT PRIMARY KEY,
    admin_id   INTEGER NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    expires_at TEXT NOT NULL,
    ip         TEXT NOT NULL DEFAULT '',
    user_agent TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_admin_sessions_admin ON admin_sessions (admin_id);

CREATE TABLE IF NOT EXISTS audit_logs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    admin_id   INTEGER,
    action     TEXT NOT NULL,
    target     TEXT NOT NULL DEFAULT '',
    detail     TEXT NOT NULL DEFAULT '',
    ip         TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_audit_logs_created ON audit_logs (created_at DESC);

CREATE TABLE IF NOT EXISTS routers (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    name              TEXT NOT NULL,
    host              TEXT NOT NULL,
    api_port          INTEGER NOT NULL DEFAULT 8728,
    api_tls           INTEGER NOT NULL DEFAULT 0,
    api_user          TEXT NOT NULL,
    api_password_enc  TEXT NOT NULL DEFAULT '',
    ftp_host          TEXT NOT NULL DEFAULT '',
    ftp_port          INTEGER NOT NULL DEFAULT 21,
    ftp_user          TEXT NOT NULL DEFAULT '',
    ftp_password_enc  TEXT NOT NULL DEFAULT '',
    portal_token      TEXT NOT NULL DEFAULT '',
    stub_hash         TEXT NOT NULL DEFAULT '',
    verified          INTEGER NOT NULL DEFAULT 0,
    verify_override   INTEGER NOT NULL DEFAULT 0,
    probe_state       TEXT NOT NULL DEFAULT 'unknown',
    enabled           INTEGER NOT NULL DEFAULT 1,
    notes             TEXT NOT NULL DEFAULT '',
    identity          TEXT NOT NULL DEFAULT '',
    model             TEXT NOT NULL DEFAULT '',
    board_name        TEXT NOT NULL DEFAULT '',
    ros_version       TEXT NOT NULL DEFAULT '',
    arch              TEXT NOT NULL DEFAULT '',
    license_level     TEXT NOT NULL DEFAULT '',
    free_hdd_space    INTEGER NOT NULL DEFAULT 0,
    uptime_seconds    INTEGER NOT NULL DEFAULT 0,
    clock_offset_secs INTEGER NOT NULL DEFAULT 0,
    last_seen_at      TEXT,
    last_probe_at     TEXT,
    created_at        TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at        TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_routers_name ON routers (name);

CREATE UNIQUE INDEX IF NOT EXISTS idx_routers_endpoint ON routers (host, api_port);

CREATE TABLE IF NOT EXISTS router_probes (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    router_id   INTEGER NOT NULL REFERENCES routers(id) ON DELETE CASCADE,
    probed_at   TEXT NOT NULL DEFAULT (datetime('now')),
    result      TEXT NOT NULL DEFAULT 'unknown',
    latency_ms  INTEGER NOT NULL DEFAULT 0,
    address     TEXT NOT NULL DEFAULT '',
    checks_json TEXT NOT NULL DEFAULT '[]',
    caps_json   TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS idx_router_probes_router
    ON router_probes (router_id, probed_at DESC);

CREATE TABLE IF NOT EXISTS hotspot_servers (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    router_id    INTEGER NOT NULL REFERENCES routers(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    interface    TEXT NOT NULL DEFAULT '',
    address_pool TEXT NOT NULL DEFAULT '',
    profile      TEXT NOT NULL DEFAULT '',
    disabled     INTEGER NOT NULL DEFAULT 0,
    raw_json     TEXT NOT NULL DEFAULT '{}',
    synced_at    TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (router_id, name)
);

CREATE TABLE IF NOT EXISTS router_events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    router_id  INTEGER NOT NULL REFERENCES routers(id) ON DELETE CASCADE,
    level      TEXT NOT NULL DEFAULT 'info',
    message    TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_router_events_router
    ON router_events (router_id, created_at DESC);
