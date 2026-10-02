#!/usr/bin/env python3
"""Check an actual installed CLI against owned, isolated Docker worker fixtures.

Uses an already installed image only. No login, provider, image build/pull,
volume deletion or unrelated-container cleanup. Keep output evidence on failure.
"""
from pathlib import Path
import json
import os
import signal
import subprocess
import sys
import time
import uuid

cli, image, output = sys.argv[1:]
root = Path(output).resolve()
root.mkdir(parents=True, exist_ok=False)
cli = str(Path(cli).resolve())

def docker(*args):
    return subprocess.check_output(['docker', *args], text=True, timeout=15).strip()

platform = docker('image', 'inspect', '--format', '{{.Os}}/{{.Architecture}}', image)
assert platform in ('linux/arm64', 'linux/amd64'), platform
image = docker('image', 'inspect', '--format', '{{.Id}}', image)
assert image.startswith('sha256:') and len(image) == 71
base = ['run', '--rm', '--pull', 'never', '--platform', platform, '--network', 'none', '--read-only']
control = docker(*base, '-d', '--entrypoint', '/bin/sh', image, '-c', 'exec sleep 300')
receipt = []
try:
    for mode in ('graceful', 'timeout', 'cancel', 'startup-cancel'):
        label = 'stoneage.test.cli-lifecycle=' + uuid.uuid4().hex
        case = root/mode
        case.mkdir()
        script = 'printf "%s\\n" "$1"; exec sleep 300'
        if mode == 'graceful':
            script = 'printf "%s\\n" "$1"; exec cat >/dev/null'
        elif mode == 'startup-cancel':
            script = 'exec sleep 300'
        greeting = json.dumps(dict(schema_version=1, ok=True, ready=True,
                                   scenario='controlled-battle-v8', platform=platform.replace('/', '-'),
                                   rules_digest='a'*64))
        command = ['docker', *base, '-i', '--label', label, '--entrypoint', '/bin/sh', image, '-c', script, 'fixture', greeting]
        environment = case/'environment.json'
        environment.write_text(json.dumps(dict(schema_version=1, command=command), indent=2)+'\n')
        def owned():
            return docker('ps', '-aq', '--filter', 'label='+label).split()
        process = None
        try:
            with (case/'stdout.log').open('x') as out, (case/'stderr.log').open('x') as err:
                process = subprocess.Popen([cli, 'ai', 'environment', 'check', '--environment', str(environment)], stdout=out, stderr=err)
                if mode in ('cancel', 'startup-cancel'):
                    deadline = time.monotonic()+15
                    while not owned() and process.poll() is None and time.monotonic() < deadline:
                        time.sleep(.05)
                    assert len(owned()) == 1, 'worker did not start'
                    process.send_signal(signal.SIGINT)
                code = process.wait(timeout=25)
            remaining = owned()
            data = dict(mode=mode, exit=code, remaining_containers=remaining,
                        control_running=docker('inspect', '--format', '{{.State.Running}}', control)=='true')
            (case/'result.json').write_text(json.dumps(data, indent=2)+'\n')
            receipt.append(data)
            assert (code == 0) == (mode == 'graceful'), data
            assert not remaining and data['control_running'], data
        finally:
            if process is not None and process.poll() is None:
                process.kill()
                process.wait(timeout=10)
            # Only this fixture's random label is eligible for test cleanup.
            for identifier in owned():
                docker('rm', '-f', identifier)
finally:
    docker('rm', '-f', control)
(root/'passed.json').write_text(json.dumps(dict(image=image, platform=platform, checks=receipt), indent=2)+'\n')
print('Installed CLI worker lifecycle passed: graceful, timeout, SIGINT and startup cancellation; unrelated container preserved.')
