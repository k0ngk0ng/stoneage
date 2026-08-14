#!/usr/bin/env bash
set -euo pipefail

package_root="$(cd "$(dirname "$0")" && pwd)"
"$package_root/stop-gateway.sh"
"$package_root/start-gateway.sh"
