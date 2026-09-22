#!/usr/bin/env python3
"""Decode preserved client graphics for Web asset extraction and offline tools.

The preserved Windows client stores its pictures in a pair of files:

* ``adrn_15.bin`` is an 80-byte little-endian index.
* ``real_15.bin`` contains RD records with a small RLE codec.

The old 1.82 source is used here as a format reference only.  This script is
an independent, bounds-checked decoder and emits ordinary RGBA PNG files plus
JSON metadata for asset tooling.  It deliberately extracts a
small, deterministic slice (map 1006, selected sprites and one battle map) so
the client does not ship the 764 MB source archive.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import struct
import sys
import zlib
from dataclasses import dataclass
from pathlib import Path
from typing import Iterable


ADR_RECORD_SIZE = 80
SPRADR_RECORD_SIZE = 12
SPRSTART = 100000
CG_INVISIBLE = 99
TRANSPARENT_INDEX = 0


class AssetError(RuntimeError):
    """Raised when a legacy asset is malformed or internally inconsistent."""


@dataclass(frozen=True)
class AdrRecord:
    bitmap_no: int
    offset: int
    size: int
    xoffset: int
    yoffset: int
    width: int
    height: int
    # MAP_ATTR.atari_x/atari_y are the hit-box extents used by the native
    # setPartsPrio()/checkPrioPartsVsChar() depth test.  They live directly
    # before the packed ``hit`` word in ADRN and are needed by the web
    # renderer as well as the offline compositor.
    hit_x: int
    hit_y: int
    hit: int
    bmp_number: int


@dataclass(frozen=True)
class Bitmap:
    bitmap_no: int
    width: int
    height: int
    xoffset: int
    yoffset: int
    pixels: bytes


@dataclass(frozen=True)
class SpriteFrame:
    bitmap_no: int
    x: int
    y: int
    sound_no: int


@dataclass(frozen=True)
class SpriteAnimation:
    direction: int
    action: int
    frame_ms: int
    frames: tuple[SpriteFrame, ...]


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def parse_adrn(path: Path) -> tuple[dict[int, AdrRecord], dict[int, list[int]]]:
    data = path.read_bytes()
    if len(data) % ADR_RECORD_SIZE:
        raise AssetError(f"{path} is not a multiple of {ADR_RECORD_SIZE} bytes")

    # The 2.5 table is mostly ordered but contains a small appended block of
    # duplicate bitmap numbers.  The original client assigns ``adrnbuff[id]``
    # while reading, so the last occurrence wins.
    records: dict[int, AdrRecord] = {}
    by_number: dict[int, list[int]] = {}
    for offset in range(0, len(data), ADR_RECORD_SIZE):
        record = data[offset : offset + ADR_RECORD_SIZE]
        bitmap_no, image_offset, size, xoffset, yoffset, width, height = struct.unpack_from(
            "<IIIiiII", record, 0
        )
        # MAP_ATTR starts at byte 28.  The first two bytes are the native
        # hit-box extents; the final unsigned int is aligned to byte 48
        # within MAP_ATTR, hence byte 76 in the complete record.
        hit_x, hit_y = struct.unpack_from("<BB", record, 28)
        hit = struct.unpack_from("<H", record, 30)[0]
        bmp_number = struct.unpack_from("<I", record, 76)[0]
        item = AdrRecord(
            bitmap_no=bitmap_no,
            offset=image_offset,
            size=size,
            xoffset=xoffset,
            yoffset=yoffset,
            width=width,
            height=height,
            hit_x=hit_x,
            hit_y=hit_y,
            hit=hit,
            bmp_number=bmp_number,
        )
        records[bitmap_no] = item
        if bmp_number:
            by_number[bmp_number] = [bitmap_no]
    return records, by_number


def decode_rd(blob: bytes, expected_width: int, expected_height: int) -> tuple[int, int, bytes]:
    """Decode one RD record using the client's literal/repeat RLE.

    The legacy ``RD`` payload is stored in bottom-up scanline order (the same
    convention used by the Windows bitmap routines).  Leaving the rows in
    payload order makes every character look upside down on a modern texture
    renderer: the red robe is above the blue head in the phone build.  Decode
    the bytes first, then normalize them to top-down RGBA/PNG order here so
    sprites, map objects and battle tiles all share the same correction.
    """

    def top_down(pixels: bytes, width: int, height: int) -> bytes:
        row_size = width
        return b"".join(
            pixels[row * row_size : (row + 1) * row_size]
            for row in range(height - 1, -1, -1)
        )

    if len(blob) < 16 or blob[:2] != b"RD":
        raise AssetError("bitmap record does not start with RD")
    compressed = blob[2]
    width, height, payload_size = struct.unpack_from("<III", blob, 4)
    if width <= 0 or height <= 0 or width > 4096 or height > 4096:
        raise AssetError(f"unreasonable bitmap dimensions {width}x{height}")
    if expected_width and width != expected_width:
        raise AssetError(f"index width {expected_width} disagrees with RD width {width}")
    if expected_height and height != expected_height:
        raise AssetError(f"index height {expected_height} disagrees with RD height {height}")
    payload_end = min(len(blob), payload_size)
    if payload_end < 16:
        raise AssetError("RD payload size is shorter than its header")
    payload = blob[16:payload_end]
    target_size = width * height

    if compressed == 0:
        pixels = payload[:target_size]
        if len(pixels) != target_size:
            raise AssetError("uncompressed RD record is truncated")
        return width, height, top_down(pixels, width, height)
    if compressed != 1:
        raise AssetError(f"unsupported RD compression flag {compressed}")

    output = bytearray()
    position = 0
    while position < len(payload) and len(output) < target_size:
        marker = payload[position]
        position += 1
        if marker & 0x80:
            if marker & 0x40:
                value = 0
            else:
                if position >= len(payload):
                    raise AssetError("truncated RD repeat value")
                value = payload[position]
                position += 1
            if marker & 0x20:
                if position + 2 > len(payload):
                    raise AssetError("truncated RD long repeat length")
                count = ((marker & 0x0F) << 16) | (payload[position] << 8) | payload[position + 1]
                position += 2
            elif marker & 0x10:
                if position >= len(payload):
                    raise AssetError("truncated RD repeat length")
                count = ((marker & 0x0F) << 8) | payload[position]
                position += 1
            else:
                count = marker & 0x0F
            if count <= 0:
                raise AssetError("zero-length RD repeat")
            output.extend(bytes((value,)) * count)
        else:
            if marker & 0x10:
                if position >= len(payload):
                    raise AssetError("truncated RD literal length")
                count = ((marker & 0x0F) << 8) | payload[position]
                position += 1
            else:
                count = marker & 0x0F
            if count <= 0 or position + count > len(payload):
                raise AssetError("truncated RD literal data")
            output.extend(payload[position : position + count])
            position += count

        if len(output) > target_size:
            # The native encoder tests two zero bytes before checking eBuf.
            # When the last pixel is transparent it can emit a final C2 run
            # containing one extra zero. The client displays width*height
            # pixels. Accept only this exact, terminal encoder artifact;
            # other overruns still indicate malformed data.
            if marker == 0xC2 and position == len(payload) and len(output) == target_size + 1:
                del output[target_size:]
            else:
                raise AssetError("RD decompressor produced too many pixels")

    if len(output) != target_size:
        raise AssetError(f"RD decompressor produced {len(output)} pixels, expected {target_size}")
    return width, height, top_down(bytes(output), width, height)


def read_bitmap(real_path: Path, records: dict[int, AdrRecord], bitmap_no: int) -> Bitmap:
    if bitmap_no < 0 or bitmap_no not in records:
        raise AssetError(f"bitmap number {bitmap_no} is outside the adrn table")
    record = records[bitmap_no]
    with real_path.open("rb") as stream:
        stream.seek(record.offset)
        blob = stream.read(record.size)
    if len(blob) != record.size:
        raise AssetError(f"bitmap {bitmap_no} is truncated at offset {record.offset}")
    width, height, pixels = decode_rd(blob, record.width, record.height)
    return Bitmap(bitmap_no, width, height, record.xoffset, record.yoffset, pixels)


def load_palette(path: Path) -> list[tuple[int, int, int]]:
    """Load the 224 BGR entries used by the legacy client.

    The Windows client reserves indices 0..15 and 240..255 for system colors
    and reads 224 BGR triples for indices 16..239.  The trailing bytes in the
    708-byte SAP files are intentionally preserved but not interpreted.
    """

    data = path.read_bytes()
    if len(data) < 224 * 3:
        raise AssetError(f"palette {path} is too short")
    palette = [(0, 0, 0)] * 256
    for index in range(224):
        blue, green, red = data[index * 3 : index * 3 + 3]
        palette[16 + index] = (red, green, blue)
    # Reasonable defaults for the reserved Windows colors.  Index zero stays
    # transparent in the generated RGBA files, matching DEF_COLORKEY=0.
    reserved = [
        (0, 0, 0), (128, 0, 0), (0, 128, 0), (128, 128, 0),
        (0, 0, 128), (128, 0, 128), (0, 128, 128), (192, 192, 192),
        (192, 220, 192), (166, 202, 240), (222, 0, 0), (255, 95, 0),
        (255, 255, 160), (0, 95, 210), (80, 210, 255), (40, 225, 40),
    ]
    palette[:16] = reserved
    palette[240:] = [
        (245, 195, 150), (225, 160, 95), (195, 125, 70), (155, 85, 30),
        (70, 65, 55), (40, 35, 30), (255, 251, 240), (160, 160, 164),
        (128, 128, 128), (255, 0, 0), (0, 255, 0), (255, 255, 0),
        (0, 0, 255), (255, 0, 255), (0, 255, 255), (255, 255, 255),
    ]
    return palette


def png_chunk(kind: bytes, payload: bytes) -> bytes:
    return (
        struct.pack(">I", len(payload))
        + kind
        + payload
        + struct.pack(">I", zlib.crc32(kind + payload) & 0xFFFFFFFF)
    )


def write_png(path: Path, bitmap: Bitmap, palette: list[tuple[int, int, int]]) -> None:
    rgba = bytearray()
    for row in range(bitmap.height):
        rgba.append(0)
        for pixel in bitmap.pixels[row * bitmap.width : (row + 1) * bitmap.width]:
            red, green, blue = palette[pixel]
            rgba.extend((red, green, blue, 0 if pixel == TRANSPARENT_INDEX else 255))
    payload = b"\x89PNG\r\n\x1a\n"
    payload += png_chunk(b"IHDR", struct.pack(">IIBBBBB", bitmap.width, bitmap.height, 8, 6, 0, 0, 0))
    payload += png_chunk(b"IDAT", zlib.compress(bytes(rgba), 9))
    payload += png_chunk(b"IEND", b"")
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(payload)


def resolve_graphic(value: int, records: dict[int, AdrRecord], by_number: dict[int, list[int]]) -> int | None:
    """Resolve a map/object action number to a real bitmap index."""

    if value <= 0 or value == CG_INVISIBLE:
        return None
    # Both DAT cells and the server's M response contain the logical action
    # number.  The PC client always calls realGetNo(), which looks up that
    # action in bitmapnumbertable and returns the physical adrn record.  A
    # physical record may have the same integer as an action (5000 and 2 are
    # examples), so checking ``value in records`` first silently selects the
    # wrong picture.  The by_number table has already retained the last
    # occurrence, matching LOADREALBIN.CPP's assignment semantics.
    candidates = by_number.get(value)
    if candidates:
        return candidates[-1]
    return None


def read_map(path: Path) -> tuple[int, int, list[list[int]]]:
    data = path.read_bytes()
    if len(data) < 8:
        raise AssetError(f"map {path} is too short")
    width, height = struct.unpack_from("<ii", data, 0)
    if width <= 0 or height <= 0 or width > 4096 or height > 4096:
        raise AssetError(f"map {path} has unreasonable dimensions {width}x{height}")
    cells = width * height
    raw = data[8:]
    if len(raw) != cells * 2 and len(raw) != cells * 2 * 3:
        raise AssetError(f"map {path} has unexpected payload size {len(raw)}")
    values = struct.unpack("<" + "H" * (len(raw) // 2), raw)
    layer_count = len(values) // cells
    return width, height, [list(values[index * cells : (index + 1) * cells]) for index in range(layer_count)]


def read_battle_map(path: Path) -> list[int]:
    data = path.read_bytes()
    if len(data) < 804 or data[:4] != b"SAB ":
        raise AssetError(f"battle map {path} is not a SAB 20x20 file")
    # The old client explicitly reads each tile high-byte first.
    return [(data[4 + i * 2] << 8) | data[5 + i * 2] for i in range(400)]


def read_sprite_index(path: Path) -> dict[int, tuple[int, int]]:
    data = path.read_bytes()
    if len(data) % SPRADR_RECORD_SIZE:
        raise AssetError(f"{path} is not a multiple of {SPRADR_RECORD_SIZE} bytes")
    result: dict[int, tuple[int, int]] = {}
    for offset in range(0, len(data), SPRADR_RECORD_SIZE):
        spr_no, anim_offset, anim_count = struct.unpack_from("<IIH", data, offset)
        result[spr_no] = (anim_offset, anim_count)
    return result


def read_sprite_animations(path: Path, index: tuple[int, int]) -> list[SpriteAnimation]:
    data = path.read_bytes()
    offset, count = index
    animations: list[SpriteAnimation] = []
    for _ in range(count):
        if offset + 12 > len(data):
            raise AssetError("sprite animation header is truncated")
        direction, action, duration, frame_count = struct.unpack_from("<HHII", data, offset)
        offset += 12
        frames: list[SpriteFrame] = []
        for _ in range(frame_count):
            if offset + 10 > len(data):
                raise AssetError("sprite frame is truncated")
            bitmap_no, x, y, sound_no = struct.unpack_from("<IhhH", data, offset)
            offset += 10
            frames.append(SpriteFrame(bitmap_no, x, y, sound_no))
        frame_ms = max(1, duration // max(1, frame_count << 4))
        animations.append(SpriteAnimation(direction, action, frame_ms, tuple(frames)))
    return animations


# ANIM_LIST from reference/anson1788-stoneage systeminc/loadsprbin.h: sprite
# bin's action id maps to 0=ATTACK, 1=DAMAGE, 2=DEAD, 3=STAND, 4=WALK, ...
# STAND is the canonical idle (pattern.cpp:179 falls back to it), NOT action 0.
ANIM_STAND = 3
ANIM_WALK = 4


def choose_sprite_frames(animations: Iterable[SpriteAnimation], actions: set[int] = {ANIM_STAND, ANIM_WALK}) -> list[SpriteFrame]:
    chosen: list[SpriteFrame] = []
    seen: set[tuple[int, int]] = set()
    for animation in animations:
        if animation.action not in actions or not animation.frames:
            continue
        key = (animation.direction, animation.action)
        if key in seen:
            continue
        seen.add(key)
        chosen.extend(animation.frames if animation.action == ANIM_WALK else animation.frames[:1])
    return chosen


def paint_isometric(
    bitmap_ids: list[list[int]],
    records: dict[int, AdrRecord],
    real_path: Path,
    palette: list[tuple[int, int, int]],
    output: Path,
    extra_bitmaps: dict[int, Bitmap],
    overlay_ids: list[list[int | None]] | None = None,
    extra_layers: list[list[list[int | None]]] | None = None,
    crop_rect: tuple[int, int, int, int] | None = None,
) -> tuple[int, int]:
    height = len(bitmap_ids)
    width = len(bitmap_ids[0]) if height else 0
    # Keep the complete diamond in the generated texture.  The previous
    # version used a small symmetric margin, which clipped every tile whose
    # row was below its column (exactly where the player's starting area is).
    # Keep both axes positive in the baked PNG.  PC coordinates are
    # screen_x=(x+y)*32 and screen_y=(y-x)*24; the smallest y is -(width-1)
    # * 24, so offset the canvas by the width rather than by the height.
    origin_x = 256
    origin_y = (width - 1) * 24 + 256
    canvas_width = (width + height) * 32 + 512
    canvas_height = (width + height) * 24 + 512 + 120
    pixels = bytearray(canvas_width * canvas_height * 4)

    def blit(bitmap: Bitmap, left: int, top: int) -> None:
        for y in range(bitmap.height):
            dy = top + y
            if dy < 0 or dy >= canvas_height:
                continue
            for x in range(bitmap.width):
                dx = left + x
                if dx < 0 or dx >= canvas_width:
                    continue
                index = bitmap.pixels[y * bitmap.width + x]
                if index == TRANSPARENT_INDEX:
                    continue
                red, green, blue = palette[index]
                destination = (dy * canvas_width + dx) * 4
                pixels[destination : destination + 4] = bytes((red, green, blue, 255))

    def get_bitmap(bitmap_no: int | None) -> Bitmap | None:
        # At this point the caller has already resolved the logical map image
        # number to an ADRN record number.  Record zero is a real bitmap slot;
        # only ``None`` (or a malformed negative value) means "no picture".
        if bitmap_no is None or bitmap_no < 0:
            return None
        if bitmap_no in extra_bitmaps:
            return extra_bitmaps[bitmap_no]
        try:
            item = read_bitmap(real_path, records, bitmap_no)
        except AssetError:
            return None
        extra_bitmaps[bitmap_no] = item
        return item

    # The legacy renderer walks a rectangle in the same diagonal order as
    # drawMap(): start at (x=0,y=height-1), walk down-left diagonals, then
    # continue from the right edge.  It submits every tile first and every
    # parts/object layer afterwards (DISP_PRIO_TILE=1, DISP_PRIO_PARTS=10).
    # SortComp() then puts the later display-buffer entry first when two
    # records have the same priority.  Keep the layers separate and reverse
    # each submission list before blitting; otherwise the near edge of a
    # cliff/roof is painted underneath its far edge (the old static PNGs had
    # exactly that mismatch with drawMap()).
    layers: list[list[list[int | None]]] = [
        [[value if value is not None and value >= 0 else None for value in row] for row in bitmap_ids]
    ]
    if overlay_ids is not None:
        layers.append(overlay_ids)
    if extra_layers:
        layers.extend(extra_layers)
    for layer in layers:
        if len(layer) != height or any(len(layer_row) != width for layer_row in layer):
            raise AssetError("isometric layer dimensions do not match the tile layer")
        draw_list: list[tuple[Bitmap, int, int]] = []
        ti = height - 1
        tj = 0
        while ti >= 0:
            row = ti
            column = tj
            while row >= 0 and column >= 0:
                value = layer[row][column]
                # Logical values up to CG_INVISIBLE were removed before the
                # matrix was resolved.  ``value`` is now a physical ADRN
                # record number, where 0..99 are perfectly valid pictures.
                # Filtering those a second time punched transparent holes in
                # battle and field maps whenever (for example) logical tile
                # 205 resolved to physical record 66.
                if value is not None and value >= 0:
                    bitmap = get_bitmap(value)
                    if bitmap is not None:
                        anchor_x = origin_x + (column + row) * 32
                        anchor_y = origin_y + (row - column) * 24
                        draw_list.append((bitmap, anchor_x + bitmap.xoffset, anchor_y + bitmap.yoffset))
                row -= 1
                column -= 1
            if tj < width - 1:
                tj += 1
            else:
                ti -= 1
        for bitmap, left, top in reversed(draw_list):
            blit(bitmap, left, top)
    # Write the already composited RGBA canvas without going through indexed
    # conversion again.
    write_left, write_top, write_width, write_height = 0, 0, canvas_width, canvas_height
    if crop_rect is not None:
        write_left, write_top, write_width, write_height = crop_rect
        if write_left < 0 or write_top < 0 or write_width <= 0 or write_height <= 0:
            raise AssetError("invalid isometric crop rectangle")
        if write_left + write_width > canvas_width or write_top + write_height > canvas_height:
            raise AssetError("isometric crop rectangle exceeds rendered canvas")
    rows = bytearray()
    for y in range(write_top, write_top + write_height):
        rows.append(0)
        start = (y * canvas_width + write_left) * 4
        rows.extend(pixels[start : start + write_width * 4])
    payload = b"\x89PNG\r\n\x1a\n"
    payload += png_chunk(b"IHDR", struct.pack(">IIBBBBB", write_width, write_height, 8, 6, 0, 0, 0))
    payload += png_chunk(b"IDAT", zlib.compress(bytes(rows), 9))
    payload += png_chunk(b"IEND", b"")
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_bytes(payload)
    return write_width, write_height


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--client-root", type=Path, default=Path("runtime/legacy-client"))
    parser.add_argument("--output", type=Path, default=Path("build/legacy-assets"))
    parser.add_argument(
        "--map",
        type=int,
        default=1006,
        dest="map_number",
        help="primary map to bake (the online renderer reads M; this is the static fallback)",
    )
    parser.add_argument(
        "--include-map",
        action="append",
        type=int,
        default=[],
        dest="include_maps",
        help="also extract visual bitmap resources referenced by this DAT for online M windows (repeatable)",
    )
    parser.add_argument(
        "--map-origin-x",
        type=int,
        default=0,
        help="left map coordinate to export (0 exports from the map edge)",
    )
    parser.add_argument(
        "--map-origin-y",
        type=int,
        default=0,
        help="top map coordinate to export (0 exports from the map edge)",
    )
    parser.add_argument(
        "--map-width",
        type=int,
        default=0,
        help="number of map cells to export (0 exports to the map edge)",
    )
    parser.add_argument(
        "--map-height",
        type=int,
        default=0,
        help="number of map cells to export (0 exports to the map edge)",
    )
    parser.add_argument("--battle-map", type=int, default=0)
    parser.add_argument(
        "--sprite",
        action="append",
        type=int,
        help="sprite number to extract (repeatable; defaults to player 100000 and monster 100250)",
    )
    parser.add_argument(
        "--palette",
        type=int,
        default=1,
        help="palette variant in data/pal (1 is the normal in-game palette)",
    )
    args = parser.parse_args()

    data_root = args.client_root / "data"
    map_root = args.client_root / "map"
    real_path = data_root / "real_15.bin"
    adrn_path = data_root / "adrn_15.bin"
    spr_path = data_root / "spr_4.bin"
    spradrn_path = data_root / "spradrn_5.bin"
    palette_path = data_root / "pal" / f"Palet_{args.palette}.sap"
    for required in (real_path, adrn_path, spr_path, spradrn_path, palette_path):
        if not required.is_file():
            raise AssetError(f"missing required asset: {required}")

    records, by_number = parse_adrn(adrn_path)
    palette = load_palette(palette_path)
    bitmap_cache: dict[int, Bitmap] = {}
    manifest: dict[str, object] = {
        "format": 1,
        "palette_id": args.palette,
        "source": {
            "real": {"path": str(real_path), "sha256": sha256(real_path)},
            "adrn": {"path": str(adrn_path), "sha256": sha256(adrn_path)},
            "spr": {"path": str(spr_path), "sha256": sha256(spr_path)},
            "spradrn": {"path": str(spradrn_path), "sha256": sha256(spradrn_path)},
            "palette": {"path": str(palette_path), "sha256": sha256(palette_path)},
        },
        "adrn_records": len(records),
        "bitmaps": {},
        "sprites": {},
    }

    def emit_bitmap(bitmap_no: int, label: str | None = None) -> str:
        if bitmap_no not in bitmap_cache:
            bitmap_cache[bitmap_no] = read_bitmap(real_path, records, bitmap_no)
        bitmap = bitmap_cache[bitmap_no]
        filename = f"bitmap_{bitmap_no}.png"
        write_png(args.output / "bitmaps" / filename, bitmap, palette)
        record = records[bitmap_no]
        key = str(bitmap_no)
        manifest["bitmaps"][key] = {
            "file": f"bitmaps/{filename}",
            "width": bitmap.width,
            "height": bitmap.height,
            "xoffset": bitmap.xoffset,
            "yoffset": bitmap.yoffset,
            "bmp_number": record.bmp_number,
            "label": label,
        }
        return f"bitmaps/{filename}"

    def physical_or_alias(value: int) -> int | None:
        if value <= CG_INVISIBLE:
            return None
        # Map/DAT/M values are actions, not adrn record indexes.  Resolve the
        # logical number before considering any direct/physical fallback.
        return resolve_graphic(value, records, by_number)

    # Online M windows can be on a different floor from the baked fallback.
    # Extract the visual IDs from explicitly included DATs as well, so the
    # phone can resolve the server's logical tile/object values without
    # shipping the legacy BIN or pretending that the map is offline.
    resource_maps: dict[str, dict[str, object]] = {}
    for included_number in sorted(set(args.include_maps)):
        included_base = map_root / str(included_number)
        included_path = next(
            (candidate for candidate in (included_base.with_suffix(".DAT"), included_base.with_suffix(".dat"))),
            None,
        )
        if included_path is None or not included_path.is_file():
            raise AssetError(f"missing included map/{included_number}.DAT")
        included_width, included_height, included_layers = read_map(included_path)
        included_cells = included_width * included_height
        visual_ids = {
            value
            for layer in included_layers[:2]
            for value in layer
            if value > CG_INVISIBLE
        }
        physical_ids: set[int] = set()
        for logical_id in sorted(visual_ids):
            physical_id = physical_or_alias(logical_id)
            if physical_id is not None:
                physical_ids.add(physical_id)
                emit_bitmap(physical_id, f"online_map_{included_number}")
        resource_maps[str(included_number)] = {
            "source": f"map/{included_number}.DAT",
            "width": included_width,
            "height": included_height,
            "logical_visual_ids": sorted(visual_ids),
            "physical_bitmap_ids": sorted(physical_ids),
        }

    map_base = map_root / str(args.map_number)
    dat_path = next(
        (candidate for candidate in (map_base.with_suffix(".DAT"), map_base.with_suffix(".dat"))),
        None,
    )
    if dat_path is None or not dat_path.is_file():
        raise AssetError(f"missing map/{args.map_number}.DAT")
    dat_width, dat_height, dat_layers = read_map(dat_path)

    # The 2.5 client draws its field from map/<floor>.DAT: layer 0 is the
    # ground tile, layer 1 is the object/parts layer, and layer 2 is the
    # event/hit metadata.  The similarly named .MAP files are editor/cache
    # artifacts used by other tools; treating them as the ground layer turns
    # character bitmaps into a fake floor (the old mobile build did exactly
    # that).  Keep the DAT source authoritative so the phone sees the same
    # cells and the same walkability as the PC client.
    map_width, map_height = dat_width, dat_height
    base_layer = dat_layers[0] if dat_layers else [0] * (map_width * map_height)
    scenery_layer = dat_layers[1] if len(dat_layers) > 1 else [0] * len(base_layer)
    logic_layer = dat_layers[2] if len(dat_layers) > 2 else [0] * len(base_layer)
    event_layer = []
    map_source = "DAT0+DAT1+DAT2"

    origin_x = max(0, args.map_origin_x)
    origin_y = max(0, args.map_origin_y)
    if origin_x >= map_width or origin_y >= map_height:
        raise AssetError("map export origin is outside the map")
    export_width = args.map_width if args.map_width > 0 else map_width - origin_x
    export_height = args.map_height if args.map_height > 0 else map_height - origin_y
    export_width = min(export_width, map_width - origin_x)
    export_height = min(export_height, map_height - origin_y)
    if export_width <= 0 or export_height <= 0:
        raise AssetError("map export rectangle is empty")

    def crop_layer(layer: list[int]) -> list[int]:
        return [
            value
            for row in range(export_height)
            for value in layer[(origin_y + row) * map_width + origin_x :
                               (origin_y + row) * map_width + origin_x + export_width]
        ]

    base_layer = crop_layer(base_layer)
    scenery_layer = crop_layer(scenery_layer)
    logic_layer = crop_layer(logic_layer)
    # The renderer consumes row-major lists of rows; retain that shape after
    # cropping so the same routine is safe for tiny portrait chunks and for a
    # full small map such as 1006.
    base_layer = [base_layer[row * export_width : (row + 1) * export_width]
                  for row in range(export_height)]
    scenery_layer = [scenery_layer[row * export_width : (row + 1) * export_width]
                     for row in range(export_height)]
    logic_layer = [logic_layer[row * export_width : (row + 1) * export_width]
                   for row in range(export_height)]

    base_matrix = [
        [physical_or_alias(value) for value in base_layer[row]]
        for row in range(export_height)
    ]
    scenery_matrix = [
        [physical_or_alias(value) for value in scenery_layer[row]]
        for row in range(export_height)
    ]
    referenced: set[int] = {
        bitmap_no
        for layer in (base_matrix, scenery_matrix)
        for row in layer
        for bitmap_no in row
        if bitmap_no is not None
    }
    for bitmap_no in sorted(referenced):
        emit_bitmap(bitmap_no, "map_" + str(args.map_number))
    extra_bitmaps: dict[int, Bitmap] = dict(bitmap_cache)
    map_image = args.output / "maps" / f"map_{args.map_number}.png"
    map_width_px, map_height_px = paint_isometric(
        base_matrix,
        records,
        real_path,
        palette,
        map_image,
        extra_bitmaps,
        scenery_matrix,
    )
    manifest["map"] = {
        "id": args.map_number,
        "width": export_width,
        "height": export_height,
        "source_width": map_width,
        "source_height": map_height,
        "origin": [origin_x, origin_y],
        "image": f"maps/map_{args.map_number}.png",
        "source": map_source,
        "render": {
            "width": map_width_px,
            "height": map_height_px,
            # paint_isometric() renders the cropped matrix, not the source
            # floor.  Keep the manifest origin in the same coordinate space
            # as the PNG so a client can place a server coordinate by first
            # subtracting the crop origin.
            "origin_x": 256,
            "origin_y": (export_width - 1) * 24 + 256,
            "tile_step": [32, 24],
        },
        "tile_bitmap": base_layer,
        "dat_layers": {
            "object_bitmap": scenery_layer,
            "logic_image": logic_layer,
            "events": event_layer,
        },
        "object_bitmap_resolved": [value for row in scenery_matrix for value in row],
    }

    sprite_index = read_sprite_index(spradrn_path)
    sprite_numbers = args.sprite if args.sprite else [100000, 100250]
    missing_sprites: list[int] = []
    for sprite_no in sorted(set(sprite_numbers)):
        if sprite_no not in sprite_index:
            # A batch driven off enemybase.txt regularly asks for IDs that the
            # bundled spradrn_5.bin does not contain (retired/DLC monsters).
            # Skip so one gap does not abort the whole cook.
            missing_sprites.append(sprite_no)
            continue
        animations = read_sprite_animations(spr_path, sprite_index[sprite_no])
        output_animations: list[dict[str, object]] = []
        for animation in animations:
            if animation.action not in (ANIM_STAND, ANIM_WALK) or not animation.frames:
                continue
            frames: list[dict[str, object]] = []
            selected_frames = animation.frames if animation.action == ANIM_WALK else animation.frames[:1]
            for frame_number, frame in enumerate(selected_frames):
                file_name = emit_bitmap(frame.bitmap_no, f"sprite_{sprite_no}")
                frames.append({"file": file_name, "x": frame.x, "y": frame.y, "sound": frame.sound_no})
            output_animations.append(
                {
                    "direction": animation.direction,
                    "action": animation.action,
                    "frame_ms": animation.frame_ms,
                    "frames": frames,
                }
            )
        manifest["sprites"][str(sprite_no)] = {
            "actions": output_animations,
            "source_offset": sprite_index[sprite_no][0],
            "source_animation_count": sprite_index[sprite_no][1],
        }
    if missing_sprites:
        manifest["missing_sprites"] = sorted(missing_sprites)
        sys.stderr.write(
            f"warning: {len(missing_sprites)} sprite(s) absent from spradrn_5.bin: "
            + ", ".join(str(n) for n in sorted(missing_sprites))
            + "\n"
        )

    npc_action = 16065
    npc_bitmap = resolve_graphic(npc_action, records, by_number)
    if npc_bitmap is not None:
        manifest["npc"] = {"action": npc_action, "bitmap": emit_bitmap(npc_bitmap, "npc_16065")}

    battle_path = data_root / "battleMap" / f"battle{args.battle_map:02d}.sab"
    if battle_path.is_file():
        battle_actions = read_battle_map(battle_path)
        battle_tiles = [physical_or_alias(tile_id) for tile_id in battle_actions]
        for tile_id in battle_tiles:
            if tile_id is not None:
                referenced.add(tile_id)
                emit_bitmap(tile_id, f"battle_{args.battle_map}")
        battle_image = args.output / "battle" / f"battle_{args.battle_map:02d}.png"
        battle_width_px, battle_height_px = paint_isometric(
            [battle_tiles[row * 20 : (row + 1) * 20] for row in range(20)],
            records,
            real_path,
            palette,
            battle_image,
            extra_bitmaps,
        )
        manifest["battle"] = {
            "id": args.battle_map,
            "image": f"battle/battle_{args.battle_map:02d}.png",
            "render": {
                "width": battle_width_px,
                "height": battle_height_px,
                "origin_x": 256,
                "origin_y": (20 - 1) * 24 + 256,
                "tile_step": [32, 24],
            },
            "tiles": battle_actions,
            "physical_tiles": battle_tiles,
        }

    # Ship the exact logical->physical table for values that were actually
    # extracted.  This avoids choosing a coincidentally equal physical key in
    # a mobile manifest (for example logical 5000 must not resolve to the
    # file named bitmap_5000.png).
    logical_aliases: dict[str, str] = {}
    for logical, candidates in by_number.items():
        if not candidates:
            continue
        physical = candidates[-1]
        if str(physical) in manifest["bitmaps"]:
            logical_aliases[str(logical)] = str(physical)
    manifest["bitmap_aliases"] = logical_aliases
    if resource_maps:
        manifest["resource_maps"] = resource_maps

    args.output.mkdir(parents=True, exist_ok=True)
    (args.output / "manifest.json").write_text(
        json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
    )
    print(
        json.dumps(
            {
                "output": str(args.output),
                "adrn_records": len(records),
                "bitmaps": len(manifest["bitmaps"]),
                "sprites": sorted(manifest["sprites"]),
                "map": args.map_number,
                "battle_map": args.battle_map,
            },
            ensure_ascii=False,
        )
    )
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except AssetError as error:
        print(f"asset-cooker: {error}", file=sys.stderr)
        raise SystemExit(2)
