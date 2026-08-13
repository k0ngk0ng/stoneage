#!/usr/bin/env bash
set -euo pipefail

container_name="${STONEAGE_SERVER_CONTAINER:-stoneage-legacy-local}"

if ! docker container inspect "$container_name" >/dev/null 2>&1; then
  echo "Container $container_name does not exist."
  exit 0
fi

docker stop "$container_name"
docker rm "$container_name"
echo "Stopped $container_name; runtime data was preserved."
