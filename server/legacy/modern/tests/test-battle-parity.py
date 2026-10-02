#!/usr/bin/env python3
"""Same native characters/seeds/orders through training and actual arena hooks."""
import json
import itertools
import os
import re
from pathlib import Path
import subprocess
import sys

binary, config, output = sys.argv[1:4]
switch_only = sys.argv[4:] == ["switch"]
recipient_only = sys.argv[4:] == ["recipient"]
assert not sys.argv[4:] or switch_only or recipient_only
output = Path(output)
checked = []
if not switch_only:
    recipient = output / "recipient"
    recipient.mkdir()
    with (recipient / "stdout.log").open("wb") as stdout, (recipient / "stderr.log").open("wb") as stderr:
        result = subprocess.run([binary, config, "recipient", "1", "1", "42", "0", "0"],
                                stdout=stdout, stderr=stderr, timeout=40,
                                env=dict(os.environ, STONEAGE_BATTLE_RECORD_DIR=str(recipient / "records"),
                                         STONEAGE_LADDER_DB=str(recipient / "arena.sqlite3"), STONEAGE_BATTLE_RECORD_MIN_FREE_BYTES="0"))
    assert result.returncode == 0, f"native recipient probe failed: {recipient}"
    recipient_cases = [json.loads(line[10:]) for line in (recipient / "stdout.log").read_bytes().splitlines()
                       if line.startswith(b"RECIPIENT|")]
    assert [case["case"] for case in recipient_cases] == ["ordinary", "guardian", "reflection", "guardian_reflection", "absorb", "vanish", "counter_reflection"]
    (output / "recipient-passed.json").write_text(json.dumps({"schema_version": 1, "cases": recipient_cases}, indent=2))
    if recipient_only:
        print(f"Native damage recipient, guardian, reflection, absorption, vanish and counter passed; {output}")
        sys.exit(0)
switch = output / "pet-switch"
switch.mkdir()
with (switch / "stdout.log").open("wb") as stdout, (switch / "stderr.log").open("wb") as stderr:
    result = subprocess.run([binary, config, "switch", "1", "1", "42", "0", "0"],
                            stdout=stdout, stderr=stderr, timeout=40,
                            env=dict(os.environ, STONEAGE_BATTLE_RECORD_DIR=str(switch / "records"),
                                     STONEAGE_LADDER_DB=str(switch / "arena.sqlite3"), STONEAGE_BATTLE_RECORD_MIN_FREE_BYTES="0"))
assert result.returncode == 0, f"native switch probe failed: {switch}"
lines = (switch / "stdout.log").read_bytes().splitlines()
switch_results = [json.loads(line[7:]) for line in lines if line.startswith(b"SWITCH|")]
switch_frames = [json.loads(line[12:]) for line in lines if line.startswith(b"SWITCHFRAME|")]
assert len(switch_results) == 1 and len(switch_frames) == 4
assert [f["turn"] for f in switch_frames] == [0, 1, 2, 3]
(output / "switch-passed.json").write_text(json.dumps({"schema_version":1,"result":switch_results[0],"frames":4},indent=2))
if switch_only:
    print(f"Native pet switch, outgoing command, recall, resummon and no MP cost passed; {output}")
    sys.exit(0)

capacity = output / "pet-capacity"
capacity.mkdir()
with (capacity / "stdout.log").open("wb") as stdout, (capacity / "stderr.log").open("wb") as stderr:
    result = subprocess.run([binary, config, "capacity", "1", "1", "42", "0", "0"],
                            stdout=stdout, stderr=stderr, timeout=40,
                            env=dict(os.environ, STONEAGE_BATTLE_RECORD_DIR=str(capacity / "records"),
                                     STONEAGE_LADDER_DB=str(capacity / "arena.sqlite3"), STONEAGE_BATTLE_RECORD_MIN_FREE_BYTES="0"))
assert result.returncode == 0, f"native capacity gate failed: {capacity}"
capacity_cases = [json.loads(line[9:]) for line in (capacity / "stdout.log").read_bytes().splitlines()
                  if line.startswith(b"CAPACITY|")]
