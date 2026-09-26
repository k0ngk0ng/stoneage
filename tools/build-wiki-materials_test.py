import importlib.util
import json
from pathlib import Path
import subprocess
import unittest

ROOT=Path(__file__).resolve().parent.parent
spec=importlib.util.spec_from_file_location('materials',ROOT/'tools/build-wiki-materials.py')
module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)

class MaterialReferenceTest(unittest.TestCase):
    def test_sound_mapping_matches_deployed_client_for_every_tone(self):
        script="""const fs=require('fs');const src=fs.readFileSync('client/web/runtimeassets/index.html','utf8');const a=src.indexOf('  function soundFileForTone('),b=src.indexOf('  function playSoundEffect(',a);eval(src.slice(a,b));console.log(JSON.stringify(Array.from({length:11000},(_,i)=>soundFileForTone(i))));"""
        actual=json.loads(subprocess.check_output(['node','-e',script],cwd=ROOT))
        self.assertEqual(actual,[module.tone_file(i) for i in range(11000)])
    def test_native_rides_and_white_tiger_keep_exact_character_groups(self):
        table=module.ride_table(ROOT/'server/legacy/source/2.5/gmsv')
        self.assertEqual(len(table['100872']),12)
        self.assertEqual(table['100872']['104025'],[100000,100005,100010,100015,100700,100705])
        self.assertEqual(table['100872']['104036'],[100220,100225,100230,100235,100810,100815])
        self.assertIn(100000,table['100352']['101000'])
        self.assertNotIn('0',table)

if __name__=='__main__': unittest.main()
