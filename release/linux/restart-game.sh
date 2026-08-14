#!/usr/bin/env bash
set -euo pipefail

package_root="$(cd "$(dirname "$0")" && pwd)"
"$package_root/stop-game.sh"
"$package_root/start-game.sh"
