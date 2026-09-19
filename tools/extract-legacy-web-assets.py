#!/usr/bin/env python3
"""Build the browser asset pack directly from the preserved sa_2903 data.

The browser pack is a reproducible selection of the original indexed graphics plus
source-rendered field and battle viewports from the sa_2903 data set.
"""

from __future__ import annotations

import argparse
import json
import re
import shutil
import sys
from pathlib import Path


REPO = Path(__file__).resolve().parents[1]
COOKER = REPO / "tools"
sys.path.insert(0, str(COOKER))
import asset_cooker as legacy  # noqa: E402  (the decoder is shared, output is not)


CREATION_SPRITES = tuple(100000 + index * 20 for index in range(12))
CREATION_SPRITE_ACTIONS = frozenset((3, 4))  # ANIM_STAND / ANIM_WALK
FIELD_BOOTSTRAP_ACTIONS = frozenset((3, 4))  # complete visible STAND/WALK rows
# 100025 is sa_2903's default new-character image (the web create form uses
# it as its initial value). Keep it in the compact field-action pack even
# though it is not among the twelve character-select portraits.
FIELD_ACTION_SPRITES = tuple(dict.fromkeys((*CREATION_SPRITES, 100025)))
# SYSTEMINC/BATTLEMAP.H in the preserved sa_2903 client.  The accompanying
# data directory also contains two later battle SABs, but the 2.5 executable
# never indexes them: NETPROC.CPP and BATTLEMAP.CPP both fall back to map 0
# when the server field is outside this range.
BATTLE_MAP_FILES_25 = 218


UI_BITMAPS = {
    # field chrome
    **{f"window_{i}": 26001 + i for i in range(9)},
    "title_system": 26010,
    "title_logout": 26011,
    "task_bar_back": 26012,
    "battle_bar_player": 26013,
    "battle_bar_pet": 26014,
    "title_chat": 26015,
    "title_bgm": 26016,
    "title_se": 26017,
    "title_result": 26018,
    "close": 26042,
    "return": 26043,
    "cancel": 26050,
    "ok": 26093,
    "yes": 26094,
    "no": 26095,
    "exit": 26096,
    "seal": 26097,
    "buy": 26098,
    # field menu / task bar states
    "field_menu_left": 26100,
    "field_menu": 26101,
    "field_menu_on": 26102,
    "field_card": 26103,
    "field_card_on": 26104,
    "field_group": 26105,
    "field_group_on": 26106,
    "mail_lamp": 26107,
    "field_menu_right": 26110,
    "field_join": 26111,
    "field_join_on": 26112,
    "field_duel": 26113,
    "field_duel_on": 26114,
    "field_action": 26115,
    "field_action_on": 26116,
    "field_time_0": 26117,
    "field_time_1": 26118,
    "field_time_2": 26119,
    "field_time_3": 26120,
    "field_menu_right_back": 26121,
    # _SA_VERSION_25 / _SA_VERSION_SPECIAL field controls.  The 26xxx
    # records are logical CG numbers and resolve through bitmapnumbertable;
    # the 35xxx/55xxx records are direct ADRN entries used by the 2.5 build.
    "field_menu_left_25": 26236,
    # Trade-capable 2.5 field plate used when _SPECIAL_LOGO is not enabled
    # (the native drawField() bNewServer=false path).  Keep this alongside
    # the 172px special-logo plate so either server handshake can be packed.
    "field_menu_left_trade": 26233,
    "field_trade": 26234,
    "field_trade_on": 26235,
    "field_channel": 26237,
    "field_channel_on": 26238,
    "field_help_25": 55237,
    "field_help_25_on": 55238,
    "field_signin": 55240,
    "field_signin_on": 55239,
    "field_changeteam": 55247,
    "field_changeteam_on": 55248,
    "field_market": 55101,
    "field_market_on": 55100,
    "field_street_vendor": 35227,
    "field_street_vendor_on": 35226,
    # sa_2903's classic 2.5 trade surface.  Later clients remap the new
    # _TRADESYSTEM2 artwork to CG 26328, but that logical record is not in
    # the bundled 2.5 ADRN table.  The actual 620x456 window shipped beside
    # sa_2903 is logical 40000 (physical record 126231).
    "trade_window_25": 40000,
    **{f"task_map_{state}": 26150 + state for state in range(2)},
    **{f"task_status_{state}": 26152 + state for state in range(2)},
    **{f"task_pet_{state}": 26154 + state for state in range(2)},
    **{f"task_item_{state}": 26156 + state for state in range(2)},
    **{f"task_mail_{state}": 26158 + state for state in range(2)},
    **{f"task_album_{state}": 26160 + state for state in range(2)},
    **{f"task_system_{state}": 26162 + state for state in range(2)},
    # battle menu states
    "battle_attack": 25100,
    "battle_attack_on": 25101,
    "battle_magic": 25102,
    "battle_magic_on": 25103,
    "battle_capture": 25104,
    "battle_capture_on": 25105,
    "battle_help": 25106,
    "battle_help_on": 25107,
    "battle_guard": 25108,
    "battle_guard_on": 25109,
    "battle_item": 25110,
    "battle_item_on": 25111,
    "battle_pet": 25112,
    "battle_pet_on": 25113,
    "battle_escape": 25114,
    "battle_escape_on": 25115,
    "battle_button_base": 25116,
    "battle_button_cross": 25117,
    # login / character selection
    "title": 29001,
    "title_name_0": 29002,
    "title_name_1": 29003,
    "title_name_2": 29004,
    "title_name_3": 29005,
    "title_name_4": 29006,
    "title_name_5": 29007,
    "title_name_6": 29008,
    "title_name_7": 29009,
    "title_name_8": 29010,
    "title_loading": 29019,
    "title_id_pass": 29020,
    "title_id_pass_ok": 29021,
    "title_id_pass_quit": 29022,
    "character_select_bg": 29032,
    "character_login": 29033,
    "character_new": 29034,
    "character_delete": 29035,
    "character_back": 29036,
}

