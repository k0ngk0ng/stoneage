#!/usr/bin/env bash
set -euo pipefail

package_root="$(cd "$(dirname "$0")" && pwd)"
container_name="${STONEAGE_SERVER_CONTAINER:-stoneage-revival-server}"
upstream_port="${STONEAGE_UPSTREAM_PORT:-19065}"

command -v docker >/dev/null 2>&1 || {
  echo "Docker is required." >&2
  exit 1
}
docker info >/dev/null 2>&1 || {
  echo "Docker daemon is not running." >&2
  exit 1
}
mkdir -p "$package_root/runtime" "$package_root/logs"

if ! docker container inspect "$container_name" >/dev/null 2>&1; then
  docker run --detach \
    --name "$container_name" --init --ulimit core=-1 \
    --publish "127.0.0.1:$upstream_port:9065" \
    --volume "$package_root/runtime/legacy-server:/game" \
    --volume "$package_root/server/legacy/modern/run.sh:/modern/run.sh:ro" \
    alpine:3.22 sh /modern/run.sh >/dev/null
elif [[ "$(docker inspect -f '{{.State.Running}}' "$container_name")" != "true" ]]; then
  docker start "$container_name" >/dev/null
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
