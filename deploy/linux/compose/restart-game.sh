#!/usr/bin/env bash
set -euo pipefail
. "$(dirname "$0")/common.sh"

start_or_restart_container "$game_container"
wait_tcp "$game_host" 9300 "SAAC"
wait_tcp "$game_host" 9065 "GMSV"
