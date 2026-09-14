#!/usr/bin/env python3
"""Compile the modern white-tiger ride mapping against the archived source."""

from pathlib import Path
import os
import re
import subprocess
import tempfile


ROOT = Path(__file__).resolve().parents[4]
SOURCE = ROOT / "server/legacy/source/2.5/gmsv/char/family.c"
CHAR_BASE_HEADER = ROOT / "server/legacy/source/2.5/gmsv/include/char_base.h"
PATCH = ROOT / "server/legacy/modern/patches/0011-white-tiger-ride-mapping.patch"
BUILD = ROOT / "server/legacy/modern/build.sh"


def extract_helper(source: str) -> str:
    start = source.index("/* STONEAGE_WHITE_TIGER_RIDE")
    end = source.index("#endif", start)
    return source[start:end]


def extract_ride_pet_lookup(source: str, header: str) -> str:
    """Extract the production ride-character table and lookup function."""

    type_match = re.search(
        r"typedef\s+struct\s*\{\s*int\s+charNo;\s*"
        r"int\s+Noindex;\s*int\s+sex;\s*\}\s*tagRidePetList\s*;",
        header,
        re.DOTALL,
    )
    table_match = re.search(
        r"tagRidePetList\s+RPlistMode\s*\[\]\s*=\s*\{.*?\n\};",
        source,
        re.DOTALL,
    )
    function_match = re.search(
        r"int\s+RIDEPET_getNOindex\s*\(\s*int\s+baseNo\s*\)\s*\{",
        source,
    )
    if type_match is None:
        raise AssertionError("could not find the production tagRidePetList type")
    if table_match is None:
        raise AssertionError("could not find the production RPlistMode table")
    if function_match is None:
        raise AssertionError("could not find the production RIDEPET_getNOindex function")

    opening = source.find("{", function_match.start(), function_match.end())
    depth = 0
    for index in range(opening, len(source)):
        if source[index] == "{":
            depth += 1
        elif source[index] == "}":
            depth -= 1
            if depth == 0:
                function = source[function_match.start() : index + 1]
                break
    else:
        raise AssertionError("unterminated production RIDEPET_getNOindex function")

    return "\n\n".join((type_match.group(0), table_match.group(0), function))


def check_call_order(source: str) -> None:
    """Ensure the white-tiger mapping stays behind guards and before item fallback."""

    function_start = source.index("void FAMILY_RidePet")
    function = source[function_start:]
    mapping_call = function.index("rideGraNo = STONEAGE_getWhiteTigerRideNo(")
    required_guards = (
        "CHAR_LEARNRIDE",
        "#ifdef _RIDELEVEL",
        "CHAR_WORKFIXAI",
        "CHAR_TRANSMIGRATION",
    )
    for guard in required_guards:
        guard_position = function.index(guard)
        if guard_position > mapping_call:
            raise AssertionError(
                f"white-tiger mapping moved before the {guard} restriction"
            )

    item_fallback = function.index("RIDEPET_getPETindex(")
    if mapping_call > item_fallback:
        raise AssertionError("white-tiger mapping moved after the ride-certificate fallback")


def harness_source(lookup: str, helper: str) -> str:
    return f"""
#include <stdio.h>

{lookup}

{helper}

static int expect(int condition, const char *message)
{{
    if (condition) return 0;
    fprintf(stderr, "white-tiger ride: %s\\n", message);
    return 1;
}}

int main(void)
{{
    int errors = 0;
    int i;

    for (i = 0; i < 48; i++) {{
        errors += expect(
            STONEAGE_getWhiteTigerRideNo(100000 + i * 5, 100872)
                == 104025 + i / 4,
            "ordinary character color must select its native white-tiger sprite");
    }}
    for (i = 0; i < 24; i++) {{
        errors += expect(
            STONEAGE_getWhiteTigerRideNo(100700 + i * 5, 100872)
                == 104025 + i / 2,
            "family-leader color must select its native white-tiger sprite");
    }}
    errors += expect(
        STONEAGE_getWhiteTigerRideNo(100000, 100871) == 0,
        "other pets must not use the white-tiger mapping");
    errors += expect(
        STONEAGE_getWhiteTigerRideNo(100240, 100872) == 0,
        "unknown character images must not use the mapping");
    errors += expect(
        STONEAGE_getWhiteTigerRideNo(100700, 100872) == 104025,
        "the first family-leader row must start at 104025");
    errors += expect(
        STONEAGE_getWhiteTigerRideNo(100815, 100872) == 104036,
        "the last family-leader row must end at 104036");
    return errors != 0;
}}
"""


def main() -> None:
    source = SOURCE.read_bytes()
    source_text = SOURCE.with_name("char_base.c").read_bytes().decode("latin1")
    header_text = CHAR_BASE_HEADER.read_bytes().decode("latin1")
    build_text = BUILD.read_text()
    patch = PATCH.read_bytes()

    if "0011-white-tiger-ride-mapping.patch" not in build_text:
        raise AssertionError("modern/build.sh does not apply the white-tiger ride patch")

    build_root = ROOT / "build"
    build_root.mkdir(exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="gmsv-white-tiger-", dir=build_root) as temp:
        temp_path = Path(temp)
        target = temp_path / "src/gmsv/char/family.c"
        target.parent.mkdir(parents=True)
        target.write_bytes(source)
        subprocess.run(
            ["patch", "--batch", "--forward", "-d", str(temp_path / "src/gmsv"), "-p1"],
            input=patch,
            cwd=ROOT,
            check=True,
        )
        patched = target.read_bytes().decode("latin1")
        if "STONEAGE_WHITE_TIGER_RIDE" not in patched:
            raise AssertionError("white-tiger ride patch marker was not applied")
        check_call_order(patched)
        lookup = extract_ride_pet_lookup(source_text, header_text)
        helper = extract_helper(patched)
        harness = temp_path / "white-tiger-ride.c"
        binary = temp_path / "white-tiger-ride"
        harness.write_text(harness_source(lookup, helper))
        subprocess.run(
            [
                os.environ.get("CC", "cc"),
                "-std=gnu89",
                "-Wall",
                "-Wextra",
                "-Werror",
                "-Wno-sign-compare",  # The unmodified native lookup uses int vs sizeof.
                "-o",
                str(binary),
                str(harness),
            ],
            cwd=ROOT,
            check=True,
        )
        subprocess.run([str(binary)], cwd=ROOT, check=True)

    print("GMSV white-tiger ride mapping tests passed for 48 colors and 12 leader rows")


if __name__ == "__main__":
    main()
