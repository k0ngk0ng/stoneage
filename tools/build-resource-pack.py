#!/usr/bin/env python3
"""Build a standard ZIP of exactly the public assets in a publication manifest."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import zipfile

MANIFEST = 'stoneage-resources.json'
MAX_FILE = 256 * 1024 * 1024


def safe_path(key):
    return bool(re.fullmatch(r'(assets|maps|audio)/(?:[A-Za-z0-9_-][A-Za-z0-9_.-]*/)*[A-Za-z0-9_-][A-Za-z0-9_.-]*', key)) and all(part not in ('.', '..') for part in key.split('/'))


def build(publication, assets, client, output, prefix='stoneage'):
    objects = publication['objects']
    digest = hashlib.sha256()
    for key, info in sorted(objects.items()):
        digest.update(f'{key}\0{info["size"]}\0{info["sha256"]}\0'.encode())
    revision = digest.hexdigest()
    prefix = prefix.strip('/')
    prefix = prefix + '/' if prefix else ''
    entries, sources, offset, total = [], [], 0, 0
    roots = {'assets': assets.resolve(), 'maps': (client / 'map').resolve(), 'audio': (client / 'data').resolve()}
    for key, expected in sorted(objects.items()):
        if not key.startswith(prefix):
            raise ValueError(f'object outside publication prefix: {key}')
        relative = key[len(prefix):]
        if not safe_path(relative):
            raise ValueError(f'unsafe resource path: {relative}')
        tree, name = relative.split('/', 1)
        if tree == 'audio' and not (name == 'auto.dat' or name.startswith(('bgm/', 'se/', 'pal/'))):
            raise ValueError(f'non-public audio path: {relative}')
        source = roots[tree] / name
        if not source.resolve().is_relative_to(roots[tree]) or source.is_symlink():
            raise ValueError(f'unsafe source: {relative}')
        size = source.stat().st_size
        with source.open('rb') as handle:
            sha = hashlib.file_digest(handle, 'sha256').hexdigest()
        if size > MAX_FILE or expected != {'size': size, 'sha256': sha}:
            raise ValueError(f'local resource differs from publication: {relative}')
        entries.append(dict(path=relative, size=size, sha256=sha, offset=offset))
        sources.append(source)
        offset += 30 + len(relative.encode()) + size
        total += size
    if not entries or len(entries) > 500000:
        raise ValueError('invalid resource count')
    header = dict(format=2, revision=revision, bytes=total, entries=entries)
    encoded = json.dumps(header, separators=(',', ':')).encode()
    output.parent.mkdir(parents=True, exist_ok=True)
    partial = output.with_suffix('.zip.partial')
    try:
        # STORE is intentional: already-compressed PNGs dominate the pack.
        # Independent entries keep browser memory bounded to one resource.
        with zipfile.ZipFile(partial, 'w', compression=zipfile.ZIP_STORED, allowZip64=True) as archive:
            archive.writestr(MANIFEST, encoded)
            for entry, source in zip(entries, sources):
                archive.write(source, entry['path'])
        partial.replace(output)
    finally:
        partial.unlink(missing_ok=True)
    with output.open('rb') as handle:
        sha = hashlib.file_digest(handle, 'sha256').hexdigest()
    catalog = dict(revision=revision, packages=[dict(name='完整游戏资源', files=len(entries), bytes=output.stat().st_size,
        unpacked_bytes=total, path=f'packs/{revision}/{output.name}', sha256=sha)])
    output.with_name('resource-packs.json').write_text(json.dumps(catalog, ensure_ascii=False, indent=2) + '\n')
    return catalog


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--publication', type=Path, required=True)
    parser.add_argument('--assets', type=Path, default=Path('client/web/assets/original'))
    parser.add_argument('--client-data', type=Path, default=Path('runtime/legacy-client'))
    parser.add_argument('--prefix', default='stoneage')
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    if args.output.suffix != '.zip':
        parser.error('--output must end with .zip')
    print(json.dumps(build(json.loads(args.publication.read_text()), args.assets, args.client_data, args.output, args.prefix), ensure_ascii=False))


if __name__ == '__main__':
    main()
