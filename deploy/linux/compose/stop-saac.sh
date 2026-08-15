#!/usr/bin/env bash
set -euo pipefail
. "$(dirname "$0")/common.sh"

if container_running "$saac_container"; then "$docker_bin" stop "$saac_container" >/dev/null; fi
wait_tcp_down "$saac_host" 9300 "SAAC"
