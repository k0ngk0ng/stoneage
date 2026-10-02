#!/usr/bin/env python3
"""Exercise distribution orchestration with substitutes, never build/pull an image.

This tests architecture/version guards and volume retention; native image/runtime
correctness is checked separately by release CI on both Linux architectures.
"""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

root = Path(__file__).resolve().parent.parent
(root / 'build').mkdir(exist_ok=True)


def executable(path, text):
    path.write_text('#!' + sys.executable + '\n' + text)
    path.chmod(0o755)


with tempfile.TemporaryDirectory(dir=root / 'build', prefix='training-image-test-') as tmp:
    base = Path(tmp)
    mock = base / 'bin'
    mock.mkdir()
    executable(mock / 'python3', '''import json,os,sys
from pathlib import Path
a=sys.argv[1:]
assert a[0]=='scripts/test-training-worker-lifecycle.py'
assert Path(a[1]).is_file() and a[2]=='sha256:'+'a'*64
with open(os.environ['MOCK_LOG'],'a') as f:f.write(json.dumps({'tool':'lifecycle','args':a})+'\\n')
if os.environ.get('MOCK_FAIL_LIFECYCLE'):sys.exit(8)
Path(a[3]).mkdir()
''')
    executable(mock / 'uname', '''import os,sys
print('Linux' if sys.argv[1]=='-s' else os.environ['MOCK_HOST_MACHINE'])
''')
    executable(mock / 'mktemp', '''import os,sys
from pathlib import Path
assert sys.argv[1]=='-d' and sys.argv[2].endswith('/build/training-image.XXXXXX')
p=Path(os.environ['MOCK_CASE'])/'stage';p.mkdir();print(p)
''')
    client = base / 'fixture-sactl'
    executable(client, '''import json,os,sys
from pathlib import Path
a=sys.argv[1:]
with open(os.environ['MOCK_LOG'],'a') as f:f.write(json.dumps({'tool':'client','args':a})+'\\n')
if a==['version']:print('sactl '+os.environ.get('MOCK_CLIENT_VERSION','v0.0.0-test'));sys.exit(0)
assert a[0]=='ai'
if a[1:3]==['environment','init']:
 p=Path(a[a.index('--directory')+1]);p.mkdir();(p/'environment.json').write_text('{}')
elif a[1:3]==['environment','check']:
 assert Path(a[a.index('--environment')+1]).exists()
elif a[1]=='evaluate':
 assert Path(a[a.index('--model')+1]).exists()
 Path(a[a.index('--output')+1]).write_text('{}')
elif a[1]=='verify-evaluation':assert Path(a[a.index('--report')+1]).exists()
else:raise AssertionError(a)
print('{}')
''')
    executable(mock / 'docker', '''import json,os,shutil,sys
from pathlib import Path
a=sys.argv[1:]
with open(os.environ['MOCK_LOG'],'a') as f:f.write(json.dumps({'tool':'docker','args':a})+'\\n')
if a[:2]==['image','inspect']:
 fmt=a[a.index('--format')+1]
 if fmt=='{{.Id}}':print('sha256:'+'a'*64)
 elif 'training.schema' in fmt:print('1')
 elif fmt=='{{.Os}}/{{.Architecture}}':print('linux/'+os.environ['MOCK_IMAGE_ARCH'])
 elif 'image.version' in fmt:print(os.environ.get('MOCK_IMAGE_VERSION','v0.0.0-test'))
 elif fmt.startswith('{"image_id"'):print(json.dumps({'image_id':'sha256:'+'a'*64,'os':'linux','architecture':os.environ['MOCK_IMAGE_ARCH']}))
 else:raise AssertionError(fmt)
elif a[0]=='cp':
 src=a[1].split(':',1)[1];dst=Path(a[2])
 if src=='/opt/stoneage/bin/sactl':shutil.copy2(os.environ['MOCK_CLIENT'],dst)
 elif src=='/opt/stoneage/skills/sactl':
  shutil.copytree(Path(os.environ['MOCK_ROOT'])/'.agents/skills/sactl',dst)
  if os.environ.get('MOCK_BAD_SKILL'):(dst/'SKILL.md').write_text('wrong version')
 elif src in ['/data/exported-model.safetensors','/data/experiment.json']:dst.write_text('{}')
 else:raise AssertionError(src)
elif a[0] in ['create','run']:
 assert a[a.index('--pull')+1]=='never'
 assert a[a.index('--platform')+1]=='linux/'+os.environ['MOCK_IMAGE_ARCH']
 assert a[a.index('--network')+1]=='none' and '--read-only' in a
 if a[0]=='run':
  i=a.index('sha256:'+'a'*64);cmd=a[i+1:]
  assert cmd[0] in ['experiment','train','export-model']
  if '--resume' in cmd and os.environ.get('MOCK_FAIL_RESUME'):sys.exit(7)
 elif a[0]=='create':print('fake-container')
elif a[0]=='rm':pass
elif a[:2]==['volume','create']:print(a[2])
elif a[:2]==['volume','rm']:pass
else:raise AssertionError(a)
''')

    def run_case(name, arch='amd64', expected=None, **changes):
        case = base / name
        case.mkdir()
        log = case / 'calls.jsonl'
        env = dict(os.environ, PATH=str(mock) + os.pathsep + os.environ['PATH'],
                   MOCK_CASE=str(case), MOCK_LOG=str(log), MOCK_ROOT=str(root),
                   MOCK_CLIENT=str(client), MOCK_HOST_MACHINE='aarch64' if arch == 'arm64' else 'x86_64',
                   MOCK_IMAGE_ARCH=arch)
        env.update(changes)
        result = subprocess.run(['bash', 'scripts/test-training-image.sh', 'example/training@sha256:test',
                                 expected or arch, 'v0.0.0-test'], cwd=root, env=env,
                                text=True, capture_output=True)
        calls = [json.loads(line) for line in log.read_text().splitlines()] if log.exists() else []
        return result, calls, case

    for arch in ['amd64', 'arm64']:
        result, calls, case = run_case('success-' + arch, arch)
        assert result.returncode == 0, result.stderr
        docker = [c['args'] for c in calls if c['tool'] == 'docker']
        assert not any(a[0] in ['pull', 'build'] for a in docker)
        runs = [a for a in docker if a[0] == 'run']
        commands = [a[a.index('sha256:' + 'a' * 64) + 1:] for a in runs]
        assert [c[0] for c in commands] == ['experiment', 'train', 'train', 'export-model']
        assert '--resume' not in commands[1] and '--resume' in commands[2]
        assert any(a[:2] == ['volume', 'rm'] for a in docker)
        clients = [c['args'] for c in calls if c['tool'] == 'client']
        assert [c[1] for c in clients if c[0] == 'ai'] == ['environment', 'environment', 'evaluate', 'verify-evaluation']
        assert json.loads((case / 'stage/image.json').read_text())['architecture'] == arch
        assert (case / 'stage/client-version.txt').read_text().strip() == 'sactl v0.0.0-test'
        assert len([c for c in calls if c['tool'] == 'lifecycle']) == 1

    for name, options, message in [
        ('wrong-runner', {'expected': 'arm64'}, 'requested native architecture'),
        ('wrong-image', {'MOCK_IMAGE_ARCH': 'arm64'}, 'does not match'),
        ('wrong-label', {'MOCK_IMAGE_VERSION': 'v0.0.0-old'}, 'release label differs'),
        ('wrong-cli', {'MOCK_CLIENT_VERSION': 'v0.0.0-old'}, 'CLI version differs'),
        ('wrong-skill', {'MOCK_BAD_SKILL': '1'}, 'differ'),
        ('lifecycle-fails', {'MOCK_FAIL_LIFECYCLE': '1'}, 'evidence:'),
        ('resume-fails', {'MOCK_FAIL_RESUME': '1'}, 'evidence:'),
    ]:
        result, calls, case = run_case(name, **options)
        assert result.returncode != 0 and message in result.stdout + result.stderr, (name, result)
        docker = [c['args'] for c in calls if c['tool'] == 'docker']
        assert not any(a[:2] == ['volume', 'rm'] for a in docker), name
        if name == 'wrong-runner':
            assert calls == []
        if name in ['wrong-image', 'wrong-label']:
            assert not any(a[0] in ['create', 'run'] for a in docker)
        if name in ['wrong-cli', 'wrong-skill']:
            assert not any(a[:2] == ['volume', 'create'] for a in docker)
        if name == 'resume-fails':
            assert (case / 'stage/train.log').exists() and (case / 'stage/resume.log').exists()
            assert not any(c['tool'] == 'client' and c['args'][1:2] == ['evaluate'] for c in calls)

print('Training distribution orchestration passed for amd64/arm64 substitutes; no real image executed.')
