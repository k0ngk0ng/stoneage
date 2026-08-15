#!/usr/bin/env bash
set -euo pipefail

base_container="${STONEAGE_SERVER_CONTAINER:-stoneage-legacy-local}"
saac_container="${STONEAGE_SAAC_CONTAINER:-${base_container}-saac}"
gmsv_container="${STONEAGE_GMSV_CONTAINER:-${base_container}-gmsv}"

stop_remove()
{
  container="$1"
  if docker container inspect "$container" >/dev/null 2>&1; then
    docker stop "$container" >/dev/null || true
    docker rm "$container" >/dev/null || true
    echo "Stopped $container."
  fi
}

# Older local/app releases used one supervisor container. Keep the explicit
# name usable while new launches use the two-container SAAC/GMSV layout.
if [[ -n "${STONEAGE_SERVER_CONTAINER:-}" ]] && \
   docker container inspect "$base_container" >/dev/null 2>&1 && \
   docker exec "$base_container" test -f /modern/run.sh >/dev/null 2>&1; then
  stop_remove "$base_container"
  echo "Legacy server runtime data was preserved."
  exit 0
fi

stop_remove "$gmsv_container"
stop_remove "$saac_container"
echo "Legacy server runtime data was preserved."
