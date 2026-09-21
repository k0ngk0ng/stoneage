#!/usr/bin/env python3
import hashlib
import importlib.util
import json
from pathlib import Path
import struct
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("map_pack", Path(__file__).with_name("build-map-pack.py"))
builder = importlib.util.module_from_spec(spec)
spec.loader.exec_module(builder)


class MapPackTests(unittest.TestCase):
    def test_dependencies_alias_fallback_and_publication_validation(self):
        Path("build").mkdir(exist_ok=True)
        with tempfile.TemporaryDirectory(dir="build") as temporary:
            root = Path(temporary)
            assets, maps = root / "assets", root / "maps"
            (assets / "bitmaps").mkdir(parents=True)
            maps.mkdir()
            bitmap = b"fixture image"
            (assets / "bitmaps/bitmap_1.png").write_bytes(bitmap)
            (assets / "manifest.json").write_text(json.dumps({
                "maps": {"1000": {}}, "bitmap_aliases": {"26001": "9136"},
                "bitmaps": {"26001": {"file": "bitmaps/bitmap_1.png"}},
            }))
            (maps / "1000.DAT").write_bytes(struct.pack("<ii6H", 2, 1, 26001, 26001, 0, 0, 0, 0))
            (maps / "1000.MAP").write_bytes(struct.pack("<ii2H", 2, 1, 43947, 43947))
            output = root / "test.samap"
            header = builder.build(assets, maps, output, [1000], "local-dev")
            self.assertEqual(header["unmapped_bitmaps"], [])
            self.assertEqual(len(header["entries"]), 3)
            raw = output.read_bytes()
            self.assertEqual(raw[:8], builder.MAGIC)
            start = 12 + struct.unpack_from("<I", raw, 8)[0]
            for entry in header["entries"]:
                payload = raw[start + entry["offset"]:start + entry["offset"] + entry["size"]]
                self.assertEqual(hashlib.sha256(payload).hexdigest(), entry["sha256"])
            publication = {e["path"]: {"size": e["size"], "sha256": e["sha256"]} for e in header["entries"]}
            builder.build(assets, maps, output, [1000], "verified-revision", publication)
            (assets / "bitmaps/bitmap_1.png").write_bytes(b"changed")
            with self.assertRaisesRegex(ValueError, "differs from publication"):
                builder.build(assets, maps, output, [1000], "verified-revision", publication)


if __name__ == "__main__":
    unittest.main()
