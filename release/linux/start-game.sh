#!/usr/bin/env bash
set -euo pipefail

package_root="$(cd "$(dirname "$0")" && pwd)"
container_name="${STONEAGE_SERVER_CONTAINER:-stoneage-revival-server}"
upstream_port="${STONEAGE_UPSTREAM_PORT:-19065}"
saac_port="${STONEAGE_SAAC_PORT:-9300}"

command -v docker >/dev/null 2>&1 || {
  echo "Docker is required." >&2
  exit 1
}
docker info >/dev/null 2>&1 || {
  echo "Docker daemon is not running." >&2
  exit 1
}
mkdir -p "$package_root/runtime" "$package_root/logs"

create_container()
{
  docker run --detach \
    --name "$container_name" --init --ulimit core=-1 \
    --publish "127.0.0.1:$upstream_port:9065" \
    --publish "127.0.0.1:$saac_port:9300" \
    --volume "$package_root/runtime/legacy-server:/game" \
    --volume "$package_root/server/legacy/modern/run.sh:/modern/run.sh:ro" \
    --volume "$package_root/server/legacy/modern/control.sh:/modern/control.sh:ro" \
    alpine:3.22 sh /modern/run.sh >/dev/null
}

if docker container inspect "$container_name" >/dev/null 2>&1; then
  # Containers created before independent GMSV/SAAC control was added do not
  # have the control script mount or the host-side SAAC status port. Recreate
  # only that stale wrapper; the bind mounted runtime data remains in place.
  has_saac_port=0
  if docker port "$container_name" 9300/tcp 2>/dev/null | grep -Fq "127.0.0.1:$saac_port"; then
    has_saac_port=1
  fi
  if ! docker exec "$container_name" test -f /modern/control.sh >/dev/null 2>&1 || [[ "$has_saac_port" != "1" ]]; then
    docker stop "$container_name" >/dev/null || true
    docker rm "$container_name" >/dev/null || true
    create_container
  elif [[ "$(docker inspect -f '{{.State.Running}}' "$container_name")" != "true" ]]; then
    docker start "$container_name" >/dev/null
  fi
else
  create_container
fi

for _ in {1..120}; do
  if nc -z 127.0.0.1 "$upstream_port" >/dev/null 2>&1; then
    echo "Game service is ready on 127.0.0.1:$upstream_port."
    exit 0
  fi
  sleep 0.1
done

echo "GMSV did not become ready; inspect runtime/legacy-server/logs." >&2
exit 1
