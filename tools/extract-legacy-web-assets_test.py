"""Local resource integration checks; run with the preserved client data present."""

import csv
import importlib.util
import json
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("web_assets", ROOT / "tools/extract-legacy-web-assets.py")
assets = importlib.util.module_from_spec(spec)
spec.loader.exec_module(assets)


class PetAlbumIntegrationTest(unittest.TestCase):
    def test_peruxia_catalog_matches_server_and_keeps_old_slots(self):
        rows = assets.album_catalog()
        self.assertEqual(len(rows), 225)
        self.assertEqual((rows[0]["name"], rows[0]["graphic"]), ("乌力", 28001))
        self.assertEqual((rows[223]["name"].strip(), rows[223]["graphic"]), ("梅尔顿", 28147))
        pet = rows[224]
        self.assertEqual((pet["name"], pet["albumNo"], pet["graphic"]), ("佩露夏", 125, 28196))
        data = ROOT / "server/legacy/source/2.5/gmsv/data"
        with (data / "enemybase.txt").open(encoding="gb18030") as stream:
            template = next(row for row in csv.reader(stream) if len(row) > 36 and row[6] == str(pet["templateId"]))
        self.assertEqual(template[0], pet["name"])
        self.assertEqual(int(template[36]), pet["spriteGraphic"])
        self.assertEqual(template[15:19], ["0", "100", "0", "0"])
        self.assertEqual(template[25:27], ["1", "2"])

    def test_exported_portrait_and_all_native_animation_frames(self):
        output = ROOT / "client/web/assets/original"
        manifest = json.loads((output / "manifest.json").read_text())
        pet = next(row for row in manifest["album"] if row["name"] == "佩露夏")
        self.assertEqual(pet["spriteGraphic"], 100872)
        self.assertEqual(manifest["album_graphics"]["28196"], "bitmaps/bitmap_126325.png")
        self.assertEqual(manifest["bitmap_aliases"]["28196"], "126325")
        self.assertEqual(manifest["bitmaps"]["126325"]["bmp_number"], 28196)
        self.assertTrue((output / manifest["album_graphics"]["28196"]).is_file())
        data = ROOT / "runtime/legacy-client/data"
        index = assets.legacy.read_sprite_index(data / "spradrn_5.bin")
        native = assets.legacy.read_sprite_animations(data / "spr_4.bin", index[100872])
        exported = json.loads((output / "sprites.json").read_text())["sprites"]["100872"]["actions"]
        for action in (0, 1, 2, 3, 4, 10):
            self.assertEqual({row.direction for row in native if row.action == action}, set(range(8)))
        for animation in native:
            row = next(row for row in exported if (row["action"], row["direction"]) == (animation.action, animation.direction))
            self.assertEqual(row["frame_ms"], animation.frame_ms)
            self.assertEqual(len(row["frames"]), len(animation.frames))
            for actual, expected in zip(row["frames"], animation.frames):
                self.assertEqual(actual["file"], f"bitmaps/bitmap_{expected.bitmap_no}.png")
                self.assertEqual((actual["x"], actual["y"], actual["sound"]), (expected.x, expected.y, expected.sound_no))
                self.assertTrue((output / actual["file"]).is_file())


if __name__ == "__main__":
    unittest.main()
