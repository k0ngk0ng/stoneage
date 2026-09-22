"""RD encoder edge cases; no full client archive is required for CI."""
import struct
import unittest
from pathlib import Path
import asset_cooker as cooker


def rd(width, height, payload):
    return b'RD\x01\x00' + struct.pack('<III', width, height, 16 + len(payload)) + payload


class DecoderBoundaryTest(unittest.TestCase):
    def test_native_terminal_transparent_pair(self):
        # Native encoder reads buf1+1 even when buf1 is the final zero pixel.
        self.assertEqual(cooker.decode_rd(rd(2, 2, b'\x03\x01\x02\x03\xc2'), 2, 2),
                         (2, 2, b'\x03\x00\x01\x02'))

    def test_other_overruns_remain_errors(self):
        for payload in [b'\x03\x01\x02\x03\xc3',
                        b'\x03\x01\x02\x03\x82\x01',
                        b'\x03\x01\x02\x03\xc2\x01\x00']:
            with self.subTest(payload=payload), self.assertRaises(cooker.AssetError):
                cooker.decode_rd(rd(2, 2, payload), 2, 2)

    def test_preserved_feather_records(self):
        root = Path(__file__).resolve().parents[1] / 'runtime/legacy-client/data'
        if not (root / 'adrn_15.bin').is_file():
            self.skipTest('preserved client archive unavailable')
        records, logical = cooker.parse_adrn(root / 'adrn_15.bin')
        for graphic in [24077, 24137, 24138, 24140]:
            image = cooker.read_bitmap(root / 'real_15.bin', records, logical[graphic][-1])
            self.assertEqual(len(image.pixels), image.width * image.height)
            self.assertTrue(any(image.pixels))


if __name__ == '__main__':
    unittest.main()
