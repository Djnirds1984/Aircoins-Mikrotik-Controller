# Getting Started

<cite>
**Referenced Files in This Document**
- [README.md](file://README.md)
- [INSTALLATION.md](file://INSTALLATION.md)
- [install.sh](file://install.sh)
- [main.go](file://main.go)
- [config.go](file://config.go)
</cite>

## Table of Contents
1. [Introduction](#introduction)
2. [System Requirements](#system-requirements)
3. [Installation Procedures](#installation-procedures)
4. [Initial Configuration](#initial-configuration)
5. [First-Time Setup and Login](#first-time-setup-and-login)
6. [Main Interfaces](#main-interfaces)
7. [Admin Account Management](#admin-account-management)
8. [Verification Steps](#verification-steps)
9. [Troubleshooting Guide](#troubleshooting-guide)
10. [Next Steps](#next-steps)

## Introduction
Aircoins MikroTik Controller is a centralized controller for MikroTik Hotspot deployments. It provides:
- A captive portal for hotspot clients.
- An operator panel for managing routers, vouchers, sessions, rates, and settings.
- SQLite-backed storage with encrypted router credentials.
- A systemd service installation script that auto-detects hardware and OS.

The controller exposes two primary URLs on the host where it runs:
- Captive portal at `http://<board-ip>/`.
- Operator panel at `http://<board-ip>/admin/` by default.

On first boot, the controller creates one operator account. If you do not set an admin password, it generates a random one and logs it once. You can manage credentials later through the panel or using the `aircoins-controller passwd` command.

**Section sources**
- [README.md:1-49](file://README.md#L1-L49)

## System Requirements
Supported platforms include:
- Ubuntu 22.04 / 24.04 (x64 mini PCs).
- Debian 11 / 12 (x64 mini PCs).
- Armbian on SBC boards such as Orange Pi, Raspberry Pi, Rockchip, Allwinner, and Amlogic.

Recommended board image:
- Prefer 64-bit images (`aarch64`) on ARM boards; 32-bit ARM works but compiles slowly.

Minimum software requirements:
- Go toolchain version matching the project’s requirement.
- Standard Linux utilities provided by the installer when using `install.sh`.

Memory considerations:
- Boards with very low RAM may need swap during build. The installer automatically adds swap when needed.

**Section sources**
- [INSTALLATION.md:1-21](file://INSTALLATION.md#L1-L21)
- [INSTALLATION.md:50-63](file://INSTALLATION.md#L50-L63)
- [INSTALLATION.md:123-133](file://INSTALLATION.md#L123-L133)

## Installation Procedures

### Option A: Install with `install.sh`
This is the recommended path for Ubuntu, Debian, and Armbian systems.

Steps:
1. Clone the repository.
2. Run the installer as root.
3. Review the printed URLs and health check result.
4. Log in to the operator panel and register your MikroTik routers.

Example commands:
```bash
git clone https://github.com/Djnirds1984/Aircoins-Mikrotik-Controller.git
cd Aircoins-Mikrotik-Controller
sudo ./install.sh
```

Useful variants:
```bash
sudo ./install.sh --port 8080 --portal-name "My Hotspot"
sudo ./install.sh --addr 127.0.0.1:8080 --skip-firewall --no-service
sudo ./install.sh --uninstall
```

What the installer does:
- Detects CPU architecture and OS.
- Installs required packages.
- Uses the distro Go toolchain if new enough, otherwise downloads the exact required version.
- Creates the `aircoins` service user.
- Builds the binary with CGO disabled.
- Installs a hardened systemd unit.
- Writes `/etc/aircoins/aircoins.env`.
- Opens the firewall for the selected port.
- Performs a smoke test against `/healthz`.

Important paths after installation:
- Binary: `/opt/aircoins/aircoins-controller`
- Database: `/var/lib/aircoins/aircoins.db`
- Master key: `/var/lib/aircoins/secret.key`
- Environment file: `/etc/aircoins/aircoins.env`
- Service unit: `/etc/systemd/system/aircoins.service`

**Section sources**
- [INSTALLATION.md:23-48](file://INSTALLATION.md#L23-L48)
- [INSTALLATION.md:101-105](file://INSTALLATION.md#L101-L105)
- [install.sh:1-21](file://install.sh#L1-L21)
- [install.sh:282-323](file://install.sh#L282-L323)
- [install.sh:385-419](file://install.sh#L385-L419)
- [install.sh:421-447](file://install.sh#L421-L447)
- [install.sh:449-502](file://install.sh#L449-L502)

### Option B: Manual Build and Run
Use this option when you prefer to control the build process yourself.

Steps:
1. Ensure Go meets the project requirement.
2. Build the binary.
3. Run the binary with environment variables.
4. Optionally install a systemd unit manually.

Example commands:
```bash
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o aircoins-controller .
sudo ADDR=:80 DB_PATH=/var/lib/aircoins/aircoins.db \
SECRET_KEY_PATH=/var/lib/aircoins/secret.key \
PORTAL_NAME="Aircoins Hotspot" ./aircoins-controller
```

Notes:
- Port 80 requires elevated privileges unless you use a capability or run as root.
- Without root, use a non-privileged port such as `ADDR=:8080`.
- After building, copy the systemd unit from the installer documentation and adjust paths.

**Section sources**
- [INSTALLATION.md:107-121](file://INSTALLATION.md#L107-L121)
- [README.md:50-58](file://README.md#L50-L58)

## Initial Configuration
Configuration is primarily environment-based. The installer writes defaults into `/etc/aircoins/aircoins.env`, and you can edit that file to change behavior.

Key environment variables:
- `ADDR`: Listen address. Default is `:80`.
- `DB_PATH`: SQLite database path.
- `SECRET_KEY_PATH`: Path to the master encryption key.
- `AIRCOINS_SECRET_KEY`: Optional master key override.
- `PORTAL_NAME`: Branding shown in the UI and portal.
- `ADMIN_PATH`: URL prefix for the operator panel. Default is `/admin`.
- `DASHBOARD_AT_ROOT`: Set to `1` to move the dashboard to `/` and the portal to `/portal`.
- `PORTAL_TAGLINE`: Welcome text on the portal landing page.
- `PORTAL_SUPPORT`: Contact/help text on the portal landing page.
- `ADMIN_USER`: Operator username seeded on first boot only.
- `ADMIN_PASSWORD`: Password seeded on first boot only.
- `DEFAULT_REDIRECT`: Portal fallback when MikroTik redirect parameters are absent.
- `ADMIN_SESSION_TTL`: Panel session lifetime.
- `API_TIMEOUT`: Per-call RouterOS API timeout.
- `SECURE_COOKIES`: Set to `1` behind HTTPS.
- `VERSION`: Build-time footer version.

Coin slot variables (optional):
- `COIN_NODE_TOKEN`: Shared secret for coin acceptor integration.
- `COIN_SECONDS_PER_PULSE`: Access time per pulse.
- `COIN_CENTS_PER_PULSE`: Face value recorded for reconciliation.
- `COIN_IDLE_TTL`: Idle balance TTL.
- `COIN_MAX_SESSION_MINUTES`: Maximum session duration cap.

After changing environment variables:
```bash
sudo systemctl restart aircoins
```

**Section sources**
- [README.md:70-109](file://README.md#L70-L109)
- [config.go:20-77](file://config.go#L20-L77)
- [install.sh:324-359](file://install.sh#L324-L359)

## First-Time Setup and Login
Follow these steps after installation:

1. Check service status:
   ```bash
   systemctl status aircoins
   journalctl -u aircoins -f
   ```

2. Open the captive portal:
   ```
   http://<board-ip>/
   ```

3. Open the operator panel:
   ```
   http://<board-ip>/admin/
   ```

4. Find the initial operator credentials:
   - If you did not set `ADMIN_PASSWORD`, the controller generated a random password on first boot.
   - View it with:
     ```bash
     journalctl -u aircoins | grep 'initial panel password'
     ```

5. Log in to the operator panel with the generated or configured credentials.

6. Register your MikroTik routers under `/routers`.

7. Point your MikroTik hotspot login page at the portal URL.

**Section sources**
- [README.md:11-49](file://README.md#L11-L49)
- [INSTALLATION.md:65-85](file://INSTALLATION.md#L65-L85)

## Main Interfaces

### Captive Portal
- URL: `http://<board-ip>/`
- Purpose: Guest-facing sign-in page.
- Behavior:
  - Greets devices already online.
  - Offers sign-in and voucher entry.
  - Honors MikroTik redirect parameters such as `mac`, `ip`, `link-login`, and `link-orig`.

Public routes available without signing in:
- `/`
- `/portal/login`
- `/portal/status`
- `/healthz`

**Section sources**
- [README.md:11-19](file://README.md#L11-L19)
- [README.md:44-49](file://README.md#L44-L49)

### Operator Panel
- URL: `http://<board-ip>/admin/`
- Purpose: Administrative dashboard.
- Features:
  - Router inventory and device manager.
  - Voucher generation and management.
  - Sessions, rates, tools, and settings.
- Authentication:
  - Requires operator login.
  - Session cookies are `HttpOnly` and `SameSite=Lax`.

Legacy route compatibility:
- Some routes remain mounted at the root, but they require a session now.

**Section sources**
- [README.md:18-20](file://README.md#L18-L20)
- [README.md:47-49](file://README.md#L47-L49)

## Admin Account Management

### Automatic Password Generation
On a fresh install:
- If `ADMIN_PASSWORD` is unset, the controller generates a random password.
- The password is logged exactly once.
- Restarting the service does not reset a password you changed later.

### Using `aircoins-controller passwd`
Run this command while the service is stopped to manage credentials safely.

Common operations:
- Generate a strong password and print it once.
- Set an explicit password.
- Change the operator username.
- Show the current operator username.
- Revoke all existing sessions.

Recommended usage:
```bash
systemctl stop aircoins
aircoins-controller passwd
systemctl start aircoins
```

Security notes:
- Prefer passing passwords through `ADMIN_PASSWORD` rather than as arguments.
- Changing the password revokes every existing session for that account.
- Minimum password length is enforced.

**Section sources**
- [README.md:21-43](file://README.md#L21-L43)
- [main.go:43-80](file://main.go#L43-L80)
- [main.go:82-192](file://main.go#L82-L192)
- [main.go:287-327](file://main.go#L287-L327)

## Verification Steps
Use these checks to confirm proper operation:

1. Service health:
   ```bash
   curl http://<board-ip>/healthz
   ```
   Expected response: `ok`.

2. Service status:
   ```bash
   systemctl status aircoins
   ```

3. Logs:
   ```bash
   journalctl -u aircoins -f
   ```

4. Captive portal:
   - Open `http://<board-ip>/` in a browser.

5. Operator panel:
   - Open `http://<board-ip>/admin/` and log in.

6. Installed version:
   ```bash
   /opt/aircoins/aircoins-controller --version
   ```

7. Firewall and port:
   - Confirm the selected port is open.
   - If using port 80, ensure the service has the bind capability or sufficient privileges.

**Section sources**
- [INSTALLATION.md:65-69](file://INSTALLATION.md#L65-L69)
- [install.sh:481-502](file://install.sh#L481-L502)
- [main.go:262-270](file://main.go#L262-L270)

## Troubleshooting Guide

### Common Installation Issues
- Unsupported CPU architecture: Use a supported 64-bit image.
- Build out-of-memory on small boards: The installer adds swap automatically.
- Distro Go too old: The installer fetches the official Go tarball when needed.
- Linker error due to spaces in version string: Use the latest installer or set `AIRCOINS_VERSION=dev`.
- HTTP 500 template error on router pages: Rebuild from the latest source and restart.

### Service Health Problems
- `Service unhealthy`: Check recent logs, port clashes, and database writability.
- `bind: permission denied` on port 80:
  - Add `AmbientCapabilities=CAP_NET_BIND_SERVICE` to the systemd unit.
  - Or run with `sudo`.
  - Or use `ADDR=:8080`.

### Network and Routing Problems
- Portal “not linked”: Set a router portal tag matching the hotspot `server-name`, or mark a router as default.
- Router offline:
  - Verify IP/port.
  - Allow the controller IP under RouterOS `/ip service`.
  - Check API user group permissions.
  - Check firewall rules.
  - For REST, ensure the correct `www` or `www-ssl` service is enabled and the web port matches.

### Lost Encryption Key
- If `secret.key` is lost, router passwords cannot be recovered. You must re-enter them.

### Port 80 Alternatives
- Move the panel off port 80:
  ```bash
  sudo sed -i 's|^ADDR=.*|ADDR=0.0.0.0:8080|' /etc/aircoins/aircoins.env
  sudo systemctl restart aircoins
  ```
- Redirect port 80 to another port with iptables.
- Reverse proxy port 80 to `127.0.0.1:8080` with nginx or Caddy.

**Section sources**
- [INSTALLATION.md:123-153](file://INSTALLATION.md#L123-L153)
- [INSTALLATION.md:155-217](file://INSTALLATION.md#L155-L217)
- [INSTALLATION.md:219-258](file://INSTALLATION.md#L219-L258)

## Next Steps
After verifying the controller:
1. Register each MikroTik router in `/routers`.
2. Choose the connection method that matches your RouterOS setup:
   - REST over HTTPS or HTTP.
   - Legacy API or API-SSL.
   - Auto mode tries REST first, then falls back to the binary API.
3. Point the hotspot login form at the portal URL, preserving MikroTik redirect parameters.
4. Generate voucher batches under `/vouchers`.
5. Configure ZeroTier tools under `/tools` if needed.

For detailed MikroTik wiring and voucher configuration, consult the main README.

**Section sources**
- [README.md:167-195](file://README.md#L167-L195)
- [INSTALLATION.md:65-99](file://INSTALLATION.md#L65-L99)