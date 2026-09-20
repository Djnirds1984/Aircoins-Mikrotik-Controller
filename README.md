# Aircoins MikroTik Hotspot Controller

A Go-based controller for MikroTik RouterOS **hotspot** systems, built to run on
SBC boards (Raspberry Pi, Orange Pi, NanoPi) and x86_64 mini PCs. The panel is
server rendered HTML with no JavaScript build step.

**Phase 0 + 2 scope (current):** the RouterOS integration layer, the connection
probe that powers "Test connection", and the router registry with a
test-before-save gate. Captive portal serving, vouchers and provisioning land in
later phases.

## Architecture in one paragraph

RouterOS authenticates hotspot users from its own local user database; the panel
provisions those users over the RouterOS API and completes logins with
`/ip/hotspot/active/login`. There is deliberately **no RADIUS server**: the
router enforces time and data limits itself, so existing vouchers keep working
even when the panel is offline. The panel is the source of truth for vouchers and
plans, creating router-side hotspot users just-in-time and removing them when
they are exhausted.

A hotspot profile's `login-by` list must include `http-pap`, because panel driven
logins send the password over the API (PAP). `http-chap` alone will not work; the
probe reports this explicitly.

## Commands

```
cmd/aircoins         the controller (admin panel; portal arrives in a later phase)
cmd/aircoins-probe   standalone connection tester, doubles as a field diagnostic
```

## Quick start (no hardware required)

```
go run ./cmd/aircoins -fake-router -admin-addr 127.0.0.1:8080 -data-dir ./data-dev
```

`-fake-router` points every connection at a simulated RouterOS device so the
router registry can be demonstrated and tested without a MikroTik. Open
<http://127.0.0.1:8080/admin>, create the administrator account, then add a
router with any address (for example `demo.local`).

To exercise the failing paths instead:

```
go run ./cmd/aircoins-probe -demo=ok
go run ./cmd/aircoins-probe -demo=http-chap      # login-by has no http-pap
go run ./cmd/aircoins-probe -demo=read-only      # account may read but not write
go run ./cmd/aircoins-probe -demo=no-hotspot     # hotspot package absent
go run ./cmd/aircoins-probe -demo=clock-skew     # device clock 3 hours off
go run ./cmd/aircoins-probe -demo=small-flash    # too little free flash
```

## Testing against a real router

```
go run ./cmd/aircoins-probe -host 192.168.88.1 -user admin -pass secret
go run ./cmd/aircoins-probe -host 192.168.88.1 -user admin -pass secret -allow-write
```

Read-only by default. `-allow-write` additionally proves the account can manage
the hotspot by creating and then deleting a single scratch walled-garden entry.
It always cleans up after itself, even on failure.

On the router, the API service must be enabled:

```
/ip service enable api
```

and the account should be in the `full` group, or a group with the `api`, `read`,
`write` and `policy` policies.

## The connection test

The probe ladder is the core of Phase 0. Each step reports PASS, WARN, FAIL or
SKIP, and a failing step carries a suggested fix:

| Check | What it proves |
|---|---|
| TCP reachability | the device answers on the API port at all |
| API authentication | the credentials work over `api` or `api-ssl` |
| Device identification | identity, board, model, architecture, RouterOS version, license, free flash |
| Hotspot inventory | the hotspot menu exists, plus servers, profiles, users and sessions |
| Login methods | `login-by` contains `http-pap` so panel logins will work |
| API service settings | which management services are enabled and whether addresses are restricted |
| device-mode | hotspot is permitted (RouterOS 7 can block it) |
| Write permission | the account can create and remove hotspot configuration |
| Flash headroom | how many just-in-time hotspot users fit in free flash |
| Clock agreement | device clock versus panel clock, with an NTP advisory |

## Configuration

Flags take precedence over `AIRCOINS_*` environment variables.

| Flag | Env | Default | Meaning |
|---|---|---|---|
| `-admin-addr` | `AIRCOINS_ADMIN_ADDR` | `:8080` | admin panel listen address |
| `-portal-addr` | `AIRCOINS_PORTAL_ADDR` | `:8081` | captive portal listen address |
| `-base-url` | `AIRCOINS_BASE_URL` | empty | panel URL as hotspot clients see it |
| `-data-dir` | `AIRCOINS_DATA_DIR` | `data` | database and master key location |
| `-db` | `AIRCOINS_DB` | `<data-dir>/aircoins.db` | SQLite path |
| `-secret-key` | `AIRCOINS_SECRET_KEY` | generated | 32-byte master key (base64/hex/raw) |
| `-cookie-secure` | `AIRCOINS_COOKIE_SECURE` | `false` | set `Secure` on session cookies |
| `-log-level` | `AIRCOINS_LOG_LEVEL` | `info` | debug/info/warn/error |
| `-log-format` | `AIRCOINS_LOG_FORMAT` | `json` | json/text |
| `-probe-timeout` | – | `12s` | overall probe timeout |
| `-health-interval` | – | `5m` | background router health poll (0 disables) |
| `-fake-router` | `AIRCOINS_FAKE_ROUTER` | `false` | serve a simulated device |

## Security model

* Router API passwords and FTP passwords are encrypted at rest with AES-256-GCM
  using a master key from `-secret-key` or `<data-dir>/secret.key`, created with
  `0600` permissions on first run.
