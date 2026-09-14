#!/usr/bin/env python3
"""Compile the production ride-level guard with both feature branches."""

from pathlib import Path
import os
import re
import subprocess
import tempfile


ROOT = Path(__file__).resolve().parents[4]
SOURCE = ROOT / "server/legacy/source/2.5/gmsv/char/family.c"
PATCH = ROOT / "server/legacy/modern/patches/0010-safe-ride-level-guard.patch"


def extract_guard(source: str) -> str:
    function_start = source.index("void FAMILY_RidePet")
    start = source.index("#ifdef _RIDELEVEL", function_start)
    end = source.index("#endif", start) + len("#endif")
    return source[start:end]


def harness_source(guard: str) -> str:
    # The archived family.c is CP936. Replace only the diagnostic format line
    # in the extracted guard so the strict macOS clang used by CI compiles the
    # harness as UTF-8 while retaining the production control flow verbatim.
    guard = '\n'.join(
        (line[: len(line) - len(line.lstrip())] + 'sprintf(buff, "ride-level");')
        if "sprintf(buff," in line
        else line
        for line in guard.splitlines()
    )
    return f'''\
#include <stdio.h>

#define CHAR_LV 1
#define CHAR_COLORYELLOW 2

static int harness_player_level;
static int harness_pet_level;
static int harness_ride_level;
static int harness_notice_count;

int CHAR_getInt(int charaindex, int element)
{{
    (void)element;
    return charaindex == 0 ? harness_player_level : harness_pet_level;
}}

int getRideLevel(void)
{{
    return harness_ride_level;
}}

void CHAR_talkToCli(int charaindex, int talk_index, char *message, int color)
{{
    (void)charaindex;
    (void)talk_index;
    (void)message;
    (void)color;
    harness_notice_count++;
}}

static int check_ride_level(int player_level, int pet_level, int ride_level)
{{
    int meindex = 0;
    int petindex = 1;
    harness_player_level = player_level;
    harness_pet_level = pet_level;
    harness_ride_level = ride_level;
    harness_notice_count = 0;
{guard}
    return 1;
}}

static int expect(int condition, const char *message)
{{
    if (condition) return 0;
    fprintf(stderr, "ride-level guard: %s\\n", message);
    return 1;
}}

int main(void)
{{
    int errors = 0;
#ifdef _RIDELEVEL
    errors += expect(check_ride_level(10, 20, 10) == 1,
                     "configured +10 boundary must be allowed");
    errors += expect(check_ride_level(10, 21, 10) == 0,
                     "configured +10 overflow must be rejected");
    errors += expect(harness_notice_count == 1,
                     "configured rejection must notify the player");
    errors += expect(check_ride_level(10, 11, 0) == 0,
                     "zero configured level must reject a higher pet");
#else
    errors += expect(check_ride_level(10, 15, 999) == 1,
                     "legacy +5 boundary must be allowed");
    errors += expect(check_ride_level(10, 16, 999) == 0,
                     "legacy +5 overflow must be rejected");
    errors += expect(harness_notice_count == 1,
                     "legacy rejection must notify the player");
#endif
    return errors != 0;
}}
'''


def run_branch(guard: str, enabled: bool, directory: Path) -> None:
    source = directory / ("ride-level-enabled.c" if enabled else "ride-level-legacy.c")
    binary = directory / ("ride-level-enabled" if enabled else "ride-level-legacy")
    source.write_bytes(harness_source(guard).encode("latin1"))
    command = [
        os.environ.get("CC", "cc"),
        "-std=gnu89",
        "-Wall",
        "-Wextra",
        "-Werror",
        "-o",
        str(binary),
        str(source),
    ]
    if enabled:
        command.insert(1, "-D_RIDELEVEL")
    subprocess.run(command, cwd=ROOT, check=True)
    subprocess.run([str(binary)], cwd=ROOT, check=True)


def main() -> None:
    original = SOURCE.read_bytes()
    version_text = (ROOT / "server/legacy/source/2.5/gmsv/include/version.h").read_bytes().decode("latin1")
    setup_text = (ROOT / "server/legacy/source/2.5/gmsv/setup.cf").read_bytes().decode("latin1")
    build_text = (ROOT / "server/legacy/modern/build.sh").read_text()

    if re.search(r"(?m)^#define _RIDELEVEL\b", version_text) is None:
        raise AssertionError("formal 2.5 source does not define _RIDELEVEL")
    if re.search(r"(?m)^RIDELEVEL=10\s*$", setup_text) is None:
        raise AssertionError("formal 2.5 setup does not configure RIDELEVEL")
    if "0010-safe-ride-level-guard.patch" not in build_text:
        raise AssertionError("modern/build.sh does not apply the ride-level patch")

    build_root = ROOT / "build"
    build_root.mkdir(exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="gmsv-ride-level-", dir=build_root) as temp:
        temp_path = Path(temp)
        target = temp_path / "src/gmsv/char/family.c"
        target.parent.mkdir(parents=True)
        target.write_bytes(original)
        subprocess.run(
            ["patch", "--batch", "--forward", "-d", str(temp_path / "src/gmsv"), "-p1"],
            input=PATCH.read_bytes(),
            cwd=ROOT,
            check=True,
        )
        patched_text = target.read_bytes().decode("latin1")
        if "STONEAGE_SAFE_RIDELEVEL_GUARD" not in patched_text:
            raise AssertionError("ride-level patch marker was not applied")
        guard = extract_guard(patched_text)
        run_branch(guard, enabled=True, directory=temp_path)
        run_branch(guard, enabled=False, directory=temp_path)

    print("GMSV ride-level guard tests passed for _RIDELEVEL and legacy +5 branches")


if __name__ == "__main__":
    main()
