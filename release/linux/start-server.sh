#!/usr/bin/env bash
set -euo pipefail

package_root="$(cd "$(dirname "$0")" && pwd)"
"$package_root/start-game.sh"
"$package_root/start-gateway.sh"
echo "StoneAge server started. Do not expose ports 19065 or 9300."
