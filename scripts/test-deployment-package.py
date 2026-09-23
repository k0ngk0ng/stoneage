#!/usr/bin/env python3
"""Verify the shipped package without source files or a Docker daemon."""
import os
import fnmatch
import re
import shlex
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile

root = Path(__file__).resolve().parent.parent
# Every embedded Web runtime module must exist in the image build context.
# This catches missing COPY entries without downloading/building an image.
dockerfile = (root / 'deploy/linux/Dockerfile').read_text()
web_sources = []
for line in dockerfile.splitlines():
    if line.startswith('COPY ') and './client/web/' in line:
        web_sources.extend(shlex.split(line)[1:-1])
for source in (root / 'client/web').rglob('*.go'):
    for directive in re.findall(r'^//go:embed (.+)$', source.read_text(), re.M):
        for pattern in shlex.split(directive):
            for resource in source.parent.glob(pattern):
                relative = resource.relative_to(root).as_posix()
                assert any((relative.startswith(item) if item.endswith("/") else fnmatch.fnmatchcase(relative, item)) for item in web_sources), f'image COPY missing embedded resource: {relative}'
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
            'docker-compose.ai.yml',
            'README.md',
            'VERSION',
            'bin/stoneage',
            'bin/stoneage-assets-sync',
            'bin/deploy.sh',
            'bin/sync-assets.sh',
            'bin/registry-login.sh',
            'bin/clean-images.sh',
            'bin/aliyun-certificate.py',
            'bin/battle-dataset.sh',
            'bin/battle-export.py',
            'docs/battle-training.md',
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
        assert 'STONEAGE_AI_CONTAINER_MODE=false\n' in env_example
        assert 'STONEAGE_AI_RUNTIME_IMAGE=ghcr.io/k0ngk0ng/stoneage/ai-runtime:v0.1.14\n' in env_example
        assert 'STONEAGE_ADMIN_PASSWORD=replace-with-a-long-random-password' in env_example
        assert 'STONEAGE_ADMIN_PASSWORD=admin' not in env_example
        compose = member_text('docker-compose.yml')
        ai_compose = member_text('docker-compose.ai.yml')
        base_admin = compose.split('  admin:\n', 1)[1]
        assert '/var/run/docker.sock' not in base_admin
        assert '${STONEAGE_CLIENT_DATA_ROOT:-./assets/client}:/game/client:ro' in compose
        assert '${STONEAGE_SPRITES_ROOT:-./assets/sprites}:/opt/stoneage/web-assets:ro' in compose
        assert compose.count(':/opt/stoneage/web-assets:ro') == 4
        assert compose.count('- player-admin:/run/stoneage/player-admin') == 2
        assert '- player-admin:/run/stoneage-player-admin' in compose
        assert '      - -player-admin-root\n      - /run/stoneage-player-admin' in compose
        assert '      - operator-run:/run/stoneage:ro' in compose
        assert 'STONEAGE_PLAYER_ADMIN_DIR: /run/stoneage/player-admin/saac' in compose
        assert 'STONEAGE_PLAYER_ADMIN_DIR: /run/stoneage/player-admin/gmsv' in compose
        assert '${STONEAGE_WEB_CONFIG_FILE:-./config/web/web.toml}:/etc/stoneage/web.toml:ro' in compose
        assert '${STONEAGE_GATEWAY_CONFIG_FILE:-./config/gateway/gateway.toml}:/etc/stoneage/gateway.toml:ro' in compose
        assert '  assets-sync:\n    platform: linux/amd64\n' in compose
        assert 'profiles: ["ai-container"]' in ai_compose
        assert 'STONEAGE_AI_RUNTIME_IMAGE:-ghcr.io/k0ngk0ng/stoneage/ai-runtime:' in ai_compose
        assert '-v0.1.14}}' in ai_compose
        assert 'STONEAGE_AI_WEB_BASE_URL: ${STONEAGE_AI_WEB_BASE_URL:-http://web:8088}' in ai_compose
        assert 'STONEAGE_AI_WEB_PUBLIC_URL: ${STONEAGE_AI_WEB_PUBLIC_URL:-}' in ai_compose
        assert 'STONEAGE_AI_WEB_SERVER_ID: ${STONEAGE_AI_WEB_SERVER_ID:-line-1}' in ai_compose
        assert 'STONEAGE_AI_WEB_AGENT_SOCKET: /run/stoneage-web/agent.sock' in ai_compose
        assert 'STONEAGE_AI_CONTAINER_GATEWAY_URL: http://web:8088/v1/game' in ai_compose
        assert 'STONEAGE_WEB_AGENT_SOCKET: /run/stoneage-web/agent.sock' in ai_compose
        assert 'STONEAGE_WEB_AGENT_GAME_UPSTREAM: http://admin:8081/v1/game' in ai_compose
        assert 'STONEAGE_WEB_AGENT_WORKER_UPSTREAM: http://admin:8080' in ai_compose
        assert 'STONEAGE_AI_BROKER_DB: /var/lib/stoneage-ai/runtime/broker/broker.db' in ai_compose
        assert 'STONEAGE_AI_GATEWAY_LISTEN: 0.0.0.0:8081' in ai_compose
        assert 'STONEAGE_AI_KNOWLEDGE_DATA_DIR: /game/gmsv/data' in ai_compose
        assert 'STONEAGE_AI_MAP_DATA_DIR: /game/gmsv/data' in ai_compose
        assert '${STONEAGE_GMSV_DATA_ROOT:-./data/gmsv}/data:/game/gmsv/data:ro' in ai_compose
        assert '      - /var/run/docker.sock:/var/run/docker.sock' in ai_compose
        assert 'ai-funding:' not in ai_compose
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
    assert calls.index(' pull\n') < calls.index('up -d --no-build --remove-orphans')
    assert not any(' stop' in line or ' down' in line for line in calls.splitlines()), 'image-only deployment must not pre-stop services'
    (stage / 'calls').write_text('')
    subprocess.run([str(entry), 'deploy', '--no-image-update'], env=env, check=True, capture_output=True)
    cached_calls = (stage / 'calls').read_text()
    assert ' pull\n' not in cached_calls
    assert 'up -d --no-build --remove-orphans' in cached_calls
    assert 'assets-sync' not in calls
    assert '-dry-run' in (stage / 'uploads').read_text()

    # Container AI is an explicit overlay. It requires the existing GMSV data
    # tree, grants the socket only to admin, and pre-pulls the profile-only AI
    # image target without starting that target as a long-lived service.
    ai_data = host / 'data/gmsv/data'
    ai_data.mkdir(parents=True)
    env_file = host / '.env'
    env_file.write_text(env_file.read_text().replace('STONEAGE_AI_CONTAINER_MODE=false', 'STONEAGE_AI_CONTAINER_MODE=true'))
    empty_ai_check = subprocess.run([str(entry), 'check'], env=env, capture_output=True, text=True)
    assert empty_ai_check.returncode != 0
    assert 'exp.txt' in empty_ai_check.stderr

    # The AI runtime's knowledge and navigation loaders need the complete
    # server-owned data set.  A nearly complete tree must fail closed too.
    ai_required_files = (
        'exp.txt',
        'encount.txt',
        'enemy.txt',
        'enemybase.txt',
        'group.txt',
        'map/mapwarp.txt',
        'map/mapset.txt',
    )
    for name in ai_required_files[:-1]:
        path = ai_data / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text('fixture\n')
    missing_ai_file_check = subprocess.run([str(entry), 'check'], env=env, capture_output=True, text=True)
    assert missing_ai_file_check.returncode != 0
    assert 'map/mapset.txt' in missing_ai_file_check.stderr

    (ai_data / ai_required_files[-1]).write_text('fixture\n')
    ai_check = subprocess.run([str(entry), 'check'], env=env, check=True, capture_output=True, text=True)
    assert 'AI container image:' in ai_check.stdout
    assert 'AI container network: stoneage-backend' in ai_check.stdout
    (stage / 'calls').write_text('')
    subprocess.run([str(entry), 'pull'], env=env, check=True, capture_output=True)
    ai_calls = (stage / 'calls').read_text()
    assert '-f ' + str(host / 'docker-compose.ai.yml') in ai_calls
    assert '--profile ai-container pull ai-runtime-image' in ai_calls

    dockerfile = (root / 'deploy/linux/Dockerfile').read_text()
    assert 'FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS go-source' in dockerfile
    assert 'ARG TARGETARCH=amd64' not in dockerfile
    assert 'ARG TARGETOS' in dockerfile and 'ARG TARGETARCH' in dockerfile
    assert 'GOOS="$TARGETOS" GOARCH="$TARGETARCH"' in dockerfile
    assert 'FROM go-source AS build-control' in dockerfile
    assert 'FROM go-source AS build-ai' in dockerfile
    assert 'COPY --from=build-control' in dockerfile
    assert 'COPY --from=build-ai' in dockerfile
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
