#!/usr/bin/env bash
# Aircoins MikroTik Controller - installer for Debian/Ubuntu systems.
# Targets: Armbian on SBC boards (Orange Pi / Raspberry Pi / Rockchip /
# Allwinner / Amlogic) and x64 mini PCs running Ubuntu/Debian.
#
# Usage: sudo ./install.sh [options]   (see --help for all flags)
# Serves the panel on port 80 (http://<board-ip>/) by default; the service
# user is granted CAP_NET_BIND_SERVICE so it can bind that privileged port.
# Idempotent: re-running upgrades the binary in place.
set -euo pipefail

APP_NAME="aircoins-controller"
SERVICE_NAME="aircoins"
DEFAULT_PORT="80"
DEFAULT_INSTALL_DIR="/opt/aircoins"
DEFAULT_DATA_DIR="/var/lib/aircoins"
DEFAULT_USER="aircoins"
DEFAULT_PORTAL="Aircoins Hotspot"
DEFAULT_GO_VERSION="1.23.5"
MIN_GO_MAJOR=1
MIN_GO_MINOR=23
GO_MIRROR="${GO_MIRROR:-https://go.dev/dl}"

PORT="$DEFAULT_PORT"
ADDR=""
INSTALL_DIR="$DEFAULT_INSTALL_DIR"
DATA_DIR="$DEFAULT_DATA_DIR"
SVC_USER="$DEFAULT_USER"
PORTAL_NAME="$DEFAULT_PORTAL"
GO_VERSION="$DEFAULT_GO_VERSION"
REPO_URL=""
BRANCH="main"
CONFIGURE_FIREWALL=1
ENABLE_SERVICE=1
DO_UNINSTALL=0

log()  { printf '\033[1;32m[+] %s\033[0m\n' "$*"; }
warn() { printf '\033[1;33m[!] %s\033[0m\n' "$*" >&2; }
die()  { printf '\033[1;31m[x] %s\033[0m\n' "$*" >&2; exit 1; }

# The ADDR previous releases wrote into the env file. It is a shipped default,
# not a local choice, so an upgrade may move it - see the env file section.
LEGACY_DEFAULT_ADDR="0.0.0.0:8080"

# addr_from_env FILE - print the ADDR the service will actually use, or fail
# when the file sets none. Quotes and trailing blanks are tolerated, and the
# last ADDR line wins because systemd applies EnvironmentFile lines in order.
addr_from_env() {
  local value
  value="$(sed -n 's/^[[:space:]]*ADDR[[:space:]]*=[[:space:]]*//p' "$1" 2>/dev/null | tail -1)"
  value="${value%"${value##*[![:space:]]}"}"
  value="${value%\"}"
  value="${value#\"}"
  value="${value%\'}"
  value="${value#\'}"
  [[ -n "$value" ]] || return 1
  printf '%s' "$value"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --port)          PORT="${2:?}"; shift 2 ;;
    --addr)          ADDR="${2:?}"; shift 2 ;;
    --install-dir)   INSTALL_DIR="${2:?}"; shift 2 ;;
    --data-dir)      DATA_DIR="${2:?}"; shift 2 ;;
    --user)          SVC_USER="${2:?}"; shift 2 ;;
    --portal-name)   PORTAL_NAME="${2:?}"; shift 2 ;;
    --go-version)    GO_VERSION="${2:?}"; shift 2 ;;
    --repo)          REPO_URL="${2:?}"; shift 2 ;;
    --branch)        BRANCH="${2:?}"; shift 2 ;;
    --skip-firewall) CONFIGURE_FIREWALL=0; shift ;;
    --no-service)    ENABLE_SERVICE=0; shift ;;
    --uninstall)     DO_UNINSTALL=1; shift ;;
    -h|--help)
      sed -n '1,/^while/p' "$0" | sed 's/^# \{0,1\}//'
      echo "Options: --port --addr --install-dir --data-dir --user"
      echo "  --portal-name --go-version --repo --branch --skip-firewall"
      echo "  --no-service --uninstall -h/--help"
      echo "Dashboard is served on port $DEFAULT_PORT by default (--port 8080"
      echo "  moves it back off the privileged port)."
      echo "Env: AIRCOINS_VERSION=1.2.3 (footer version), GO_MIRROR,"
      echo "  GOCACHE, GOMODCACHE"
      exit 0 ;;
    *) die "Unknown option: $1 (see --help)" ;;
  esac
