#!/usr/bin/env bash
# Build, scp, and install wan6gate on the router.
#
# Required env:
#   ROUTER_HOST   router LAN IP, e.g. 192.168.31.1
#   ROUTER_PASS   router root password
#
# Requires `expect` (macOS: comes with system; otherwise install via your
# package manager).
#
# Usage:
#   ./scripts/deploy.sh                # build + deploy + install
#   ./scripts/deploy.sh --no-build     # skip rebuild
#   ./scripts/deploy.sh --no-install   # only push binary, skip install step
set -euo pipefail
cd "$(dirname "$0")/.."

DO_BUILD=1
DO_INSTALL=1
for a in "$@"; do
    case "$a" in
        --no-build) DO_BUILD=0 ;;
        --no-install) DO_INSTALL=0 ;;
        *) echo "unknown arg: $a" >&2; exit 2 ;;
    esac
done

: "${ROUTER_HOST:?ROUTER_HOST env var must be set, e.g. export ROUTER_HOST=192.168.31.1}"
: "${ROUTER_PASS:?ROUTER_PASS env var must be set (do not commit this value)}"
ROUTER_USER="root"
REMOTE_BIN="/data/router-ops/bin/wan6gate"
SSH_HELPER="$(dirname "$0")/_ssh-helper.exp"

if [ ! -x "$SSH_HELPER" ]; then
    echo "ERR: SSH helper not found at $SSH_HELPER" >&2
    exit 1
fi

if [ "$DO_BUILD" = 1 ]; then
    echo "==> building"
    ./scripts/build.sh
fi

if [ ! -f build/wan6gate ]; then
    echo "ERR: build/wan6gate missing — run with build first" >&2
    exit 1
fi

# scp via expect (because of dropbear ssh-rsa quirks)
echo "==> uploading binary to $ROUTER_HOST:$REMOTE_BIN"
expect <<EOF
set timeout 60
spawn scp -O -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
    -o HostKeyAlgorithms=+ssh-rsa -o PubkeyAcceptedAlgorithms=+ssh-rsa \
    -o KexAlgorithms=+diffie-hellman-group14-sha1,curve25519-sha256@libssh.org \
    -o MACs=+hmac-sha1,hmac-sha2-256 \
    build/wan6gate $ROUTER_USER@$ROUTER_HOST:/tmp/wan6gate.new
expect {
    -re "(P|p)assword:" { send "$ROUTER_PASS\r" }
    timeout { puts "TIMEOUT_SCP"; exit 1 }
}
expect eof
catch wait result
exit [lindex \$result 3]
EOF

# Move into place + chmod, kill old, then install
echo "==> finalising and installing on router"
cat <<'REMOTE' | "$SSH_HELPER"
mkdir -p /data/router-ops/bin
rm -f /data/router-ops/bin/wan6gate
mv /tmp/wan6gate.new /data/router-ops/bin/wan6gate
chmod +x /data/router-ops/bin/wan6gate
/data/router-ops/bin/wan6gate version
REMOTE

if [ "$DO_INSTALL" = 1 ]; then
    cat <<REMOTE | "$SSH_HELPER"
# Stop existing daemon if any (so the new binary takes over on next watchdog tick)
[ -f /data/router-ops/wan6gate/wan6gate.pid ] && kill -15 \$(cat /data/router-ops/wan6gate/wan6gate.pid) 2>/dev/null
pkill -f 'wan6gate daemon' 2>/dev/null
sleep 1
/data/router-ops/bin/wan6gate install
echo
echo "Open http://${ROUTER_HOST}:18080 in your browser."
REMOTE
fi

echo "==> done"
