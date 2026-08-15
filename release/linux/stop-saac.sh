#!/usr/bin/env bash
set -euo pipefail

base_container="${STONEAGE_SERVER_CONTAINER:-stoneage-revival-server}"
saac_container="${STONEAGE_SAAC_CONTAINER:-${base_container}-saac}"
saac_port="${STONEAGE_SAAC_PORT:-9300}"

if docker container inspect "$saac_container" >/dev/null 2>&1; then
  docker stop "$saac_container" >/dev/null || true
fi
for _ in {1..120}; do
  if ! nc -z 127.0.0.1 "$saac_port" >/dev/null 2>&1; then
    echo "SAAC stopped."
    exit 0
  fi
  sleep 0.1
done
echo "SAAC did not stop; inspect runtime/legacy-server/logs/saac.log." >&2
exit 1
