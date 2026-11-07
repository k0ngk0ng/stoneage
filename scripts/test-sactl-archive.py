#!/usr/bin/env python3
"""Download, verify and exercise the actual native sactl release archive."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import stat
import subprocess
import sys
import tarfile
import urllib.request
import zipfile

ROOT=Path(__file__).resolve().parents[1]
parser=argparse.ArgumentParser(description=__doc__)
parser.add_argument('--target')
a=parser.parse_args()
arch={'aarch64':'arm64','arm64':'arm64','x86_64':'amd64','AMD64':'amd64'}.get(platform.machine())
system={'darwin':'darwin','linux':'linux','win32':'windows'}.get(sys.platform)
target=a.target or str(system)+'-'+str(arch)
lock=json.loads((ROOT/'packaging/sactl-catalog/release-lock.json').read_text())
asset=lock['assets'][target]
work=ROOT/'.cache/native'/target
shutil.rmtree(work,ignore_errors=True)
work.mkdir(parents=True)
archive=work/asset['url'].rsplit('/',1)[1]
request=urllib.request.Request(asset['url'],headers={'User-Agent':'stoneage-sactl-native-check'})
digest=hashlib.sha256()
with urllib.request.urlopen(request,timeout=60) as response,archive.open('wb') as output:
    count=0
    while block:=response.read(1<<16):
        count+=len(block)
        if count>128<<20:
            raise SystemExit('native archive exceeds its bounded size')
        output.write(block);digest.update(block)
if digest.hexdigest()!=asset['sha256']:
    raise SystemExit('native archive differs from independently verified release digest')
install=work/'installation';install.mkdir()
def safe(name):
    destination=(install/name).resolve()
    if not destination.is_relative_to(install):
        raise SystemExit('unsafe archive member')
if archive.suffix=='.zip':
    with zipfile.ZipFile(archive) as package:
        for member in package.infolist():
            safe(member.filename)
            if stat.S_IFMT(member.external_attr>>16)==stat.S_IFLNK:
                raise SystemExit('symlinks are not accepted in package fixtures')
        package.extractall(install)
else:
    with tarfile.open(archive) as package:
        for member in package.getmembers():
            safe(member.name)
            if not (member.isfile() or member.isdir()):
                raise SystemExit('special archive entries are not accepted')
        package.extractall(install)
name='sactl.exe' if target.startswith('windows') else 'sactl'
candidates=[path for path in install.rglob(name) if path.is_file()]
if len(candidates)!=1:
    raise SystemExit('archive must contain one sactl executable')
binary=candidates[0]
env=dict(os.environ)
for key,folder in {'XDG_CONFIG_HOME':'config','XDG_STATE_HOME':'state','XDG_CACHE_HOME':'cache','TMPDIR':'tmp','TEMP':'tmp','TMP':'tmp'}.items():
    path=work/folder;path.mkdir(exist_ok=True);env[key]=str(path)
for args,text in [(['version'],lock['version']),(['ai','--help'],'sactl ai'),(['arena','--help'],'sactl arena')]:
    result=subprocess.run([str(binary),*args],cwd=ROOT,env=env,capture_output=True,text=True,encoding='utf-8',timeout=30)
    if result.returncode or text not in result.stdout:
        raise SystemExit('native command failed: '+' '.join(args)+'\n'+result.stdout+result.stderr)
for name in ('completions/sactl.bash','completions/_sactl','local-arena-agent.md','arena-agent.example.json','skills/sactl/SKILL.md','install-sactl-skill.py'):
    if not (binary.parent/name).is_file():
        raise SystemExit('missing installed support file: '+name)
evidence=ROOT/'.cache/evidence';evidence.mkdir(parents=True,exist_ok=True)
(evidence/('native-'+target+'.json')).write_text(json.dumps({'suite':'sactl-native-archive','version':lock['version'],'target':target,'archive':archive.name,'sha256':digest.hexdigest(),'commands':['version','ai --help','arena --help'],'successful':True},indent=2)+'\n')
print('Verified actual native sactl '+lock['version']+' archive: '+target)
