#!/usr/bin/env python3
"""Verify the shipped package without source files or a Docker daemon."""
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile

root = Path(__file__).resolve().parent.parent
(root / 'build').mkdir(exist_ok=True)
with tempfile.TemporaryDirectory(dir=root / 'build', prefix='deploy-test-') as temporary:
    stage = Path(temporary)
    uploader = stage / 'uploader'
    uploader.write_text("#!/usr/bin/env bash\necho \"$*\" >> \"$MOCK_UPLOAD_LOG\"\n")
    uploader.chmod(0o755)
    subprocess.run(['python3', str(root / 'scripts/package-deployment.py'), 'v0.1.14', '--output', str(stage), '--uploader', str(uploader)], check=True)
    with tarfile.open(stage / 'stoneage-deploy-v0.1.14.tar.gz') as archive:
        names = archive.getnames()
        expected = {
            '.env.compose.example',
            'docker-compose.yml',
            'README.md',
            'VERSION',
            'bin/stoneage',
            'bin/stoneage-assets-sync',
            'bin/deploy.sh',
            'bin/sync-assets.sh',
            'bin/registry-login.sh',
            'bin/clean-images.sh',
            'bin/aliyun-certificate.py',
            'config/certificates/aliyun.json.example',
            'config/certificates/README.md',
            'config/certificates/stoneage-cdn-certificate.service',
            'config/certificates/stoneage-cdn-certificate.timer',
            'config/web/web.toml',
            'config/web/web.r2.toml.example',
            'config/gateway/gateway.toml',
            'config/gmsv/setup.cf.example',
            'config/saac/acserv.cf.example',
            'config/registry/username.example',
        }
        assert set(names) == expected, names
        assert not any(
            n == 'Dockerfile'
            or n.endswith('/Dockerfile')
            or n.startswith(('client/', 'deploy/', 'runtime/'))
            or n.endswith(('.go', '.js', '.c', '.h'))
            for n in names
        )
        assert not any(n == 'config/registry/token' or n.endswith('/.env') for n in names)

        def member_text(name):
            return archive.extractfile(name).read().decode()

        env_example = member_text('.env.compose.example')
        assert 'STONEAGE_CLIENT_DATA_ROOT=./assets/client' in env_example
        assert 'STONEAGE_SPRITES_ROOT=./assets/sprites' in env_example
        assert 'STONEAGE_GATEWAY_CONFIG_FILE=./config/gateway/gateway.toml' in env_example
        assert 'STONEAGE_WEB_CONFIG_FILE=./config/web/web.toml' in env_example
        assert 'ALIBABA_CLOUD_ACCESS_KEY_ID=\n' in env_example
        assert 'ALIBABA_CLOUD_ACCESS_KEY_SECRET=\n' in env_example
        assert 'STONEAGE_ADMIN_PASSWORD=replace-with-a-long-random-password' in env_example
        assert 'STONEAGE_ADMIN_PASSWORD=admin' not in env_example
        compose = member_text('docker-compose.yml')
        assert '${STONEAGE_CLIENT_DATA_ROOT:-./assets/client}:/game/client:ro' in compose
        assert '${STONEAGE_SPRITES_ROOT:-./assets/sprites}:/opt/stoneage/web-assets:ro' in compose
        assert compose.count(':/opt/stoneage/web-assets:ro') == 3
        assert '${STONEAGE_WEB_CONFIG_FILE:-./config/web/web.toml}:/etc/stoneage/web.toml:ro' in compose
        assert '${STONEAGE_GATEWAY_CONFIG_FILE:-./config/gateway/gateway.toml}:/etc/stoneage/gateway.toml:ro' in compose
        assert '  assets-sync:\n    platform: linux/amd64\n' in compose
        archive.extractall(stage / 'host', filter='data')
    host = stage / 'host'
    entry = host / 'bin/stoneage'
    clean_help = subprocess.run([str(entry), 'clean', '--help'], check=True, capture_output=True, text=True)
    assert 'clean [--dry-run]' in clean_help.stdout
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
    env = dict(os.environ, STONEAGE_DOCKER_BIN=str(mock), MOCK_LOG=str(stage / 'calls'), MOCK_UPLOAD_LOG=str(stage / 'uploads'))
    missing_check = subprocess.run([str(entry), 'check'], env=env, capture_output=True, text=True)
    assert missing_check.returncode != 0
    assert 'manifest.json' in missing_check.stderr
    missing_sync = subprocess.run(
        [str(entry), 'sync-assets', '--dry-run'], env=env, capture_output=True, text=True,
    )
    assert missing_sync.returncode != 0
    assert 'manifest.json' in missing_sync.stderr
    (host / 'assets/sprites/manifest.json').write_text('{}\n')
    (host / 'assets/sprites/sprites.json').write_text('{}\n')
    subprocess.run([str(entry), 'check'], env=env, check=True, capture_output=True)
    subprocess.run([str(entry), 'deploy'], env=env, check=True, capture_output=True)
    subprocess.run([str(entry), 'sync-assets', '--dry-run'], env=env, check=True, capture_output=True)
    calls = (stage / 'calls').read_text()
    assert ' pull\n' in calls
    assert 'up -d --no-build --remove-orphans' in calls
    assert 'assets-sync' not in calls
    assert '-dry-run' in (stage / 'uploads').read_text()

    dockerfile = (root / 'deploy/linux/Dockerfile').read_text()
    assert not any(
        line.lstrip().startswith('COPY') and 'client/web/assets/original' in line
        for line in dockerfile.splitlines()
    )
    assert 'client/web/assets/original' in (root / '.dockerignore').read_text()

with tempfile.TemporaryDirectory(dir=root / 'build', prefix='sprites-test-') as temporary:
    missing_root = Path(temporary)
    (missing_root / 'scripts').mkdir()
    (missing_root / 'client/web/assets/original').mkdir(parents=True)
    shutil.copy2(root / 'scripts/package-sprites.py', missing_root / 'scripts/package-sprites.py')
    result = subprocess.run(
        ['python3', str(missing_root / 'scripts/package-sprites.py'), 'v0.1.13',
         '--output', str(missing_root / 'dist')],
        capture_output=True,
        text=True,
    )
    assert result.returncode != 0
    assert 'sprite resource tree is missing' in result.stderr
print('Deployment package checks passed')
