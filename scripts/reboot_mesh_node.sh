#!/bin/sh
# reboot_mesh_node.sh <ROUTER_IP> [PASSWORD]
# Reboot a Xiaomi Mesh sub-router via Xiaomi LuCI API.
set -e

IP="${1:?router IP required (e.g. 192.168.31.2)}"
PASS="${2:-${ROUTER_PASS:?router password required (arg2 or ROUTER_PASS env)}}"
# Fixed salt used by the Xiaomi router LuCI login protocol (public constant, not a personal key).
KEY="a2ffa5c9be07488bbb04a3a47d3c5f6a"

echo "[$(date '+%Y-%m-%d %H:%M:%S')] 检查子路由 ${IP} 状态..."

# Ping check: 2 probes with 1s timeout
if ! ping -c 2 -W 1 "${IP}" >/dev/null 2>&1; then
    echo "提示: 子路由 ${IP} 当前处于离线/关机状态，本次跳过重启。"
    exit 0
fi

INIT=$(curl -s --connect-timeout 3 "http://${IP}/cgi-bin/luci/api/xqsystem/init_info" 2>/dev/null || true)
ROUTER_NAME=$(echo "$INIT" | grep -o '"routername":"[^"]*"' | cut -d'"' -f4)
DISPLAY_NAME=$(echo "$INIT" | grep -o '"displayName":"[^"]*"' | cut -d'"' -f4)
if [ -n "$ROUTER_NAME" ]; then
    echo "已连接到设备: ${DISPLAY_NAME:-小米路由器} (${ROUTER_NAME})"
fi

NEW_MODE=$(echo "$INIT" | grep -o '"newEncryptMode":[0-9]' | cut -d: -f2)

TS=$(date +%s)
RND=$((RANDOM % 9000 + 1000))
NONCE="0_00:00:00:00:00:00_${TS}_${RND}"

if [ "$NEW_MODE" = "1" ]; then
    INNER=$(printf "%s%s" "$PASS" "$KEY" | openssl dgst -sha256 | awk '{print $NF}')
    PWD_HASH=$(printf "%s%s" "$NONCE" "$INNER" | openssl dgst -sha256 | awk '{print $NF}')
else
    INNER=$(printf "%s%s" "$PASS" "$KEY" | openssl dgst -sha1 | awk '{print $NF}')
    PWD_HASH=$(printf "%s%s" "$NONCE" "$INNER" | openssl dgst -sha1 | awk '{print $NF}')
fi

LOGIN_RES=$(curl -s --connect-timeout 3 -d "username=admin&logtype=2&nonce=${NONCE}&password=${PWD_HASH}&init=0" "http://${IP}/cgi-bin/luci/api/xqsystem/login" 2>/dev/null || true)
TOKEN=$(echo "$LOGIN_RES" | grep -o '"token":"[^"]*"' | cut -d'"' -f4)

if [ -z "$TOKEN" ]; then
    echo "错误: 登录子路由 ${IP} 失败: ${LOGIN_RES}"
    exit 1
fi

echo "认证成功 (stok: ${TOKEN:0:8}...)，正在下发重启指令..."
REBOOT_RES=$(curl -s --connect-timeout 5 "http://${IP}/cgi-bin/luci/;stok=${TOKEN}/api/xqsystem/reboot?client=web" 2>/dev/null || true)
CODE=$(echo "$REBOOT_RES" | grep -o '"code":[0-9]*' | cut -d: -f2)

if [ "$CODE" = "0" ]; then
    echo "重启指令下发成功！设备正在重启中。"
    exit 0
else
    echo "警告: 重启接口返回异常: ${REBOOT_RES}"
    exit 1
fi
