#!/bin/sh
set -eu

game_root="${STONEAGE_GAME_ROOT:-/game}"
log_root="${STONEAGE_LOG_ROOT:-$game_root/saac/logs}"

mkdir -p "$log_root"
# SAAC reads acserv.cf from its working directory. Refresh that runtime copy
# from the centrally managed directory at each start; admin edits the source.
if [ -n "${STONEAGE_SAAC_CONFIG:-}" ]; then
    cp "$STONEAGE_SAAC_CONFIG" "$game_root/saac/acserv.cf"
fi
cd "$game_root/saac"
exec ./saacjt.exe >"$log_root/saac.log" 2>&1