# The original executable keeps most menu chrome in the 26xxx logical range
# and the battle/result overlays in the 25xxx/26xxx ranges.  The named table
# above covers the field/login controls, but a complete client also needs the
# rest of the indexed menu pieces (mail, album, status, item, pet, shop and
# family windows).  Emit every valid record in these ranges directly from
# sa_2903's ADRNBIN; unused holes are skipped by the cooker.
UI_BITMAP_RANGES = (
    (25000, 25200),  # battle command/result pieces
    # BattleCntDownDisp() uses the logical 25900..25909 digit records.  They
    # resolve to the small 8794..8803 REALBIN sprites; omitting this range
    # leaves the web client with no native countdown artwork.
    (25900, 25910),
    (26000, 26300),  # menu/window/task-bar pieces
    (26500, 26532),  # battle speech/result icons
    (29000, 29050),  # title/login/character creation screens
)

# BattleMenuProc() paints these ten logical CG records through
# BattleCntDownDisp().  Keep the timing contract next to the extracted
# aliases so the browser can use the same source-of-truth as the rest of the
# UI instead of relying only on physical filenames (8794..8803).
BATTLE_COUNTDOWN_LOGICAL_BASE = 25900
# The preserved 2.5 ``sa_2903.exe`` and its matching source
# (vendor/upstream/code_sa_client/SYSTEM/BATTLEMENU.CPP) use
# ``GetTickCount() + BATTLE_CNT_DOWN_TIME`` with a 30000 ms constant.  The
# 99000 ms ``_SA_VERSION_25`` value belongs to a separate 8.5 source tree and
# must not leak into this 2.5 web pack.
BATTLE_COUNTDOWN_DURATION_MS = 30000

# ``oft.cpp`` does not draw abstract primitives for ranged battle attacks.
# ATT_BOW creates two T_PRIO_BOW actions: a ground shadow and the real
# arrow/axe/stone/firecracker 28 pixels above it.  ATT_BOOMERANG uses the
# original SPR_boomerang bitmap.  Export the complete 16-way arrow rows plus
# the direct records used by the other stock 2.5 weapon types so the browser
# can render the same REALBIN art instead of CSS approximations.
BATTLE_PROJECTILE_BITMAPS = {
    *range(25630, 25646),  # CG_ARROW_00 + course/2 (visible arrow)
    *range(25650, 25666),  # CG_ARROW_00 + course/2 + 20 (ground shadow)
    25785,                 # thrown stone
    25786,                 # stone/firecracker shadow
    24350,                 # firecracker
}

# The character-select protocol carries the portrait bitmap as the second
# field of its legacy option string.  sa_2903's standard player portraits are
# the contiguous 30000–30999 logical bitmap range (64×72, matching the empty
# portrait frames in bitmap_9118).  Keep the complete range in the web pack so
# an account can use any original face without falling back to a placeholder.
FACE_BITMAPS = range(30000, 31000)

# ``__ALBUM_4`` is the album table used by the 2.5 build.  The source tree
# keeps the table as an include file because the Windows client compiles it
# directly into ``PetAlbumTbl``.  Export the first 224 records (including the
# deliberate empty slots) beside the bitmap manifest so the browser can use
# the same page numbering and species/graphic mapping without parsing C++ at
# runtime. Explicitly enabled pets are appended after these stable slots;
# never insert them into the base table or invalidate existing album saves.
ALBUM_4_SIZE = 224


def album_catalog():
    path = REPO / "reference" / "anson1788-stoneage" / "石器时代8.5客户端最新源代码" / "石器源码" / "systeminc" / "petName.h"
    if not path.is_file():
        return []
    import re

    text = path.read_text(encoding="utf-8-sig")
    rows = []
    pattern = re.compile(r'\{\s*(-?\d+)\s*,\s*"([^"]*)"\s*(?:/\*.*?\*/)?\s*,\s*(\d+)\s*\}', re.S)
    for position, match in enumerate(pattern.finditer(text)):
        if position >= ALBUM_4_SIZE:
            break
        album_no, name, graphic = match.groups()
        rows.append({
            "index": position,
            "albumNo": int(album_no),
            "name": name,
            "graphic": int(graphic),
            "valid": int(album_no) >= 0 and int(graphic) > 0,
        })
    # Keep the page count stable even if a damaged source include is missing
    # a trailing placeholder.  Empty records are exactly what MENU.CPP paints
    # for unused PetAlbum slots.
    while len(rows) < ALBUM_4_SIZE:
        rows.append({"index": len(rows), "albumNo": -1, "name": "", "graphic": 0, "valid": False})
    extensions = json.loads((REPO / "client" / "web" / "album-extensions.json").read_text(encoding="utf-8"))
    for pet in extensions:
        if any(row["graphic"] == pet["graphic"] for row in rows):
            raise ValueError(f"duplicate album graphic {pet['graphic']}")
        rows.append({**pet, "index": len(rows), "valid": True})
    return rows

# NPCs and field objects sent in C packets are ordinary REALBIN graphics, not
# SPRADR animations.  Keep the standard 2.5 NPC block plus the handful of
# static board/fixture graphics used by the bundled maps so a live field never
# turns a server NPC into a coloured rectangle while its ground is native.
ACTOR_BITMAPS = {
    *range(16000, 16300),
    # Pet/monster graphics use the direct 100000-series adrn records in
    # field C packets (for example 100290, the guardian beast in the bundled
    # village).  Keep the complete standard block so live actors never fall
    # back to a CSS rectangle when their sprite animation is not in SPRADR.
    *range(100000, 101000),
    10062,
}


def server_actor_graphics() -> set[int]:
    """Collect direct REALBIN graphics used by the in-tree 2.5 data.

    Field C records carry the graphic number verbatim.  The old range-only
    pack stopped at 100999, while the bundled 2.5 enemy tables use many
    101xxx records (for example 101728 and 101866); those NPCs consequently
    became invisible in the browser even though their sa_2903 pixels exist.
    Keep this scan limited to actor/skill tables, not itemset.txt, so the
    generated pack follows the server's 2.5 content without emitting tens of
    thousands of unrelated equipment graphics.
    """
    data_root = REPO / "server" / "legacy" / "source" / "2.5" / "gmsv" / "data"
    paths = [
        *data_root.glob("enemy*.txt"),
        data_root / "profession.txt",
        # NPCCREATE files are the authoritative source for fixed 2.5 NPCs
        # such as signs, bus stops and river/airway markers.  Their graphic
        # numbers are in the five-digit 104xx/160xx ranges and never appear
        # in enemy*.txt, so a range-only actor pack silently dropped them.
        *data_root.glob("npc/**/*.create"),
        # mapset*.txt contains the same logical graphic table for generated
        # map NPCs.  Include its first column as a conservative fallback for
        # servers that load a mapset entry without a generated .create file.
        *data_root.glob("map/mapset*.txt"),
    ]
    numbers: set[int] = set()
    pattern = re.compile(r"(?<!\d)(10\d{4})(?!\d)")
    for path in paths:
        if not path.is_file():
            continue
        try:
            text = path.read_text(encoding="utf-8", errors="ignore")
        except OSError:
            continue
        numbers.update(int(value) for value in pattern.findall(text))
        if "/npc/" in path.as_posix() or path.name.startswith("mapset"):
            # Only consume explicit NPC graphic assignments or the first
            # mapset column.  Scanning every five-digit number would pull in
            # coordinates, floor ids and event parameters as fake sprites.
            if "/npc/" in path.as_posix():
                numbers.update(
                    int(value)
                    for value in re.findall(r"(?im)^\s*graphicname\s*=\s*(\d+)\b", text)
                )
            else:
                for line in text.splitlines():
                    match = re.match(r"^\s*(1\d{4})\s+", line)
                    if match:
                        numbers.add(int(match.group(1)))
    return numbers


