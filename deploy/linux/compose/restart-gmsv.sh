#!/usr/bin/env bash
set -euo pipefail
. "$(dirname "$0")/common.sh"

require_container_running "$game_container"
"$docker_bin" exec "$game_container" sh /modern/control.sh restart-gmsv
wait_tcp "$game_host" 9065 "GMSV"
