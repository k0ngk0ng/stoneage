#!/bin/sh
set -eu

game_root="${STONEAGE_GAME_ROOT:-/game}"
log_root="${STONEAGE_LOG_ROOT:-$game_root/saac/logs}"

mkdir -p "$log_root"
cd "$game_root/saac"
exec ./saacjt.exe >"$log_root/saac.log" 2>&1