done

if [[ -z "$ADDR" ]]; then
  [[ "$PORT" =~ ^[0-9]+$ ]] || die "--port must be numeric, got '$PORT'"
  (( PORT >= 1 && PORT <= 65535 )) || die "--port out of range: $PORT"
  ADDR="0.0.0.0:${PORT}"
fi

[[ "${EUID:-$(id -u)}" -eq 0 ]] || die "Run as root: sudo ./install.sh"

# --- 1. Hardware / board auto-detection -------------------------------
ARCH_RAW="$(uname -m)"
case "$ARCH_RAW" in
  x86_64|amd64)      GOARCH="amd64";   ARCH_LABEL="x64 (Intel/AMD 64-bit)" ;;
  aarch64|arm64)     GOARCH="arm64";   ARCH_LABEL="ARM 64-bit (Pi 3+/4/5, Orange Pi, Rockchip)" ;;
  armv7l|armv6l|arm) GOARCH="armv6l";   ARCH_LABEL="ARM 32-bit (${ARCH_RAW})" ;;
  riscv64)           GOARCH="riscv64";  ARCH_LABEL="RISC-V 64-bit" ;;
  *) die "Unsupported CPU architecture: $ARCH_RAW" ;;
esac

BOARD="generic"
if [[ -r /proc/device-tree/model ]]; then
  BOARD="$(tr -d '\0' < /proc/device-tree/model)"
elif [[ -r /sys/firmware/devicetree/base/model ]]; then
  BOARD="$(tr -d '\0' < /sys/firmware/devicetree/base/model)"
fi
BOARD="$(echo "$BOARD" | xargs || true)"
[[ -n "$BOARD" ]] || BOARD="generic x86 mini PC"

# read_os_release KEY -> prints the value of KEY from /etc/os-release.
# It is read inside a subshell on purpose: sourcing os-release in this shell
# defines NAME/ID/VERSION/..., and Debian ships VERSION="13 (trixie)". That
# leaked into $VERSION and corrupted the -ldflags version below.
read_os_release() {
  (
    set +u
    # shellcheck disable=SC1091
    . /etc/os-release 2>/dev/null || :
    printf '%s' "${!1:-}"
  )
}

IS_ARMBIAN=0
PRETTY_OS="$(uname -s)"
if [[ -r /etc/os-release ]]; then
  PRETTY_OS="$(read_os_release PRETTY_NAME)"
  [[ -n "$PRETTY_OS" ]] || PRETTY_OS="$(uname -s)"
  # Armbian images report ID=debian, so match ID, ID_LIKE and PRETTY_NAME.
  OS_HINTS="$(printf '%s %s %s' "$(read_os_release ID)" "$(read_os_release ID_LIKE)" "$PRETTY_OS" \
    | tr '[:upper:]' '[:lower:]')"
  case "$OS_HINTS" in
    *armbian*) IS_ARMBIAN=1 ;;
  esac
fi

MEM_MB=0
if [[ -r /proc/meminfo ]]; then
  MEM_KB="$(awk '/^MemTotal:/ {print $2}' /proc/meminfo || echo 0)"
  MEM_MB=$(( MEM_KB / 1024 ))
fi
CPU_MODEL="$(grep -m1 -i 'model name' /proc/cpuinfo 2>/dev/null | cut -d: -f2 | xargs || true)"
[[ -n "$CPU_MODEL" ]] || CPU_MODEL="$ARCH_RAW"
NPROC="$(nproc 2>/dev/null || echo 1)"

log "Detected hardware"
echo "    Board : $BOARD"
echo "    CPU   : $CPU_MODEL ($NPROC cores, $ARCH_RAW -> Go $GOARCH)"
echo "    Arch  : $ARCH_LABEL"
echo "    OS    : $PRETTY_OS (armbian=$IS_ARMBIAN)"
echo "    RAM   : ${MEM_MB} MB"
if [[ "$GOARCH" == "armv6l" ]]; then
  warn "32-bit ARM: works but slow; a 64-bit Armbian image is recommended."
