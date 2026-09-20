#!/usr/bin/env bash
set -euo pipefail

GREEN=$'\033[32m'; YELLOW=$'\033[33m'; RED=$'\033[31m'; BLUE=$'\033[34m'; BOLD=$'\033[1m'; RESET=$'\033[0m'
info()  { echo "${BLUE}>>>${RESET} $*"; }
warn()  { echo "${YELLOW}!${RESET} $*"; }
ok()    { echo "${GREEN}OK${RESET} $*"; }
err()   { echo "${RED}ERROR${RESET} $*" >&2; }

INSTALL_DIR="/opt/aircoins"
DATA_DIR="/var/lib/aircoins"
SERVICE_USER="aircoins"
SERVICE_NAME="aircoins"
REPO_OWNER="Djnirds1984"
REPO_NAME="Aircoins-Mikrotik-Controller"
VERSION="${AIRCOINS_VERSION:-latest}"

if [[ $EUID -ne 0 ]]; then err "This installer must be run as root (use sudo)."; exit 1; fi
if ! command -v wget >/dev/null 2>&1; then err "wget is required but not installed."; exit 1; fi

machine="$(uname -m)"
case "$machine" in
  aarch64|arm64) ARCH_TAG="arm64" ;;
  armv7l|armv6l) ARCH_TAG="armv7" ;;
  x86_64|amd64)  ARCH_TAG="amd64" ;;
  *) err "Unsupported architecture: $machine"; err "Supported: arm64, armv7, amd64"; exit 1 ;;
esac
info "Detected architecture: ${ARCH_TAG} (${machine})"

if [[ "$VERSION" == "latest" ]]; then
  DOWNLOAD_URL="https://github.com/${REPO_OWNER}/${REPO_NAME}/releases/latest/download/aircoins-linux-${ARCH_TAG}"
else
  DOWNLOAD_URL="https://github.com/${REPO_OWNER}/${REPO_NAME}/releases/download/${VERSION}/aircoins-linux-${ARCH_TAG}"
fi

BIN_TMP="/tmp/aircoins-install-tmp"
info "Downloading aircoins ${VERSION} for linux-${ARCH_TAG} ..."
wget -q --tries=3 --timeout=30 "$DOWNLOAD_URL" -O "$BIN_TMP" || { err "Download failed: $DOWNLOAD_URL"; err "Check release exists or internet access."; exit 1; }
chmod +x "$BIN_TMP"
info "Downloaded $(wc -c < "$BIN_TMP" | awk '{printf "%d", $1/1024}') KiB"

if ! head -c 4 "$BIN_TMP" | od -An -tx1 | grep -q "7f 45 4c 46"; then
  err "Not a valid ELF binary."; rm -f "$BIN_TMP"; exit 1
fi
ok "Binary verified"

info "Creating system user '${SERVICE_USER}' ..."
useradd --system --home "$DATA_DIR" --shell /usr/sbin/nologin "$SERVICE_USER" 2>/dev/null || warn "  (user exists)"
ok "Service user ready"

info "Creating directories ..."
install -d -o "$SERVICE_USER" -g "$SERVICE_USER" -m 0750 "$DATA_DIR"
install -d -m 0755 "$INSTALL_DIR"
ok "Directories created"

info "Installing binary ..."
install -m 0755 "$BIN_TMP" "$INSTALL_DIR/$SERVICE_NAME"
rm -f "$BIN_TMP"
ok "Binary installed"

info "Installing systemd unit ..."
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVICE_FILE="$SCRIPT_DIR/$SERVICE_NAME.service"
if [[ ! -f "$SERVICE_FILE" ]]; then
  warn "Service file not found locally. Fetching from GitHub ..."
  wget -q "https://raw.githubusercontent.com/${REPO_OWNER}/${REPO_NAME}/main/deploy/${SERVICE_NAME}.service" -O /tmp/"$SERVICE_NAME".service
  SERVICE_FILE="/tmp/${SERVICE_NAME}.service"
fi
install -m 0644 "$SERVICE_FILE" "/etc/systemd/system/$SERVICE_NAME.service"
[[ -f /tmp/$SERVICE_NAME.service ]] && rm -f /tmp/$SERVICE_NAME.service
ok "Systemd unit installed"

info "Enabling and starting ${SERVICE_NAME} ..."
systemctl daemon-reload
systemctl enable "$SERVICE_NAME" 2>/dev/null || true
systemctl restart "$SERVICE_NAME"
sleep 2

echo ""
if systemctl is-active --quiet "$SERVICE_NAME"; then
  ok "Service is running"
  systemctl --no-pager --lines=5 status "$SERVICE_NAME" || true
else
  err "Service failed to start. Check logs: journalctl -u $SERVICE_NAME -n 50"
  exit 1
fi

PANEL_IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
cat <<EOF

============================================================
 Aircoins Mikrotik Controller -- installed successfully
============================================================

Admin panel:   http://${PANEL_IP}:8080/admin
Data dir:      ${DATA_DIR}
Install dir:   ${INSTALL_DIR}

First steps:
  1. Open http://${PANEL_IP}:8080/admin in your browser
  2. Create administrator account
  3. Add your Mikrotik router (Routers -> Add router)
  4. Enable API on router: /ip service enable api

Important:
  - Back up ${DATA_DIR}/secret.key -- needed to decrypt router passwords
  - Edit systemd unit to change ports: systemctl edit --full $SERVICE_NAME

Commands:
  sudo journalctl -fu $SERVICE_NAME      # follow logs
  sudo systemctl restart $SERVICE_NAME    # restart
  sudo systemctl status  $SERVICE_NAME    # status

EOF