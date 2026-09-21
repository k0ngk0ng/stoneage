#!/usr/bin/env python3
"""Build a seekable, uncompressed map pack from existing published Web assets.

PNG is already compressed. Independent File slices avoid inflating a whole ZIP
in browser memory and let imports resume without retaining the original file.
"""
import argparse
import hashlib
import json
from pathlib import Path
import shutil
import struct

MAGIC = b"SAMAP001"


def build(assets, maps, output, floors, revision, publication=None):
    manifest = json.loads((assets / "manifest.json").read_text())
    selected = set(map(str, floors)) if floors else set(manifest["maps"])
    files = {}
    aliases = manifest.get("bitmap_aliases", {})
    missing = set()
    for floor in sorted(selected, key=int):
        definition = manifest["maps"].get(floor)
        if definition is None:
            raise ValueError(f"unknown floor {floor}")
        candidates = sorted(p for p in maps.iterdir()
                            if p.stem == floor and p.suffix.lower() in (".dat", ".map"))
        if not candidates:
            raise ValueError(f"missing map data for floor {floor}")
        for source in candidates:
            files[f"maps/{source.name}"] = source
            # Native MAP companions contain exploration colours, not tile
            # graphic IDs. The Web client prefers DAT when both are present.
            if source.suffix.lower() == ".map" and any(p.suffix.lower() == ".dat" for p in candidates):
                continue
            data = source.read_bytes()
            width, height = struct.unpack_from("<ii", data)
            count = width * height
            if width <= 0 or height <= 0 or count > 4_000_000 or len(data) < 8 + count * 2:
                raise ValueError(f"invalid map {source.name}")
            layers = min(2, (len(data) - 8) // (count * 2))
            values = {value[0] for value in struct.iter_unpack("<H", data[8:8 + count * layers * 2])}
            for value in (v for v in values if v > 99):
                key = str(value)
                info = manifest["bitmaps"].get(str(aliases.get(key, key))) or manifest["bitmaps"].get(key)
                if not info:
                    missing.add(value)
                    continue
                relative = info["file"]
                if not relative.startswith("bitmaps/") or ".." in Path(relative).parts:
                    raise ValueError(f"unsafe bitmap path {relative}")
                files[f"assets/{relative}"] = assets / relative
    # The online client already treats unmapped legacy cells as empty. Record
    # them explicitly so a package cannot claim to repair missing source art.
    entries, offset = [], 0
    for key, path in sorted(files.items()):
        size = path.stat().st_size
        with path.open("rb") as handle:
            digest = hashlib.file_digest(handle, "sha256").hexdigest()
        if publication is not None:
            expected = publication.get(key)
            if expected != {"size": size, "sha256": digest}:
                raise ValueError(f"local file differs from publication: {key}")
        entries.append(dict(path=key, size=size, sha256=digest, offset=offset))
        offset += size
    header = dict(format=1, revision=revision, floors=sorted(selected, key=int),
                  bytes=offset, entries=entries, unmapped_bitmaps=sorted(missing))
    encoded = json.dumps(header, ensure_ascii=False, separators=(",", ":")).encode()
    output.parent.mkdir(parents=True, exist_ok=True)
    partial = output.with_suffix(output.suffix + ".partial")
    try:
        with partial.open("wb") as target:
            target.write(MAGIC + struct.pack("<I", len(encoded)) + encoded)
            for key in sorted(files):
                with files[key].open("rb") as source:
                    shutil.copyfileobj(source, target, 1024 * 1024)
        partial.replace(output)
    finally:
        partial.unlink(missing_ok=True)
    output.with_suffix(".json").write_text(json.dumps(header, ensure_ascii=False, indent=2) + "\n")
    return header


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--assets", type=Path, default=Path("client/web/assets/original"))
    parser.add_argument("--maps-dir", type=Path, default=Path("runtime/legacy-client/map"))
    parser.add_argument("--floors", help="comma-separated floor IDs; omitted = all maps")
    parser.add_argument("--output", type=Path, required=True)
    group = parser.add_mutually_exclusive_group(required=True)
    group.add_argument("--publication", type=Path, help="published _client-manifest.json; verifies every packaged byte")
    group.add_argument("--local-dev", action="store_true", help="only usable with the local-dev resource revision")
    parser.add_argument("--prefix", default="stoneage", help="object key prefix in the publication manifest")
    args = parser.parse_args()
    publication, revision = None, "local-dev"
    if args.publication:
        objects = json.loads(args.publication.read_text())["objects"]
        digest = hashlib.sha256()
        for key, info in sorted(objects.items()):
            digest.update(f'{key}\0{info["size"]}\0{info["sha256"]}\0'.encode())
        revision = digest.hexdigest()
        prefix = args.prefix.strip("/")
        prefix = prefix + "/" if prefix else ""
        publication = {key[len(prefix):]: value for key, value in objects.items() if key.startswith(prefix)}
    result = build(args.assets, args.maps_dir, args.output,
                   args.floors.split(",") if args.floors else None, revision, publication)
    print(json.dumps({"file": str(args.output), "bytes": args.output.stat().st_size,
                      "files": len(result["entries"]), "floors": len(result["floors"]),
                      "unmapped_bitmaps": len(result["unmapped_bitmaps"])}))


if __name__ == "__main__":
    main()
