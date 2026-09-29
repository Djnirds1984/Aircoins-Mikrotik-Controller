# Aircoins MikroTik Controller — Phase 1

Centralized MikroTik Hotspot Controller on Ubuntu: Go + pure HTML
templates + SQLite. Multi-router inventory, live device manager
(`/ip/hotspot/active/print`, disconnect, IP-binding block/unblock),
prepaid voucher engine, and captive portal that honors MikroTik
redirect parameters (`mac`, `ip`, `link-login`, `link-orig`).

## Quick start (Ubuntu)

Two front doors, on purpose:

- `http://<board-ip>/` — the **captive portal**. Every hotspot client that
  opens the controller IP lands here. It greets a device that is already
  online, and otherwise offers a sign-in button plus a voucher field. A
  MikroTik hotspot that redirects with `?link-login=…&mac=…&link-orig=…` is
  forwarded straight to the sign-in form with those parameters intact.
- `http://<board-ip>/admin/` — the **operator panel** (dashboard, routers,
  network, vouchers, sessions, tools). `/admin` redirects to `/admin/`.

The panel routes are *also* still mounted at the root, so `/routers`,
`/vouchers`, `/api/v1/...`, `/healthz` and existing bookmarks keep working.

```bash
sudo apt update && sudo apt install -y golang-go
go build -o /tmp/aircoins-controller .
sudo ADDR=:80 DB_PATH=/tmp/aircoins.db ./aircoins-controller
```

Port 80 is privileged, so the manual run above uses `sudo`; without root run it
as `ADDR=:8080 DB_PATH=/tmp/aircoins.db ./aircoins-controller`. `install.sh`
grants the service the capability it needs instead (see `INSTALLATION.md` §F).
The Tools page manages ZeroTier on the Debian/Ubuntu/Armbian host running
this panel: it shows installation/service/node/network status, offers a
restricted one-click installer when `zerotier-cli` is absent, and can join
or leave a validated 16-character ZeroTier network. Each joined network is
listed with the interface ZeroTier created (`portDeviceName`) and the
addresses assigned to it; when ZeroTier reports no managed address, the
addresses the kernel has on that interface are shown instead. Rerun
`sudo ./install.sh`
after upgrading an existing deployment to provision its no-argument root
helper and matching sudoers rule.

## Configuration (environment)

| Variable | Default | Purpose |
| --- | --- | --- |
| `ADDR` | `:80` | Listen address; port 80 serves `http://<board-ip>/` (privileges: `INSTALLATION.md` §F) |
| `DB_PATH` | `data/aircoins.db` | SQLite file (`:memory:` for tests) |
| `SECRET_KEY_PATH` | `data/secret.key` | Master key file (created `0600`) |
| `AIRCOINS_SECRET_KEY` | — | Master key override (base64/hex, 32 bytes) |
| `PORTAL_NAME` | `Aircoins Hotspot` | Brand in UI and portal |
| `ADMIN_PATH` | `/admin` | URL prefix of the operator panel; `/` stays the captive portal |
| `DASHBOARD_AT_ROOT` | — | Set `1` to put the dashboard back on `/` (portal moves to `/portal`) |
| `PORTAL_TAGLINE` | `Connect to the Wi-Fi to get online` | Welcome line on the portal landing page |
| `PORTAL_SUPPORT` | `Ask the front desk for a voucher code.` | Contact line on the portal landing page |
| `DEFAULT_REDIRECT` | — | Portal fallback when `link-orig` is absent |
| `API_TIMEOUT` | `12s` | Per-call RouterOS timeout |
| `SECURE_COOKIES` | — | Set `1` behind HTTPS |
| `VERSION` | `dev` | Build-time footer version only (`-ldflags "-X main.version=1.0.0"`; `install.sh` uses `AIRCOINS_VERSION=1.0.0`) |

Router API passwords are AES-256-GCM encrypted at rest; without the
master key a stolen `.db` file is useless.

## systemd example

```ini
[Unit]
Description=Aircoins MikroTik Controller
After=network-online.target

[Service]
Type=simple
User=aircoins
WorkingDirectory=/opt/aircoins
Environment=ADDR=:80
Environment=DB_PATH=/var/lib/aircoins/aircoins.db
Environment=SECRET_KEY_PATH=/var/lib/aircoins/secret.key
Environment=PORTAL_NAME=Aircoins Hotspot
# Required to bind port 80 while running as the unprivileged User= above.
AmbientCapabilities=CAP_NET_BIND_SERVICE
ExecStart=/opt/aircoins/aircoins-controller
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

## MikroTik hotspot wiring

1. Create an API user on each router (`/user` group with `read,write`
   on `hotspot`), allow the controller IP under `/ip/service`.
2. Register the router in `/routers` (IP, port `8728` or `8729` for
   API-SSL, user, pass). The controller tests the connection and
   records online/offline status with latency.
3. Point the hotspot login page at the portal, preserving variables:

```html
<form action="http://controller/portal/login?mac=$(mac)&ip=$(ip)&username=$(username)&link-login=$(link-login)&link-login-only=$(link-login-only)&link-orig=$(link-orig)&server-name=$(server-name)&error=$(error)"
  method="post">
  <input name="voucher" placeholder="AIR-XXXX-XXXX">
  <button type="submit">Connect</button>
</form>
```

Router resolution order: `server-name` → portal tag, `link-login`
host → registered address, default-portal router, single router.

## Vouchers

Generate batches in `/vouchers` (quantity, prefix, groups, profile,
time/data/device limits, price, max uses, optional immediate
provisioning). Time/data limits are enforced on-device via
`limit-uptime` / `limit-bytes-total`; the local ledger tracks
uses/expiry so a dropped API connection never burns a key: the
controller provisions → logs in → then redeems, rolling the hotspot
session back if the ledger refuses.

## Project layout

```text
main.go / config.go / sweeper.go   server entry, env config, expiry sweep
database/                          SQLite + migrations + AES credential box
handlers/                          routing, dashboard, routers, sessions,
                                   vouchers, portal, RouterOS client
templates/                         pure HTML (no JS framework)
```
