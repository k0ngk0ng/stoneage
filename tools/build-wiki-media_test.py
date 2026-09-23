"""Reject physical frame fallback when resolving logical map graphics."""
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('wiki_media', Path(__file__).with_name('build-wiki-media.py'))
media = importlib.util.module_from_spec(spec)
spec.loader.exec_module(media)


class MapGraphicTest(unittest.TestCase):
    def test_animation_frame_cannot_substitute_for_map_tile(self):
        bitmaps = {'15431': {'bmp_number': 0, 'file': 'character.png'}}
        self.assertIsNone(media.map_bitmap(15431, bitmaps, {}))
        self.assertIsNone(media.map_bitmap(15431, bitmaps, {'15431': 15431}))

    def test_validated_logical_alias_and_direct_record(self):
        tile = {'bmp_number': 698, 'file': 'terrain.png'}
        self.assertIs(media.map_bitmap(698, {'346': tile}, {'698': 346}), tile)
        self.assertIs(media.map_bitmap(698, {'698': tile}, {}), tile)
        self.assertIsNone(media.map_bitmap(699, {'346': tile}, {'699': 346}))


if __name__ == '__main__':
    unittest.main()
