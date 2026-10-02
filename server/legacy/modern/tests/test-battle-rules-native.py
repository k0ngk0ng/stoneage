#!/usr/bin/env python3
"""Verify rule provenance against the actual compiled native config parser.

Run in the prepared GMSV directory; all temporary data stays under the explicit
workspace argument. No network, account service or character save is used.
"""
import os
import json
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
import re
import shutil
import socket
import subprocess
import sys
import tempfile


binary, config, helper, workspace = map(lambda p: Path(p).resolve(), sys.argv[1:])


def command(args, cwd, env=None, success=True):
    result = subprocess.run(list(map(str, args)), cwd=cwd, env=env,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                            timeout=20)
    if success and result.returncode:
        raise AssertionError(f"native rules failed: exit {result.returncode}")
    return result


source = Path.cwd()
exported = command([binary, "--battle-rules", "--config", config], source).stdout
rows = [line.split("\t") for line in exported.decode().splitlines()]
assert all(len(row) == 3 and row[0] in ("table", "optional", "option") for row in rows)
options = {key for kind, key, value in rows if kind == "option"}
assert "battlegold" in options, "fixture expects the production gold option"

with tempfile.TemporaryDirectory(dir=workspace, prefix="rules-native-") as temporary:
    root = Path(temporary)
    (root / "data").mkdir()
    lines = ["topdir=.", "BATTLEGOLD=10", "SKILLUPPOINT=2"]
    for kind, key, value in rows:
        if kind != "table":
            continue
        shutil.copyfile(source / value, root / "data" / key)
        aliases = {"itemfile": [f"itemset{i}file" for i in range(3, 7)],
                   "petskillfile": ["petskillfile1", "petskillfile2"]}.get(key, [key])
        lines.extend(f"{alias}=data/{key}" for alias in aliases)
    base = "\n".join(lines) + "\n"
    cf = root / "setup.cf"
    cf.write_text(base)
    env = dict(os.environ, STONEAGE_BATTLE_BINARY=str(binary),
               STONEAGE_BATTLE_RECORD_DIR=str(root / "records"), TMPDIR=str(root))

    def digest(text=None, archive=False, success=True):
        if text is not None:
            cf.write_text(text)
        args = ["sh", helper, cf] + ([] if archive else ["--digest-only"])
        result = command(args, root, env, success)
        if not success:
            assert result.returncode != 0 and not re.fullmatch(rb"[a-f0-9]{64}\n", result.stdout)
            return
        assert re.fullmatch(rb"[a-f0-9]{64}\n", result.stdout)
        return result.stdout.decode().strip()

    initial = digest(archive=True)
    assert digest() == initial  # Recording disabled uses identical provenance.
    assert digest(base + "acpasswd=rule-test-secret\nacserv=private-test-host\n") == initial
    assert digest(base + "BATTLEGOLD=20\n") != initial
    assert digest(base + "BATTLEGOLD=20\nBATTLEGOLD=10\n") == initial
    spaced = "\n".join(line.replace("=", " = \t") for line in base.splitlines()) + "\n"
    assert digest(spaced) == initial
    # The native parser retains CR in table paths. Do not silently hash a
    # normalized path while the engine would load a different/missing file.
    digest(base.replace("\n", "\r\n"), success=False)
    assert digest(base + "BATTLEGOLD=1000\n") == digest(base + "BATTLEGOLD=100\n")
    # A missing value and an explicit native default must be equivalent.
    no_gold = "\n".join(line for line in lines if not line.startswith("BATTLEGOLD=")) + "\n"
    assert digest(no_gold) == digest(no_gold + "BATTLEGOLD=0\n")

    override = root / ("setup.cf." + socket.gethostname().split(".")[0])
    override.write_text(base + "BATTLEGOLD=20\n")
    assert digest(base) != initial
    override.unlink()
    assert digest() == initial

    # Native topdir prefixes the item table. Inactive aliases must not be read.
    native_rows = command([binary, "--battle-rules", "--config", cf], root).stdout.decode()
    assert "table\titemfile\t./data/itemfile\n" in native_rows
    (root / "other" / "data").mkdir(parents=True)
    item = root / "other" / "data" / "itemfile"
    shutil.copyfile(root / "data" / "itemfile", item)
    assert digest(base + "topdir=other\n") == initial
    with item.open("ab") as stream:
        stream.write(b"\n# changed combat table\n")
    assert digest() != initial
    digest(base)
    for alias in [f"itemset{i}file" for i in range(3, 7)]:
        candidate = base + f"{alias}=missing-table\n"
        cf.write_text(candidate)
        selected = command([binary, "--battle-rules", "--config", cf], root).stdout
        if b"missing-table" not in selected:
            assert digest() == initial
    assert digest(base) == initial

    optional = [(key, value) for kind, key, value in rows if kind == "optional"]
    for key, value in optional:
        path = root / value
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(b"# optional rules\n")
        assert digest() != initial
        path.unlink()
    assert digest() == initial
    (root / "data" / "petskillfile").rename(root / "saved-petskill")
    digest(success=False)
    (root / "saved-petskill").rename(root / "data" / "petskillfile")

    concurrent_env = dict(env, STONEAGE_BATTLE_RECORD_DIR=str(root / "concurrent"))
    with ThreadPoolExecutor(max_workers=6) as workers:
        outputs = list(workers.map(lambda _: command(["sh", helper, cf], root, concurrent_env).stdout, range(6)))
    assert all(output.decode().strip() == initial for output in outputs)
    published = root / "concurrent" / "rulesets" / initial
    assert all(file.is_file() for file in published.iterdir())
    assert not list(published.parent.glob(".prepare.*"))

    archived = root / "records" / "rulesets" / initial
    assert digest(archive=True) == initial
    assert not (archived / "inputs.tsv").exists()
    assert not (archived / "setup.cf").exists()
    for file in archived.iterdir():
        assert b"rule-test-secret" not in file.read_bytes()
        assert b"private-test-host" not in file.read_bytes()
    (archived / "petskillfile").write_bytes(b"corrupt")
    digest(archive=True, success=False)
    assert not list(root.glob("stoneage-rules.*"))
    assert not list((root / "records" / "rulesets").glob(".prepare.*"))

    # A worker must hash its own executable, not a caller's override intended
    # for a different standalone archive. Also reject an inherited fake label.
    worker_env = dict(env, STONEAGE_BATTLE_BINARY="/does-not-exist",
                      STONEAGE_BATTLE_RULESET_ID="0" * 64,
                      STONEAGE_BATTLE_RECORD_DIR=str(root / "worker-rules"))
    worker = subprocess.run(["sh", str(helper.parent / "run-battle-environment.sh"),
                             "--config", str(config)], cwd=source, env=worker_env,
                            input=b"close\n", stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE, timeout=20)
    assert worker.returncode == 0
    ready = json.loads(worker.stdout.splitlines()[0])
    assert ready["ready"] and ready["rules_digest"] != "0" * 64
    assert (root / "worker-rules" / "rulesets" / ready["rules_digest"]).is_dir()

print("Native rules: effective config, host override, defaults/clamping, table selection, prefixes, optional files, secret isolation, concurrent publication and archive integrity passed")
