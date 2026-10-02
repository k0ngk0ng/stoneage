#!/usr/bin/env python3
"""Attach the offline training transport without changing native combat rules."""
from pathlib import Path
import re
import sys


def integrate(root):
    edits = {
        "main.c": (
            b'    if (argc > 1 && strcmp(argv[1], "--battle-dataset") == 0)',
            b'    if (argc > 1 && strcmp(argv[1], "--battle-environment") == 0)\n'
            b'        return StoneAge_BattleEnvironmentMain(argc, argv);\n',
            b'StoneAge_BattleEnvironmentMain(argc, argv)',
        ),
        "main.c:rules": (
            b'    if (argc > 1 && strcmp(argv[1], "--battle-environment") == 0)',
            b'    if (argc > 1 && strcmp(argv[1], "--battle-rules") == 0)\n'
            b'        return StoneAge_BattleRulesMain(argc, argv);\n',
            b'StoneAge_BattleRulesMain(argc, argv)',
        ),
        "battle/battle_command.c": (
            b'\tif(\tgetfdFromCharaIndex( charaindex ) < 0 )return FALSE;',
            b'    if (StoneAge_BattleEnvironmentPacket(charaindex, pszCommand)) return TRUE;\n',
            b'StoneAge_BattleEnvironmentPacket(charaindex, pszCommand)',
        ),
    }
    pending = []
    # Extend the existing own-equipment J packet atomically: ID, MP and target
    # metadata describe the same slot revision, unlike a separately cached map.
    path = root / "char/char.c"
    raw = path.read_bytes()
    start, end = raw.index(b"\tcase 'j':"), raw.index(b"\tcase 'w':")
    section = raw[start:end]
    anchor = b'\t\t\treturn CHAR_statusSendBuffer;\n\t\t}\n }'
    addition = b'\t\t\tStoneAge_MagicObservationAppend(index, num, CHAR_statusSendBuffer, sizeof(CHAR_statusSendBuffer));\n'
    if b'StoneAge_MagicObservationAppend' in raw:
        if section.count(addition + anchor) != 1 or raw.count(b'StoneAge_MagicObservationAppend') != 1 or b'#include "stoneage_ai_observation.h"' not in raw:
            raise SystemExit("char/char.c: unexpected existing magic observation integration")
    else:
        if section.count(anchor) != 1 or b'StoneAge_MagicObservationAppend' in section:
            raise SystemExit("char/char.c: expected final J status return")
        section = section.replace(anchor, addition + anchor)
        raw = raw[:start] + section + raw[end:]
        if b'#include "stoneage_ai_observation.h"' not in raw:
            raw = b'#include "stoneage_ai_observation.h"\n' + raw
        pending.append((path, raw))
    for name, (anchor, addition, marker) in edits.items():
        path = root / name.split(":")[0]
        raw = next((data for p, data in reversed(pending) if p == path), path.read_bytes())
        if name == "battle/battle_command.c" and b'#include "stoneage_battle_dataset.h"' not in raw:
            raw = b'#include "stoneage_battle_dataset.h"\n' + raw
        if marker in raw:
            if addition + anchor not in raw:
                raise SystemExit(f"{name}: unexpected existing environment integration")
            continue
        if raw.count(anchor) != 1:
            raise SystemExit(f"{name}: expected exactly one environment integration point")
        pending.append((path, raw.replace(anchor, addition + anchor)))
    # Native reloads cannot keep advertising a digest of the old tables/config.
    # Each loader's first call belongs to startup; subsequent calls invalidate
    # metadata even if loading fails (it may have partially changed globals).
    loaders = {
        "configfile.c": ["readconfigfile"],
        "item/item.c": ["ITEM_readItemConfFile"],
        "battle/pet_skill.c": ["PETSKILL_initPetskill"],
        "magic/magic_base.c": ["MAGIC_initMagic", "ATTMAGIC_initMagic"],
        "char/enemy.c": ["ENEMYTEMP_initEnemy", "ENEMY_initEnemy"],
        "char/char_base.c": ["CHAR_Ride_CF_init", "CHAR_FmLeaderRide_init"],
        "char/char_data.c": ["LoadEXP"],
        "battle/battle_event.c": ["need_item_eneny_init"],
    }
    for name, functions in loaders.items():
        path = root / name
        raw = next((data for p, data in reversed(pending) if p == path), path.read_bytes())
        original = raw
        if b'#include "stoneage_ladder.h"' not in raw:
            raw = b'#include "stoneage_ladder.h"\n' + raw
        for function in functions:
            pattern = rb'\b' + function.encode() + rb'\s*\([^;{}]*\)\s*\{'
            matches = list(re.finditer(pattern, raw))
            if len(matches) != 1:
                raise SystemExit(f"{name}: expected exactly one rule loader {function}")
            position = matches[0].end()
            addition = (b'\n    static int stoneage_rules_loaded;\n'
                        b'    if (stoneage_rules_loaded) StoneAge_BattleRulesChanged();\n'
                        b'    stoneage_rules_loaded = 1;\n')
            if raw[position:].startswith(addition):
                continue
            if b'stoneage_rules_loaded' in raw[position:raw.find(b'}', position)]:
                raise SystemExit(f"{name}: unexpected rule loader integration")
            raw = raw[:position] + addition + raw[position:]
        if raw != original:
            pending.append((path, raw))
    for path, raw in pending:
        path.write_bytes(raw)


if __name__ == "__main__":
    integrate(Path(sys.argv[1]))