fi
if (( MEM_MB > 0 && MEM_MB < 512 )); then
  warn "Only ${MEM_MB} MB RAM: a 1 GB swap file is created below if none exists."
fi

# --- 2. Uninstall path --------------------------------------------------
if (( DO_UNINSTALL )); then
  log "Removing $SERVICE_NAME ..."
  systemctl disable --now "$SERVICE_NAME" 2>/dev/null || true
  rm -f "/etc/systemd/system/${SERVICE_NAME}.service"
  rm -f /etc/sudoers.d/aircoins-tools /usr/local/sbin/aircoins-install-zerotier
  systemctl daemon-reload 2>/dev/null || true
  rm -rf "$INSTALL_DIR"
  id "$SVC_USER" >/dev/null 2>&1 && userdel "$SVC_USER" 2>/dev/null || true
  log "Uninstalled. Data kept at $DATA_DIR and /etc/aircoins (remove manually)."
  exit 0
fi

# --- 3. OS packages (Debian/Ubuntu/Armbian/Raspberry Pi OS) -------------
command -v apt-get >/dev/null 2>&1 \
  || die "apt-get not found: this installer targets Debian/Ubuntu/Armbian. See INSTALLATION.md for manual steps."

export DEBIAN_FRONTEND=noninteractive
log "Installing OS dependencies (apt) ..."
apt-get update -y
apt-get install -y --no-install-recommends \
  ca-certificates curl git tar gzip procps sudo \
  build-essential pkg-config \
  ufw iptables iproute2 systemd

if (( MEM_MB > 0 && MEM_MB <= 1024 )); then
  HAVE_SWAP="$(awk '/^SwapTotal:/ {print $2}' /proc/meminfo || echo 0)"
  if (( HAVE_SWAP < 500000 )) && [[ ! -f /swapfile ]]; then
    log "Creating 1 GB /swapfile for the build (small board) ..."
    fallocate -l 1G /swapfile 2>/dev/null || dd if=/dev/zero of=/swapfile bs=1M count=1024 status=none
    chmod 600 /swapfile
    mkswap /swapfile >/dev/null
    swapon /swapfile || warn "Could not enable swap; build may still succeed."
    grep -q '/swapfile' /etc/fstab 2>/dev/null || echo '/swapfile none swap sw 0 0' >> /etc/fstab
  fi
fi

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
SRC_DIR="$SCRIPT_DIR"
if [[ -n "$REPO_URL" ]]; then
  SRC_DIR="/tmp/aircoins-src"
  log "Cloning $REPO_URL (branch $BRANCH) ..."
  rm -rf "$SRC_DIR"
  git clone --depth 1 --branch "$BRANCH" "$REPO_URL" "$SRC_DIR"
elif [[ ! -f "$SCRIPT_DIR/go.mod" ]]; then
  die "go.mod not found next to install.sh. Run from the project root or pass --repo <url>."
fi
grep -q 'modernc.org/sqlite' "$SRC_DIR/go.mod" \
  || warn "go.mod does not mention modernc.org/sqlite; continuing anyway."
