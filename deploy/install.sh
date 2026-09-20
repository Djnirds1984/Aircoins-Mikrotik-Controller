#!/usr/bin/env bash
# Install the Aircoins hotspot controller on a Debian-based SBC or mini PC
# (Raspberry Pi OS, Armbian, Ubuntu Server, Debian).
#
# Run as root from the directory containing the release binary:
#
#   sudo ./install.sh ./aircoins-linux-arm64   # use -amd64 or -armv7 as needed
#
# Or let the script pick the correct release asset for the current host:
#
#   sudo ./install.sh                          # auto-detects arm64/armv7/amd64
#
set -euo pipefail

INSTALL_DIR=/opt/aircoins
DATA_DIR=/var/lib/aircoins
SERVICE_USER=aircoins
SERVICE_NAME=aircoins
VERSION=${AIRCOINS_VERSION:-latest}

detect_arch() {
  local machine
  machine="$(uname -m)"
  case "$machine" in
    aarch64|arm64) echo "arm64" ;;
    armv7l)        echo "armv7" ;;
    x86_64|amd64)  echo "amd64" ;;
    *) echo "unsupported architecture: $machine" >&2; exit 1 ;;
  esac
}

ARCH_TAG=$(detect_arch)
BIN_SRC=${1:-""}

if [[ -z "$BIN_SRC" || ! -e "$BIN_SRC" ]]; then
  # No usable path supplied: download the release asset for this architecture.
  BIN_SRC="/tmp/aircoins-linux-$ARCH_TAG"
  echo "downloading aircoins-$VERSION for linux-$ARCH_TAG ..."
  wget -q "https://github.com/Djnirds1984/Aircoins-Mikrotik-Controller/releases/download/$VERSION/aircoins-linux-$ARCH_TAG" -O "$BIN_SRC"
  chmod +x "$BIN_SRC"
fi

if [[ $EUID -ne 0 ]]; then
  echo "this script must run as root" >&2
  exit 1
fi

if [[ ! -x "$BIN_SRC" ]]; then
  echo "usage: $0 [path-to-aircoins-binary]" >&2
  echo "       the binary must match linux-$ARCH_TAG (aarch64/armv7/amd4)" >&2
  exit 1
fi

echo "creating the $SERVICE_USER service account"
useradd --system --home "$DATA_DIR" --shell /usr/sbin/nologin "$SERVICE_USER" 2>/dev/null || \
  echo "  (account already exists)"

echo "creating $DATA_DIR"
install -d -o "$SERVICE_USER" -g "$SERVICE_USER" -m 0750 "$DATA_DIR"

echo "installing the binary to $INSTALL_DIR"
install -d -m 0755 "$INSTALL_DIR"
install -m 0755 "$BIN_SRC" "$INSTALL_DIR/$SERVICE_NAME"

echo "installing the systemd unit"
install -m 0644 "$(dirname "$0")/$SERVICE_NAME.service" "/etc/systemd/system/$SERVICE_NAME.service"

systemctl daemon-reload

if systemctl is-enabled --quiet "$SERVICE_NAME" 2>/dev/null; then
  echo "restarting $SERVICE_NAME"
  systemctl restart "$SERVICE_NAME"
else
  echo "enabling and starting $SERVICE_NAME"
  systemctl enable --now "$SERVICE_NAME"
fi

sleep 2
systemctl --no-pager --lines=0 status "$SERVICE_NAME" || true

cat <<EOF

Installed. Next steps:

  1. Edit /etc/systemd/system/$SERVICE_NAME.service and set
     -base-url to the address hotspot clients use to reach this panel, then
       systemctl daemon-reload && systemctl restart $SERVICE_NAME
  2. Open http://$(hostname -I | awk '{print $1}'):8080/admin and create the
     administrator account.
  3. Allow the panel in the router's walled garden so pre-authentication clients
     can reach it:
       /ip hotspot walled-garden add dst-host=<panel host> action=allow
       /ip hotspot walled-garden ip add dst-address=<panel ip> action=accept
  4. The master key is generated at $DATA_DIR/secret.key. Back it up: without it
     the stored router passwords cannot be decrypted.

EOF
