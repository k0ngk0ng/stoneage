"""Reject physical frame fallback when resolving logical map graphics."""
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('wiki_media', Path(__file__).with_name('build-wiki-media.py'))
media = importlib.util.module_from_spec(spec)
spec.loader.exec_module(media)


class MapGraphicTest(unittest.TestCase):
    def test_gate_cells_collapse_but_distant_entrances_remain(self):
        def marker(key, x, kind='exit'):
            return {'key': key, 'name': key, 'x': x, 'y': .5, 'kind': kind}
        entries = {
            'npc:gate': {'fields': [{'label': '功能', 'value': 'Warp'}], 'links': [{'key': 'map:100'}]},
            'npc:local': {'links': [{'key': 'map:100'}]},
            'npc:inside': {'links': [{'key': 'map:2000'}]},
            'map:2000': {'name': '玛丽娜丝渔村'},
        }
        markers = [marker('npc:gate', .5, 'npc'), marker('npc:local', .4, 'npc'),
                   marker('npc:inside', .5, 'npc'), marker('map:2000', .5),
                   marker('map:2000', .501), marker('map:2000', .502),
                   marker('map:2000', .8), marker('map:3000', .5)]
        result = media.map_markers('map:100', markers, entries)
        self.assertEqual([m['key'] for m in result], ['npc:local', 'map:2000', 'map:2000', 'map:3000'])
        self.assertEqual(result[1]['x'], .501)
        self.assertEqual(result[1]['name'], '玛丽娜丝渔村')

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
