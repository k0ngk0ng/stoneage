#!/usr/bin/env bash
set -euo pipefail
. "$(dirname "$0")/common.sh"

start_or_restart_container "$gmsv_container"
wait_tcp "$gmsv_host" 9065 "GMSV"
