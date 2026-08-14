#!/usr/bin/env bash
set -euo pipefail

package_root="$(cd "$(dirname "$0")" && pwd)"
admin_listen="${STONEAGE_ADMIN_LISTEN:-127.0.0.1:8080}"
operator_socket="${STONEAGE_OPERATOR_SOCKET:-$package_root/run/operator.sock}"
auth_database="${STONEAGE_AUTH_DB:-$package_root/runtime/stoneage-auth.db}"
server_config="${STONEAGE_SERVER_CONFIG:-$package_root/runtime/legacy-server/gmsv/setup.cf}"
saac_config="${STONEAGE_SAAC_CONFIG:-$package_root/runtime/legacy-server/saac/acserv.cf}"
gateway_address="${STONEAGE_GATEWAY_ADDRESS:-127.0.0.1:9065}"
upstream_address="${STONEAGE_UPSTREAM_ADDRESS:-127.0.0.1:19065}"
saac_address="${STONEAGE_SAAC_ADDRESS:-127.0.0.1:9300}"
run_root="$package_root/run"
log_root="$package_root/logs"
operator_pid_file="$run_root/operator.pid"
admin_pid_file="$run_root/admin.pid"

mkdir -p "$run_root" "$log_root" "$(dirname "$auth_database")"

running_pid()
{
  local pid_file="$1"
  if [[ ! -f "$pid_file" ]]; then
    return 1
  fi
  local pid
  pid="$(tr -dc '0-9' <"$pid_file")"
  if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
    printf '%s\n' "$pid"
    return 0
  fi
  rm -f "$pid_file"
  return 1
}

if ! running_pid "$operator_pid_file" >/dev/null; then
  nohup "$package_root/bin/stoneage-operator" \
    -package-root "$package_root" \
    -socket "$operator_socket" \
    -gateway "$gateway_address" \
    -upstream "$upstream_address" \
    -saac "$saac_address" \
    -auth-db "$auth_database" \
    </dev/null >"$log_root/operator.log" 2>&1 &
  printf '%s\n' "$!" >"$operator_pid_file"
fi

for _ in {1..40}; do
  if [[ -S "$operator_socket" ]]; then
    break
  fi
  sleep 0.05
done
if [[ ! -S "$operator_socket" ]]; then
  echo "Operator failed to start; inspect $log_root/operator.log" >&2
  exit 1
fi

if ! running_pid "$admin_pid_file" >/dev/null; then
  admin_args=(
    -db "$auth_database"
    -listen "$admin_listen"
    -config "$server_config"
    -saac-config "$saac_config"
    -operator-socket "$operator_socket"
  )
  if [[ -n "${STONEAGE_ADMIN_SETUP_TOKEN:-}" ]]; then
    admin_args+=( -setup-token "$STONEAGE_ADMIN_SETUP_TOKEN" )
  fi
  case "${STONEAGE_ADMIN_COOKIE_SECURE:-}" in
    1|true|TRUE|yes|YES|on|ON) admin_args+=( -cookie-secure ) ;;
  esac
  nohup "$package_root/bin/stoneage-admin" serve "${admin_args[@]}" \
    </dev/null >"$log_root/admin.log" 2>&1 &
  printf '%s\n' "$!" >"$admin_pid_file"
fi

echo "StoneAge admin is listening on $admin_listen"
echo "Database: $auth_database"
echo "Open /setup only when STONEAGE_ADMIN_SETUP_TOKEN is set; otherwise use create-admin once."
