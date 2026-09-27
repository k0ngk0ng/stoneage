#!/bin/sh
set -eu

native_root="${1:-/src/gmsv}"
native_config="${2:-/battle-smoke.cf}"
modern_root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
smoke_root="$(mktemp -d "$native_root/../ladder-smoke.XXXXXX")"
export TMPDIR="$smoke_root"
export STONEAGE_LADDER_DB="$smoke_root/ladder.db"
export STONEAGE_LADDER_SMOKE_ARCHIVE="$smoke_root/checkpoint.data"
export STONEAGE_LADDER_SMOKE_MAPS="$smoke_root/maps"
python3 - "$STONEAGE_LADDER_SMOKE_MAPS" <<'PY'
import struct,sys
from pathlib import Path
root=Path(sys.argv[1]);root.mkdir()
for floor in (0,7001,7025):
    header=b"LS2MAP"+struct.pack("!H",floor)+b"ladder-smoke".ljust(32,b"\0")+struct.pack("!HH",64,64)
    (root/str(floor)).write_bytes(header+b"\0"*(64*64*4))
PY
export STONEAGE_BATTLE_RECORD_DIR="$smoke_root/records"
export STONEAGE_BATTLE_RECORD_MIN_FREE_BYTES=0
smoke_sanitize=""
if [ "${STONEAGE_LADDER_SMOKE_SANITIZE:-0}" = 1 ]; then
    smoke_sanitize="-fsanitize=address"
    export ASAN_OPTIONS=detect_leaks=0
fi

# Preserve the engine's globals without starting its original service main.
objcopy --redefine-sym main=StoneAge_UnusedServiceMain "$native_root/main.o" "$smoke_root/main.o"
gcc -O0 -g3 -std=gnu89 -fcommon -D_FORTIFY_SOURCE=0 $smoke_sanitize \
    -I"$native_root/include" -I"$modern_root" \
    -c "$modern_root/tests/ladder-native-smoke.c" -o "$smoke_root/test.o"
gcc -O0 -g3 -std=gnu99 $smoke_sanitize -I"$modern_root" \
    -c "$modern_root/stoneage_ladder_core.c" -o "$smoke_root/core.o"
set -- "$smoke_root/test.o" "$smoke_root/main.o" "$smoke_root/core.o"
for object in "$native_root"/*.o; do
    case "$object" in
        */main.o|*/stoneage_ladder.o|*/stoneage_ladder_core.o) ;;
        *) set -- "$@" "$object" ;;
    esac
done
gcc $smoke_sanitize -o "$smoke_root/native-test" "$@" \
    "$native_root/char/libchar.a" "$native_root/npc/libnpc.a" \
    "$native_root/map/libmap.a" "$native_root/item/libitem.a" \
    "$native_root/magic/libmagic.a" "$native_root/battle/libbattle.a" \
    -lm -lpthread -lsqlite3
cd "$native_root"
# The legacy map enumerator uses 64-byte path entries.
export STONEAGE_LADDER_SMOKE_MAPS="../$(basename "$smoke_root")/maps"
if ! "$smoke_root/native-test" "$native_config" >"$smoke_root/output.log" 2>&1; then
    tail -60 "$smoke_root/output.log"
    exit 1
fi
python3 - "$smoke_root/output.log" <<'PY'
import json
import sys
from pathlib import Path
lines = Path(sys.argv[1]).read_bytes().decode("cp936", errors="replace").splitlines()
results = [json.loads(line[7:])["snapshot"]["result"] for line in lines if line.startswith("LADDER|")]
assert len(results) == 1, "missing native combat result"
result = results[0]
assert len(result["members"]) == 10 and result["mode"] == 5
assert result["rated"] and result["reason"] == "defeat"
assert sum(p["rating_delta"] for p in result["members"]) == 0
assert sum(p["statistics"]["damage"] for p in result["members"]) > 0
assert sum(p["statistics"]["pet_damage"] for p in result["members"]) > 0
assert sum(p["statistics"]["player_kills"] for p in result["members"]) >= 5
assert sum(p["statistics"]["player_kills"] for p in result["members"]) == sum(p["statistics"]["deaths"] for p in result["members"])
stats_results = [json.loads(line[13:])["snapshot"]["result"] for line in lines if line.startswith("LADDER_STATS|")]
assert len(stats_results) == 1
left, right = (p["statistics"] for p in stats_results[0]["members"])
assert left["healing"] == 19 and left["revives"] == 1 and left["deaths"] == 1
assert right["damage"] == left["damage_taken"] - 5, "unrelated HP loss was misattributed to poison"
assert right["player_kills"] == 1 and left["damage"] == 0, "reflected damage credited to attacker"
team_results = [json.loads(line[len("LADDER_TEAM_STATS|"):])["snapshot"]["result"] for line in lines if line.startswith("LADDER_TEAM_STATS|")]
assert len(team_results) == 1
contributor, finisher, target, untouched = (p["statistics"] for p in team_results[0]["members"])
assert contributor["damage"] == 22 and contributor["pet_damage"] == 12
assert contributor["assists"] == 2, "duplicate hits or prior-life damage inflated assists"
assert contributor["player_kills"] == contributor["pet_kills"] == 0
assert finisher["player_kills"] == 2 and finisher["pet_kills"] == 1 and finisher["assists"] == 0
assert target["deaths"] == 2 and target["revives"] == 1 and target["healing"] == 20
assert target["damage_taken"] == contributor["damage"] + finisher["damage"]
assert target["pet_damage_taken"] > 7 and untouched["damage_taken"] == untouched["assists"] == 0
ride_results = [json.loads(line[len("LADDER_RIDE_STATS|"):])["snapshot"]["result"] for line in lines if line.startswith("LADDER_RIDE_STATS|")]
assert len(ride_results) == 1
attacker, rider = (p["statistics"] for p in ride_results[0]["members"])
assert attacker["damage"] == rider["damage_taken"] > rider["ride_damage_taken"] > 0
assert rider["ride_damage_taken"] == rider["pet_damage_taken"]
assert attacker["player_kills"] == attacker["pet_kills"] == rider["deaths"] == 1
assert attacker["pet_damage"] == rider["damage"] == 0
ride_hp = [list(map(int, line.split("|")[1:])) for line in lines if line.startswith("LADDER_RIDE_EXPECTED|")]
assert len(ride_hp) == 1
assert attacker["damage"] == sum(ride_hp[0]), "overkill must not count beyond the actual HP lost"
assert rider["ride_damage_taken"] == ride_hp[0][1]
for line in lines:
    if line.startswith("native "):
        print(line)
print("Native ladder combat and reconnect smoke passed; artifacts:", sys.argv[1])
PY

# Exercise the saved native payload through the real SAAC listener, writer,
# acknowledgement and restart/load path. All files and sockets are fixtures.
python3 "$modern_root/tests/test-saac-ladder-checkpoint.py" \
    --binary "$native_root/../saac/saacjt.exe" \
    --work "$smoke_root/saac" --archive "$STONEAGE_LADDER_SMOKE_ARCHIVE"
