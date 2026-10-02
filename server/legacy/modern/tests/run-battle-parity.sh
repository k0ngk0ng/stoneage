#!/bin/sh
set -eu
native_root="${1:?native GMSV build directory}"
native_config="${2:?native configuration}"
probe="${3:-}"
modern_root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
parity_root="$(mktemp -d "$native_root/../battle-parity.XXXXXX")"
export TMPDIR="$parity_root"
objcopy --redefine-sym main=StoneAge_UnusedServiceMain "$native_root/main.o" "$parity_root/main.o"
gcc -O0 -g3 -std=gnu89 -fcommon -D_FORTIFY_SOURCE=0 -I"$native_root/include" -I"$modern_root" \
    -c "$modern_root/tests/battle-parity-native.c" -o "$parity_root/test.o"
gcc -O0 -g3 -std=gnu99 -I"$modern_root" -c "$modern_root/stoneage_ladder_core.c" -o "$parity_root/core.o"
gcc -O2 -g3 -std=gnu89 -fcommon -w -Drand=parity_transport_rand -I"$native_root/include" \
    -c "$native_root/autil.c" -o "$parity_root/transport.o"
set -- "$parity_root/test.o" "$parity_root/main.o" "$parity_root/core.o" "$parity_root/transport.o"
for object in "$native_root"/*.o; do
    case "$object" in
        */main.o|*/autil.o|*/stoneage_ladder.o|*/stoneage_ladder_core.o|*/stoneage_battle_dataset.o) ;;
        *) set -- "$@" "$object" ;;
    esac
done
gcc -no-pie -Wl,--wrap=rand,--wrap=srand -o "$parity_root/native-parity" "$@" "$native_root/char/libchar.a" "$native_root/npc/libnpc.a" \
    "$native_root/map/libmap.a" "$native_root/item/libitem.a" "$native_root/magic/libmagic.a" \
    "$native_root/battle/libbattle.a" -lm -lpthread -lsqlite3
cd "$native_root"
if [ "$probe" = guardian ]; then
    python3 "$modern_root/tests/test-guardian-parity.py" "$parity_root/native-parity" "$native_config" "$parity_root"
elif [ -n "$probe" ]; then
    python3 "$modern_root/tests/test-battle-parity.py" "$parity_root/native-parity" "$native_config" "$parity_root" "$probe"
else
    python3 "$modern_root/tests/test-battle-parity.py" "$parity_root/native-parity" "$native_config" "$parity_root"
fi
