#!/usr/bin/env python3
"""Regression test for the bounded legacy object-name formatter.

The legacy source is GBK and the production arena signboard is deliberately
kept as a fixture.  The test applies the byte-preserving rewriter to a copy,
then uses a small C harness to exercise the rewritten formatting expression
under AddressSanitizer.  The old 32-byte expression must trip ASan for the
62-byte production name; the rewritten expression must preserve all bytes in
the non-VIP and VIP branches.
"""

from __future__ import annotations

import os
from pathlib import Path
import shlex
import shutil
import subprocess
import sys
import tempfile


ROOT = Path(__file__).resolve().parents[1]
SOURCE = ROOT / "server/legacy/source/2.5/gmsv/char/char.c"
FIXTURE = ROOT / "server/legacy/source/2.5/gmsv/data/npc/genout/shop_m.create"
REWRITER = ROOT / "scripts/modernize-object-cstring.py"


def fail(message: str) -> None:
    raise AssertionError(message)


def run(command: list[str], *, env: dict[str, str] | None = None) -> subprocess.CompletedProcess[str]:
    result = subprocess.run(
        command,
        cwd=ROOT,
        env=env,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    if result.returncode != 0:
        fail(
            f"command failed ({result.returncode}): {' '.join(command)}\n"
            f"stdout:\n{result.stdout}\nstderr:\n{result.stderr}"
        )
    return result


def legacy_string_copy(source: bytes, capacity: int) -> bytes:
    """Model the archive's strcpysafe/strncpy2 byte and GBK boundary rules."""

    if capacity <= 0:
        return b""
    if len(source) + 1 <= capacity:
        return source

    limit = capacity - 1
    result = bytearray()
    index = 0
    while index < limit and index < len(source):
        byte = source[index]
        if byte & 0x80:
            if index + 1 >= limit or index + 1 >= len(source):
                break
            result.extend(source[index : index + 2])
            index += 2
        else:
            result.append(byte)
            index += 1
    return bytes(result)


def legacy_escape(source: bytes) -> bytes:
    """Match makeEscapeString for the bytes used by the fixture and cases."""

    escaped = bytearray()
    replacements = {b"\n": b"\\n", b",": b"\\c", b"|": b"\\z", b"\\": b"\\y"}
    index = 0
    while index < len(source):
        byte = source[index : index + 1]
        if source[index] & 0x80:
            # The legacy routine treats CP936 characters as two-byte units.
            escaped.extend(source[index : index + 2])
            index += 2
        elif byte in replacements:
            escaped.extend(replacements[byte])
            index += 1
        else:
            escaped.extend(byte)
            index += 1
    return bytes(escaped)


def c_bytes(source: bytes) -> str:
    return ", ".join(f"0x{byte:02x}" for byte in source + b"\0")


def c_ascii(length: int) -> str:
    return c_bytes(b"A" * length)


def source_name_block(source: bytes) -> bytes:
    start = source.index(b"BOOL _CHAR_makeObjectCString(")
    end = source.index(b"\nvoid CHAR_sendCSpecifiedObjindex", start)
    return source[start:end]


def arena_signboard_name() -> bytes:
    lines = FIXTURE.read_bytes().splitlines()
    for index, line in enumerate(lines[:-5]):
        if line == b"floorid=2000" and lines[index + 1] == b"borncorner=87,78,87,78":
            name = lines[index + 5]
            if not name.startswith(b"name="):
                fail("arena signboard fixture has no name field")
            return name[len(b"name=") :]
    fail("arena signboard fixture was not found")


def formatting_block(source: bytes) -> str:
    start = source.index(b"char Name[")
    end = source.index(b"\n#endif", start)
    return source[start:end].decode("ascii")


def make_harness(production_name: bytes, block: str) -> str:
    return f"""\
#include <stddef.h>
#include <stdio.h>
#include <string.h>

static char *makeEscapeString(const unsigned char *source, char *dest, size_t capacity)
{{
    static const unsigned char escaped[] = {{'\\n', 'n', ',', 'c', '|', 'z', '\\\\', 'y'}};
    size_t out = 0;
    size_t i;
    (void)escaped;
    for (i = 0; source[i] != '\\0' && out + 1 < capacity; ++i) {{
        if (source[i] & 0x80) {{
            if (source[i + 1] == '\\0' || out + 2 >= capacity) break;
            dest[out++] = (char)source[i++];
            dest[out++] = (char)source[i];
        }} else if (source[i] == '\\n' || source[i] == ',' ||
                   source[i] == '|' || source[i] == '\\\\') {{
            if (out + 2 >= capacity) break;
            dest[out++] = '\\\\';
            dest[out++] = source[i] == '\\n' ? 'n' :
                          source[i] == ',' ? 'c' :
                          source[i] == '|' ? 'z' : 'y';
        }} else {{
            dest[out++] = (char)source[i];
        }}
    }}
    dest[out] = '\\0';
    return dest;
}}

static void build_name(int show_vip, const unsigned char *source,
                       char *output, size_t output_capacity)
{{
    char VipName[32] = "";
    char escapename[256];
    if (show_vip != 0)
        snprintf(VipName, sizeof(VipName), "VIP-");
#define getShowVip() show_vip
#define CHAR_getChar(index, key) ((char *)source)
    {block}
#undef CHAR_getChar
#undef getShowVip
    snprintf(output, output_capacity, "%s", Name);
}}

static void print_hex(const unsigned char *value)
{{
    size_t i;
    for (i = 0; value[i] != '\\0'; ++i)
        printf("%02x", value[i]);
}}

int main(void)
{{
    static const unsigned char production[] = {{{c_bytes(production_name)}}};
    static const unsigned char boundary31[] = {{{c_ascii(31)}}};
    static const unsigned char boundary32[] = {{{c_ascii(32)}}};
    static const unsigned char boundary62[] = {{{c_ascii(62)}}};
    static const unsigned char boundary63[] = {{{c_ascii(63)}}};
    static const unsigned char max_escaped[] = {{{c_bytes(b'|' * 63)}}};
    static const unsigned char escaped[] = {{0x41, 0x2c, 0x42, 0x7c, 0x43,
                                              0x5c, 0x44, 0x0a, 0x45, 0x00}};
    const unsigned char *cases[] = {{boundary31, boundary32, boundary62,
                                     boundary63, production, escaped, max_escaped}};
    size_t mode, index;
    char output[1024];
    for (mode = 0; mode <= 2; ++mode) {{
        for (index = 0; index < sizeof(cases) / sizeof(cases[0]); ++index) {{
            build_name((int)mode, cases[index], output, sizeof(output));
            printf("%zu %zu ", mode, index);
            print_hex((const unsigned char *)output);
            putchar('\\n');
        }}
    }}
    return 0;
}}
"""


def old_harness(production_name: bytes, block: str) -> str:
    return f"""\
#include <stdio.h>
static const unsigned char production[] = {{{c_bytes(production_name)}}};
int main(void)
{{
    char VipName[32] = "";
    char escapename[256];
#define getShowVip() 0
#define CHAR_getChar(index, key) ((char *)production)
#define makeEscapeString(source, dest, capacity) (source)
    {block}
    puts(Name);
    return 0;
}}
"""


def compile_c(compiler: list[str], source: Path, output: Path, extra: list[str] | None = None) -> None:
    flags = [
        *compiler,
        "-std=c99",
        "-O1",
        "-g",
        "-fsanitize=address",
        "-fno-omit-frame-pointer",
    ]
    if extra:
        flags.extend(extra)
    flags.extend([str(source), "-o", str(output)])
    run(flags)


def main() -> int:
    if not REWRITER.is_file():
        fail(f"missing object-cstring rewriter: {REWRITER}")

    original = SOURCE.read_bytes()
    original_block = source_name_block(original)
    if b"char Name[32];" not in original_block:
        fail("the regression fixture no longer contains the historical 32-byte Name buffer")

    raw_name = arena_signboard_name()
    if len(raw_name) != 160:
        fail(f"arena signboard fixture changed: expected 160 bytes, got {len(raw_name)}")
    production_name = legacy_string_copy(raw_name, 64)
    if len(production_name) != 62:
        fail(f"legacy CHAR_NAME load changed: expected 62 bytes, got {len(production_name)}")

    compiler = shlex.split(os.environ.get("CC", "cc"))
    if not compiler or shutil.which(compiler[0]) is None:
        fail("a C compiler is required for the object-cstring ASan regression")

    with tempfile.TemporaryDirectory(prefix=".object-cstring-test-", dir=ROOT) as temporary:
        temp = Path(temporary)
        rewritten_source = temp / "char.c"
        rewritten_source.write_bytes(original)
        run([sys.executable, str(REWRITER), str(rewritten_source)])
        rewritten = rewritten_source.read_bytes()
        rewritten_block = source_name_block(rewritten)

        required = (
            b"STONEAGE_SAFE_OBJECT_NAME",
            b"char Name[sizeof(escapename) + sizeof(VipName)];",
            b'snprintf(Name, sizeof(Name), "%s%s",',
            b'snprintf(Name, sizeof(Name), "%s",',
        )
        for marker in required:
            if marker not in rewritten_block:
                fail(f"rewriter did not produce expected object-name change: {marker!r}")
        if b"sprintf(Name," in rewritten_block:
            fail("unsafe Name sprintf remains after object-cstring rewrite")

        before_second_run = rewritten
        run([sys.executable, str(REWRITER), str(rewritten_source)])
        if rewritten_source.read_bytes() != before_second_run:
            fail("object-cstring rewriter is not idempotent")
        if SOURCE.read_bytes() != original:
            fail("object-cstring regression modified the checked-in source")

        harness_source = temp / "object_name_harness.c"
        harness_binary = temp / "object_name_harness"
        harness_source.write_text(make_harness(production_name, formatting_block(rewritten_block)), encoding="ascii")
        compile_c(compiler, harness_source, harness_binary)
        harness = run(
            [str(harness_binary)],
            env={**os.environ, "ASAN_OPTIONS": "detect_leaks=0:halt_on_error=1"},
        )

        expected_cases = [
            b"A" * 31,
            b"A" * 32,
            b"A" * 62,
            b"A" * 63,
            production_name,
            b"A,B|C\\D\nE",
            b"|" * 63,
        ]
        expected_lines: dict[tuple[int, int], bytes] = {}
        for mode in range(3):
            for index, value in enumerate(expected_cases):
                expected = value
                if mode == 2:
                    expected = b"VIP-" + legacy_escape(value)
                expected_lines[(mode, index)] = expected

        actual_lines: dict[tuple[int, int], bytes] = {}
        for line in harness.stdout.splitlines():
            mode, index, encoded = line.split(" ", 2)
            actual_lines[(int(mode), int(index))] = bytes.fromhex(encoded)
        if actual_lines != expected_lines:
            fail(f"object-name formatter output mismatch:\nexpected={expected_lines}\nactual={actual_lines}")

        old_source = temp / "old_object_name.c"
        old_binary = temp / "old_object_name"
        old_source.write_text(old_harness(production_name, formatting_block(original_block)), encoding="ascii")
        compile_c(compiler, old_source, old_binary, ["-fno-builtin-sprintf"])
        old = subprocess.run(
            [str(old_binary)],
            cwd=ROOT,
            env={**os.environ, "ASAN_OPTIONS": "detect_leaks=0:halt_on_error=1"},
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
        if "AddressSanitizer: stack-buffer-overflow" not in old.stderr:
            fail(f"historical formatter did not produce the expected ASan overflow: {old.stderr}")
        if old.returncode == 0:
            fail("historical 32-byte sprintf unexpectedly accepted the 62-byte name")

        print("object-cstring regression passed: arena name 160 -> CHAR_NAME 62; old overflow detected; VIP 0/1/2 and 31/32/62/63-byte cases passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
