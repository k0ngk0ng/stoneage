#!/usr/bin/env bash
set -euo pipefail

container_name="${STONEAGE_SERVER_CONTAINER:-stoneage-revival-server}"

if ! docker container inspect "$container_name" >/dev/null 2>&1 || [[ "$(docker inspect -f '{{.State.Running}}' "$container_name")" != "true" ]]; then
  echo "Game service container is not running." >&2
  exit 1
fi

docker exec "$container_name" sh /modern/control.sh stop-saac
for _ in {1..120}; do
  if ! docker exec "$container_name" nc -z 127.0.0.1 9300 >/dev/null 2>&1; then
    echo "SAAC stopped."
    exit 0
  fi
  sleep 0.1
done

echo "SAAC did not stop; inspect runtime/legacy-server/logs/saac.log." >&2
exit 1
