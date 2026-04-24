#!/bin/sh
# update-dlc.sh - download latest dlc.dat and reload dns-proxy
#
# Called from:
#   - entrypoint.sh on first start (if dlc.dat absent)
#   - crond daily (03:00)
#   - Manually: docker exec <container> /app/update-dlc.sh
#
# After successful download, sends SIGHUP to dns-proxy so it
# reloads geosite data in-place without restarting.
set -e

DLC_URL="https://github.com/v2fly/domain-list-community/releases/latest/download/dlc.dat"
DLC_PATH="/data/dlc.dat"
TMP_PATH="/data/dlc.dat.tmp"

log() {
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*"
}

log "Starting dlc.dat update..."

# Download to tmp file first (atomic replace on success). When dlc.dat already
# exists, curl sends a conditional request and skips the body if it is unchanged.
CURL_ARGS="-fsSL --connect-timeout 15 --max-time 120 --retry 3 --retry-delay 5"
if [ -f "${DLC_PATH}" ]; then
    CURL_ARGS="${CURL_ARGS} -z ${DLC_PATH}"
fi

rm -f "${TMP_PATH}"
if ! curl ${CURL_ARGS} -o "${TMP_PATH}" "${DLC_URL}"; then
    rm -f "${TMP_PATH}"
    log "ERROR: failed to download dlc.dat from ${DLC_URL}"
    exit 1
fi

if [ ! -s "${TMP_PATH}" ]; then
    rm -f "${TMP_PATH}"
    log "dlc.dat is already up to date"
    exit 0
fi

# Basic sanity check: file must be > 1MB (dlc.dat is typically ~3-5MB)
SIZE=$(wc -c < "${TMP_PATH}")
if [ "${SIZE}" -lt 1048576 ]; then
    log "ERROR: downloaded file is suspiciously small (${SIZE} bytes), aborting"
    rm -f "${TMP_PATH}"
    exit 1
fi

# Atomic replace
mv "${TMP_PATH}" "${DLC_PATH}"
log "dlc.dat updated: $(du -sh ${DLC_PATH} | cut -f1)"

# Send SIGHUP to dns-proxy to trigger geosite reload without restart
# pgrep matches process name from /proc - works inside Alpine
PID=$(pgrep -x dns-proxy 2>/dev/null || true)
if [ -n "${PID}" ]; then
    kill -HUP "${PID}"
    log "Sent SIGHUP to dns-proxy (PID=${PID}) - geosite will reload"
else
    log "dns-proxy not running yet; dlc.dat will be loaded on next start"
fi
