#!/usr/bin/env python3
"""Package public sprite resources separately from application images."""
import argparse
from pathlib import Path
import re
import tarfile

root = Path(__file__).resolve().parent.parent
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('version')
parser.add_argument('--output', default='dist')
args = parser.parse_args()
if not re.fullmatch(r'v[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.-]+)?', args.version):
    parser.error('expected a release tag')
source = root / 'client/web/assets/original'
output = root / args.output
output.mkdir(parents=True, exist_ok=True)
archive = output / f'stoneage-sprites-{args.version}.tar.gz'
files = sorted(source.rglob('*'))
if not any(p.suffix == '.png' for p in files):
    raise SystemExit('sprite resource tree is missing')
with tarfile.open(archive, 'w:gz', compresslevel=1) as tar:
    for path in files:
        if path.is_symlink():
            raise ValueError(f'refusing symlink: {path}')
        if not path.is_file():
            continue
        relative = path.relative_to(source)
        # Only generated browser resource formats are distributable.
        if str(relative) not in {'vendor/html2canvas.min.js', 'vendor/LICENSE.html2canvas'} and path.suffix.lower() not in {'.png', '.json', '.webp', '.jpg', '.jpeg', '.gif', '.svg', '.bin', '.sap'}:
            raise ValueError(f'unexpected resource type: {relative}')
        info = tar.gettarinfo(str(path), arcname=str(Path('assets/sprites') / relative))
        info.uid = info.gid = 0
        info.uname = info.gname = ''
        info.mode = 0o644
        with path.open('rb') as stream:
            tar.addfile(info, stream)
print(archive)