# Include the actual 2.5 actor table in addition to the standard 100000
# block.  ``actor_pack`` still skips malformed/reserved records individually.
ACTOR_BITMAPS.update(server_actor_graphics())


def record_visual_metadata(record):
    """Expose the ADRN hit/depth metadata consumed by map.cpp.

    ``hit`` packs the native priority class in its hundreds digit and the
    walk/hit flag in its low two digits. ``atari_x``/``atari_y`` are the
    sprite hit-box extents used by checkPrioPartsVsChar(); keeping them beside
    every bitmap lets the browser reproduce the original tree/roof/NPC
    ordering instead of sorting only by a tile's screen Y coordinate.
    """
    return {
        "hit_x": int(record.hit_x),
        "hit_y": int(record.hit_y),
        "hit": int(record.hit % 100),
        "prio_type": int(record.hit // 100),
    }


def emit_bitmap(name: str, number: int, records, real_path, palette, output, manifest):
    # ``number`` is always the logical CG/ADRN number passed by the caller.
    # The alias table is written as JSON strings, so normalize it before using
    # the value as a ``records`` key.  Passing an already-resolved physical
    # number here used to look it up a second time and handed a string to
    # read_bitmap(), making a clean asset rebuild fail on the direct 2.5
    # controls (35227/552xx).
    alias = manifest.setdefault("bitmap_aliases", {}).get(str(number))
    physical = int(alias) if alias is not None else int(number)
    bitmap = legacy.read_bitmap(real_path, records, physical)
    filename = f"bitmap_{physical}.png"
    legacy.write_png(output / "bitmaps" / filename, bitmap, palette)
    record = records[physical]
    manifest.setdefault("bitmaps", {})[str(number)] = {
        "file": f"bitmaps/{filename}",
        "physical": physical,
        "width": bitmap.width,
        "height": bitmap.height,
        "xoffset": bitmap.xoffset,
        "yoffset": bitmap.yoffset,
        "bmp_number": record.bmp_number,
        "name": name,
        **record_visual_metadata(record),
    }
    manifest.setdefault("ui", {})[name] = {
        "file": f"bitmaps/{filename}",
        "bitmap": number,
        "width": bitmap.width,
        "height": bitmap.height,
        "xoffset": bitmap.xoffset,
        "yoffset": bitmap.yoffset,
    }
    return f"bitmaps/{filename}"


def emit_graphic(logical: int, records, by_number, real_path, palette, output, manifest):
    """Emit one logical map/object graphic and retain its PC lookup.

    Values in a DAT or an online M packet are ``bmp_number`` values, while
    ``real_15.bin`` is addressed by the physical adrn record number.  The
    Windows client performs that translation through ``bitmapnumbertable``
    before drawing.  Keep the same translation in the manifest instead of
    making the browser guess whether a number happens to be physical.
    """
    # DAT tables in this client contain both logical bitmap numbers (the
    # ``bmp_number`` field) and a few direct adrn record numbers.  The
    # original bitmapnumbertable accepts either form; preserve that behavior
    # instead of silently dropping direct entries such as 22413 on floor 1000.
    candidates = by_number.get(logical) or ([logical] if logical in records else None)
    if not candidates:
        return None
    physical = candidates[-1]
    aliases = manifest.setdefault("bitmap_aliases", {})
    aliases[str(logical)] = str(physical)
    bitmaps = manifest.setdefault("bitmaps", {})
    existing = bitmaps.get(str(physical))
    if existing:
        return existing.get("file")

    bitmap = legacy.read_bitmap(real_path, records, physical)
    filename = f"bitmap_{physical}.png"
    legacy.write_png(output / "bitmaps" / filename, bitmap, palette)
    record = records[physical]
    bitmaps[str(physical)] = {
        "file": f"bitmaps/{filename}",
        "physical": physical,
        "width": bitmap.width,
        "height": bitmap.height,
        "xoffset": bitmap.xoffset,
        "yoffset": bitmap.yoffset,
        "bmp_number": record.bmp_number,
        **record_visual_metadata(record),
    }
    return f"bitmaps/{filename}"


def server_item_graphics() -> list[int]:
    """Return every logical item graphic used by the running 2.5 GMSV.

    ``ITEM_readItemConfFile()`` reads ``imagenumber`` from the eighteenth
    comma-delimited itemset field.  Those values are logical ``bmp_number``
    ids, not physical ADRN record indexes.  Sprite extraction can happen to
    create a file named ``bitmap_24008.png`` for physical record 24008, while
    the meat item whose logical image is 24008 actually resolves to physical
    record 6926.  Keeping an explicit item pack prevents that namespace
    collision from making valid inventory records render as empty slots.
    """
    path = REPO / "runtime" / "legacy-server" / "gmsv" / "data" / "itemset.txt"
    numbers: set[int] = set()
    try:
        lines = path.read_bytes().splitlines()
    except OSError:
        return []
    for line in lines:
        if not line or line.startswith(b"#"):
            continue
        fields = line.split(b",")
        if len(fields) < 18:
            continue
        try:
            number = int(fields[17].strip())
        except ValueError:
            continue
        if number > 0:
            numbers.add(number)
    return sorted(numbers)


def item_pack(records, by_number, real_path, palette, output, manifest):
    """Emit and index all item icons referenced by the deployed 2.5 data."""
    emitted = 0
    missing = []
    for logical in server_item_graphics():
        try:
            filename = emit_graphic(logical, records, by_number, real_path, palette, output, manifest)
        except legacy.AssetError:
            filename = None
        if filename:
            emitted += 1
        else:
            missing.append(logical)
    manifest["item_visual_bitmap_count"] = emitted
    manifest["missing_item_graphics"] = missing


def ui_pack(records, by_number, real_path, palette, output, manifest):
    """Refresh the original UI bitmap subset without rebuilding map/SPR data."""
    manifest.setdefault("bitmaps", {})
    manifest.setdefault("ui", {})
    manifest.setdefault("bitmap_aliases", {})
    for name, logical in UI_BITMAPS.items():
        # Most CG_* constants are logical bitmap numbers with an ADRN
        # translation.  A few 2.5 additions are direct ADRN records and have
        # bmp_number == 0, so address those by record number only when no
        # logical alias exists.
        candidates = by_number.get(logical) or ([logical] if logical in records else None)
        if not candidates:
            continue
        physical = candidates[-1]
        manifest["bitmap_aliases"][str(logical)] = str(physical)
        emit_bitmap(name, logical, records, real_path, palette, output, manifest)
    for start, end in UI_BITMAP_RANGES:
        for logical in range(start, end):
            candidates = by_number.get(logical)
            if not candidates:
                continue
            physical = candidates[-1]
            manifest["bitmap_aliases"][str(logical)] = str(physical)
            try:
                emit_bitmap(f"cg_{logical}", logical, records, real_path, palette, output, manifest)
            except legacy.AssetError:
                # Reserved ADRN records are not drawable; skip only the bad
                # alias and leave the rest of the deterministic UI pack.
                manifest["bitmap_aliases"].pop(str(logical), None)
    for logical in sorted(BATTLE_PROJECTILE_BITMAPS):
        # Several weapon records (notably the stone/firecracker pair) are
        # addressed directly by ADRN number and therefore have bmp_number 0.
        # Match the named UI path's physical fallback instead of requiring a
        # logical alias that does not exist in the 2.5 table.
        candidates = by_number.get(logical) or ([logical] if logical in records else None)
        if not candidates:
            continue
        physical = candidates[-1]
        manifest["bitmap_aliases"][str(logical)] = str(physical)
        try:
            emit_bitmap(f"battle_projectile_{logical}", logical, records, real_path, palette, output, manifest)
        except legacy.AssetError:
            manifest["bitmap_aliases"].pop(str(logical), None)


def map_data_paths() -> list[Path]:
    """Every floor's DAT data file, however its extension happens to be cased.

    The preserved set is not consistent: 76 floors ship lowercase (200, 600,
    3000.., 6004x..) while the rest are ``.DAT``.  A case-sensitive glob
    silently skipped the lowercase ones, so nothing collected their tiles and
    the browser painted the Garuka cave entrance and every other lowercase
    floor's ground as black.
    """
    map_root = REPO / "runtime" / "legacy-client" / "map"
    return [
        path
        for path in map_root.iterdir()
        if path.is_file() and path.suffix.lower() == ".dat" and path.stem.isdigit()
    ]


def collect_map_resources(records, by_number, real_path, palette, output, manifest):
    """Index every visual used by the preserved DAT maps.

    M packets contain a small viewport rather than a complete map image.  A
    browser therefore needs the original ground/object bitmaps for whichever
    floor the character enters.  Shipping only map 200 made every other floor
    fall back to a fake gradient.  The DAT files are compact indexes; emit the
    referenced graphics once and expose all floor dimensions/render geometry
    so the browser can paint the live M window with the same isometric math as
    the PC client.
    """
    maps = manifest.setdefault("maps", {})
    missing: dict[str, list[int]] = {}
    visual_ids: set[int] = set()
    map_paths = map_data_paths()
    for map_path in sorted(map_paths, key=lambda path: int(path.stem)):
        try:
            width, height, layers = legacy.read_map(map_path)
        except (OSError, ValueError, legacy.AssetError):
            continue
        floor = int(map_path.stem)
        ids = sorted({value for layer in layers[:2] for value in layer if value > legacy.CG_INVISIBLE})
        visual_ids.update(ids)
        maps[str(floor)] = {
            "id": floor,
            "source": f"runtime/legacy-client/map/{map_path.name}",
            "source_width": width,
            "source_height": height,
            "origin": [0, 0],
            "render": {
                "origin_x": 256,
                # Isometric Y is bounded by the floor's row count.  Using
                # width here shifts every rectangular floor (for example
                # 1006, 30×40) by (height-width)*24 pixels and makes live M
                # actors/doors disagree with the server coordinates.
                "origin_y": (height - 1) * 24 + 256,
                "tile_step": [32, 24],
            },
        }

    for logical in sorted(visual_ids):
        try:
            emit_graphic(logical, records, by_number, real_path, palette, output, manifest)
        except legacy.AssetError:
            missing.setdefault("bitmap_decode", []).append(logical)
    if missing:
        manifest["missing_map_resources"] = missing
    manifest["map_visual_bitmap_count"] = len(visual_ids)


def collect_map_collision_metadata(manifest):
    """Export the 2.5 server's image walkability table.

    A live ``M`` packet contains logical tile/object image numbers, while the
    server validates a step through ``MAP_walkAbleFromPoint`` and
    ``data/map/mapset.txt``.  Keeping this small table beside the browser
    assets lets the client reject the same obviously blocked cells locally;
    it does not invent collision rules from a different client/version.
    """
    path = REPO / "runtime" / "legacy-server" / "gmsv" / "data" / "map" / "mapset.txt"
    if not path.is_file():
        return
    walkable: dict[str, int] = {}
    height: dict[str, int] = {}
    for line in path.read_text(encoding="latin1").splitlines():
        stripped = line.strip()
        if not stripped or stripped.startswith("#"):
            continue
        fields = stripped.split()
        if len(fields) < 5:
            continue
        try:
            image = int(fields[0], 10)
            # MAP_readMapConfFile starts its data fields at token 4 (the
            # fourth space-delimited field, one-based): walkable then height.
            walk = int(fields[3], 10)
            has_height = int(fields[4], 10)
        except ValueError:
            continue
        walkable[str(image)] = walk if walk in (0, 1, 2) else (1 if walk else 0)
        height[str(image)] = 1 if has_height else 0
    if walkable:
        manifest["map_images"] = {"walkable": walkable, "have_height": height}


def map_pack(map_number, records, by_number, real_path, palette, output, manifest):
    # The same casing split as the floor scan: floor 200's data is 200.dat.
    # A single spelling works on a case-insensitive volume and fails on CI.
    map_base = REPO / "runtime" / "legacy-client" / "map" / str(map_number)
    map_path = next(
        (candidate for candidate in (map_base.with_suffix(".DAT"), map_base.with_suffix(".dat")) if candidate.is_file()),
        None,
    )
    if map_path is None:
        raise legacy.AssetError(f"missing map/{map_number}.DAT")
    width, height, layers = legacy.read_map(map_path)
    # A full 800x1200 map is needlessly large for a 640x480 client window.
    # Keep a deterministic original-data slice around the centre of the map;
    # online M packets still replace this image with their own viewport.
    if map_number == 200 and width > 160:
        # The field renderer requests roughly a 20×20 tile window around the player;
        # keeping exactly that source rectangle avoids decoding a multi-
        # megapixel texture every animation tick.
        origin_x, origin_y, crop_w, crop_h = 308, 617, 24, 24
    else:
        origin_x, origin_y = 0, 0
        crop_w, crop_h = width, height
    crop_w = min(crop_w, width - origin_x)
    crop_h = min(crop_h, height - origin_y)

    def crop(layer):
        return [
            layer[(origin_y + y) * width + origin_x : (origin_y + y) * width + origin_x + crop_w]
            for y in range(crop_h)
        ]

    def resolve(value):
        candidates = by_number.get(value)
        return candidates[-1] if value > legacy.CG_INVISIBLE and candidates else None

    base = [[resolve(v) for v in row] for row in crop(layers[0])]
    objects = [[resolve(v) for v in row] for row in crop(layers[1])] if len(layers) > 1 else [[None] * crop_w for _ in range(crop_h)]
    cache = {}
    image = output / "maps" / f"map_{map_number}.png"
    if map_number == 200:
        # Emit the exact 640×480 viewport used by the web field preview. The
        # source map remains the authority; this is merely a pre-crop so a
        # browser never has to upload/decode a multi-megapixel texture.
        source_origin_x = 256
        source_origin_y = (crop_w - 1) * 24 + 256
        crop_rect = (source_origin_x + 24 * 32 - 320, source_origin_y - 240, 640, 480)
    else:
        crop_rect = None
    px_w, px_h = legacy.paint_isometric(base, records, real_path, palette, image, cache, objects, crop_rect=crop_rect)
    manifest["map"] = {
        "id": map_number,
        "width": crop_w,
        "height": crop_h,
        "origin": [origin_x, origin_y],
        "image": f"maps/map_{map_number}.png",
        "source": f"runtime/legacy-client/map/{map_path.name}",
        "render": {
            "width": px_w,
            "height": px_h,
            "origin_x": -448 if map_number == 200 else 256,
            "origin_y": 240 if map_number == 200 else (crop_h - 1) * 24 + 256,
            "tile_step": [32, 24],
        },
    }


def sprite_pack(
    sprite_numbers,
    records,
    real_path,
    palette,
    output,
    manifest,
    spr_path,
    spradrn_path,
    animation_filter=None,
):
    index = legacy.read_sprite_index(spradrn_path)
    sprites = manifest.setdefault("sprites", {})
    for sprite_no in sprite_numbers:
        if sprite_no not in index:
            continue
        animations = legacy.read_sprite_animations(spr_path, index[sprite_no])
        out = []
        for animation in animations:
            if animation_filter is not None and not animation_filter(animation):
                continue
            # pattern.cpp selects the requested action directly from the
            # SPRADRNBIN table.  Field actors normally use STAND/WALK, while
            # battle movies and skill effects use ATTACK/DAMAGE/DEAD and the
            # remaining action ids.  Dropping those records made the browser
            # silently fall back to a standing frame during a hit or death.
            if not animation.frames:
                continue
            frames = []
            for frame in animation.frames:
                physical = frame.bitmap_no
                try:
                    bitmap = legacy.read_bitmap(real_path, records, physical)
                except legacy.AssetError:
                    # The original tables contain a few retired/DLC frame
                    # records whose RD payload is malformed in the bundled
                    # 2.5 REALBIN.  The Windows client simply falls back to
                    # the previous decoded frame; omit only this bad frame
                    # and keep the rest of the direction/action available.
                    continue
                filename = f"bitmap_{physical}.png"
                legacy.write_png(output / "bitmaps" / filename, bitmap, palette)
                # pattern.cpp adds both the animation frame offset and the
                # REALBIN bitmap offset before StockTaskDispBuffer paints the
                # image. Preserve the latter here; otherwise a sprite is
                # re-centred by the web renderer and drifts into neighbouring
                # tiles/objects.
                frames.append({
                    "file": f"bitmaps/{filename}",
                    "x": frame.x,
                    "y": frame.y,
                    # PATTERN.CPP copies SPR_FRAME.SoundNo into the live
                    # action frame.  Values below 10000 are SE numbers,
                    # 10000..10099 mark real hit/contact frames, and values
                    # from 10100 upward are combo events.  Battle playback
                    # must consume these native events instead of guessing a
                    # fixed percentage of an attack animation.
                    "sound": int(frame.sound_no),
                    # actorFrame() only needs the REALBIN draw offset.  The
                    # remaining hit/priority/size metadata belongs to the
                    # bitmap index and duplicated it in every one of the
                    # 471k frame records, inflating sprites.json by tens of
                    # megabytes without changing a rendered pixel.
                    "xoffset": bitmap.xoffset,
                    "yoffset": bitmap.yoffset,
                })
            if frames:
                out.append({"direction": animation.direction, "action": animation.action, "frame_ms": animation.frame_ms, "frames": frames})
        sprites[str(sprite_no)] = {"actions": out}


def field_bootstrap_sprite_manifest(sprite_manifest):
    """Keep the complete field-idle rows for every animated actor.

    A raw REALBIN actor entry is not necessarily a standalone transparent
    sprite; for animated graphics it can be a packed/native backing record.
    The browser therefore needs complete STAND/WALK rows before revealing the
    first map.  PATTERN.CPP can enter either row at any frame selected by its
    running process clock, so keeping only frames[0] exposes packed backing
    records until the complete SPR manifest arrives.  Attack/death/skill rows
    remain in the background-only table.
    """
    sprites = {}
    for sprite_no, sprite in sprite_manifest.get("sprites", {}).items():
        actions = []
        for animation in sprite.get("actions", []):
            if int(animation.get("action", -1)) not in FIELD_BOOTSTRAP_ACTIONS:
                continue
            frames = animation.get("frames", [])
            if not frames:
                continue
            actions.append({**animation, "frames": frames})
        if actions:
            sprites[str(sprite_no)] = {"actions": actions}
    return {"format": sprite_manifest.get("format", 2), "sprites": sprites}


def face_pack(records, by_number, real_path, palette, output, manifest):
    """Emit the original 64×72 character-select portraits.

    These are regular REALBIN records rather than entries in SPRADRNBIN: the
    legacy client draws them directly from the face image number in the
    CharList option payload.  Store a logical-number lookup in the manifest so
    the browser never has to guess a physical record number.
    """
    faces = manifest.setdefault("faces", {})
    for logical in FACE_BITMAPS:
        candidates = by_number.get(logical)
        if not candidates:
            continue
        physical = candidates[-1]
        bitmap = legacy.read_bitmap(real_path, records, physical)
        filename = f"bitmap_{physical}.png"
        legacy.write_png(output / "bitmaps" / filename, bitmap, palette)
        record = records[physical]
        faces[str(logical)] = {
            "file": f"bitmaps/{filename}",
            "physical": physical,
            "width": bitmap.width,
            "height": bitmap.height,
            "xoffset": bitmap.xoffset,
            "yoffset": bitmap.yoffset,
            "bmp_number": record.bmp_number,
            **record_visual_metadata(record),
        }


def actor_pack(records, by_number, real_path, palette, output, manifest):
    """Emit static NPC/field actor graphics used by the original client."""
    actors = manifest.setdefault("actor_bitmaps", {})
    for logical in sorted(ACTOR_BITMAPS):
        if logical not in by_number and logical not in records:
            continue
        try:
            filename = emit_graphic(logical, records, by_number, real_path, palette, output, manifest)
        except legacy.AssetError:
            # A handful of reserved 100000-series slots are malformed or
            # unused in the preserved table.  Keep valid live actor graphics
            # while skipping only those unusable records.
            continue
        if filename:
            actors[str(logical)] = filename


def album_pack(records, by_number, real_path, palette, output, manifest):
    """Emit the 2.5 PetAlbumTbl face graphics used by the album detail pane.

    Album records store ``faceGraNo`` logical numbers (28001, 28002, ...),
    which are not part of the normal NPC 100000-series actor range.  Omitting
    them leaves the detail pane with a blank frame even though the original
    client draws the same indexed pet portrait from REALBIN.
    """
    album_graphics = manifest.setdefault("album_graphics", {})
    for row in manifest.get("album", []):
        logical = int(row.get("graphic", 0) or 0)
        if logical <= 0:
            continue
        try:
            filename = emit_graphic(logical, records, by_number, real_path, palette, output, manifest)
        except legacy.AssetError:
            continue
        if filename:
            album_graphics[str(logical)] = filename


def battle_pack(battle_numbers, records, by_number, real_path, palette, output, manifest):
    battles = manifest.setdefault("battles", {})
    # Refreshing an existing pack must also remove entries produced by an
    # older extractor that enumerated every SAB found beside the executable.
    # battle218/219 belong to the later data set and are unreachable in the
    # sa_2903 BATTLE_MAP_FILES table.
    for battle_key in list(battles):
        try:
            battle_number = int(battle_key)
        except (TypeError, ValueError):
            continue
        if battle_number < 0 or battle_number >= BATTLE_MAP_FILES_25:
            del battles[battle_key]
    battle_output = output / "battle"
    if battle_output.is_dir():
        for image in battle_output.glob("battle_*.png"):
            match = re.fullmatch(r"battle_(\d+)\.png", image.name)
            if match and int(match.group(1)) >= BATTLE_MAP_FILES_25:
                image.unlink()
    # Battle maps reuse a small ground-tile set.  Decode each physical REALBIN
    # bitmap once for the whole batch instead of once per 20×20 SAB file.
    bitmap_cache = {}
    for battle_number in sorted({int(number) for number in battle_numbers if 0 <= int(number) < BATTLE_MAP_FILES_25}):
        _battle_pack_one(battle_number, records, by_number, real_path, palette, output, battles, bitmap_cache)
    if "0" in battles:
        manifest["battle"] = battles["0"]


def _battle_pack_one(battle_number, records, by_number, real_path, palette, output, battles, bitmap_cache=None):
    if battle_number < 0 or battle_number >= BATTLE_MAP_FILES_25:
        return
    path = REPO / "runtime" / "legacy-client" / "data" / "battleMap" / f"battle{battle_number:02d}.sab"
    if not path.is_file():
        return
    tiles = legacy.read_battle_map(path)
    physical = [by_number.get(value, [None])[-1] if value > legacy.CG_INVISIBLE and by_number.get(value) else None for value in tiles]
    image = output / "battle" / f"battle_{battle_number:02d}.png"
    # sa_2903 presents a fixed 640×480 field back buffer.  Bake exactly the
    # viewport around the centre tile instead of asking the browser to decode
    # and repeatedly rasterise the complete 1792×1592 isometric texture.
    # BATTLEMAP.CPP::ReadBattleMap() submits the 20×20 cells at
    #     x = 32 * -9 + (row + column) * 32
    #     y = 24 * 10 + (row - column) * 24
    # on the logical 640×480 back-buffer.  paint_isometric() keeps the same
    # cells in a positive canvas at origin (256, 712), so the native viewport
    # starts at (256 - -288, 712 - 240) == (544, 472).  The old (672, 440)
    # crop displaced the complete field 128px left and 32px down, exposing
    # large transparent/black wedges on the right side of every battle.
    crop_rect = (544, 472, 640, 480)
    px_w, px_h = legacy.paint_isometric(
        [physical[row * 20 : (row + 1) * 20] for row in range(20)],
        records,
        real_path,
        palette,
        image,
        bitmap_cache if bitmap_cache is not None else {},
        crop_rect=crop_rect,
    )
    battles[str(battle_number)] = {
        "id": battle_number,
        "image": f"battle/battle_{battle_number:02d}.png",
        "render": {"width": px_w, "height": px_h, "origin_x": 0, "origin_y": 0, "tile_step": [32, 24]},
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=Path, default=REPO / "client" / "web" / "assets" / "original")
    parser.add_argument("--map", type=int, default=200)
    parser.add_argument("--battle", type=int, default=0)
    parser.add_argument("--all-battles", action="store_true", help="extract all 218 sa_2903 battleMap viewports")
    parser.add_argument("--battles-only", action="store_true", help="refresh battle PNGs in an existing browser asset pack")
    parser.add_argument("--ui-only", action="store_true", help="refresh UI PNGs and aliases in an existing browser asset pack")
    parser.add_argument("--items-only", action="store_true", help="refresh 2.5 item PNGs and logical ADRN aliases in an existing browser asset pack")
    parser.add_argument("--album-only", action="store_true", help="refresh the pet album and portraits in an existing browser asset pack")
    parser.add_argument(
        "--map-origins-only",
        action="store_true",
        help="repair generated map projection origins in an existing browser asset pack",
    )
    parser.add_argument(
        "--creation-sprites-only",
        action="store_true",
        help="refresh the small pre-world character-selection SPR pack",
    )
    parser.add_argument(
        "--field-bootstrap-sprites-only",
        action="store_true",
        help="refresh the field STAND/WALK bootstrap pack from an existing sprites.json",
    )
    parser.add_argument("--sprite", type=int, action="append", default=[100000, 100025, 100250])
    args = parser.parse_args()

    if args.map_origins_only:
        manifest_path = args.output / "manifest.json"
        if not manifest_path.is_file():
            parser.error("--map-origins-only requires an existing manifest.json")
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        changed = 0
        for entry in (manifest.get("maps") or {}).values():
            height = int(entry.get("source_height") or 0)
            if height <= 0:
                continue
            render = entry.setdefault("render", {})
            expected = (height - 1) * 24 + 256
            if render.get("origin_y") != expected:
                render["origin_y"] = expected
                changed += 1
        temporary_manifest = manifest_path.with_name(manifest_path.name + ".tmp")
        temporary_manifest.write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        temporary_manifest.replace(manifest_path)
        print(json.dumps({"output": str(args.output), "map_origins_changed": changed}, ensure_ascii=False))
        return 0

    root = REPO / "runtime" / "legacy-client"
    data = root / "data"
    real_path, adrn_path = data / "real_15.bin", data / "adrn_15.bin"
    records, by_number = legacy.parse_adrn(adrn_path)
    # Palet_1 is the normal field palette used by sa_2903.  Palet_0 is the
    # title/alternate bank and makes the same indexed pixels look purple.
    palette = legacy.load_palette(data / "pal" / "Palet_1.sap")
    args.output.mkdir(parents=True, exist_ok=True)
    # Keep the original indexed palette banks beside the normal PNG pack.
    # Live 2.5 M/MC headers can select Palet_0..15 for fixed-colour rooms;
    # the browser applies those banks to decoded map bitmaps at runtime.
    palette_output = args.output / "pal"
    palette_output.mkdir(parents=True, exist_ok=True)
    for palette_file in sorted((data / "pal").glob("Palet_*.sap")):
        shutil.copyfile(palette_file, palette_output / palette_file.name)
    battle_numbers = (
        sorted(
            number
            for path in (data / "battleMap").glob("battle*.sab")
            if 0 <= (number := int(path.stem[6:])) < BATTLE_MAP_FILES_25
        )
        if args.all_battles
        else [args.battle]
    )
    if args.album_only:
        manifest_path = args.output / "manifest.json"
        if not manifest_path.is_file():
            parser.error("--album-only requires an existing manifest.json")
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        manifest["album"] = album_catalog()
        if not manifest["album"]:
            parser.error("pet album source is missing")
        album_pack(records, by_number, real_path, palette, args.output, manifest)
        missing = [row["graphic"] for row in manifest["album"] if row.get("spriteGraphic") and str(row["graphic"]) not in manifest.get("album_graphics", {})]
        if missing:
            parser.error(f"missing enabled pet portraits: {missing}")
        temporary_manifest = manifest_path.with_name(manifest_path.name + ".tmp")
        temporary_manifest.write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        temporary_manifest.replace(manifest_path)
        print(json.dumps({"output": str(args.output), "album": len(manifest["album"])}, ensure_ascii=False))
        return 0
    if args.ui_only:
        manifest_path = args.output / "manifest.json"
        if not manifest_path.is_file():
            parser.error("--ui-only requires an existing manifest.json")
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        ui_pack(records, by_number, real_path, palette, args.output, manifest)
        temporary_manifest = manifest_path.with_name(manifest_path.name + ".tmp")
        temporary_manifest.write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        temporary_manifest.replace(manifest_path)
        print(json.dumps({"output": str(args.output), "ui": len(manifest.get("ui", {}))}, ensure_ascii=False))
        return 0
    if args.items_only:
        manifest_path = args.output / "manifest.json"
        if not manifest_path.is_file():
            parser.error("--items-only requires an existing manifest.json")
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        item_pack(records, by_number, real_path, palette, args.output, manifest)
        temporary_manifest = manifest_path.with_name(manifest_path.name + ".tmp")
        temporary_manifest.write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        temporary_manifest.replace(manifest_path)
        print(json.dumps({"output": str(args.output), "items": manifest.get("item_visual_bitmap_count", 0), "missing": len(manifest.get("missing_item_graphics", []))}, ensure_ascii=False))
        return 0
    if args.battles_only:
        manifest_path = args.output / "manifest.json"
        if not manifest_path.is_file():
            parser.error("--battles-only requires an existing manifest.json")
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        battle_pack(battle_numbers, records, by_number, real_path, palette, args.output, manifest)
        temporary_manifest = manifest_path.with_name(manifest_path.name + ".tmp")
        temporary_manifest.write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        temporary_manifest.replace(manifest_path)
        print(json.dumps({"output": str(args.output), "battles": len(battle_numbers)}, ensure_ascii=False))
        return 0
    if args.creation_sprites_only:
        creation_manifest = {"format": 2, "sprites": {}}
        sprite_pack(
            CREATION_SPRITES,
            records,
            real_path,
            palette,
            args.output,
            creation_manifest,
            data / "spr_4.bin",
            data / "spradrn_5.bin",
            animation_filter=lambda animation: (
                animation.direction == 0 and animation.action in CREATION_SPRITE_ACTIONS
            ),
        )
        (args.output / "creation-sprites.json").write_text(
            json.dumps(creation_manifest, ensure_ascii=False, separators=(",", ":")) + "\n",
            encoding="utf-8",
        )
        print(
            json.dumps(
                {"output": str(args.output), "creation_sprites": sorted(creation_manifest["sprites"])},
                ensure_ascii=False,
            )
        )
        return 0
    if args.field_bootstrap_sprites_only:
        sprite_manifest_path = args.output / "sprites.json"
        if not sprite_manifest_path.is_file():
            parser.error("--field-bootstrap-sprites-only requires an existing sprites.json")
        sprite_manifest = json.loads(sprite_manifest_path.read_text(encoding="utf-8"))
        bootstrap_manifest = field_bootstrap_sprite_manifest(sprite_manifest)
        (args.output / "field-bootstrap-sprites.json").write_text(
            json.dumps(bootstrap_manifest, ensure_ascii=False, separators=(",", ":")) + "\n",
            encoding="utf-8",
        )
        print(
            json.dumps(
                {"output": str(args.output), "field_bootstrap_sprites": len(bootstrap_manifest["sprites"])},
                ensure_ascii=False,
            )
        )
        return 0
    manifest = {
        "format": 2,
        "source": "runtime/legacy-client/data (sa_2903.exe companion data)",
        "palette": "Palet_1.sap",
        "bitmaps": {},
        "ui": {},
        "bitmap_aliases": {},
        "sprites": {},
        "faces": {},
        "actor_bitmaps": {},
        "maps": {},
        "album": album_catalog(),
    }

    ui_pack(records, by_number, real_path, palette, args.output, manifest)
    item_pack(records, by_number, real_path, palette, args.output, manifest)
    countdown_digits = {}
    for digit in range(10):
        logical = BATTLE_COUNTDOWN_LOGICAL_BASE + digit
        info = manifest["bitmaps"].get(str(logical))
        if info:
            countdown_digits[str(digit)] = {
                "logical": logical,
                "file": info["file"],
                "physical": info.get("physical"),
                "xoffset": info.get("xoffset"),
                "yoffset": info.get("yoffset"),
                "width": info.get("width"),
                "height": info.get("height"),
            }
    manifest["battle_countdown"] = {
        "duration_ms": BATTLE_COUNTDOWN_DURATION_MS,
        "logical_base": BATTLE_COUNTDOWN_LOGICAL_BASE,
        "digits": countdown_digits,
    }
    collect_map_collision_metadata(manifest)
    # Battle/field C and BC records carry SPR character numbers directly.  A
    # static REALBIN fallback is not enough for those actors: the native
    # client resolves the eight direction/action tables from SPRADRNBIN.  The
    # preserved table is small (841 records), so ship every original sprite
    # entry rather than trying to predict which skill/NPC a live server will
    # send later.  This also keeps battle attack/death animations available.
    sprite_index = legacy.read_sprite_index(data / "spradrn_5.bin")
    sprite_numbers = set(args.sprite)
    sprite_numbers.update(sprite_index)
    sprite_pack(sorted(sprite_numbers), records, real_path, palette, args.output, manifest, data / "spr_4.bin", data / "spradrn_5.bin")
    face_pack(records, by_number, real_path, palette, args.output, manifest)
    actor_pack(records, by_number, real_path, palette, args.output, manifest)
    album_pack(records, by_number, real_path, palette, args.output, manifest)
    # Keep every original DAT floor usable by a live M packet.  The browser
    # only downloads the graphics visible in its current 38×37 window, so
    # this index does not force all 4,700 images over the wire at once.
    collect_map_resources(records, by_number, real_path, palette, args.output, manifest)
    map_pack(args.map, records, by_number, real_path, palette, args.output, manifest)
    battle_pack(battle_numbers, records, by_number, real_path, palette, args.output, manifest)
    # Sprite tables contain hundreds of thousands of repeated frame records.
    # They are not needed to paint the first map (the map/UI bitmap index is),
    # and embedding them in the bootstrap manifest made a phone download and
    # parse roughly 188 MB before NOW LOADING could repaint.  Keep the full
    # tables in a separately cacheable resource; the browser requests this
    # file after the core map manifest is ready and renders actors as soon as
    # it arrives.  Older asset packs may still carry ``sprites`` in the main
    # file, so the web client keeps a compatibility fallback for those packs.
    sprite_manifest = {
        "format": manifest.get("format", 2),
        "sprites": manifest.pop("sprites", {}),
    }
    creation_sprite_manifest = {
        "format": sprite_manifest["format"],
        "sprites": {
            str(sprite_no): {
                "actions": [
                    animation
                    for animation in sprite_manifest["sprites"].get(str(sprite_no), {}).get("actions", [])
                    if animation.get("direction") == 0
                    and animation.get("action") in CREATION_SPRITE_ACTIONS
                ]
            }
            for sprite_no in CREATION_SPRITES
            if str(sprite_no) in sprite_manifest["sprites"]
        },
    }
    # FIELD.CPP enables its Action window only after the player's complete
    # directional SPR rows are resident.  Keep those twelve stock player
    # graphics in a compact first-response resource; the full table remains
    # available for NPC and battle actors and is loaded in the background.
    field_sprite_manifest = {
        "format": sprite_manifest["format"],
        "sprites": {
            str(sprite_no): sprite_manifest["sprites"][str(sprite_no)]
            for sprite_no in FIELD_ACTION_SPRITES
            if str(sprite_no) in sprite_manifest["sprites"]
        },
    }
    field_bootstrap_manifest = field_bootstrap_sprite_manifest(sprite_manifest)
    (args.output / "sprites.json").write_text(
        json.dumps(sprite_manifest, ensure_ascii=False, separators=(",", ":")) + "\n",
        encoding="utf-8",
    )
    (args.output / "field-sprites.json").write_text(
        json.dumps(field_sprite_manifest, ensure_ascii=False, separators=(",", ":")) + "\n",
        encoding="utf-8",
    )
    (args.output / "field-bootstrap-sprites.json").write_text(
        json.dumps(field_bootstrap_manifest, ensure_ascii=False, separators=(",", ":")) + "\n",
        encoding="utf-8",
    )
    (args.output / "creation-sprites.json").write_text(
        json.dumps(creation_sprite_manifest, ensure_ascii=False, separators=(",", ":")) + "\n",
        encoding="utf-8",
    )
    (args.output / "manifest.json").write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({"output": str(args.output), "bitmaps": len(manifest["bitmaps"]), "map_visual_bitmaps": manifest.get("map_visual_bitmap_count", 0), "maps": len(manifest.get("maps", {})), "sprites": sorted(sprite_manifest["sprites"]), "map": manifest.get("map"), "battles": len(manifest.get("battles", {}))}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
