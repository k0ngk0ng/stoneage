#!/usr/bin/env bash
set -euo pipefail

project_root="$(cd "$(dirname "$0")/.." && pwd)"
container_name="${STONEAGE_SERVER_CONTAINER:-stoneage-legacy-local}"
saac_container="${STONEAGE_SAAC_CONTAINER:-${container_name}-saac}"
gmsv_container="${STONEAGE_GMSV_CONTAINER:-${container_name}-gmsv}"
gateway_pid_file="$project_root/runtime/gateway.pid"

if [[ -n "${STONEAGE_SERVER_CONTAINER:-}" ]] && \
   docker container inspect "$container_name" >/dev/null 2>&1 && \
   docker exec "$container_name" test -f /modern/run.sh >/dev/null 2>&1; then
  docker inspect -f 'server={{.Name}} running={{.State.Running}} status={{.State.Status}}' "$container_name"
  if [[ "$(docker inspect -f '{{.State.Running}}' "$container_name")" == "true" ]]; then
    if docker exec "$container_name" nc -z 127.0.0.1 9065 >/dev/null 2>&1; then echo "gmsv=running"; else echo "gmsv=stopped"; fi
    if docker exec "$container_name" nc -z 127.0.0.1 9300 >/dev/null 2>&1; then echo "saac=running"; else echo "saac=stopped"; fi
  else
    echo "gmsv=stopped"
    echo "saac=stopped"
  fi
else
  for service in "$saac_container" "$gmsv_container"; do
    if docker container inspect "$service" >/dev/null 2>&1; then
      docker inspect -f "server={{.Name}} running={{.State.Running}} status={{.State.Status}}" "$service"
    else
      echo "server=$service status=missing"
    fi
  done
  if docker container inspect "$gmsv_container" >/dev/null 2>&1 && \
     [[ "$(docker inspect -f '{{.State.Running}}' "$gmsv_container")" == "true" ]] && \
     nc -z 127.0.0.1 "${STONEAGE_UPSTREAM_PORT:-19065}" >/dev/null 2>&1; then
    echo "gmsv=running"
  else
    echo "gmsv=stopped"
  fi
  if docker container inspect "$saac_container" >/dev/null 2>&1 && \
     [[ "$(docker inspect -f '{{.State.Running}}' "$saac_container")" == "true" ]] && \
     nc -z 127.0.0.1 "${STONEAGE_SAAC_PORT:-9300}" >/dev/null 2>&1; then
    echo "saac=running"
  else
    echo "saac=stopped"
  fi
fi

gateway_reported=0
if [[ -f "$gateway_pid_file" ]]; then
  gateway_pid="$(tr -dc '0-9' <"$gateway_pid_file")"
  if [[ -n "$gateway_pid" ]] && kill -0 "$gateway_pid" 2>/dev/null; then
    echo "gateway=running pid=$gateway_pid"
    gateway_reported=1
  fi
fi
if [[ "$gateway_reported" != "1" ]]; then
  gateway_pid="$(pgrep -f 'stoneage-gateway.*(-listen|-routes)' | head -n 1 || true)"
  if [[ -n "$gateway_pid" ]]; then
    printf '%s\n' "$gateway_pid" >"$gateway_pid_file"
    echo "gateway=running pid=$gateway_pid recovered=true"
  else
    echo "gateway=stopped"
  fi
fi

if pgrep -f 'sa_2903-local[.]exe' >/dev/null 2>&1; then
  echo "client=running"
else
  echo "client=stopped"
fi
