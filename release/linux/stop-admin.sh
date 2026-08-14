#!/usr/bin/env bash
set -euo pipefail

package_root="$(cd "$(dirname "$0")" && pwd)"
run_root="$package_root/run"
admin_pid_file="$run_root/admin.pid"
operator_pid_file="$run_root/operator.pid"
operator_socket="${STONEAGE_OPERATOR_SOCKET:-$run_root/operator.sock}"

stop_pid_file()
{
  local pid_file="$1"
  if [[ -f "$pid_file" ]]; then
    local pid
    pid="$(tr -dc '0-9' <"$pid_file")"
    if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
      kill "$pid" || true
      for _ in {1..50}; do
        if ! kill -0 "$pid" 2>/dev/null; then
          break
        fi
        sleep 0.1
      done
    fi
    rm -f "$pid_file"
  fi
}

stop_pid_file "$admin_pid_file"
stop_pid_file "$operator_pid_file"
rm -f "$operator_socket"
echo "StoneAge admin and operator stopped; SQLite accounts were preserved."