* Panel sessions are stored as SHA-256 token hashes, so a database leak does not
  expose usable cookies.
* Passwords are hashed with Argon2id.
* Every mutating request requires a CSRF token.
* **Saving a router requires a passing connection test.** The test returns a
  short-lived HMAC token bound to the exact address, port, TLS flag and username
  that were probed, so a router cannot be tested at one address and saved at
  another. "Save without verifying" is an explicit, visible escape hatch that
  marks the router unverified.
* The panel sets a strict Content-Security-Policy with `script-src 'self'`, which
  is why all JavaScript lives in `app.js` rather than inline attributes.

## Layout

```
cmd/aircoins                 the controller
cmd/aircoins-probe           the connection tester
internal/admin               admin panel HTTP layer
internal/auth                Argon2id password hashing, token generation
internal/config              flags and environment
internal/crypto              master key, AES-256-GCM, HMAC
internal/domain              shared types (no dependencies)
internal/httpx               middleware, template rendering
internal/logging             slog setup
internal/routeros            RouterOS API client, hotspot operations, probe ladder
internal/routeros/faketos    simulated RouterOS device
internal/store               SQL and repositories
internal/storage             SQLite open and migrations
internal/version             build metadata
internal/webui               embedded templates and static assets
internal/storage/migrations  forward-only schema migrations
docs/                        Phase 0 findings and operations notes
deploy/                      systemd unit and install script
```

## Building for SBCs and mini PCs

SQLite is accessed through the pure-Go `modernc.org/sqlite` driver, so every
target builds with `CGO_ENABLED=0`:

```
make build-linux-arm64   # Raspberry Pi 3/4/5, most SBCs
make build-linux-armv7   # 32-bit boards
make build-linux-amd64   # x86_64 mini PCs
```

## Installation on a Raspberry Pi, Orange Pi (Armbian) or x86_64 mini PC

The controller ships as a single static binary with no external runtime
dependencies beyond systemd. SQLite is embedded, so there is nothing else to
install.

### 1. Get the binary

**Option A — download a release (recommended):**

```sh
# Pick the asset that matches your board:
#   aircoins-linux-arm64  -> Raspberry Pi 3/4/5, Orange Pi 5/5B, NanoPi R5S, etc.
#   aircoins-linux-armv7  -> 32-bit boards (Orange Pi Zero, etc.)
#   aircoins-linux-amd64  -> x86_64 mini PCs and VMs
```

Download the matching asset from the [releases page](https://github.com/Djnirds1984/Aircoins-Mikrotik-Controller/releases)
and verify the `sha256` checksum that accompanies it.

**Option B — build from source:**

```sh
make build-linux-arm64   # on any machine; cross-compiles correctly
make build-linux-amd64
```

### 2. Install on the target device

Copy the binary to the board (via `scp`, a USB stick, or build directly on it)
and run the bundled installer as root:

```sh
sudo ./install.sh ./aircoins-linux-arm64   # use -amd64 or -armv7 as needed
```

The script:

* creates a system user `aircoins` (no login shell),
* installs the binary to `/opt/aircoins/aircoins`,
* writes `/etc/systemd/system/aircoins.service` with security hardening and
  `CAP_NET_BIND_SERVICE` so the panel can bind port 80,
* starts and enables the service under systemd.

### 3. First run

```sh
sudo journalctl -fu aircoins
```

Then open the panel in a browser on the same network:

```
http://<panel-ip>:8080/admin
```

On first launch the panel has no administrator. Create one, then add your
MikroTik by clicking **Routers → Add router** and running **Test connection**
before saving.

### Architecture quick reference

| Board family         | Use binary           | Notes                                          |
|---|---|---|
| Raspberry Pi 3/4/5, Orange Pi 5/5B, Rock 5, NanoPi R5S | `aircoins-linux-arm64` | 64-bit ARM Debian/Armbian |
| Orange Pi Zero, NanoPi Zero, other 32-bit ARM | `aircoins-linux-armv7` | 32-bit ARM, Raspberry Pi OS Lite 32-bit |
| x86_64 mini PCs, Intel NUC, VMs      | `aircoins-linux-amd64` | standard Ubuntu/Debian |

### Manual installation (no systemd)

If your board does not run systemd you can run the panel directly:

```sh
mkdir -p /opt/aircoins /var/lib/aircoins
cp aircoins-linux-arm64 /opt/aircoins/aircoins
chmod +x /opt/aircoins/aircoins
/opt/aircoins/aircoins -admin-addr :8080 -data-dir /var/lib/aircoins
```

The master key and SQLite database live under the `-data-dir` path (`/var/lib/aircoins`
by default). **Back up `secret.key`** — without it the encrypted router passwords
stored in the database cannot be recovered.

## Roadmap

| Phase | Status | Scope |
|---|---|---|
| 0 | done | probe ladder, simulated device, findings harness |
| 1 | done | module, config, SQLite migrations, transport, logging |
| 2 | done | router registry with test-before-save |
| 3 | next | provisioning wizard, walled garden, portal stub push |
| 4 | | plans, voucher batches, just-in-time user push and reconciliation |
| 5 | | captive portal, API login, session confirmation |
| 6 | | live sessions, usage polling, kick, device limits |
| 7 | | reports, RBAC, audit log, backups, alerting |
| 8 | | packaging, install script, Docker |
| 9 | | billing and payment providers |

