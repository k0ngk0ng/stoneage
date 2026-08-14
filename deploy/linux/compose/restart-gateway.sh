#!/usr/bin/env bash
set -euo pipefail
. "$(dirname "$0")/common.sh"

start_or_restart_container "$gateway_container"
wait_tcp "$gateway_host" 9065 "Gateway"
