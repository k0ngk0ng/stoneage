#!/usr/bin/env bash
set -euo pipefail

package_root="$(cd "$(dirname "$0")" && pwd)"
container_name="${STONEAGE_SERVER_CONTAINER:-stoneage-revival-server}"
gateway_pid_file="$package_root/gateway.pid"

if docker container inspect "$container_name" >/dev/null 2>&1; then
  docker inspect -f 'server={{.State.Status}}' "$container_name"
  if [[ "$(docker inspect -f '{{.State.Running}}' "$container_name")" == "true" ]]; then
    if docker exec "$container_name" nc -z 127.0.0.1 9065 >/dev/null 2>&1; then
      echo "gmsv=running"
    else
      echo "gmsv=stopped"
    fi
    if docker exec "$container_name" nc -z 127.0.0.1 9300 >/dev/null 2>&1; then
      echo "saac=running"
    else
      echo "saac=stopped"
    fi
  else
    echo "gmsv=stopped"
    echo "saac=stopped"
  fi
else
  echo "server=stopped"
  echo "gmsv=stopped"
  echo "saac=stopped"
fi
if [[ -f "$gateway_pid_file" ]]; then
  gateway_pid="$(tr -dc '0-9' <"$gateway_pid_file")"
  if [[ -n "$gateway_pid" ]] && kill -0 "$gateway_pid" 2>/dev/null; then
    echo "gateway=running pid=$gateway_pid"
    exit 0
  fi
fi
echo "gateway=stopped"
