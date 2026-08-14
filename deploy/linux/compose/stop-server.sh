#!/usr/bin/env bash
set -euo pipefail

"$(dirname "$0")/stop-gateway.sh"
"$(dirname "$0")/stop-game.sh"
echo "StoneAge server stopped; runtime data was preserved."
