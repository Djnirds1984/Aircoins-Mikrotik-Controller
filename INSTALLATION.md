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
systemd unit that serves the panel on **port 80** (the unprivileged
`aircoins` user gets `CAP_NET_BIND_SERVICE` — see section F), writes
`/etc/aircoins/aircoins.env`, opens the firewall, and smoke-tests
`/healthz`.

## A. One-line install

```bash
git clone https://github.com/Djnirds1984/Aircoins-Mikrotik-Controller.git
cd Aircoins-Mikrotik-Controller
sudo ./install.sh
```

Variants:

```bash
sudo ./install.sh --port 8080 --portal-name "My Hotspot"   # move off port 80
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
2. Open `http://<board-ip>/` → dashboard, `/healthz` → `ok` (port 80 by
   default, see section F).
3. `/routers`: register each MikroTik. Pick the **Connection method** that
   matches your RouterOS setup:
   - `REST over HTTPS (www-ssl)` / `REST over HTTP (www)` — the v7 REST API,
     served by the `www-ssl` / `www` service. Leave **Web port for REST** empty
     for HTTPS (443); HTTP needs the `www` port typed explicitly (80, or
     whatever `/ip service print` shows).
   - `API (8728)` / `API-SSL (8729)` — the legacy binary API (`/ip service
     api`), kept for RouterOS 6 and older setups.
   - `Auto` tries REST first, then the binary API, and remembers whichever
     answered, so later page loads connect straight away.

   Allow the board IP under RouterOS `/ip service` either way.
4. Point the hotspot login page at
   `http://<board-ip>/portal/login?mac=$(mac)&ip=$(ip)&...`
   (full snippet in `README.md`).
5. Generate voucher batches under `/vouchers`.
6. `/tools` manages ZeroTier on this panel host. When the official
   `zerotier-cli` is absent, the page offers an Install button. The button
   invokes only the root-owned `/usr/local/sbin/aircoins-install-zerotier`
   helper, which `install.sh` permits for the `aircoins` user through
   `/etc/sudoers.d/aircoins-tools`. The helper accepts no arguments and
   installs from ZeroTier's official `https://install.zerotier.com` script.
   On an existing deployment, rerun `sudo ./install.sh` to provision this
   helper and its updated systemd unit. The page can then show service and
   node status, Join/Leave networks without accepting shell input, and list
   each joined network with its tunnel interface and assigned IP addresses.
   Those come from `zerotier-cli -j listnetworks` (`portDeviceName` and
   `assignedAddresses`); when ZeroTier reports no managed address for a
   network - for example a DHCP or manually addressed network - the panel
   falls back to the addresses the kernel has on that interface.

Paths: binary `/opt/aircoins/aircoins-controller`, DB
`/var/lib/aircoins/aircoins.db`, master key
`/var/lib/aircoins/secret.key` (`0600` — back it up, passwords are
unrecoverable without it), config `/etc/aircoins/aircoins.env`,
unit `/etc/systemd/system/aircoins.service`.

## D. Manual install (no script)

```bash
go version            # need the version in go.mod (>= 1.26), https://go.dev/dl/
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o aircoins-controller .
sudo ADDR=:80 DB_PATH=/var/lib/aircoins/aircoins.db \
SECRET_KEY_PATH=/var/lib/aircoins/secret.key \
PORTAL_NAME="Aircoins Hotspot" ./aircoins-controller
```

Port 80 is privileged: run it with `sudo` as above, or use `ADDR=:8080`
without root (section F lists the capability options).

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
  (`ss -tlnp` — an existing web server often owns port 80) and `DB_PATH`
  writability.
- `bind: permission denied` while listening on port 80: the unit lost
  `AmbientCapabilities=CAP_NET_BIND_SERVICE`, or the binary was started by hand
  without the capability — section F has the fixes.
- Portal "not linked": set a router portal tag matching hotspot
  `server-name`, or tick Default portal.
- Router offline: check IP/port, `/ip service` allowed address, API user group,
  firewall. With `REST` selected the service must be `www` or `www-ssl` —
  `/ip service print` shows which are enabled and on which port; type that port
  into **Web port for REST** (80 must be typed explicitly, never left blank).
  `Auto` accepts either protocol, so it is the quickest way to find out.
- Lost `secret.key`: router passwords are unrecoverable, re-enter them.

## F. The HTTP port (80 by default)

