#!/usr/bin/env bash
set -euo pipefail

package_root="$(cd "$(dirname "$0")" && pwd)"
run_root="$package_root/run"
operator_socket="${STONEAGE_OPERATOR_SOCKET:-$run_root/operator.sock}"

report()
{
  local name="$1"
  local pid_file="$2"
  if [[ -f "$pid_file" ]]; then
    local pid
    pid="$(tr -dc '0-9' <"$pid_file")"
    if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
      echo "$name=running pid=$pid"
      return
    fi
  fi
  echo "$name=stopped"
}

report operator "$run_root/operator.pid"
report admin "$run_root/admin.pid"
if [[ -S "$operator_socket" ]]; then
  echo "operator_socket=ready path=$operator_socket"
else
  echo "operator_socket=missing"
fi
