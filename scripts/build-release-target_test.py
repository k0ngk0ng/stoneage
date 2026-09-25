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
    # The linux leg packages .deb/.rpm through nfpm; the layout test mocks it
    # so it never needs the real tool.
    nfpm = mock_bin / 'nfpm'
    nfpm.write_text('''#!/usr/bin/env python3
import pathlib, sys
args = sys.argv[1:]
target = pathlib.Path(args[args.index('--target') + 1])
target.write_text('mock package')
''')
    nfpm.chmod(0o755)
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
        # The standalone client archive carries the binary, the config example
        # and its documentation, because it is installed on an operator
        # machine rather than deployed with the server.
        sactl_prefix = f'stoneage-sactl-v0.1.99-{target_os}-{arch}'
        if target_os == 'windows':
            with zipfile.ZipFile(dist / f'{sactl_prefix}.zip') as archive:
                sactl_names = archive.namelist()
                payload = archive.read(f'{sactl_prefix}/sactl.exe').decode()
        else:
            with tarfile.open(dist / f'{sactl_prefix}.tar.gz') as archive:
                sactl_names = archive.getnames()
                payload = archive.extractfile(f'{sactl_prefix}/sactl{suffix}').read().decode()
        assert payload == f'{target_os}/{arch}', payload
        for entry in ['sactl.toml.example', 'sactl.md', 'install-sactl.sh', 'install-sactl-skill.py', 'skills/sactl/SKILL.md', 'skills/sactl/references/battle.md', 'skills/sactl/references/session.md']:
            assert f'{sactl_prefix}/{entry}' in sactl_names, entry
    with tarfile.open(dist / 'stoneage-deploy-v0.1.99.tar.gz') as archive:
        assert archive.extractfile('VERSION').read() == b'v0.1.99\n'
        assert archive.extractfile('bin/stoneage-assets-sync').read() == b'linux/amd64'
    with tarfile.open(dist / 'stoneage-sactl-v0.1.99-linux-arm64.tar.gz') as archive:
        assert archive.extractfile('stoneage-sactl-v0.1.99-linux-arm64/skills/sactl/SKILL.md')
        assert archive.extractfile('stoneage-sactl-v0.1.99-linux-arm64/install-sactl-skill.py')
    # Linux additionally ships an arm64 client plus .deb and .rpm packages.
    for name in ['sactl_0.1.99_amd64.deb', 'sactl_0.1.99_arm64.deb',
                 'sactl-0.1.99-1.x86_64.rpm', 'sactl-0.1.99-1.aarch64.rpm',
                 'stoneage-sactl-v0.1.99-linux-arm64.tar.gz']:
        assert (dist / name).exists(), name
    # 4 control-plane archives, 4 assets-sync archives, 5 sactl archives,
    # 4 Linux packages and the deploy bundle.
    assert len(list(dist.iterdir())) == 20, sorted(p.name for p in dist.iterdir())
print('Release target package layouts passed')
