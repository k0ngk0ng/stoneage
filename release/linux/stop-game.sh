#!/usr/bin/env bash
set -euo pipefail

container_name="${STONEAGE_SERVER_CONTAINER:-stoneage-revival-server}"

if docker container inspect "$container_name" >/dev/null 2>&1; then
  docker stop "$container_name" >/dev/null || true
  docker rm "$container_name" >/dev/null || true
fi
echo "Game service stopped; runtime data was preserved."
