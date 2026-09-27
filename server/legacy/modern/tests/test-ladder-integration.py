#!/usr/bin/env python3
"""Check the actual build transformations and compile their native call sites.

This is a source/build boundary check, not an end-to-end gameplay test.
"""
import hashlib
import importlib.util
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[4]
MODERN = ROOT / "server/legacy/modern"
ARCHIVE = ROOT / "server/legacy/source/2.5/gmsv"
BUILD = ROOT / "build/ladder/integration"
BUILD.mkdir(parents=True, exist_ok=True)
spec = importlib.util.spec_from_file_location("ladder_integration", MODERN / "integrate-ladder.py")
integration = importlib.util.module_from_spec(spec)
spec.loader.exec_module(integration)

with tempfile.TemporaryDirectory(dir=BUILD) as temporary:
    work = Path(temporary)
    env = dict(os.environ, TMPDIR=str(work), CLANG_MODULE_CACHE_PATH=str(BUILD / "module-cache"))
    originals = {}
    for name in (*integration.SOURCES, "makefile"):
        data = (ARCHIVE / name).read_bytes()
        originals[name] = hashlib.sha256(data).digest()
        target = work / name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(data)
    shutil.copytree(ARCHIVE / "include", work / "include", dirs_exist_ok=True)
    for header in MODERN.glob("*.h"):
        shutil.copyfile(header, work / "include" / header.name)
    with (MODERN / "patches/0032-battle-records.patch").open("rb") as patch:
        subprocess.run(["patch", "-p1", "-d", str(work)], stdin=patch, check=True, capture_output=True)
    # Detect missing anchors before writing any file, even ones already read.
    original_main = (work / "main.c").read_bytes()
    battle = work / "battle/battle.c"
    original_battle = battle.read_bytes()
    battle.write_bytes(original_battle.replace(b"StoneAge_BattleRecordExecuting(", b"UnknownRecordExecuting("))
    try:
        integration.integrate(work)
    except ValueError:
        assert (work / "main.c").read_bytes() == original_main
    else:
        raise AssertionError("source drift was silently accepted")
    battle.write_bytes(original_battle)
    integration.integrate(work)
    first = {p: p.read_bytes() for p in work.rglob("*") if p.is_file()}
    integration.integrate(work)
    assert all(p.read_bytes() == data for p, data in first.items()), "integration is not idempotent"
    # A previous transformed build must fail, not silently skip newly added
    # resource/save hooks. Check the whole operation remains write-free.
    battle.write_bytes(first[battle].replace(integration.MARKER, b"/* STONEAGE_LADDER_INTEGRATION_V1 */\n"))
    stale = {p: p.read_bytes() for p in first}
    try:
        integration.integrate(work)
    except ValueError as exc:
        assert "regenerate the build copy" in str(exc)
        assert all(p.read_bytes() == data for p, data in stale.items())
    else:
        raise AssertionError("outdated integration was silently accepted")
    battle.write_bytes(first[battle])
    for name in integration.SOURCES:
        if name.endswith(".h"):
            continue  # compiled through the actual callers below
        source = work / name
        # Clang's C89 checks are stricter than the archive's GCC defaults;
        # legacy implicit prototypes are unrelated to the new integration.
        compatibility = ["-Wno-error=return-mismatch"] if b"clang" in subprocess.check_output(["cc", "--version"]) else []
        subprocess.run(["cc", "-std=gnu89", "-D_FORTIFY_SOURCE=0", "-w", *compatibility, "-fsyntax-only",
                        "-I" + str(work / "include"), "-I" + str(MODERN), str(source)],
                       env=env, check=True)
    for name in ("stoneage_ladder.c", "stoneage_ladder_core.c"):
        subprocess.run(["cc", "-std=gnu89", "-D_FORTIFY_SOURCE=0", "-Wno-enum-conversion", "-fsyntax-only",
                        "-I" + str(work / "include"), "-I" + str(MODERN), str(MODERN / name)],
                       env=env, check=True)
    for name, digest in originals.items():
        assert hashlib.sha256((ARCHIVE / name).read_bytes()).digest() == digest, "historical source changed"
print("Ladder build integration: checked anchors, idempotence and native call-site compilation passed")