The installer serves the panel on **port 80**, so the dashboard is
`http://<board-ip>/` and the portal is
`http://<board-ip>/portal/login?mac=$(mac)&ip=$(ip)&...` - no port suffix.
`ADDR` in `/etc/aircoins/aircoins.env` is the only knob that decides the
listen port:

```bash
sudo sed -i 's|^ADDR=.*|ADDR=0.0.0.0:8080|' /etc/aircoins/aircoins.env   # move off 80
sudo systemctl restart aircoins
```

A fresh install picks the port with `--port 8080` (or `--addr host:port`);
`install.sh` opens that port in the firewall. Every link the panel emits is
relative (`/portal/login?...`, the flash redirects) and it never prints its own
host or port, so any port and a reverse proxy both work unchanged.

### Why it can bind a privileged port as a normal user

The unit runs as the unprivileged `aircoins` user, so `install.sh` writes
`AmbientCapabilities=CAP_NET_BIND_SERVICE` into it. Keep that line, and leave
`CapabilityBoundingSet=` unset so the `sudo`-based ZeroTier helper can still
acquire CAP_SETUID/CAP_SETGID. Verified on Linux: a process holding the ambient
capability binds a privileged port and its `sudo` child still runs as uid 0.

If you manage the unit yourself, the alternatives are:

- `sudo setcap cap_net_bind_service=+ep /opt/aircoins/aircoins-controller` -
  scoped to the binary only, but file capabilities are ignored on `nosuid`
  mounts (`/tmp` is one, `/opt` is not) and are lost whenever the binary is
  replaced, so `install.sh` would have to reapply them.
- `sudo sysctl -w net.ipv4.ip_unprivileged_port_start=80` - system-wide;
  persist it in `/etc/sysctl.d/`.
- `ADDR=0.0.0.0:8080` - needs no privilege at all.

A manual run that reports `bind: permission denied` on port 80 hit exactly this
case: run it with `sudo`, add the capability, or use `ADDR=:8080`.

### If something else already owns port 80

Check first with `sudo ss -tlnp | grep ':80 '`; then move the panel
(`ADDR=0.0.0.0:8080` plus an iptables redirect) or reverse proxy it.

### Keep the panel on 8080 and redirect 80 to it

```bash
sudo ./install.sh --port 8080          # or ADDR=0.0.0.0:8080 + restart
sudo iptables -t nat -A PREROUTING -p tcp --dport 80 -j REDIRECT --to-ports 8080
sudo apt-get install -y iptables-persistent && sudo netfilter-persistent save
```

No capability is needed in this layout. The redirect happens in the network
stack, so `curl http://127.0.0.1/` from the board itself still needs `:8080`
(add an OUTPUT rule if you want that too).

### Reverse proxy on 80 → 127.0.0.1:8080

nginx:

```nginx
server {
    listen 80 default_server;
    server_name _;
    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

Caddy: `:80 { reverse_proxy 127.0.0.1:8080 }`. Set `ADDR=127.0.0.1:8080` in
`/etc/aircoins/aircoins.env` so only the proxy can reach the panel (it then
needs no privileged port at all), and `SECURE_COOKIES=1` once the proxy
terminates TLS. The panel already honours `X-Forwarded-For` / `X-Real-IP` for
client addresses.

Whichever layout you pick, keep the form action in the hotspot login page and
the walled-garden entry for the panel in sync with the address clients use
(`dst-port` left empty allows any port).

### MikroTik side: which address answers

- `192.168.254.139` is the board's own address. When MikroTik hands it out
  over DHCP, pin it so the URL cannot move:
  `/ip dhcp-server lease make-static [find address=192.168.254.139]`, then
  `/ip dns static add name=aircoins address=192.168.254.139` to use
  `http://aircoins/`.
- The router's own address (`192.168.254.1`) keeps serving the hotspot login
  page, and the hotspot service owns the router's port 80, so it cannot be
  re-pointed at the panel. To reach the panel through the router's address,
  DNAT a free port instead:

  ```text
  /ip firewall nat add chain=dstnat protocol=tcp dst-port=8090 \
      dst-address=192.168.254.1 action=dst-nat \
      to-addresses=192.168.254.139 to-ports=80
  ```

- The panel's own port is unrelated to **Web port for REST** on `/routers`,
  which is the router's `www`/`www-ssl` port.

