# INSTALLATION — Armbian SBC boards & x64 Ubuntu/Debian mini PCs

Installs the **Aircoins MikroTik Controller** (Go + pure HTML + SQLite)
as a systemd service via `install.sh`, which **auto-detects** hardware
and OS, then installs every dependency.

Supported: Orange Pi / Raspberry Pi / Rockchip / Allwinner / Amlogic
boards on Armbian (arm64 preferred, armv6l works but slow), and x64
mini PCs on Ubuntu 22.04/24.04 or Debian 11/12.

What `install.sh` does: detects CPU arch (Go amd64/arm64/armv6l),
board model, OS/Armbian flag, RAM/cores; installs `curl git`,
`build-essential`, `ufw/iptables`; reuses distro Go when it satisfies the
version `go.mod` asks for (currently 1.26.5) and otherwise fetches that
exact official tarball for the detected arch (SHA256-checked); adds
swap on ≤ 1 GB boards; creates the `aircoins` user, builds
`CGO_ENABLED=0` (pure-Go SQLite, no libsqlite3), installs a hardened
systemd unit, writes `/etc/aircoins/aircoins.env`, opens the firewall,
and smoke-tests `/healthz`.

## A. One-line install

```bash
git clone https://github.com/Djnirds1984/Aircoins-Mikrotik-Controller.git
cd Aircoins-Mikrotik-Controller
sudo ./install.sh
```

Variants:

```bash
sudo ./install.sh --port 8080 --portal-name "My Hotspot"
sudo ./install.sh --repo https://github.com/Djnirds1984/Aircoins-Mikrotik-Controller.git --branch main
sudo ./install.sh --addr 127.0.0.1:8080 --skip-firewall --no-service
sudo ./install.sh --uninstall
```

Flags: `--port --addr --install-dir --data-dir --user --portal-name`
`--go-version --repo --branch --skip-firewall --no-service --uninstall`.

The footer version comes from `AIRCOINS_VERSION`, else `git describe`,
else `dev`:

```bash
sudo AIRCOINS_VERSION=1.2.3 ./install.sh
```

## B. Board-specific notes

Orange Pi / Armbian SBCs: prefer a 64-bit (`aarch64`) image — 32-bit
`armv7l` works but compiles slowly (installer warns and continues).
First build takes 3–8 min on quad-core ARM; use good storage and a
5V 3A supply; set a static IP (`nmcli`).

Raspberry Pi (Pi OS / Armbian): same flow; on Pi OS Lite install git
first (`sudo apt install -y git`). Pi Zero (512 MB): swap is added
automatically, expect ~15 min.

x64 mini PC (Ubuntu/Debian): build takes ~1–2 min. For a public
portal, terminate HTTPS in Caddy/Nginx and set `SECURE_COOKIES=1`
in `/etc/aircoins/aircoins.env`, then `systemctl restart aircoins`.

## C. Post-install checklist

1. `systemctl status aircoins`, `journalctl -u aircoins -f`.
2. Open `http://<board-ip>:8080/` → dashboard, `/healthz` → `ok`.
3. `/routers`: register each MikroTik (API `8728`/`8729`), allow the
   board IP under RouterOS `/ip service`.
4. Point the hotspot login page at
   `http://<board-ip>:8080/portal/login?mac=$(mac)&ip=$(ip)&...`
   (full snippet in `README.md`).
5. Generate voucher batches under `/vouchers`.

Paths: binary `/opt/aircoins/aircoins-controller`, DB
`/var/lib/aircoins/aircoins.db`, master key
`/var/lib/aircoins/secret.key` (`0600` — back it up, passwords are
unrecoverable without it), config `/etc/aircoins/aircoins.env`,
unit `/etc/systemd/system/aircoins.service`.

## D. Manual install (no script)

```bash
go version            # need the version in go.mod (>= 1.26), https://go.dev/dl/
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o aircoins-controller .
ADDR=:8080 DB_PATH=/var/lib/aircoins/aircoins.db \
SECRET_KEY_PATH=/var/lib/aircoins/secret.key \
PORTAL_NAME="Aircoins Hotspot" ./aircoins-controller
```

Copy the systemd unit from `install.sh` section 5, adjusting
`User/WorkingDirectory/EnvironmentFile/ExecStart/ReadWritePaths`.

## E. Troubleshooting

- `Unsupported CPU architecture`: use a 64-bit image (x86_64/aarch64).
- Build OOM on 512 MB: re-run, installer adds `/swapfile` + `-p=2`.
- Distro Go too old: installer fetches the official tarball
  (`--go-version`, default = the version in `go.mod`).
- Build aborts with `usage: link [options] main.o`: the version handed to
  `-ldflags` contained spaces — Debian ships `VERSION="13 (trixie)"`, which
  the installer used to pick up (fixed in the current `install.sh`, and the
  version is now sanitized). Use the latest script, or run
  `sudo AIRCOINS_VERSION=dev ./install.sh`.
- HTTP 500 `template error` on `/routers/<id>`: the device-manager page used
  to abort on its cached-data branch (a helper was called with an `int` where
  it expects an `int64`). Fixed in the current source: rebuild from the latest
  `main` and restart the service.
- `Service unhealthy`: `journalctl -u aircoins -e`; check port clash
  (`ss -tlnp`) and `DB_PATH` writability.
- Portal "not linked": set a router portal tag matching hotspot
  `server-name`, or tick Default portal.
- Router offline: check IP/API port, `/ip service` allowed address,
  API user group, firewall.
- Lost `secret.key`: router passwords are unrecoverable, re-enter them.

