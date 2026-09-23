#!/bin/sh
set -eu
cd /src/gmsv
export STONEAGE_BATTLE_RECORD_DIR=/src/battle-smoke
export STONEAGE_BATTLE_RECORD_MIN_FREE_BYTES=0
export STONEAGE_RELEASE_VERSION=build-smoke
export STONEAGE_BATTLE_RULESET_ID
STONEAGE_BATTLE_RULESET_ID="$(sh /modern/prepare-battle-rules.sh /battle-smoke.cf)"
./gmsvjt.exe --battle-dataset --config /battle-smoke.cf --matches 16 --points 120 --seed 42 --repeats 2 --max-turns 100
python3 /modern/tests/verify-battle-dataset.py /src/battle-smoke --exporter /battle-export.py --matches 16
export STONEAGE_BATTLE_RECORD_DIR=/src/battle-censored
./gmsvjt.exe --battle-dataset --config /battle-smoke.cf --matches 2 --points 120 --seed 42 --max-turns 1
python3 /modern/tests/verify-battle-dataset.py /src/battle-censored --exporter /battle-export.py --matches 2 --censored
