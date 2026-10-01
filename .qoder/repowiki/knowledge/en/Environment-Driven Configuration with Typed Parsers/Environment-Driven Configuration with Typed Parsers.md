---
kind: configuration_system
name: Environment-Driven Configuration with Typed Parsers
category: configuration_system
scope:
    - '**'
source_files:
    - config.go
    - main.go
    - INSTALLATION.md
    - install.sh
---

## Approach

The controller uses a **pure environment-variable configuration system** — no YAML, TOML, JSON, or `.env` files are loaded at runtime. All settings come from `os.Getenv`, with defaults hard-coded in `config.go`. The design is documented in the package comment of `main.go`:

> "Configuration is environment based so it runs unchanged under systemd"

This makes deployment uniform across manual runs, systemd units, and containers.

## Key Files

- `config.go` — central `loadConfig()` plus typed helpers (`envOr`, `envBool`, `envInt`, `envDuration`, `splitListenAddr`).
- `main.go` — calls `loadConfig()`, wires parsed values into `database.Config` and `handlers.Config`, and exposes CLI subcommands (`--version`, `passwd`) that also consume config.
- `INSTALLATION.md` — documents the supported environment variables for production deployment.
- `install.sh` — sets `CAP_NET_BIND_SERVICE` / ambient capabilities so the default `ADDR=:80` binds without root.

## Architecture

### Single source of truth: `appConfig`

```go
type appConfig struct {
    DB      database.Config
    Handler handlers.Config
}
```

`loadConfig()` populates these two structs and returns `(appConfig, string, error)` — the second return value is the validated HTTP listen address, split out so callers can log it before starting the server.

### Typed env parsers

| Helper | Purpose | Failure behavior |
|---|---|---|
| `envOr(key, fallback)` | String with default | Returns fallback on empty/missing |
| `envBool(key)` | Boolean flag | Accepts `1`, `true`, `yes`, `on`; everything else is `false` |
| `envInt(key, fallback)` | Positive integer | Logs to stderr and returns fallback on invalid/negative |
| `envDuration(key, fallback)` | `time.ParseDuration` | Same lenient fallback pattern |
| `splitListenAddr(addr)` | Validates `:port` or `host:port` | Fatal error (stops boot) |

The lenient parsers exist by design: coin-slot money-to-time rates must not crash the controller on a typo, while the listen address is strict because an invalid port would make the service unreachable.

### Configuration surface area

All runtime knobs are single environment variables:

| Variable | Default | Meaning |
|---|---:|---|
| `DB_PATH` | `data/aircoins.db` | SQLite file path |
| `SECRET_KEY_PATH` | `data/secret.key` | Path to the symmetric key used by `database.Secretbox` |
| `API_TIMEOUT` | `12s` | MikroTik REST call timeout |
| `PORTAL_NAME` | `Aircoins Hotspot` | Portal title |
| `PORTAL_TAGLINE` | `Connect to the Wi-Fi to get online` | Portal tagline |
| `PORTAL_SUPPORT` | `Ask the front desk for a voucher code.` | Support text |
| `ADMIN_PATH` | `/admin` | Admin panel URL prefix |
| `DASHBOARD_AT_ROOT` | `false` | Serve dashboard at `/` instead of captive portal |
| `ADMIN_USER` | `admin` | Initial operator username (first-boot only) |
| `ADMIN_PASSWORD` | *(empty)* | Initial operator password; if unset a random base32 password is generated once |
| `DEFAULT_REDIRECT` | *(empty)* | Guest redirect after login |
| `SECURE_COOKIES` | `false` | Set `Secure` cookie flag |
| `ADMIN_SESSION_TTL` | *(no TTL)* | Panel session lifetime; ignored if malformed |
| `COIN_NODE_TOKEN` | *(empty)* | Shared secret for piso-wifi coin slot endpoint |
| `COIN_SECONDS_PER_PULSE` | `300` | Seconds per coin pulse |
| `COIN_CENTS_PER_PULSE` | `500` | Cents per coin pulse |
| `COIN_IDLE_TTL` | `20m` | Idle coin-session TTL |
| `COIN_MAX_SESSION_MINUTES` | `240` | Max session length |
| `ADDR` | `:80` | Listen address |

### Secrets handling

There are two secret channels:

1. **Operational secrets** (e.g. `ADMIN_PASSWORD`, `COIN_NODE_TOKEN`) are read directly via `os.Getenv` and never written back to disk.
2. **Database encryption key** lives in a separate file (`SECRET_KEY_PATH`, default `data/secret.key`), managed by `database/secretbox.go`. On first boot, if `AIRCOINS_SECRET_KEY` is set, a new key is generated and persisted there.

The initial admin password has special semantics: `ADMIN_PASSWORD` is consumed only during the one-time bootstrap (see `bootstrapAdmin` in `main.go`). Once an account exists, changing `ADMIN_PASSWORD` has no effect — operators use `/admin/settings` instead. This is enforced by the `Count > 0` early-return in `bootstrapAdmin`.

### CLI overrides

Two CLI modes bypass normal startup but still call `loadConfig()`:

- `aircoins-controller --version` — prints the build-injected `main.version` (set via `-ldflags -X main.version=...`) and exits.
- `aircoins-controller passwd [password] [--user <name>] [--show]` — resets the operator credentials using the same `DB_PATH`/`SECRET_KEY_PATH` as the running service.

### Validation rules

- `ADDR` must be a valid `:port` or `host:port`; ports outside 1–65535 cause a fatal error.
- `API_TIMEOUT` must parse as a positive `time.Duration`.
- `ADMIN_SESSION_TTL` is **non-fatal**: invalid values are logged to stderr and ignored so the hotspot continues to operate.
- Coin-rate integers (`COIN_SECONDS_PER_PULSE`, `COIN_CENTS_PER_PULSE`) are non-fatal for the same reason — a fat-fingered zero must not render every coin worthless.
- `ADMIN_PASSWORD` provided explicitly (not generated) is treated as a hard requirement: a too-short password stops boot rather than silently falling back to a random one.

### Conventions observed

- Every env var name is UPPER_SNAKE_CASE and documented alongside its default in `config.go`.
- Defaults are declared next to the variable they configure, not centralized in a map.
- Parsing helpers encapsulate validation + logging in one place, keeping `loadConfig()` declarative.
- There is no config file format, no config merging, no feature-flag registry — all behavior is controlled through the env vars listed above.
- The `handlers.Config` and `database.Config` structs are populated by `loadConfig`, which keeps each subsystem's config scoped to its own package.