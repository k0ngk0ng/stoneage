#!/usr/bin/env python3
"""Import the verified 12-person white-tiger set using its original ADRN offsets.

Requires a readable, original 80-byte ADRN table for the same resource group.
Encoded indexes and guessed offsets are rejected. Output is a standalone sprite
pack for review; no application manifest is changed by this command.
"""

from __future__ import annotations

import argparse
import json
import mmap
from pathlib import Path
import struct
import sys
import zlib

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "tools"))
from asset_cooker import AssetError, parse_adrn, read_sprite_animations, read_sprite_index, sha256


SOURCE_IDS = range(110048, 110060)
DESTINATION_FIRST = 104025
EXPECTED_ACTIONS = {(direction, action) for direction in range(8)
                    for action in (0, 1, 2, 3, 4, 10, 12)}
NAMESPACE = "bitmaps/white-tiger-8.5"
# Audited across all 12 characters and all 8 directions in the retained pack.
# frame_ms is the historical manifest name for native animation ticks.
EXPECTED_ACTION_TIMING = {
    0: (9, 6), 1: (2, 3), 2: (8, 9), 3: (33, 6),
    4: (9, 3), 10: (2, 3), 12: (9, 6),
}


def validate_animation(animation) -> None:
    if (len(animation.frames), animation.frame_ms) != EXPECTED_ACTION_TIMING[animation.action]:
        raise AssetError("white-tiger action frame count or timing differs from the audited source")
    events = [(i, frame.sound_no) for i, frame in enumerate(animation.frames)
              if frame.sound_no]
    expected = [(3, 10101), (6, 10001)] if animation.action in (0, 12) else []
    if events != expected:
        raise AssetError("white-tiger action events differ from the audited source")


def rgba_rd(blob: bytes, width: int, height: int) -> bytes:
    """Read the client's zlib BGRA RD records, preserving alpha and scanlines."""
    if len(blob) < 16 or blob[:3] != b"RD\x20":
        raise AssetError("expected a true-color RD record")
    w, h, length = struct.unpack_from("<III", blob, 4)
    if (w, h, length) != (width, height, len(blob)):
        raise AssetError("RD header does not match the original ADRN record")
    if not 0 < width <= 4096 or not 0 < height <= 4096:
        raise AssetError("invalid bitmap dimensions")
    expected = width * height * 4
    decoder = zlib.decompressobj()
    pixels = decoder.decompress(blob[16:], expected + 1)
    if (len(pixels) != expected or not decoder.eof
            or decoder.unused_data or decoder.unconsumed_tail):
        raise AssetError("RD pixels are truncated, oversized, or have trailing data")
    rgba = bytearray(expected)
    stride = width * 4
    for y in range(height):
        row = pixels[(height - 1 - y) * stride:(height - y) * stride]
        out = y * stride
        rgba[out:out + stride:4] = row[2::4]
        rgba[out + 1:out + stride:4] = row[1::4]
        rgba[out + 2:out + stride:4] = row[0::4]
        rgba[out + 3:out + stride:4] = row[3::4]
    return bytes(rgba)


def png_rgba(width: int, height: int, rgba: bytes) -> bytes:
    def chunk(kind: bytes, data: bytes) -> bytes:
        return (struct.pack(">I", len(data)) + kind + data
                + struct.pack(">I", zlib.crc32(kind + data)))

    stride = width * 4
    scanlines = b"".join(b"\0" + rgba[i:i + stride]
                         for i in range(0, len(rgba), stride))
    return (b"\x89PNG\r\n\x1a\n"
            + chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 6, 0, 0, 0))
            + chunk(b"IDAT", zlib.compress(scanlines)) + chunk(b"IEND", b""))