# NOTE: pure-Go SQLite needs no libsqlite3; build-essential stays for safety.
# --- 4. Go toolchain (distro if new enough, else official tarball) ------
version_ge() {
  local IFS=.
  local -a have=($1) need=($2)
  local i
  for ((i=0; i<${#need[@]}; i++)); do
    local h=${have[$i]:-0} n=${need[$i]:-0}
    h=${h%%[^0-9]*}; n=${n%%[^0-9]*}
    (( 10#$h > 10#$n )) && return 0
    (( 10#$h < 10#$n )) && return 1
  done
  return 0
}

go_is_ok() {
  command -v go >/dev/null 2>&1 || return 1
  local v
  v="$(go version 2>/dev/null | awk '{print $3}' | sed 's/^go//')" || return 1
  [[ -n "$v" ]] || return 1
  version_ge "$v" "${MIN_GO_MAJOR}.${MIN_GO_MINOR}"
}

install_go_tarball() {
  local ver="$1" goarch="$2"
  local file="go${ver}.linux-${goarch}.tar.gz"
  local url="${GO_MIRROR}/${file}"
  log "Installing Go ${ver} for linux-${goarch} ..."
  rm -f "/tmp/${file}" "/tmp/${file}.sha256"
  curl -fSL --retry 3 -o "/tmp/${file}" "$url"
  curl -fSL --retry 3 -o "/tmp/${file}.sha256" "${url}.sha256" || true
  if [[ -s "/tmp/${file}.sha256" ]]; then
    ( cd /tmp && sha256sum -c "/tmp/${file}.sha256" ) || die "Go tarball checksum mismatch."
  else
    warn "No .sha256 published; skipping checksum verification."
  fi
  rm -rf /usr/local/go
  tar -C /usr/local -xzf "/tmp/${file}"
  ln -sf /usr/local/go/bin/go /usr/local/bin/go
  rm -f "/tmp/${file}" "/tmp/${file}.sha256"
}

# go.mod (e.g. "go 1.26.5") sets the floor: raise the minimum and download
# that exact tarball when it is newer than the hardcoded default above.
GOMOD_GO_VERSION="$(awk '$1 == "go" { print $2; exit }' "$SRC_DIR/go.mod" 2>/dev/null || true)"
if [[ -n "$GOMOD_GO_VERSION" ]] && ! version_ge "${MIN_GO_MAJOR}.${MIN_GO_MINOR}" "$GOMOD_GO_VERSION"; then
  MIN_GO_MAJOR="${GOMOD_GO_VERSION%%.*}"
  MIN_GO_MINOR="$(printf '%s' "${GOMOD_GO_VERSION#*.}" | cut -d. -f1)"
  GO_VERSION="$GOMOD_GO_VERSION"
  log "go.mod requires Go $GOMOD_GO_VERSION; using it as the minimum."
fi

if go_is_ok; then
  log "Go already present: $(go version)"
else
  log "Trying distro Go package ..."
  if apt-get install -y golang-go 2>/dev/null && go_is_ok; then
    log "Distro Go is new enough: $(go version)"
  else
    warn "Distro Go missing/too old (need >= ${MIN_GO_MAJOR}.${MIN_GO_MINOR}); fetching official tarball."
    install_go_tarball "$GO_VERSION" "$GOARCH"
    go_is_ok || die "Go installation failed."
    log "Go installed: $(go version)"
  fi
fi
export PATH="/usr/local/go/bin:/usr/local/bin:$PATH"
export GOCACHE="${GOCACHE:-/root/.cache/go-build}"
export GOMODCACHE="${GOMODCACHE:-/root/go/pkg/mod}"
mkdir -p "$GOCACHE" "$GOMODCACHE"

# --- 5. Service user, directories, build, systemd unit -------------------
if ! id "$SVC_USER" >/dev/null 2>&1; then
  log "Creating service user '$SVC_USER' ..."
  useradd --system --no-create-home --shell /usr/sbin/nologin "$SVC_USER"
fi

log "Preparing directories ..."
mkdir -p "$INSTALL_DIR" "$DATA_DIR" /etc/aircoins
chown -R "$SVC_USER:$SVC_USER" "$DATA_DIR"
chmod 750 "$DATA_DIR"

# Footer version: AIRCOINS_VERSION wins, then the git description, then "dev".
VERSION_STR="${AIRCOINS_VERSION:-}"
if [[ -z "$VERSION_STR" ]] && [[ -d "$SRC_DIR/.git" ]]; then
  VERSION_STR="$(git -C "$SRC_DIR" describe --tags --always --dirty 2>/dev/null || echo dev)"
fi
[[ -n "$VERSION_STR" ]] || VERSION_STR="dev"
# go build splits -ldflags on whitespace and forwards every token to the
# linker, so a version containing a space (Debian's "13 (trixie)") turns into
# a stray linker argument and aborts with "usage: link [options] main.o".
# Keep only characters that are safe inside a single -ldflags token.
SAFE_VERSION="$(printf '%s' "$VERSION_STR" | tr -c 'A-Za-z0-9._+-' '-' \
  | sed -e 's/-\{2,\}/-/g' -e 's/^[.-]*//' -e 's/[.-]*$//')"
if [[ -n "$SAFE_VERSION" && "$SAFE_VERSION" != "$VERSION_STR" ]]; then
  warn "Version '$VERSION_STR' is not ldflags-safe; using '$SAFE_VERSION'."
  VERSION_STR="$SAFE_VERSION"
fi

log "Building $APP_NAME ($VERSION_STR) for linux/$GOARCH ..."
BUILD_JOBS="$NPROC"
if (( MEM_MB > 0 && MEM_MB < 1024 && NPROC > 2 )); then
  BUILD_JOBS=2
fi
(
  cd "$SRC_DIR"
  CGO_ENABLED=0 GOFLAGS="-p=$BUILD_JOBS" \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION_STR}" \
    -o "$INSTALL_DIR/$APP_NAME" .
)
chown "$SVC_USER:$SVC_USER" "$INSTALL_DIR/$APP_NAME"
chmod 755 "$INSTALL_DIR/$APP_NAME"
log "Binary installed: $INSTALL_DIR/$APP_NAME"
ENV_FILE="/etc/aircoins/aircoins.env"
if [[ ! -f "$ENV_FILE" ]]; then
  log "Writing $ENV_FILE ..."
  cat > "$ENV_FILE" <<EOF
# Aircoins MikroTik Controller - managed by install.sh (edit freely).
ADDR=$ADDR
DB_PATH=$DATA_DIR/aircoins.db
SECRET_KEY_PATH=$DATA_DIR/secret.key
PORTAL_NAME=$PORTAL_NAME
# DEFAULT_REDIRECT=https://example.com/
API_TIMEOUT=12s
# SECURE_COOKIES=1

# Piso Wi-Fi coin slot. Uncomment and set a token to enable it; while
# COIN_NODE_TOKEN is empty the controller refuses every coin report, so leaving
# this out is safe and costs nothing.
# COIN_NODE_TOKEN=$(openssl rand -hex 24)
# COIN_SECONDS_PER_PULSE=300
# COIN_IDLE_TTL=20m
EOF
  chmod 640 "$ENV_FILE"
else
  # An existing file wins: hand-edited settings are the operator's business.
  # The one exception is the ADDR older releases shipped here, which is a
  # product default rather than a local choice - it is migrated so an upgrade
  # lands on the port this run selected instead of silently keeping the old one.
  PREV_ADDR="$(addr_from_env "$ENV_FILE" || true)"
  if [[ "$PREV_ADDR" == "$LEGACY_DEFAULT_ADDR" && "$ADDR" != "$PREV_ADDR" ]]; then
    BACKUP="${ENV_FILE}.bak-$(date +%Y%m%d%H%M%S)"
    cp -a "$ENV_FILE" "$BACKUP"
    sed -i "s|^[[:space:]]*ADDR[[:space:]]*=.*|ADDR=$ADDR|" "$ENV_FILE"
    log "Migrated ADDR $PREV_ADDR -> $ADDR in $ENV_FILE (backup: $BACKUP)"
  else
    log "Keeping existing $ENV_FILE${PREV_ADDR:+ (ADDR=$PREV_ADDR)}"
  fi
fi
chown root:"$SVC_USER" "$ENV_FILE" 2>/dev/null || true

# The service takes ADDR from the env file, so the firewall, the smoke-test and
# the printed URLs must follow the effective value - never the requested one.
EFFECTIVE_ADDR="$(addr_from_env "$ENV_FILE" || true)"
[[ -n "$EFFECTIVE_ADDR" ]] || EFFECTIVE_ADDR="$ADDR"
if [[ "$EFFECTIVE_ADDR" != *:* ]]; then
  # A bare "8080" means "all interfaces on that port"; spell it out so the port
  # split and the URLs below stay correct.
  EFFECTIVE_ADDR="0.0.0.0:$EFFECTIVE_ADDR"
fi
if [[ "$EFFECTIVE_ADDR" != "$ADDR" ]]; then
  warn "$ENV_FILE sets ADDR=$EFFECTIVE_ADDR (not $ADDR); using that address."
fi

TOOLS_HELPER="/usr/local/sbin/aircoins-install-zerotier"
log "Installing the restricted host-tools helper ..."
install -o root -g root -m 0755 "$SRC_DIR/scripts/aircoins-install-zerotier" "$TOOLS_HELPER"
SUDOERS_FILE="/etc/sudoers.d/aircoins-tools"
printf '%s ALL=(root) NOPASSWD: %s\n' "$SVC_USER" "$TOOLS_HELPER" > "$SUDOERS_FILE"
chown root:root "$SUDOERS_FILE"
chmod 0440 "$SUDOERS_FILE"
visudo -cf "$SUDOERS_FILE" >/dev/null


UNIT_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
log "Installing systemd unit $UNIT_FILE ..."
cat > "$UNIT_FILE" <<EOF
[Unit]
Description=Aircoins MikroTik Controller
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$SVC_USER
Group=$SVC_USER
WorkingDirectory=$INSTALL_DIR
EnvironmentFile=$ENV_FILE
ExecStart=$INSTALL_DIR/$APP_NAME
Restart=on-failure
RestartSec=3
# The controller itself accepts no general privilege escalation. The one
# allowed sudo target is the no-argument, root-owned installer helper above.
NoNewPrivileges=false
# The helper must be able to write package files while the controller is idle.
ProtectSystem=false
ProtectHome=true
PrivateTmp=true
ReadWritePaths=$DATA_DIR
# Port 80 is privileged, so the unprivileged service user needs the bind
# capability. A process holding it was verified to still run sudo correctly,
# which is what the root helper below depends on.
AmbientCapabilities=CAP_NET_BIND_SERVICE
# Do not set CapabilityBoundingSet= (empty): sudo must be able to acquire
# CAP_SETUID/CAP_SETGID while entering the fixed root helper.

[Install]
WantedBy=multi-user.target
EOF

# --- 6. Firewall ---------------------------------------------------------
LISTEN_PORT="${EFFECTIVE_ADDR##*:}"
[[ "$LISTEN_PORT" =~ ^[0-9]+$ ]] && (( LISTEN_PORT >= 1 && LISTEN_PORT <= 65535 )) \
  || die "ADDR=$EFFECTIVE_ADDR in $ENV_FILE carries no valid port"
if (( CONFIGURE_FIREWALL )); then
  if command -v ufw >/dev/null 2>&1; then
    if ufw status 2>/dev/null | grep -qi "status: active"; then
      ufw allow "${LISTEN_PORT}/tcp" comment 'Aircoins controller' || true
      log "ufw: allowed ${LISTEN_PORT}/tcp"
    else
      log "ufw inactive; enabling with SSH + controller port open ..."
      ufw allow 22/tcp comment 'SSH' || true
      ufw allow "${LISTEN_PORT}/tcp" comment 'Aircoins controller' || true
      ufw --force enable || warn "Could not enable ufw; open TCP $LISTEN_PORT manually."
    fi
  elif command -v iptables >/dev/null 2>&1; then
    iptables -C INPUT -p tcp --dport "$LISTEN_PORT" -j ACCEPT 2>/dev/null \
      || iptables -I INPUT -p tcp --dport "$LISTEN_PORT" -j ACCEPT \
      || warn "Could not add iptables rule; open TCP $LISTEN_PORT manually."
    command -v netfilter-persistent >/dev/null 2>&1 && netfilter-persistent save || true
    log "iptables: allowed ${LISTEN_PORT}/tcp"
  else
    warn "No firewall tool found; ensure TCP $LISTEN_PORT is reachable."
  fi
else
  log "Firewall step skipped (--skip-firewall)."
fi

# --- 7. Enable, start, smoke-test ----------------------------------------
if (( ENABLE_SERVICE )); then
  log "Enabling and restarting $SERVICE_NAME ..."
  systemctl daemon-reload
  systemctl enable "$SERVICE_NAME"
  # `enable --now` does not restart an already active unit, which leaves the
  # previous binary running after an upgrade. Always restart so the executable
  # just installed is the process serving requests.
  systemctl restart "$SERVICE_NAME"
  if ! systemctl is-active --quiet "$SERVICE_NAME"; then
    warn "Service did not become active; recent logs:"
    journalctl -u "$SERVICE_NAME" --no-pager -n 30 2>/dev/null || true
    die "Failed to start $SERVICE_NAME."
  fi
  systemctl --no-pager --lines=20 status "$SERVICE_NAME" || true
else
  log "Skipping systemd enable (--no-service). Run: $INSTALL_DIR/$APP_NAME"
fi

INSTALLED_VERSION="$($INSTALL_DIR/$APP_NAME --version 2>/dev/null || true)"
[[ -n "$INSTALLED_VERSION" ]] || INSTALLED_VERSION="version unavailable"

HOST_PART="${EFFECTIVE_ADDR%%:*}"
if [[ "$HOST_PART" == "0.0.0.0" || -z "$HOST_PART" ]]; then HOST_PART="127.0.0.1"; fi
# Browsers already default to port 80, so it is left out of the printed URLs.
URL_HOST="$HOST_PART"
(( LISTEN_PORT == 80 )) || URL_HOST="${HOST_PART}:${LISTEN_PORT}"
# Mirror the controller's default so the printed URLs are right. A custom
# ADMIN_PATH in the env file is honoured as well.
ADMIN_PATH="$(sed -n 's/^[[:space:]]*ADMIN_PATH[[:space:]]*=[[:space:]]*//p' "$ENV_FILE" 2>/dev/null | tail -n1)"
ADMIN_PATH="${ADMIN_PATH:-/admin}"
ADMIN_PATH="/${ADMIN_PATH#/}"; ADMIN_PATH="${ADMIN_PATH%/}"
# Wait for the service to actually answer before probing it. A single curl
# right after `systemctl restart` races the process: the controller applies
# pending migrations and (on first boot) derives a PBKDF2 password hash before
# it binds the port, which on a low-power board (Orange Pi) takes long enough
# that the first probe gets "connection refused" even though the service is
# perfectly healthy. Retry for up to 30s before calling it a failure.
HEALTH_URL="http://${URL_HOST}/healthz"
health_ok=0
for _ in $(seq 1 30); do
  if curl -fsS --max-time 5 "$HEALTH_URL" 2>/dev/null | grep -q ok; then
    health_ok=1
    break
  fi
  sleep 1
done
if (( health_ok )); then
  log "Health check OK: $HEALTH_URL"
else
  warn "Health check failed on $HEALTH_URL; recent logs:"
  journalctl -u "$SERVICE_NAME" --no-pager -n 30 2>/dev/null || true
  die "Service unhealthy. Inspect: journalctl -u $SERVICE_NAME -e"
fi

cat <<EOF

==================== Aircoins installed ====================
Portal (guests) : http://${URL_HOST}/
Panel (admin)   : http://${URL_HOST}${ADMIN_PATH}/
Health          : $HEALTH_URL
Binary          : $INSTALL_DIR/$APP_NAME ($INSTALLED_VERSION)
Data            : $DATA_DIR/aircoins.db (+ secret.key)
Config          : $ENV_FILE
Service         : systemctl status $SERVICE_NAME
Logs            : journalctl -u $SERVICE_NAME -f
Board           : $BOARD ($ARCH_LABEL, ${MEM_MB} MB RAM)
============================================================
EOF

# The panel needs credentials. On a fresh install the controller generated a
# random password and logged it once, so print it here instead of leaving the
# operator to grep the journal. ADMIN_USER/ADMIN_PASSWORD are only seeds for a
# brand new account and are ignored once one exists.
ADMIN_NAME="$(sed -n 's/^[[:space:]]*ADMIN_USER[[:space:]]*=[[:space:]]*//p' "$ENV_FILE" 2>/dev/null | tail -n1)"
ADMIN_NAME="${ADMIN_NAME:-admin}"
GENERATED_PW="$(journalctl -u "$SERVICE_NAME" --no-pager -n 200 2>/dev/null \
  | sed -n 's/.*generated an initial panel password.*password=\([^ ]*\).*/\1/p' | tail -n1)"
if [[ -n "$GENERATED_PW" ]]; then
  cat <<EOF

Operator sign-in (generated on first boot):
    user     : $ADMIN_NAME
    password : $GENERATED_PW
Change it in the panel under Settings. It is shown here once;
afterwards only its hash is stored.
EOF
else
  cat <<EOF

The panel requires a sign-in. To see the generated password:
    journalctl -u $SERVICE_NAME | grep 'initial panel password'
EOF
fi

cat <<EOF
Next: sign in, then register routers under ${ADMIN_PATH}/routers and
point your MikroTik hotspot login page at the portal URL
(see INSTALLATION.md).
EOF
