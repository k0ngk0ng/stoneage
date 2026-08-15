#!/usr/bin/env bash
set -euo pipefail

package_root="$(cd "$(dirname "$0")" && pwd)"
STONEAGE_SERVER_CONTAINER="${STONEAGE_SERVER_CONTAINER:-stoneage-revival-server}" \
  "$package_root/scripts/stop-legacy-server.sh"
echo "Game service stopped; runtime data was preserved."
