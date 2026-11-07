#!/usr/bin/env python3
"""Focused release-validation tests, written before the sactl synchronizer.

Failure scenarios: wrong checksum-file digest; wrong archive digest; wrong source
URL; duplicate/missing assets; duplicate checksum entries; draft/prerelease;
version downgrade; and accidental edits to another product's catalog.
"""
import hashlib
import importlib.util
import json
from pathlib import Path
import shutil
import sys
import unittest
from unittest.mock import patch

ROOT=Path(__file__).resolve().parents[1]
spec=importlib.util.spec_from_file_location('sactl_sync',ROOT/'scripts/sync-sactl-catalog.py')
sync=importlib.util.module_from_spec(spec)
spec.loader.exec_module(sync)

class Fixture:
    def __init__(self,version='0.2.18'):
        self.tag='v'+version
        self.base='https://github.com/k0ngk0ng/stoneage/releases/download/'+self.tag+'/'
        self.metadata={'tag_name':self.tag,'draft':False,'prerelease':False,'assets':[]}
        lines=[]
        for target in sync.TARGETS:
            name=sync.archive_name(version,target)
            digest=hashlib.sha256(('fixture:'+name).encode()).hexdigest()
            lines.append(digest+'  '+name)
            self.metadata['assets'].append({'name':name,'digest':'sha256:'+digest,'browser_download_url':self.base+name})
        self.sums=('\n'.join(lines)+'\n').encode()
        self.metadata['assets'].append({'name':'SHA256SUMS','digest':'sha256:'+hashlib.sha256(self.sums).hexdigest(),'browser_download_url':self.base+'SHA256SUMS'})
    def fetch(self,url,api=False):
        if url==getattr(self,'api_url','https://api.github.com/repos/k0ngk0ng/stoneage/releases/latest') and api:
            return json.dumps(self.metadata).encode()
        if url==self.base+'SHA256SUMS' and not api:
            return self.sums
        raise AssertionError('unexpected fetch '+url)
    def run(self):
        with patch.object(sync,'fetch',side_effect=self.fetch):
            return sync.release()

class ReleaseChecks(unittest.TestCase):
    def test_valid_release_preserves_all_five_targets(self):
        item=Fixture().run()
        self.assertEqual(item['version'],'0.2.18')
        self.assertEqual(set(item['assets']),set(sync.TARGETS))
    def test_explicit_release_tag_is_verified(self):
        fixture=Fixture()
        fixture.api_url='https://api.github.com/repos/k0ngk0ng/stoneage/releases/tags/v0.2.18'
        with patch.object(sync,'fetch',side_effect=fixture.fetch):
            result=sync.release('v0.2.18')
        self.assertEqual(result['version'],'0.2.18')

    def test_explicit_tag_cannot_return_another_release(self):
        fixture=Fixture()
        fixture.api_url='https://api.github.com/repos/k0ngk0ng/stoneage/releases/tags/v0.2.19'
        with patch.object(sync,'fetch',side_effect=fixture.fetch):
            with self.assertRaisesRegex(ValueError,'requested'):
                sync.release('v0.2.19')

    def test_bad_checksum_file_digest(self):
        fixture=Fixture();fixture.metadata['assets'][-1]['digest']='sha256:'+'0'*64
        with self.assertRaisesRegex(ValueError,'SHA256SUMS'):
            fixture.run()
    def test_wrong_independent_archive_checksum(self):
        fixture=Fixture();fixture.metadata['assets'][0]['digest']='sha256:'+'0'*64
        with self.assertRaisesRegex(ValueError,'independent checksum'):
            fixture.run()
    def test_wrong_archive_origin(self):
        fixture=Fixture();fixture.metadata['assets'][0]['browser_download_url']='https://example.invalid/file'
        with self.assertRaisesRegex(ValueError,'origin'):
            fixture.run()
    def test_wrong_checksum_origin(self):
        fixture=Fixture();fixture.metadata['assets'][-1]['browser_download_url']='https://example.invalid/SHA256SUMS'
        with self.assertRaisesRegex(ValueError,'origin'):
            fixture.run()
    def test_missing_asset(self):
        fixture=Fixture();fixture.metadata['assets'].pop(0)
        with self.assertRaisesRegex(ValueError,'missing'):
            fixture.run()
    def test_duplicate_assets(self):
        fixture=Fixture();fixture.metadata['assets'].append(dict(fixture.metadata['assets'][0]))
        with self.assertRaisesRegex(ValueError,'duplicate'):
            fixture.run()
    def test_duplicate_checksum_entries(self):
        fixture=Fixture();fixture.sums+=fixture.sums.splitlines(keepends=True)[0]
        fixture.metadata['assets'][-1]['digest']='sha256:'+hashlib.sha256(fixture.sums).hexdigest()
        with self.assertRaisesRegex(ValueError,'duplicate'):
            fixture.run()
    def test_malformed_digest(self):
        fixture=Fixture();fixture.metadata['assets'][0]['digest']='sha256:'+'A'*64
        with self.assertRaisesRegex(ValueError,'SHA-256'):
            fixture.run()
    def test_draft_and_prerelease(self):
        for flag in ('draft','prerelease'):
            fixture=Fixture();fixture.metadata[flag]=True
            with self.subTest(flag=flag),self.assertRaisesRegex(ValueError,'stable'):
                fixture.run()
    def test_sync_does_not_touch_other_catalogs(self):
        root=ROOT/'.cache/test-sync/catalogs'
        shutil.rmtree(root,ignore_errors=True)
        (root/'Formula').mkdir(parents=True);(root/'bucket').mkdir()
        (root/'Formula/wirectl.rb').write_bytes(b'other formula')
        (root/'bucket/wirectl.json').write_bytes(b'other manifest')
        sync.write_catalogs(root,Fixture().run())
        self.assertEqual((root/'Formula/wirectl.rb').read_bytes(),b'other formula')
        self.assertEqual((root/'bucket/wirectl.json').read_bytes(),b'other manifest')
        sync.check_catalogs(root,Fixture().run())
    def test_downgrade_is_rejected_before_writes(self):
        root=ROOT/'.cache/test-sync/downgrade'
        shutil.rmtree(root,ignore_errors=True);root.mkdir(parents=True)
        sync.write_catalogs(root,Fixture('0.2.19').run())
        before=(root/'Formula/sactl.rb').read_bytes()
        with self.assertRaisesRegex(ValueError,'downgrade'):
            sync.write_catalogs(root,Fixture('0.2.18').run())
        self.assertEqual((root/'Formula/sactl.rb').read_bytes(),before)

if __name__=='__main__':
    result=unittest.TextTestRunner(verbosity=2).run(unittest.defaultTestLoader.loadTestsFromTestCase(ReleaseChecks))
    evidence=ROOT/'.cache/evidence';evidence.mkdir(parents=True,exist_ok=True)
    (evidence/'sactl-sync-tests.json').write_text(json.dumps({'suite':'sactl-release-validation','tests':result.testsRun,'successful':result.wasSuccessful(),'failures':[str(t) for t,_ in result.failures],'errors':[str(t) for t,_ in result.errors]},indent=2)+'\n')
    sys.exit(not result.wasSuccessful())
