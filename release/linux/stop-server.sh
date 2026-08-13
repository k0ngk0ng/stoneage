#!/usr/bin/env bash
set -euo pipefail

package_root="$(cd "$(dirname "$0")" && pwd)"
container_name="${STONEAGE_SERVER_CONTAINER:-stoneage-revival-server}"
gateway_pid_file="$package_root/gateway.pid"

if [[ -f "$gateway_pid_file" ]]; then
  gateway_pid="$(tr -dc '0-9' <"$gateway_pid_file")"
  if [[ -n "$gateway_pid" ]] && kill -0 "$gateway_pid" 2>/dev/null; then
    kill "$gateway_pid" || true
  fi
  rm -f "$gateway_pid_file"
fi
if docker container inspect "$container_name" >/dev/null 2>&1; then
  docker stop "$container_name" >/dev/null || true
  docker rm "$container_name" >/dev/null || true
fi
echo "StoneAge server stopped; runtime data was preserved."

