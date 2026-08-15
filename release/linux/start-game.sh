#!/usr/bin/env bash
set -euo pipefail

package_root="$(cd "$(dirname "$0")" && pwd)"
upstream_port="${STONEAGE_UPSTREAM_PORT:-19065}"
saac_port="${STONEAGE_SAAC_PORT:-9300}"

STONEAGE_SERVER_CONTAINER="${STONEAGE_SERVER_CONTAINER:-stoneage-revival-server}" \
  STONEAGE_UPSTREAM_PORT="$upstream_port" \
  STONEAGE_SAAC_PORT="$saac_port" \
  "$package_root/scripts/run-legacy-server.sh" "$package_root/runtime/legacy-server"

for _ in {1..120}; do
  if nc -z 127.0.0.1 "$upstream_port" >/dev/null 2>&1 && \
     nc -z 127.0.0.1 "$saac_port" >/dev/null 2>&1; then
    echo "Game service is ready (GMSV $upstream_port, SAAC $saac_port)."
    exit 0
  fi
  sleep 0.1
done

echo "GMSV/SAAC did not become ready; inspect runtime/legacy-server/logs." >&2
exit 1
