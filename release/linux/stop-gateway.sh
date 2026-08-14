#!/usr/bin/env bash
set -euo pipefail

package_root="$(cd "$(dirname "$0")" && pwd)"
gateway_pid_file="$package_root/gateway.pid"

if [[ -f "$gateway_pid_file" ]]; then
  gateway_pid="$(tr -dc '0-9' <"$gateway_pid_file")"
  if [[ -n "$gateway_pid" ]] && kill -0 "$gateway_pid" 2>/dev/null; then
    kill "$gateway_pid" || true
    for _ in {1..50}; do
      if ! kill -0 "$gateway_pid" 2>/dev/null; then
        break
      fi
      sleep 0.1
    done
  fi
  rm -f "$gateway_pid_file"
fi
echo "Gateway stopped."
