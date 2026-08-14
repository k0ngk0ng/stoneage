#!/usr/bin/env bash
set -euo pipefail
. "$(dirname "$0")/common.sh"

if container_running "$gateway_container" && nc -z "$gateway_host" 9065 >/dev/null 2>&1; then
  echo "gateway=running"
else
  echo "gateway=stopped"
fi
if container_running "$game_container" && nc -z "$game_host" 9065 >/dev/null 2>&1; then
  echo "gmsv=running"
else
  echo "gmsv=stopped"
fi
if container_running "$game_container" && nc -z "$game_host" 9300 >/dev/null 2>&1; then
  echo "saac=running"
else
  echo "saac=stopped"
fi
