# Aircoins MikroTik Controller — Phase 1

Centralized MikroTik Hotspot Controller on Ubuntu: Go + pure HTML
templates + SQLite. Multi-router inventory, live device manager
(`/ip/hotspot/active/print`, disconnect, IP-binding block/unblock),
prepaid voucher engine, and captive portal that honors MikroTik
redirect parameters (`mac`, `ip`, `link-login`, `link-orig`).

## Quick start (Ubuntu)

```bash
sudo apt update && sudo apt install -y golang-go
go build -o /tmp/aircoins-controller .
ADDR=:8080 DB_PATH=data/aircoins.db ./aircoins-controller
```

Open `http://localhost:8080/` (dashboard), `/routers`,
`/vouchers`, `/sessions`, `/portal/login`, `/healthz`.

## Configuration (environment)

| Variable | Default | Purpose |
| --- | --- | --- |
| `ADDR` | `:8080` | Listen address |
| `DB_PATH` | `data/aircoins.db` | SQLite file (`:memory:` for tests) |
| `SECRET_KEY_PATH` | `data/secret.key` | Master key file (created `0600`) |
| `AIRCOINS_SECRET_KEY` | — | Master key override (base64/hex, 32 bytes) |
| `PORTAL_NAME` | `Aircoins Hotspot` | Brand in UI and portal |
| `DEFAULT_REDIRECT` | — | Portal fallback when `link-orig` is absent |
| `API_TIMEOUT` | `12s` | Per-call RouterOS timeout |
| `SECURE_COOKIES` | — | Set `1` behind HTTPS |
| `VERSION` | `dev` | Footer version (`-ldflags "-X main.version=1.0.0"`) |

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
Environment=ADDR=:8080
Environment=DB_PATH=/var/lib/aircoins/aircoins.db
Environment=SECRET_KEY_PATH=/var/lib/aircoins/secret.key
Environment=PORTAL_NAME=Aircoins Hotspot
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
<form action="http://controller:8080/portal/login?mac=$(mac)&ip=$(ip)&username=$(username)&link-login=$(link-login)&link-login-only=$(link-login-only)&link-orig=$(link-orig)&server-name=$(server-name)&error=$(error)"
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
