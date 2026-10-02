#!/bin/sh
# Run from the prepared gmsv directory. No account server or network listener.
set -eu
if [ "$#" -ne 2 ] || [ "$1" != "--config" ]; then
    echo 'usage: run-battle-environment.sh --config setup.cf' >&2
    exit 2
fi
: "${STONEAGE_BATTLE_RECORD_DIR:?set a persistent training data directory}"
helper_dir="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
# Derive this for every worker; never trust a stale caller-supplied rule label.
STONEAGE_BATTLE_BINARY=./gmsvjt.exe
export STONEAGE_BATTLE_BINARY
STONEAGE_BATTLE_RULESET_ID="$(sh "$helper_dir/prepare-battle-rules.sh" "$2")"
export STONEAGE_BATTLE_RULESET_ID
exec ./gmsvjt.exe --battle-environment --config "$2"
