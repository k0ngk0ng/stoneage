#!/usr/bin/env python3
"""Add durable SAAC character saves to a build copy, preserving CP936 bytes."""
from pathlib import Path
import hashlib
import re
import sys

MARKER = b"/* STONEAGE_DURABLE_CHARACTER_STORE_V2_" + hashlib.sha256(Path(__file__).read_bytes()).hexdigest()[:16].encode() + b" */\n"
ARCHIVE = Path(__file__).resolve().parent.parent / "source/2.5/saac"


def replace(data, old, new):
    if data.count(old) != 1:
        raise ValueError(f"expected one anchor, got {data.count(old)}: {old!r}")
    return data.replace(old, new)


def transform(data):
    if MARKER in data:
        return data
    if b"STONEAGE_DURABLE_CHARACTER_STORE_" in data:
        raise ValueError("outdated character store integration; regenerate the build copy")
    data = replace(data, b"\tmemset(savebuf, 0, sizeof(savebuf));", b"""    if (!charinfo || !*charinfo) {
        saacproto_ACCharSave_send(ti, FAILED, "empty character archive", mesgid);
        return -1;
    }
\tmemset(savebuf, 0, sizeof(savebuf));""")
    start = data.index(b"int saveCharOne(")
    end = data.index(b"\nstatic int makeSaveCharString(", start)
    body = data[start:end]
    body = replace(body, b"\tFILE *fp;\n", b"")
    body, count = re.subn(rb'\tfp= fopen\( fn , "w" \);\n\tif\( fp == NULL \) \{[^{}]*?\n\t\}', b"", body)
    if count != 1:
        raise ValueError("character file-open anchor changed")
    body = replace(body, b'\tfprintf( fp , "%s" , input );\n\tfclose(fp);',
                   b"\tif (StoneAge_CharacterStoreWrite(fn, input) < 0) return -1;")
    body = replace(body, b"\tchmod(fn,0777);\n", b"")
    # The old HP normalization shifts overlapping bytes left in the payload.
    body = replace(body, b"strcpy((strp+6), strp1);", b"memmove(strp+6, strp1, strlen(strp1)+1);")
    return MARKER + b'#include "stoneage_character_store.h"\n' + data[:start] + body + data[end:]


def integrate(root):
    root = Path(root).resolve()
    if root == ARCHIVE.resolve():
        raise ValueError("refusing to edit historical sources; use a build copy")
    source = root / "char.c"
    makefile = root / "makefile"
    updated = transform(source.read_bytes())
    make = makefile.read_bytes()
    if b"stoneage_character_store.c" not in make:
        make = replace(make, b"\nSRC = main.c recv.c util.c char.c ",
                       b"\nSRC = main.c recv.c util.c char.c stoneage_character_store.c ")
    # Validate every anchor before writing either file.
    if source.read_bytes() != updated:
        source.write_bytes(updated)
    if makefile.read_bytes() != make:
        makefile.write_bytes(make)


if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit("usage: integrate-character-store.py <build-copy-of-saac>")
    integrate(sys.argv[1])
