#!/usr/bin/env python3
"""Exercise all release layouts without paying for four cross-compilations."""
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import zipfile

root = Path(__file__).resolve().parent.parent
(root / 'build').mkdir(exist_ok=True)
with tempfile.TemporaryDirectory(dir=root / 'build', prefix='release-layout-') as temporary:
    stage = Path(temporary)
    mock_bin = stage / 'mock-bin'
    mock_bin.mkdir()
    go = mock_bin / 'go'
    go.write_text('''#!/usr/bin/env python3
import os, pathlib, sys
args = sys.argv[1:]
output = pathlib.Path(args[args.index('-o') + 1])
if args[-1] == './client/web':
    assert '-ldflags=-s -w -X main.releaseVersion=v0.1.99' in args
output.parent.mkdir(parents=True, exist_ok=True)
output.write_text(os.environ['GOOS'] + '/' + os.environ['GOARCH'])
output.chmod(0o755)
''')
    go.chmod(0o755)
    dist = stage / 'dist'
    env = dict(os.environ, PATH=f'{mock_bin}:{os.environ["PATH"]}',
               RELEASE_TAG='v0.1.99', RELEASE_STAGE=str(stage / 'packages'), RELEASE_DIST=str(dist))
    for target_os, arch in [('linux', 'amd64'), ('darwin', 'arm64'), ('darwin', 'amd64'), ('windows', 'amd64')]:
        subprocess.run(['bash', 'scripts/build-release-target.sh', target_os, arch], cwd=root, env=env, check=True)
        prefix = f'stoneage-control-plane-v0.1.99-{target_os}-{arch}'
        suffix = '.exe' if target_os == 'windows' else ''
        if target_os == 'windows':
            assert not (dist / f'{prefix}.tar.gz').exists()
            with zipfile.ZipFile(dist / f'{prefix}.zip') as archive:
                names = archive.namelist()
        else:
            with tarfile.open(dist / f'{prefix}.tar.gz') as archive:
                names = archive.getnames()
        for command in ['stoneage-admin', 'stoneage-gateway', 'stoneage-operator', 'stoneage-assets-sync']:
            assert f'{prefix}/bin/{command}{suffix}' in names
        assert (f'{prefix}/bin/stoneage-web' in names) == (target_os == 'linux')
        with tarfile.open(dist / f'stoneage-assets-sync-v0.1.99-{target_os}-{arch}.tar.gz') as archive:
            assert archive.extractfile(f'bin/stoneage-assets-sync{suffix}').read().decode() == f'{target_os}/{arch}'
    with tarfile.open(dist / 'stoneage-deploy-v0.1.99.tar.gz') as archive:
        assert archive.extractfile('VERSION').read() == b'v0.1.99\n'
        assert archive.extractfile('bin/stoneage-assets-sync').read() == b'linux/amd64'
    assert len(list(dist.iterdir())) == 9
print('Release target package layouts passed')
