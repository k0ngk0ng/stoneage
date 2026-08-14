#!/usr/bin/env bash
set -euo pipefail

runtime_root="${1:-runtime/legacy-server}"
container_name="${STONEAGE_SERVER_CONTAINER:-stoneage-legacy-local}"
image="${STONEAGE_RUNTIME_IMAGE:-alpine:3.22}"
# The client-facing compatibility gateway owns 9065. Keep the numeric GMSV
# private on 19065 so both protocol dialects can run on the same host.
host_port="${STONEAGE_UPSTREAM_PORT:-${STONEAGE_GAME_PORT:-19065}}"
saac_host_port="${STONEAGE_SAAC_PORT:-9300}"

if [[ ! -x "$runtime_root/saac/saacjt.exe" || ! -x "$runtime_root/gmsv/gmsvjt.exe" ]]; then
  echo "Prepared server runtime is missing under $runtime_root." >&2
  echo "Run scripts/prepare-legacy-server.sh first." >&2
  exit 1
fi

runtime_root="$(cd "$runtime_root" && pwd)"
project_root="$(cd "$(dirname "$0")/.." && pwd)"

if docker container inspect "$container_name" >/dev/null 2>&1; then
  echo "Container $container_name already exists." >&2
  echo "Use scripts/stop-legacy-server.sh before starting a replacement." >&2
  exit 1
fi

docker_args=(
  run --detach
  --name "$container_name"
  --init
  --ulimit core=-1
  --publish "127.0.0.1:$host_port:9065"
  --publish "127.0.0.1:$saac_host_port:9300"
  --volume "$runtime_root:/game"
  --volume "$project_root/server/legacy/modern/run.sh:/modern/run.sh:ro"
  --volume "$project_root/server/legacy/modern/control.sh:/modern/control.sh:ro"
)

if [[ -n "${STONEAGE_SERVER_PLATFORM:-}" ]]; then
  docker_args+=(--platform "$STONEAGE_SERVER_PLATFORM")
fi

container_id="$(docker "${docker_args[@]}" "$image" sh /modern/run.sh)"
echo "Started local-only legacy server container: $container_id"
echo "Numeric GMSV upstream will be available at 127.0.0.1:$host_port after initialization."
echo "SAAC will be available at 127.0.0.1:$saac_host_port for local status checks."
echo "Logs: $runtime_root/logs"
