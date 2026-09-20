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

## Step 3: First Run (Development Mode)

Start the controller in development mode with a simulated router:

```bash
go run ./cmd/aircoins -fake-router -admin-addr 127.0.0.1:8080 -data-dir ./data-dev
```

Flags:
- `-fake-router`: Simulates a Mikrotik device for testing
- `-admin-addr 127.0.0.1:8080`: Admin panel address
- `-data-dir ./data-dev`: Database and config storage location

## Step 4: Access the Web Panel

1. Open a browser to: `http://127.0.0.1:8080/admin`
2. Complete the first-run setup wizard to create an admin account
3. Add your first router via **Routers → Add router**

## Step 5: Test Against Real Hardware

Test connection to your Mikrotik router:

```bash
go run ./cmd/aircoins-probe -host 192.168.88.1 -user admin -pass your_password
```

With write permissions testing:

```bash
go run ./cmd/aircoins-probe -host 192.168.88.1 -user admin -pass your_password -allow-write
```

## Step 6: Production Deployment

Build for your target architecture:

```bash
make build-linux-amd64   # x86_64 mini PCs
make build-linux-arm64   # Raspberry Pi 3/4/5, Orange Pi 5, etc.
make build-linux-armv7   # 32-bit SBCs (Orange Pi Zero, etc.)
```

Binaries are placed in `bin/`.

### Install on SBC/Mini PC

Copy the appropriate binary to your device:

```bash
scp bin/aircoins-linux-arm64 user@your-device-ip:~/
ssh user@your-device-ip
sudo ./install.sh ./aircoins-linux-arm64
```

The install script:
- Creates a system user `aircoins`
- Installs the binary to `/opt/aircoins/aircoins`
- Sets up systemd service with security hardening
- Starts the service automatically

Access: `http://<device-ip>:8080/admin`

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
- View logs: `sudo journalctl -fu aircoins`

## Directory Structure

```
cmd/aircoins          # Main controller binary
cmd/aircoins-probe    # Connection testing tool
deploy/               # Systemd service and install script
docs/                 # Documentation
internal/             # Source code
```