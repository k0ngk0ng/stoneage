#!/usr/bin/env bash
set -euo pipefail
. "$(dirname "$0")/common.sh"

start_or_restart_container "$saac_container"
wait_tcp "$saac_host" 9300 "SAAC"