def import_pack(source: Path, adrn: Path, output: Path) -> dict:
    if output.exists():
        raise AssetError("output already exists; choose a new review directory")
    records, _ = parse_adrn(adrn)
    indexes = read_sprite_index(source / "spradrn.bin")
    if not set(SOURCE_IDS).issubset(indexes):
        raise AssetError("source is missing one or more white-tiger characters")
    sprites = {}
    needed = set()
    mappings = []
    for source_id in SOURCE_IDS:
        animations = read_sprite_animations(source / "spr.bin", indexes[source_id])
        keys = {(a.direction, a.action) for a in animations}
        if keys != EXPECTED_ACTIONS or len(animations) != len(EXPECTED_ACTIONS):
            raise AssetError(f"incomplete direction/action coverage for {source_id}")
        frame_ids = {f.bitmap_no for a in animations for f in a.frames}
        if len(frame_ids) != 256 or sum(len(a.frames) for a in animations) != 576:
            raise AssetError(f"unexpected frame coverage for {source_id}")
        actions = []
        for animation in animations:
            validate_animation(animation)
            frames = []
            for frame in animation.frames:
                record = records.get(frame.bitmap_no)
                if record is None:
                    raise AssetError(f"missing ADRN bitmap {frame.bitmap_no}")
                if abs(record.xoffset) > 4096 or abs(record.yoffset) > 4096:
                    raise AssetError("invalid original frame offsets")
                frames.append({
                    "file": f"{NAMESPACE}/bitmap_{frame.bitmap_no}.png",
                    "x": frame.x, "y": frame.y, "sound": frame.sound_no,
                    "xoffset": record.xoffset, "yoffset": record.yoffset,
                })
            actions.append({"direction": animation.direction, "action": animation.action,
                            "frame_ms": animation.frame_ms, "frames": frames})
        destination = DESTINATION_FIRST + source_id - SOURCE_IDS.start
        sprites[str(destination)] = {"actions": actions}
        mappings.append({"source": source_id, "destination": destination})
        needed.update(frame_ids)

    # Validate every input record against the corresponding RD header, not just
    # the imported subset. A wrong table must fail before anything is emitted.
    rendered = {}
    with (source / "real.bin").open("rb") as stream:
        with mmap.mmap(stream.fileno(), 0, access=mmap.ACCESS_READ) as data:
            for record in records.values():
                start, end = record.offset, record.offset + record.size
                if record.size < 16 or end > len(data) or data[start:start + 3] != b"RD\x20":
                    raise AssetError(f"ADRN does not index this real.bin: {record.bitmap_no}")
                header = struct.unpack_from("<III", data, start + 4)
                if header != (record.width, record.height, record.size):
                    raise AssetError(f"ADRN/RD mismatch for {record.bitmap_no}")
            for bitmap in sorted(needed):
                r = records[bitmap]
                rgba = rgba_rd(data[r.offset:r.offset + r.size], r.width, r.height)
                rendered[bitmap] = png_rgba(r.width, r.height, rgba)

    provenance = {
        "format": 1, "mappings": mappings, "unique_bitmaps": len(needed),
        "offsets": "original ADRN; all indexed RD headers validated",
        "source_sha256": {name: sha256(source / name)
                          for name in ("real.bin", "spr.bin", "spradrn.bin")},
        "adrn_sha256": sha256(adrn),
    }
    images = output / NAMESPACE
    images.mkdir(parents=True)
    for bitmap, png in rendered.items():
        (images / f"bitmap_{bitmap}.png").write_bytes(png)
    (output / "sprites.json").write_text(json.dumps({"format": 1, "sprites": sprites}) + "\n")
    bootstrap = {key: {"actions": [a for a in value["actions"] if a["action"] in (3, 4)]}
                 for key, value in sprites.items()}
    (output / "field-bootstrap-sprites.json").write_text(
        json.dumps({"format": 1, "sprites": bootstrap}) + "\n")
    (output / "provenance.json").write_text(json.dumps(provenance, indent=2) + "\n")
    return provenance


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", required=True, type=Path)
    parser.add_argument("--adrn", required=True, type=Path, help="original readable ADRN80 table")
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    try:
        result = import_pack(args.source, args.adrn, args.output)
    except (AssetError, OSError, ValueError, zlib.error) as error:
        parser.exit(1, f"white-tiger import: {error}\n")
    print(f"Imported {len(result['mappings'])} characters and {result['unique_bitmaps']} bitmaps")


if __name__ == "__main__":
    main()
