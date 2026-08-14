#!/usr/bin/env bash
set -euo pipefail

docker_bin="${STONEAGE_DOCKER_BIN:-docker}"
game_container="${STONEAGE_GAME_CONTAINER:-stoneage-legacy-server}"
gateway_container="${STONEAGE_GATEWAY_CONTAINER:-stoneage-gateway}"
game_host="${STONEAGE_GAME_HOST:-legacy-server}"
gateway_host="${STONEAGE_GATEWAY_HOST:-gateway}"

container_running()
{
  [[ "$("$docker_bin" inspect -f '{{.State.Running}}' "$1" 2>/dev/null || true)" == "true" ]]
}

start_or_restart_container()
{
  container="$1"
  if container_running "$container"; then
    "$docker_bin" restart "$container" >/dev/null
  else
    "$docker_bin" start "$container" >/dev/null
  fi
}

require_container_running()
{
  if ! container_running "$1"; then
    echo "container $1 is not running" >&2
    exit 1
  fi
}

wait_tcp()
{
  host="$1"
  port="$2"
  label="$3"
  for _ in {1..120}; do
    if nc -z "$host" "$port" >/dev/null 2>&1; then
      echo "$label is ready."
      return 0
    fi
    sleep 0.1
  done
  echo "$label did not become ready at $host:$port." >&2
  return 1
}

wait_tcp_down()
{
  host="$1"
  port="$2"
  label="$3"
  for _ in {1..120}; do
    if ! nc -z "$host" "$port" >/dev/null 2>&1; then
      echo "$label stopped."
      return 0
    fi
    sleep 0.1
  done
  echo "$label did not stop at $host:$port." >&2
  return 1
}
