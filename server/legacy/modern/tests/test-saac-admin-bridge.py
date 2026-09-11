#!/usr/bin/env python3
"""Compile and run the local SAAC admin bridge harness."""

import os
from pathlib import Path
import subprocess
import tempfile


def main() -> None:
    root = Path(__file__).resolve().parents[4]
    build_root = root / "build"
    build_root.mkdir(exist_ok=True)
    compiler = os.environ.get("CC", "cc")
    with tempfile.TemporaryDirectory(prefix="saac-bridge-", dir=build_root) as temp:
        binary = Path(temp) / "saac-admin-bridge-harness"
        source = root / "server/legacy/modern/tests/saac_admin_bridge_harness.c"
        subprocess.run(
            [compiler, "-std=gnu89", "-Wall", "-Wextra", "-Werror", "-o", str(binary), str(source)],
            cwd=root,
            check=True,
        )
        subprocess.run([str(binary), temp], cwd=root, check=True)
    print("SAAC admin bridge harness passed")


if __name__ == "__main__":
    main()
