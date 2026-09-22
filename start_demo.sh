#!/usr/bin/env bash
set -u
ROOT=/mnt/c/Users/CITYCONNECT/Documents/GitHub/Aircoins-Mikrotik-Controller
cd "$ROOT" || exit 1

# Kill any previously started demo servers.
for exe in aircoins-demo aircoins-netdemo; do
  pkill -f "$exe.exe" 2>/dev/null || true
done
sleep 1

BIN=/tmp/aircoins-demo.exe
DB=/tmp/aircoins-demo.db
KEY=/tmp/aircoins-demo.key
rm -f "$BIN" "$DB" "$KEY"

if ! go build -o "$BIN" . ; then
  echo "BUILD_FAIL"
  exit 1
fi
echo "BUILD_OK"

ADDR=127.0.0.1:19111 DB_PATH=$DB API_TIMEOUT=4s SECRET_KEY_PATH=$KEY \
  nohup "$BIN" >/tmp/aircoins-demo.log 2>&1 &
disown 2>/dev/null || true
sleep 2

echo "health: $(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:19111/healthz)"
echo "picker: $(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:19111/network)"
echo "create: $(curl -s -X POST http://127.0.0.1:19111/api/v1/routers \
  -H 'Content-Type: application/json' \
  -d '{"name":"demo","host":"192.168.88.1","port":8728,"username":"admin","password":"x","portal_tag":"hotspot1"}')"
for t in servers server-profiles user-profiles walled-garden walled-garden-ip; do
  code=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:19111/network/1?tab=$t")
  echo "tab-$t: $code"
done
echo "portal: $(curl -s -o /dev/null -w '%{http_code}' 'http://127.0.0.1:19111/portal/login?mac=AA:BB:CC:DD:EE:FF&ip=192.168.88.10&link-login=https://x/login&link-orig=https://example.com')"
