#!/usr/bin/env bash
set -euo pipefail
. "$(dirname "$0")/common.sh"

if container_running "$game_container"; then
  "$docker_bin" stop "$game_container" >/dev/null
fi
echo "Game service stopped."
