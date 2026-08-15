#!/usr/bin/env bash
set -euo pipefail

runtime_root="${1:-runtime/legacy-server}"
base_container="${STONEAGE_SERVER_CONTAINER:-stoneage-legacy-local}"
saac_container="${STONEAGE_SAAC_CONTAINER:-${base_container}-saac}"
gmsv_container="${STONEAGE_GMSV_CONTAINER:-${base_container}-gmsv}"
network_name="${STONEAGE_SERVER_NETWORK:-${base_container}-network}"
image="${STONEAGE_RUNTIME_IMAGE:-alpine:3.22}"
# Keep numeric GMSV private on 19065 so the client-facing compatibility
# gateway can own host port 9065. SAAC is mapped only for local diagnostics.
host_port="${STONEAGE_UPSTREAM_PORT:-${STONEAGE_GAME_PORT:-19065}}"
saac_host_port="${STONEAGE_SAAC_PORT:-9300}"

if [[ ! -x "$runtime_root/saac/saacjt.exe" || ! -x "$runtime_root/gmsv/gmsvjt.exe" ]]; then
  echo "Prepared server runtime is missing under $runtime_root." >&2
  echo "Run scripts/prepare-legacy-server.sh first." >&2
  exit 1
fi

runtime_root="$(cd "$runtime_root" && pwd)"
project_root="$(cd "$(dirname "$0")/.." && pwd)"

# Accept an explicitly selected pre-split legacy wrapper from older local
# sessions. New launches use two containers; this compatibility path avoids
# needlessly interrupting a server that is already running.
if [[ -n "${STONEAGE_SERVER_CONTAINER:-}" ]] && \
   docker container inspect "$base_container" >/dev/null 2>&1 && \
   docker exec "$base_container" test -f /modern/run.sh >/dev/null 2>&1; then
  echo "Using existing combined legacy server container $base_container."
  exit 0
fi

container_exists()
{
  docker container inspect "$1" >/dev/null 2>&1
}

container_running()
{
  [[ "$(docker inspect -f '{{.State.Running}}' "$1" 2>/dev/null || true)" == "true" ]]
}

if container_exists "$saac_container" || container_exists "$gmsv_container"; then
  if ! container_exists "$saac_container" || ! container_exists "$gmsv_container"; then
    echo "Only one split legacy service container exists; remove the incomplete pair or set both STONEAGE_SAAC_CONTAINER and STONEAGE_GMSV_CONTAINER." >&2
    exit 1
  fi
  if ! container_running "$saac_container"; then
    docker start "$saac_container" >/dev/null
  fi
  if ! container_running "$gmsv_container"; then
    docker start "$gmsv_container" >/dev/null
  fi
  echo "Legacy SAAC/GMSV containers are already available: $saac_container, $gmsv_container."
  exit 0
fi

if ! docker network inspect "$network_name" >/dev/null 2>&1; then
  docker network create "$network_name" >/dev/null
fi

docker_args=(
  --detach
  --init
  --ulimit core=-1
  --network "$network_name"
  --volume "$runtime_root:/game"
  --volume "$project_root/server/legacy/modern/run-saac.sh:/modern/run-saac.sh:ro"
  --volume "$project_root/server/legacy/modern/run-gmsv.sh:/modern/run-gmsv.sh:ro"
)
if [[ -n "${STONEAGE_SERVER_PLATFORM:-}" ]]; then
  docker_args+=(--platform "$STONEAGE_SERVER_PLATFORM")
fi

docker run "${docker_args[@]}" \
  --name "$saac_container" \
  --network-alias saac \
  --publish "127.0.0.1:$saac_host_port:9300" \
  "$image" sh /modern/run-saac.sh >/dev/null

docker run "${docker_args[@]}" \
  --name "$gmsv_container" \
  --network-alias gmsv \
  --publish "127.0.0.1:$host_port:9065" \
  --env "STONEAGE_SAAC_HOST=saac" \
  --env "STONEAGE_SAAC_PORT=9300" \
  "$image" sh /modern/run-gmsv.sh >/dev/null

echo "Started split legacy server containers: SAAC=$saac_container, GMSV=$gmsv_container"
echo "Numeric GMSV upstream will be available at 127.0.0.1:$host_port after initialization."
echo "SAAC will be available at 127.0.0.1:$saac_host_port for local status checks."
echo "Logs: $runtime_root/logs"
