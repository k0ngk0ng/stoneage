#!/usr/bin/env bash
set -euo pipefail

container_name="${STONEAGE_SERVER_CONTAINER:-stoneage-revival-server}"
upstream_port="${STONEAGE_UPSTREAM_PORT:-19065}"

if ! docker container inspect "$container_name" >/dev/null 2>&1 || [[ "$(docker inspect -f '{{.State.Running}}' "$container_name")" != "true" ]]; then
  echo "Game service container is not running." >&2
  exit 1
fi

docker exec "$container_name" sh /modern/control.sh restart-gmsv
for _ in {1..120}; do
  if nc -z 127.0.0.1 "$upstream_port" >/dev/null 2>&1; then
    echo "GMSV restarted."
    exit 0
  fi
  sleep 0.1
done

echo "GMSV did not become ready; inspect runtime/legacy-server/logs/gmsv.log." >&2
exit 1
