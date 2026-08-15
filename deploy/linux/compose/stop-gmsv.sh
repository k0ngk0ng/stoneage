#!/usr/bin/env bash
set -euo pipefail
. "$(dirname "$0")/common.sh"

if container_running "$gmsv_container"; then "$docker_bin" stop "$gmsv_container" >/dev/null; fi
wait_tcp_down "$gmsv_host" 9065 "GMSV"
