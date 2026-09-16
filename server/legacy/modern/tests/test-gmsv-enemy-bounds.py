#!/usr/bin/env python3
"""Compile the enemy/group lookup guards with valid and missing rows."""

from pathlib import Path
import os
import re
import subprocess
import tempfile


ROOT = Path(__file__).resolve().parents[4]
SOURCE = ROOT / "server/legacy/source/2.5/gmsv/char/enemy.c"
PATCH = ROOT / "server/legacy/modern/patches/0014-safe-enemy-group-bounds.patch"


def extract_function(source: str, name: str) -> str:
    pattern = re.compile(
        r"static\s+BOOL\s+" + re.escape(name) + r"\s*\([^{}]*\)\s*\{",
        re.MULTILINE | re.DOTALL,
    )
    match = pattern.search(source)
    if match is None:
        raise RuntimeError(f"could not find production function {name}")
    opening = source.find("{", match.start(), match.end())
    depth = 0
    for index in range(opening, len(source)):
        if source[index] == "{":
            depth += 1
        elif source[index] == "}":
            depth -= 1
            if depth == 0:
                return source[match.start() : index + 1]
    raise RuntimeError(f"unterminated production function {name}")


def harness_source(source: str) -> str:
    resolve_group = extract_function(source, "ENEMY_resolveGroupArray")
    check_enemy = extract_function(source, "ENEMY_checkGroupEnemyArray")
    return f"""\
#include <stddef.h>
#include <stdio.h>

#define TRUE 1
#define FALSE 0
#define CREATEPROB1 13
#define ENEMY_ID1 3
typedef int BOOL;
typedef enum {{ GROUP_ID = 0, GROUP_APPEARBYITEMID, GROUP_NOTAPPEARBYITEMID,
                GROUP_ENEMY0, GROUP_DATAINTNUM }} GROUP_DATAINT;

static int group_count = 2;
static int enemy_count = 4;
static int group_data[2][GROUP_DATAINTNUM + 10] = {{
    {{77, -1, -1, 101, -1, -1, -1, -1, -1, -1, -1, -1, -1}},
    {{88, -1, -1, -1, 103, -1, -1, -1, -1, -1, -1, -1, -1}}
}};

static BOOL GROUP_CHECKINDEX(int index)
{{
    return index >= 0 && index < group_count;
}}

static int GROUP_getGroupArray(int groupid)
{{
    int index;
    for (index = 0; index < group_count; index++)
        if (group_data[index][GROUP_ID] == groupid) return index;
    return -1;
}}

static int GROUP_getInt(int index, GROUP_DATAINT element)
{{
    return group_data[index][element];
}}

static BOOL ENEMY_CHECKINDEX(int index)
{{
    return index >= 0 && index < enemy_count;
}}

{resolve_group}

{check_enemy}

static int expect(int condition, const char *message)
{{
    if (condition) return 0;
    fprintf(stderr, "enemy bounds: %s\\n", message);
    return 1;
}}

int main(void)
{{
    int group_array = -99;
    int errors = 0;

    errors += expect(ENEMY_resolveGroupArray(77, &group_array) && group_array == 0,
                     "valid group must resolve");
    group_array = -99;
    errors += expect(!ENEMY_resolveGroupArray(1230, &group_array) && group_array == -1,
                     "missing group must fail and clear output");
    errors += expect(!ENEMY_resolveGroupArray(77, NULL),
                     "null group output must fail");

    errors += expect(ENEMY_checkGroupEnemyArray(0, 0, 1),
                     "configured valid enemy must pass");
    errors += expect(!ENEMY_checkGroupEnemyArray(0, 0, -1),
                     "configured missing enemy must fail");
    errors += expect(ENEMY_checkGroupEnemyArray(0, 1, -1),
                     "sparse empty enemy slot must pass as empty");
    errors += expect(!ENEMY_checkGroupEnemyArray(0, 1, 1),
                     "empty slot with a live enemy array must fail");
    errors += expect(ENEMY_checkGroupEnemyArray(1, 1, 3),
                     "valid enemy in a second group must pass");
    errors += expect(!ENEMY_checkGroupEnemyArray(-1, 0, 1),
                     "invalid group array must fail");
    errors += expect(!ENEMY_checkGroupEnemyArray(0, 10, -1),
                     "invalid slot must fail");
    errors += expect(!ENEMY_checkGroupEnemyArray(0, 0, 99),
                     "out of range enemy array must fail");

    return errors != 0;
}}
"""


def main() -> None:
    original = SOURCE.read_bytes()
    build_root = ROOT / "build"
    build_root.mkdir(exist_ok=True)
    compiler = os.environ.get("CC", "cc")
    with tempfile.TemporaryDirectory(prefix="gmsv-enemy-bounds-", dir=build_root) as temp:
        temp_path = Path(temp)
        target = temp_path / "src/gmsv/char/enemy.c"
        target.parent.mkdir(parents=True)
        target.write_bytes(original)
        subprocess.run(
            ["patch", "--batch", "--forward", "-d", str(temp_path / "src/gmsv"), "-p1"],
            input=PATCH.read_bytes(),
            cwd=ROOT,
            check=True,
        )
        patched = target.read_bytes().decode("latin1")
        if "STONEAGE_SAFE_ENEMY_GROUP_BOUNDS" not in patched:
            raise AssertionError("enemy group bounds patch was not applied")
        harness = temp_path / "enemy_bounds.c"
        binary = temp_path / "enemy-bounds"
        harness.write_text(harness_source(patched))
        subprocess.run(
            [compiler, "-std=gnu89", "-Wall", "-Wextra", "-Werror", "-o", str(binary), str(harness)],
            cwd=ROOT,
            check=True,
        )
        subprocess.run([str(binary)], cwd=ROOT, check=True)
    print("GMSV enemy/group bounds tests passed")


if __name__ == "__main__":
    main()
