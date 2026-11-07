#!/usr/bin/env python3
"""Synchronize only sactl catalogs using official digests and independent checksums."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import urllib.request

ROOT=Path(__file__).resolve().parents[1]
REPOSITORY='k0ngk0ng/stoneage'
TARGETS=('darwin-arm64','darwin-amd64','linux-arm64','linux-amd64','windows-amd64')
VERSION=re.compile(r'(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\Z')


def archive_name(version,target):
    return 'stoneage-sactl-v'+version+'-'+target+('.zip' if target.startswith('windows') else '.tar.gz')


def fetch(url,api=False):
    headers={'User-Agent':'stoneage-sactl-catalog','Accept':'application/vnd.github+json' if api else 'application/octet-stream'}
    if api and os.environ.get('GH_TOKEN'):
        headers['Authorization']='Bearer '+os.environ['GH_TOKEN']
    with urllib.request.urlopen(urllib.request.Request(url,headers=headers),timeout=60) as response:
        data=response.read(1024*1024+1)
    if len(data)>1024*1024:
        raise ValueError('release metadata or checksums are too large')
    return data


def digest(asset):
    value=asset.get('digest','')
    if not re.fullmatch(r'sha256:[0-9a-f]{64}',value):
        raise ValueError('GitHub asset must have a SHA-256 digest')
    return value[7:]


def release(requested=None):
    endpoint='https://api.github.com/repos/'+REPOSITORY+'/releases/latest'
    if requested:
        requested=requested.removeprefix('v')
        if not VERSION.fullmatch(requested):
            raise ValueError('requested release must use stable SemVer')
        endpoint='https://api.github.com/repos/'+REPOSITORY+'/releases/tags/v'+requested
    metadata=json.loads(fetch(endpoint,api=True))
    tag=metadata['tag_name']
    version=tag.removeprefix('v')
    if requested and version!=requested:
        raise ValueError('metadata differs from the requested release')
    if metadata.get('draft') or metadata.get('prerelease') or tag!='v'+version or not VERSION.fullmatch(version):
        raise ValueError('only stable versioned releases are packaged')
    assets={asset['name']:asset for asset in metadata['assets']}
    if len(assets)!=len(metadata['assets']):
        raise ValueError('duplicate release asset names')
    base='https://github.com/'+REPOSITORY+'/releases/download/'+tag+'/'
    def asset(name):
        if name not in assets:
            raise ValueError('missing release asset: '+name)
        item=assets[name]
        if item['browser_download_url']!=base+name:
            raise ValueError('unexpected release download origin: '+name)
        return item
    sums_asset=asset('SHA256SUMS')
    raw=fetch(base+'SHA256SUMS')
    sums_digest=digest(sums_asset)
    if hashlib.sha256(raw).hexdigest()!=sums_digest:
        raise ValueError('SHA256SUMS differs from its GitHub digest')
    checksums={}
    for line in raw.decode('utf-8').splitlines():
        match=re.fullmatch(r'([0-9a-f]{64})\s+\*?(?:\./)?([^/\\]+)',line)
        if not match or match[2] in checksums:
            raise ValueError('invalid or duplicate checksum entry')
        checksums[match[2]]=match[1]
    result={'repository':REPOSITORY,'tag':tag,'version':version,'checksums_sha256':sums_digest,'assets':{}}
    for target in TARGETS:
        name=archive_name(version,target)
        checksum=digest(asset(name))
        if checksums.get(name)!=checksum:
            raise ValueError('independent checksum mismatch: '+name)
        result['assets'][target]={'url':base+name,'sha256':checksum}
    return result


def validate_lock(item):
    version=item['version']
    if item.get('repository')!=REPOSITORY or not VERSION.fullmatch(version) or item.get('tag')!='v'+version:
        raise ValueError('invalid release lock source or version')
    if set(item['assets'])!=set(TARGETS) or not re.fullmatch(r'[0-9a-f]{64}',item.get('checksums_sha256','')):
        raise ValueError('invalid release lock assets')
    for target in TARGETS:
        value=item['assets'][target]
        expected='https://github.com/'+REPOSITORY+'/releases/download/v'+version+'/'+archive_name(version,target)
        if value['url']!=expected or not re.fullmatch(r'[0-9a-f]{64}',value['sha256']):
            raise ValueError('invalid release lock asset URL or digest')


def render_formula(item):
    text=(ROOT/'packaging/sactl-catalog/sactl.rb.in').read_text().replace('@VERSION@',item['version'])
    for target in TARGETS:
        text=text.replace('@'+target.upper().replace('-','_')+'_SHA256@',item['assets'][target]['sha256'])
    return text


def render_scoop(item):
    manifest=json.loads((ROOT/'packaging/sactl-catalog/sactl.json.in').read_text())
    asset=item['assets']['windows-amd64']
    manifest.update(version=item['version'],url=asset['url'],hash=asset['sha256'],extract_dir='stoneage-sactl-v'+item['version']+'-windows-amd64')
    return manifest


def check_catalogs(root,item):
    validate_lock(item)
    if (root/'Formula/sactl.rb').read_text()!=render_formula(item):
        raise ValueError('sactl Formula differs from verified release')
    if json.loads((root/'bucket/sactl.json').read_text())!=render_scoop(item):
        raise ValueError('sactl Scoop manifest differs from verified release')
    if json.loads((root/'packaging/sactl-catalog/release-lock.json').read_text())!=item:
        raise ValueError('sactl release lock differs from verified release')


def write_catalogs(root,item):
    validate_lock(item)
    lock=root/'packaging/sactl-catalog/release-lock.json'
    if lock.exists():
        current=json.loads(lock.read_text())['version']
        if tuple(map(int,item['version'].split('.')))<tuple(map(int,current.split('.'))):
            raise ValueError('refusing to downgrade sactl catalogs')
    # Validate and render everything before touching either catalog. These are
    # the only product paths the synchronizer can update.
    rendered={root/'Formula/sactl.rb':render_formula(item),
              root/'bucket/sactl.json':json.dumps(render_scoop(item),indent=2)+'\n',
              lock:json.dumps(item,indent=2,sort_keys=True)+'\n'}
    for path,text in rendered.items():
        path.parent.mkdir(parents=True,exist_ok=True)
        path.write_text(text)


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--version',help='verify a specific published stable release')
    parser.add_argument('--offline',action='store_true',help='use the previously verified release lock')
    parser.add_argument('--check',action='store_true',help='verify catalogs without changing them')
    parser.add_argument('--output',type=Path,default=ROOT)
    args=parser.parse_args()
    output=args.output.resolve()
    if not output.is_relative_to(ROOT):
        parser.error('output must stay inside this repository')
    item=json.loads((ROOT/'packaging/sactl-catalog/release-lock.json').read_text()) if args.offline else release(args.version)
    if args.check:
        check_catalogs(output,item)
    else:
        write_catalogs(output,item)
    print('Verified sactl '+item['version']+' · '+str(len(TARGETS))+' platforms')

if __name__=='__main__':
    try:
        main()
    except (OSError,KeyError,ValueError) as error:
        raise SystemExit(str(error))
