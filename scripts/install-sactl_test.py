#!/usr/bin/env python3
"""Exercise the real Unix installer with CDN fixtures, without network or user files."""
from pathlib import Path
import hashlib, os, shutil, subprocess, tarfile, tempfile
root=Path(__file__).resolve().parent.parent
with tempfile.TemporaryDirectory(dir=root/'build',prefix='sactl-cdn-install-') as tmp:
    stage=Path(tmp); fixture=stage/'cdn';fixture.mkdir();mock=stage/'mock';mock.mkdir()
    bundle=stage/'stoneage-sactl-v0.1.99-linux-arm64';bundle.mkdir()
    (bundle/'sactl').write_text('#!/bin/sh\necho sactl-test\n')
    shutil.copy(root/'config/sactl/sactl.toml.example',bundle)
    shutil.copytree(root/'.agents/skills/sactl',bundle/'skills/sactl')
    archive=fixture/(bundle.name+'.tar.gz')
    with tarfile.open(archive,'w:gz') as f:f.add(bundle,arcname=bundle.name)
    (fixture/'SHA256SUMS').write_text(hashlib.sha256(archive.read_bytes()).hexdigest()+'  ./'+archive.name+'\n')
    (mock/'uname').write_text('#!/bin/sh\ncase "$1" in -s) echo Linux;; -m) echo aarch64;; esac\n')
    (mock/'curl').write_text('''#!/usr/bin/env python3
import os,pathlib,shutil,sys
args=sys.argv[1:];url=args[1];assert url.startswith('https://cdn.example/game/downloads/sactl/v0.1.99/'),url
shutil.copy(pathlib.Path(os.environ['TEST_CDN'])/url.rsplit('/',1)[1],args[args.index('-o')+1])
''')
    for p in mock.iterdir():p.chmod(0o755)
    home=stage/'user';env=dict(os.environ,PATH=str(mock)+':'+os.environ['PATH'],SACTL_INSTALL_HOME=str(home),TEST_CDN=str(fixture),XDG_CONFIG_HOME=str(home/'.config'),XDG_STATE_HOME=str(home/'.local/state'),TMPDIR=str(stage),PREFIX=str(home/'.local/bin'))
    cmd=['bash',str(root/'scripts/install-sactl.sh'),'--download','v0.1.99','--cdn-base','https://cdn.example/game']
    subprocess.run(cmd,env=env,check=True,capture_output=True)
    for agent in ['.agents','.claude']:assert (home/agent/'skills/sactl/references/battle.md').is_file()
    config=home/'.config/sactl/sactl.toml';config.write_text('existing user config')
    subprocess.run(cmd,env=env,check=True,capture_output=True);assert config.read_text()=='existing user config'
    # Streamed one-liner works without BASH_SOURCE or a local checkout.
    subprocess.run(['bash','-s','--',*cmd[2:]],input=(root/'scripts/install-sactl.sh').read_bytes(),env=env,check=True,capture_output=True)
    binary=home/'.local/bin/sactl';before=binary.read_bytes()
    archive.write_bytes(b'tampered')
    result=subprocess.run(cmd,env=env,capture_output=True)
    assert result.returncode!=0 and binary.read_bytes()==before
print('CDN installation, standard skills, config preservation, streamed script and checksum rejection passed')
