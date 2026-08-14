#!/usr/bin/env bash
set -euo pipefail
. "$(dirname "$0")/common.sh"

"$(dirname "$0")/restart-game.sh"
"$(dirname "$0")/restart-gateway.sh"
