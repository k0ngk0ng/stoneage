#!/bin/sh
# Isolated runtime entry: no production game/account directory is needed.
set -eu
root="${STONEAGE_DEFAULTS_ROOT:-/opt/stoneage/defaults}/gmsv"
cd "$root"
export STONEAGE_BATTLE_RULESET_ID
STONEAGE_BATTLE_RULESET_ID="$(/opt/stoneage/bin/prepare-battle-rules.sh "$root/setup.cf")"
exec ./gmsvjt.exe --battle-dataset --config "$root/setup.cf" "$@"
