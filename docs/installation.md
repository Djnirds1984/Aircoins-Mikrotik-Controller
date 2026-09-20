# Installation Guide

Complete instructions for installing and running the Aircoins Mikrotik Controller from GitHub.

## Prerequisites

- **Go 1.26+** - [Download Go](https://go.dev/dl/)
- **Git**
- **Make** (for production builds)

## Step 1: Clone the Repository

```bash
git clone https://github.com/Djnirds1984/Aircoins-Mikrotik-Controller.git
cd Aircoins-Mikrotik-Controller
```

## Step 2: Install Dependencies

```bash
go mod download
```

## Step 3: Test Connection to Your Real Router

First, test connectivity to your actual Mikrotik board:

```bash
go run ./cmd/aircoins-probe -host 192.168.88.1 -user admin -pass your_real_password
```

For write permission testing (tests API write access):

```bash
go run ./cmd/aircoins-probe -host 192.168.88.1 -user admin -pass your_real_password -allow-write
```

## Step 4: First Run (Development Mode)

Start the controller pointing at your real hardware:

```bash
go run ./cmd/aircoins -admin-addr 127.0.0.1:8080 -data-dir ./data-dev
```

Flags:
- `-admin-addr 127.0.0.1:8080`: Admin panel address
- `-data-dir ./data-dev`: Database and config storage location

## Step 5: Access the Web Panel and Add Router

1. Open a browser to: `http://127.0.0.1:8080/admin`
2. Complete the first-run setup wizard to create an admin account
3. Add your real router via **Routers → Add router** using the real IP, credentials, and API port
4. Use the **Test Connection** feature to verify the connection before saving

## Step 6: Production Deployment

Build for your target architecture (optional - the installer can download pre-built binaries):

```bash
make build-linux-amd64   # x86_64 mini PCs
make build-linux-arm64   # Raspberry Pi 3/4/5, Orange Pi 5, etc.
make build-linux-armv7   # 32-bit SBCs (Orange Pi Zero, etc.)
```

Binaries are placed in `bin/`.

### Install on SBC/Mini PC

The easiest way to install on your target device is the one-command installer,
which auto-detects your architecture:

```bash
# On the target device:
curl -fsSL https://raw.githubusercontent.com/Djnirds1984/Aircoins-Mikrotik-Controller/main/deploy/install.sh | sudo bash
```

Or from a local clone:

```bash
sudo ./deploy/install.sh
```

The installer will:
- Detect your CPU architecture (arm64, armv7, or amd64)
- Download the matching release binary from GitHub
- Create a system user `aircoins`
- Install the binary to `/opt/aircoins/aircoins`
- Set up a systemd service with security hardening
- Start and enable the service

Access the panel at: `http://<device-ip>:8080/admin`

## Step 7: Configure Mikrotik Router

Enable API access on your Mikrotik:

```routeros
/ip service enable api
```

API user must have `read`, `write`, and `policy` permissions.

## Troubleshooting

- Ensure port 8080 is not already in use
- Verify Go version: `go version` (must be 1.26+)
- Check firewall settings when accessing from another device
- View development logs in terminal, or `sudo journalctl -fu aircoins` in production
- Verify API service is enabled on Mikrotik: `/ip service enable api`
- Confirm API user has read/write/policy permissions

## Directory Structure

```
cmd/aircoins          # Main controller binary
cmd/aircoins-probe    # Connection testing tool
deploy/               # Systemd service and install script
docs/                 # Documentation
internal/             # Source code
```