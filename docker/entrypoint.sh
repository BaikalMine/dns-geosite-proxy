#!/bin/sh
# entrypoint.sh - container init script
#
# Sequence:
#   1. Download dlc.dat on first start (if /data/dlc.dat is missing)
#   2. Start crond for weekly auto-updates
#   3. Exec dns-proxy (replaces this shell - PID 1)
#
# SIGHUP → dns-proxy reloads geosite without restart
# SIGTERM/SIGINT → dns-proxy graceful shutdown
set -e

DLC_PATH="/data/dlc.dat"
CONFIG_PATH="${DNS_PROXY_CONFIG:-/etc/dns-proxy/config.json}"

echo "[entrypoint] Starting dns-geosite-proxy..."
echo "[entrypoint] Config: ${CONFIG_PATH}"

# Download dlc.dat if not present (first run or volume is empty)
if [ ! -f "${DLC_PATH}" ]; then
    echo "[entrypoint] ${DLC_PATH} not found - downloading..."
    /app/update-dlc.sh
fi

# Verify config exists
if [ ! -f "${CONFIG_PATH}" ]; then
    echo "[entrypoint] ERROR: config not found at ${CONFIG_PATH}"
    echo "[entrypoint] Mount config.json to ${CONFIG_PATH} or set DNS_PROXY_CONFIG env"
    exit 1
fi

# Start crond in background (-b = background, -l 8 = log level notice)
echo "[entrypoint] Starting crond for weekly dlc.dat updates..."
crond -b -l 8

# Hand off to dns-proxy (exec replaces shell, proxy becomes PID 1)
echo "[entrypoint] Launching dns-proxy..."
exec /app/dns-proxy -config "${CONFIG_PATH}" "$@"
