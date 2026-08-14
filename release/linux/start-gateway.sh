#!/usr/bin/env bash
set -euo pipefail

package_root="$(cd "$(dirname "$0")" && pwd)"
listen_address="${STONEAGE_GATEWAY_LISTEN:-127.0.0.1:9065}"
upstream_port="${STONEAGE_UPSTREAM_PORT:-19065}"
gateway_pid_file="$package_root/gateway.pid"
log_root="$package_root/logs"
auth_database="${STONEAGE_AUTH_DB:-$package_root/runtime/stoneage-auth.db}"

mkdir -p "$log_root" "$(dirname "$auth_database")"
if [[ -f "$gateway_pid_file" ]]; then
  gateway_pid="$(tr -dc '0-9' <"$gateway_pid_file")"
  if [[ -n "$gateway_pid" ]] && kill -0 "$gateway_pid" 2>/dev/null; then
    echo "Gateway is already running (PID $gateway_pid)."
    exit 0
  fi
fi
if ! nc -z 127.0.0.1 "$upstream_port" >/dev/null 2>&1; then
  echo "Game service is not ready on 127.0.0.1:$upstream_port." >&2
  exit 1
fi

nohup "$package_root/bin/stoneage-gateway" \
  -listen "$listen_address" -upstream "127.0.0.1:$upstream_port" \
  -auth-required -auth-db "$auth_database" \
  </dev/null >"$log_root/gateway.log" 2>&1 &
printf '%s\n' "$!" >"$gateway_pid_file"
echo "Gateway started on $listen_address."
