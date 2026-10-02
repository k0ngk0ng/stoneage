#!/usr/bin/env python3
"""Exercise offline archive durability through the real native transport.

Use a separately linked binary with BATTLE_LOG_QUEUE_SIZE=2 to force queue
pressure. The caller supplies an unused output directory; retain all evidence.
"""
import json
import os
from pathlib import Path
import subprocess
import sys
from concurrent.futures import ThreadPoolExecutor

binary, config, output = sys.argv[1:]
root = Path(output)
root.mkdir(parents=True, exist_ok=False)
env = dict(os.environ, STONEAGE_BATTLE_RECORD_DIR=str(root/'records'),
           STONEAGE_BATTLE_RECORD_MIN_FREE_BYTES='0')
commands = []
games = 16
turns = 6
for seed in range(1, games+1):
    commands.append(f'reset-pets {seed} 35 {turns} 5 ' + '30 30 30 30 '*20)
    for turn in range(turns):
        commands.append(f'step {turn} ' + 'G W|FF|FF '*10)
commands.append('close')

def run(name, requests, environment):
    result = subprocess.run([binary, '--battle-environment', '--config', config],
                            input='\n'.join(requests)+'\n', text=True,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                            errors='replace', env=environment, timeout=60)
    (root/(name+'.stdout')).write_text(result.stdout)
    (root/(name+'.stderr')).write_text(result.stderr)
    (root/(name+'.exit.json')).write_text(json.dumps({'exit': result.returncode}))
    return result

result = run('pressure', commands, env)
assert result.returncode == 0, (result.returncode, result.stdout[-500:])
frames = [json.loads(line) for line in result.stdout.splitlines()]
assert len(frames) == 1+games*(turns+1)
assert all(frame.get('ok') for frame in frames)
def verify_records(path, count):
    matches = sorted(path.glob('*/*/metadata.json'))
    assert len(matches) == count, len(matches)
    events = 0
    for metadata in matches:
        directory = metadata.parent
        m = json.loads(metadata.read_text())
        r = json.loads((directory/'result.json').read_text())
        assert r['storage_complete'] and r['dropped_events'] == 0, r
        assert r['end_reason'] == 'turn_limit', r
        sequence = m['seq']
        for line in (directory/'events.jsonl').read_text().splitlines():
            e = json.loads(line)
            assert e['seq'] == sequence+1, (directory, sequence, e['seq'])
            sequence = e['seq']
            events += 1
        assert r['seq'] == sequence+1, r
    return events

events = verify_records(root/'records', games)

# Parallel trainers intentionally share one persistent archive volume. They
# must publish their status via separate temporary files, with no rename races.
parallel_env = dict(env, STONEAGE_BATTLE_RECORD_DIR=str(root/'parallel'))
with ThreadPoolExecutor(max_workers=2) as workers:
    futures = [workers.submit(run, f'parallel-{i}', commands, parallel_env) for i in range(2)]
    for future in futures:
        r = future.result()
        assert r.returncode == 0, (r.returncode, r.stderr[-1000:])
parallel_events = verify_records(root/'parallel', games*2)

# A game may finish even if its optional archive cannot be written. This must
# be a nonzero process exit once the caller explicitly requested recording.
bad_parent = root/'not-a-directory'
bad_parent.write_text('fixture')
bad_env = dict(env, STONEAGE_BATTLE_RECORD_DIR=str(bad_parent/'records'))
bad = run('storage-failure', commands[:turns+1]+['close'], bad_env)
assert bad.returncode != 0, 'recording storage failure was silently accepted'
assert 'Battle recorder: storage error' in bad.stderr

without = dict(env)
without.pop('STONEAGE_BATTLE_RECORD_DIR')
no_archive = run('without-archive', commands[:turns+1]+['close'], without)
assert no_archive.returncode == 0, no_archive.stderr[-500:]
receipt = {'matches': games, 'events': events, 'all_sequences_contiguous': True,
           'parallel_matches': games*2, 'parallel_events': parallel_events,
           'storage_failure_rejected': True, 'optional_archive_supported': True}
(root/'passed.json').write_text(json.dumps(receipt, indent=2)+'\n')
print(json.dumps(receipt))
