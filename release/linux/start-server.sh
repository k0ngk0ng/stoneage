#!/usr/bin/env bash
set -euo pipefail

package_root="$(cd "$(dirname "$0")" && pwd)"
listen_address="${STONEAGE_GATEWAY_LISTEN:-127.0.0.1:9065}"
upstream_port="${STONEAGE_UPSTREAM_PORT:-19065}"
container_name="${STONEAGE_SERVER_CONTAINER:-stoneage-revival-server}"
gateway_pid_file="$package_root/gateway.pid"
log_root="$package_root/logs"

command -v docker >/dev/null 2>&1 || {
  echo "Docker is required." >&2
  exit 1
}
docker info >/dev/null 2>&1 || {
  echo "Docker daemon is not running." >&2
  exit 1
}
mkdir -p "$log_root"

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
    break
  fi
  sleep 0.1
done
nc -z 127.0.0.1 "$upstream_port" >/dev/null 2>&1 || {
  echo "GMSV did not become ready; inspect runtime/legacy-server/logs." >&2
  exit 1
}

if [[ -f "$gateway_pid_file" ]]; then
  gateway_pid="$(tr -dc '0-9' <"$gateway_pid_file")"
  if [[ -n "$gateway_pid" ]] && kill -0 "$gateway_pid" 2>/dev/null; then
    echo "Gateway is already running (PID $gateway_pid)."
    exit 0
  fi
fi
nohup "$package_root/bin/stoneage-gateway" \
  -listen "$listen_address" -upstream "127.0.0.1:$upstream_port" \
  </dev/null >"$log_root/gateway.log" 2>&1 &
printf '%s\n' "$!" >"$gateway_pid_file"
echo "StoneAge server started: $listen_address"
echo "Do not expose ports 19065 or 9300."

