#!/bin/sh
set -eu

game_root="${STONEAGE_GAME_ROOT:-/game}"
log_root="$game_root/logs"

mkdir -p "$log_root"
cd "$game_root/saac"
exec ./saacjt.exe >"$log_root/saac.log" 2>&1
