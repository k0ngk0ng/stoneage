#!/usr/bin/env bash
set -euo pipefail

base_container="${STONEAGE_SERVER_CONTAINER:-stoneage-revival-server}"
gmsv_container="${STONEAGE_GMSV_CONTAINER:-${base_container}-gmsv}"
upstream_port="${STONEAGE_UPSTREAM_PORT:-19065}"

if docker container inspect "$gmsv_container" >/dev/null 2>&1; then
  docker stop "$gmsv_container" >/dev/null || true
fi
for _ in {1..120}; do
  if ! nc -z 127.0.0.1 "$upstream_port" >/dev/null 2>&1; then
    echo "GMSV stopped."
    exit 0
  fi
  sleep 0.1
done
echo "GMSV did not stop; inspect runtime/legacy-server/logs/gmsv.log." >&2
exit 1
