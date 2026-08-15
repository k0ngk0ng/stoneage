#!/usr/bin/env bash
set -euo pipefail

base_container="${STONEAGE_SERVER_CONTAINER:-stoneage-revival-server}"
gmsv_container="${STONEAGE_GMSV_CONTAINER:-${base_container}-gmsv}"
upstream_port="${STONEAGE_UPSTREAM_PORT:-19065}"

if ! docker container inspect "$gmsv_container" >/dev/null 2>&1; then
  echo "GMSV container is not present." >&2
  exit 1
fi
if [[ "$(docker inspect -f '{{.State.Running}}' "$gmsv_container")" == "true" ]]; then
  docker restart "$gmsv_container" >/dev/null
else
  docker start "$gmsv_container" >/dev/null
fi
for _ in {1..120}; do
  if nc -z 127.0.0.1 "$upstream_port" >/dev/null 2>&1; then
    echo "GMSV restarted."
    exit 0
  fi
  sleep 0.1
done
echo "GMSV did not become ready; inspect runtime/legacy-server/logs/gmsv.log." >&2
exit 1
