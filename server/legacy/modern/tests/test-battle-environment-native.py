#!/usr/bin/env python3
"""Gate the built C environment transport; no account server or network."""
import json
import os
from pathlib import Path
import subprocess
import sys

binary, config = sys.argv[1:]
commands = []
expected = []
for mode in range(1, 6):
    for pets in (False, True):
        operation = "reset-pets" if pets else "reset"
        builds = "30 30 30 30 " * (mode * 2 * (2 if pets else 1))
        commands.append(f"{operation} 42 35 1 {mode} {builds}".strip())
        expected.append((mode, pets, 0))
        actions = []
        for side in range(2):
            for seat in range(mode):
                actions.append("G")
                if pets:
                    actions.append(f"W|1|{side * 10 + seat + 5:X}")
        commands.append("step 0 " + " ".join(actions))
        expected.append((mode, pets, 1))
commands.append("close")
result = subprocess.run([binary, "--battle-environment", "--config", config],
                        input="\n".join(commands) + "\n", text=True,
                        stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                        errors="replace", timeout=30)
if result.returncode:
    raise SystemExit(f"native environment failed: exit={result.returncode}\n{result.stdout}")
frames = [json.loads(line) for line in result.stdout.splitlines()]
assert frames[0]["ready"] and frames[0]["scenario"] == "controlled-battle-v8"
assert frames[0]["platform"] in ("linux-amd64", "linux-arm64")
assert frames[0]["rules_digest"] == os.environ.get("STONEAGE_BATTLE_RULESET_ID", "")
assert len(frames) == len(expected) + 1
for frame, (mode, pets, turn) in zip(frames[1:], expected):
    assert frame["ok"] and frame["schema_version"] == 1
    assert frame["mode"] == mode and frame["turn"] == turn
    assert not frame["terminated"] and frame["truncated"] == (turn == 1)
    assert frame["winner_side"] == -1 and len(frame["members"]) == mode * 2
    for i, member in enumerate(frame["members"]):
        assert member["side"] == i // mode and member["seat"] == i % mode
        packets = [(p["function"], bytes.fromhex(p["hex"])) for p in member["packets"]]
        assert any(f == "B" and v.startswith(b"BP|") for f, v in packets)
        assert any(f == "B" and v.startswith(b"BC|") for f, v in packets)
        assert any(f == "S" and v.startswith(b"P1|") for f, v in packets)
        if pets:
            assert any(f == "S" and v.startswith(b"K0|") for f, v in packets)
            assert any(f == "S" and v.startswith(b"W0|") for f, v in packets)

# Reject a malformed reset without partially creating a new battle.
bad = subprocess.run([binary, "--battle-environment", "--config", config],
                     input="reset 1 35 100 1 30 30 30 30 31 30 30 30\n",
                     stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                     text=True, errors="replace", timeout=10)
assert bad.returncode == 2
assert json.loads(bad.stdout.splitlines()[-1])["error"] == "invalid_request"
print("Native battle environment: all modes, player/pet packets, truncation and malformed requests passed")

# Bounds/length failures are checked by the C process too, before any character
# creation. The Go validator is not the security boundary for this transport.
base = "reset-pets-roster 42 35 10 1 " + "30 30 30 30 " * 4
valid = base + "20 2 2 " + "60 20 20 20 11 20 20 20 60 67 " * 2
invalid = [
    base + "20 2 3 " + "30 30 30 30 7 " * 6,
    valid.rsplit(" ", 2)[0],
    valid + "7",
    valid.replace("60 20 20 20 11", "61 20 20 20 11", 1),
    valid.replace("60 20 20 20 11", "60 20 20 20 255", 1),
    valid.replace("60 20 20 20 11", "60 20 20 20 0", 1),
    valid.replace("20 2 2 ", "11 2 2 ", 1),
]
for request in invalid:
    result = subprocess.run([binary, "--battle-environment", "--config", config],
                            input=request.strip() + "\n", text=True,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                            errors="replace", timeout=10)
    assert result.returncode == 2, (request, result.stdout)
    assert json.loads(result.stdout.splitlines()[-1])["error"] == "invalid_request"
print("Native multi-reserve parser: rejects count, length, unequal budgets, unsupported skills and healing")

skills_base = "reset-pets-skills-roster 42 35 10 1 " + "30 30 30 30 " * 4 + "0 0 0 "
for suffix in ("131 131", "254 128", "127 127"):
    result = subprocess.run([binary, "--battle-environment", "--config", config],
                            input=skills_base + suffix + "\nclose\n", text=True,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE, errors="replace", timeout=10)
    assert result.returncode == 0, result.stdout
    assert json.loads(result.stdout.splitlines()[1])["turn"] == 0
for suffix in ("255 131", "0 131", "256 131", "131", "131 131 1", "-1 131"):
    result = subprocess.run([binary, "--battle-environment", "--config", config],
                            input=skills_base + suffix + "\n", text=True,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE, errors="replace", timeout=10)
    assert result.returncode == 2, (suffix, result.stdout)
    assert json.loads(result.stdout.splitlines()[-1])["error"] == "invalid_request"
print("Native active skills parser: packs legal subsets, rejects eighth slot, unknown bits and wrong lengths")
