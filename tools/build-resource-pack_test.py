import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
import zipfile

spec = importlib.util.spec_from_file_location('resource_pack', Path(__file__).with_name('build-resource-pack.py'))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class ResourcePackTest(unittest.TestCase):
    def test_complete_zip_and_reject_unpublished_bytes(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            assets, client = root/'assets', root/'client'
            paths = {'assets/manifest.json': assets/'manifest.json', 'assets/pet.png': assets/'pet.png',
                     'maps/1000.DAT': client/'map/1000.DAT', 'audio/bgm/1.wav': client/'data/bgm/1.wav'}
            objects = {}
            for key, source in paths.items():
                source.parent.mkdir(parents=True, exist_ok=True)
                data = key.encode()
                source.write_bytes(data)
                objects['game/'+key] = {'size': len(data), 'sha256': hashlib.sha256(data).hexdigest()}
            output = root/'packs/stoneage-resources.zip'
            catalog = module.build({'objects': objects}, assets, client, output, 'game')
            with zipfile.ZipFile(output) as archive:
                self.assertIsNone(archive.testzip())
                header = json.loads(archive.read('stoneage-resources.json'))
                self.assertEqual(len(header['entries']), 4)
                for entry in header['entries']:
                    self.assertEqual(archive.read(entry['path']), paths[entry['path']].read_bytes())
                self.assertEqual(header['revision'], catalog['revision'])
            (assets/'pet.png').write_bytes(b'changed')
            with self.assertRaisesRegex(ValueError, 'differs from publication'):
                module.build({'objects': objects}, assets, client, output, 'game')
            for path in ['assets/../private', 'audio/../../key', '/assets/a', 'assets/a%2fb', 'assets/a?x']:
                self.assertFalse(module.safe_path(path))


if __name__ == '__main__':
    unittest.main()
