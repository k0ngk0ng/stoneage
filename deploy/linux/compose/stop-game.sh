#!/usr/bin/env bash
set -euo pipefail
. "$(dirname "$0")/common.sh"

if container_running "$gmsv_container"; then "$docker_bin" stop "$gmsv_container" >/dev/null; fi
if container_running "$saac_container"; then "$docker_bin" stop "$saac_container" >/dev/null; fi
echo "Game service stopped."
