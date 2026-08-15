#!/usr/bin/env bash
set -euo pipefail
. "$(dirname "$0")/common.sh"

if container_running "$gateway_container"; then
  "$docker_bin" stop "$gateway_container" >/dev/null
fi
wait_tcp_down "$gateway_host" 9065 "Gateway"