assert len(capacity_cases) == 6
scenarios = [(*s, 0) for s in itertools.product(range(1, 6), (0, 1), (1, 42, 7), (0, 10, 20))]
scenarios += [(*s, 2) for s in itertools.product(range(1, 6), (0, 1), (1,), (0, 20))]
scenarios = [(*s, 0) for s in scenarios]
scenarios += [(mode, 1, 42, 20, 2, reserves) for mode in range(1, 6) for reserves in (1, 2)]
for mode, pets, seed, healing, items, reserves in scenarios:
    traces = []
    pet_hits = []
    status_movies = []
    healing_effects = []
    consumed_items = []
    for route in ("offline", "arena"):
        name = f"{mode}v{mode}-pets{pets}-seed{seed}-heal{healing}-items{items}" + (f"-reserves{reserves}" if reserves else "") + f"-{route}"
        folder = output / name
        folder.mkdir()
        env = dict(os.environ, STONEAGE_LADDER_DB=str(folder / "arena.sqlite3"),
                   STONEAGE_BATTLE_RECORD_DIR=str(folder / "records"),
                   STONEAGE_BATTLE_RECORD_MIN_FREE_BYTES="0")
        with (folder / "stdout.log").open("wb") as stdout, (folder / "stderr.log").open("wb") as stderr:
            result = subprocess.run([binary, config, route, str(mode), str(pets), str(seed), str(healing), str(items), str(reserves)],
                                    env=env, stdout=stdout, stderr=stderr, timeout=40)
        assert result.returncode == 0, f"{name}: native failure; inspect {folder}"
        lines = (folder / "stdout.log").read_bytes().splitlines()
        frames = [json.loads(line[7:]) for line in lines if line.startswith(b"PARITY|")]
        assert frames and frames[-1]["ended"], f"{name}: no complete battle"
        # Equality alone could compare two broken adapters that both
        # silently skip every pet command. Require actual public hits.
        public = [bytes.fromhex(packet) for frame in frames
                  for packet in frame["packets"][0]]
        hits = sum(int(actor, 16) % 10 >= 5 for packet in public
                   for actor in re.findall(rb"BH\|a([0-9A-Fa-f]+)\|", packet))
        if pets:
            assert hits > 0, f"{name}: pet instructions never produced a public hit"
        if reserves:
            assert sum(b"BS|" in packet for packet in public) >= reserves + 1, f"{name}: no public multi-pet switching"
        pet_hits.append(hits)
        status_movies.append(sum(b"BM|" in packet for packet in public))
        healing_effects.append(sum(len(re.findall(rb"BD\|r[0-9A-Fa-f]+\|0\|1\|d[0-9A-Fa-f]+\|", packet)) for packet in public))
        initial_items = sum(row.count(1234) for row in frames[0]["inventory"])
        final_items = sum(row.count(1234) for row in frames[-1]["inventory"])
        assert initial_items == mode * 2 * items
        consumed_items.append(initial_items - final_items)
        if items:
            assert consumed_items[-1] > 0 and healing_effects[-1] > 0, f"{name}: items never consumed/healed"
        for frame in frames[1:]:
            # In these attack/guard/break/status scenes each decision produces
            # one public movie, including for already removed members.
            # Newly knocked-out actors must not get a duplicate copy.
            movies = [[p for p in map(bytes.fromhex, packets)
                       if not p.startswith((b"BA|", b"BP|", b"BC|", b"BVS|"))]
                      for packets in frame["packets"]]
            assert len(movies[0]) == 1 and all(m == movies[0] for m in movies), (
                f"{name}: missing/duplicate spectator movie at turn {frame['turn']}")
        traces.append(frames)
    for turn, (left, right) in enumerate(zip(*traces)):
        if left != right:
            difference = {"mode": mode, "pets": pets, "seed": seed, "healing": healing, "turn": turn,
                          "offline": left, "arena": right}
            (output / "difference.json").write_text(json.dumps(difference, indent=2))
            raise AssertionError(f"parity differs at {mode}v{mode}, pets={pets}, seed={seed}, turn={turn}; {output}/difference.json")
    assert len(traces[0]) == len(traces[1]), f"different terminal turns: {mode}/{pets}/{seed}"
    assert pet_hits[0] == pet_hits[1] and status_movies[0] == status_movies[1]
    assert healing_effects[0] == healing_effects[1]
    assert consumed_items[0] == consumed_items[1]
    checked.append({"mode": mode, "pets": pets, "seed": seed, "healing": healing,
                    "pet_hits": pet_hits[0], "status_movies": status_movies[0],
                    "healing_effects": healing_effects[0], "items": items, "consumed_items": consumed_items[0], "reserves": reserves,
                    "turns": len(traces[0]) - 1, "winner": traces[0][-1]["winner"]})
for mode in range(1, 6):
    assert any(c["mode"] == mode and c["status_movies"] > 0 for c in checked), (
        f"{mode}v{mode}: no native status movie exercised")
    for healing in (10, 20):
        assert any(c["mode"] == mode and c["healing"] == healing and c["healing_effects"] > 0 for c in checked), (
            f"{mode}v{mode}: magic {healing} produced no public healing effects")
(output / "passed.json").write_text(json.dumps({
    "schema_version": 1,
    "scenario_version": "controlled-battle-v8",
    "rng_scope": "test-only transport stream isolation; native combat draws unchanged",
    "comparison": "combat slots/stats, inventory consumption, raw B packets, rounds, outcome, combat RNG calls",
    "client_projection": "requires separate Go TestNativeArenaProjectionParity",
    "pet_capacity_cases": capacity_cases,
    "scenarios": checked,
}, indent=2))
print(f"Native training/arena parity: {len(checked)} scenarios; raw B packets, combat state, rounds, terminal outcomes and arena restoration passed; {output}")
