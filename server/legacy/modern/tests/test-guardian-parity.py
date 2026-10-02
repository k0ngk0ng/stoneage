#!/usr/bin/env python3
"""Guardian loadouts through the real offline and arena entry/command paths."""
import json
import os
from pathlib import Path
import re
import subprocess
import sys

binary, config, output = sys.argv[1:]
output = Path(output)

def require_native_success(result, folder):
    if result.returncode:
        # The image build discards its filesystem on failure; surface the
        # native assertion here rather than only naming an inaccessible log.
        detail = (folder / "stderr.log").read_bytes()[-8000:].decode("utf-8", errors="replace")
        raise AssertionError(f"native probe failed ({result.returncode}): {folder}\n{detail}")

cases = []
for mode in (1, 2, 5):
    for seed in (1, 42):
        traces = []
        guardian_hits = []
        name = f"guardian-{mode}v{mode}-seed{seed}"
        for route in ("offline", "arena"):
            folder = output / f"{name}-{route}"
            folder.mkdir()
            env = dict(os.environ, STONEAGE_LADDER_DB=str(folder / "arena.sqlite3"),
                       STONEAGE_BATTLE_RECORD_DIR=str(folder / "records"),
                       STONEAGE_BATTLE_RECORD_MIN_FREE_BYTES="0")
            with (folder / "stdout.log").open("wb") as stdout, (folder / "stderr.log").open("wb") as stderr:
                result = subprocess.run([binary, config, route, str(mode), "1", str(seed), "0", "0", "0", "131"],
                                        env=env, stdout=stdout, stderr=stderr, timeout=40)
            require_native_success(result, folder)
            lines = (folder / "stdout.log").read_bytes().splitlines()
            frames = [json.loads(line[7:]) for line in lines if line.startswith(b"PARITY|")]
            assert frames and frames[-1]["ended"], f"incomplete guardian battle: {folder}"
            packets = [bytes.fromhex(packet) for frame in frames for packet in frame["packets"][0]]
            hits = sum(bool(int(flags, 16) & 512) for packet in packets for flags in re.findall(rb"\|f([0-9A-Fa-f]+)\|", packet))
            guardian_hits.append(hits)
            traces.append(frames)
        assert traces[0] == traces[1], f"offline/arena guardian discrepancy: {name}"
        assert guardian_hits[0] > 0 and guardian_hits[0] == guardian_hits[1], f"no native guardian effect: {name}"
        cases.append({"name": name, "mode": mode, "seed": seed, "turns": len(traces[0])-1, "guardian_hits": guardian_hits[0]})
report = {"schema_version": 1, "scenario_version": "controlled-battle-v8", "cases": cases}
(output / "guardian-parity-passed.json").write_text(json.dumps(report, indent=2))
print(f"Six complete guardian battles: native offline/arena effects and settlement agree; {output}")
