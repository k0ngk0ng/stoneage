#!/usr/bin/env python3
"""Verify the shipped package without source files or a Docker daemon."""
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile

root = Path(__file__).resolve().parent.parent
(root / 'build').mkdir(exist_ok=True)
with tempfile.TemporaryDirectory(dir=root / 'build', prefix='deploy-test-') as temporary:
    stage = Path(temporary)
    subprocess.run(['python3', str(root / 'scripts/package-deployment.py'), 'v0.1.12', '--output', str(stage)], check=True)
    with tarfile.open(stage / 'stoneage-deploy-v0.1.12.tar.gz') as archive:
        names = archive.getnames()
        assert len(names) == 14, names
        assert all(n.split('/')[0] in {'.env.compose.example', 'docker-compose.yml', 'README.md', 'VERSION', 'bin', 'config'} for n in names)
        assert not any(n.endswith(('.go', '.js')) or n.endswith('/token') for n in names)
        archive.extractall(stage / 'host', filter='data')
    host = stage / 'host'
    entry = host / 'bin/stoneage'
    subprocess.run([str(entry), 'init'], check=True, capture_output=True)
    assert (host / '.env').stat().st_mode & 0o777 == 0o600
    for name in ('config/gmsv/setup.cf', 'config/saac/acserv.cf'):
        assert (host / name).stat().st_mode & 0o777 == 0o600
    before = (host / '.env').read_bytes()
    assert subprocess.run([str(entry), 'init'], capture_output=True).returncode != 0
    assert (host / '.env').read_bytes() == before
    assert subprocess.run([str(entry), 'deploy', '--build'], capture_output=True).returncode != 0
    assert '\n    build:' not in (host / 'docker-compose.yml').read_text()
    mock = stage / 'docker'
    mock.write_text('''#!/usr/bin/env bash
printf '%s\\n' "$*" >> "$MOCK_LOG"
for arg in "$@"; do [[ "$arg" != build ]] || exit 99; done
case "$*" in
  *"ps -q "*) echo test-container ;;
  inspect*) echo 'running healthy' ;;
esac
''')
    mock.chmod(0o755)
    env = dict(os.environ, STONEAGE_DOCKER_BIN=str(mock), MOCK_LOG=str(stage / 'calls'))
    subprocess.run([str(entry), 'check'], env=env, check=True, capture_output=True)
    subprocess.run([str(entry), 'deploy'], env=env, check=True, capture_output=True)
    subprocess.run([str(entry), 'sync-assets', '--dry-run'], env=env, check=True, capture_output=True)
    calls = (stage / 'calls').read_text()
    assert ' pull\n' in calls
    assert 'up -d --no-build --remove-orphans' in calls
    assert 'assets-sync' in calls
print('Deployment package checks passed')
