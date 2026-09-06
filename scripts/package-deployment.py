#!/usr/bin/env python3
"""Build a production-only Compose package from an explicit allowlist."""
import argparse
import io
from pathlib import Path
import re
import tarfile

root = Path(__file__).resolve().parent.parent
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('version')
parser.add_argument('--output', default='dist')
parser.add_argument('--uploader', required=True, help='prebuilt Linux amd64 stoneage-assets-sync executable')
args = parser.parse_args()
if not re.fullmatch(r'v[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.-]+)?', args.version):
    parser.error('version must be a release tag such as v0.1.12')
output = root / args.output
output.mkdir(parents=True, exist_ok=True)
files = {
    'docker-compose.yml': 'docker-compose.yml',
    '.env.compose.example': '.env.compose.example',
    'README.md': 'docs/docker-compose.md',
    **{f'bin/{name}': f'bin/{name}' for name in
       ('stoneage', 'deploy.sh', 'sync-assets.sh', 'registry-login.sh', 'aliyun-certificate.py')},
    **{f'config/{name}': f'config/{name}' for name in
       ('web/web.toml', 'web/web.r2.toml.example', 'gateway/gateway.toml',
        'gmsv/setup.cf.example', 'saac/acserv.cf.example', 'registry/username.example', 'certificates/aliyun.json.example', 'certificates/README.md',
        'certificates/stoneage-cdn-certificate.service', 'certificates/stoneage-cdn-certificate.timer')},
}
if args.uploader:
    uploader = Path(args.uploader).resolve()
    if not uploader.is_file() or uploader.is_symlink():
        parser.error('--uploader must be a regular executable file')
    files['bin/stoneage-assets-sync'] = str(uploader)
archive = output / f'stoneage-deploy-{args.version}.tar.gz'
with tarfile.open(archive, 'w:gz') as tar:
    for target, source in files.items():
        source_path = root / source
        if source_path.is_symlink():
            raise ValueError(f'refusing symlink: {source}')
        data = source_path.read_bytes()
        if target in ('.env.compose.example', 'docker-compose.yml'):
            data = re.sub(rb'v\d+\.\d+\.\d+', args.version.encode(), data)
        info = tarfile.TarInfo(target)
        info.size = len(data)
        info.mode = 0o755 if target.startswith('bin/') else 0o644
        tar.addfile(info, io.BytesIO(data))
    data = (args.version + '\n').encode()
    info = tarfile.TarInfo('VERSION')
    info.size = len(data)
    info.mode = 0o644
    tar.addfile(info, io.BytesIO(data))
print(archive)
