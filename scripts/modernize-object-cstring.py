#!/usr/bin/env python3
"""Preserve complete NPC names when formatting legacy object packets.

The archived source is GBK. Apply checked byte replacements rather than
transcoding it, and keep the patch idempotent for local incremental builds.
"""

from pathlib import Path
import sys


def rewrite(data: bytes) -> bytes:
    if b"STONEAGE_SAFE_OBJECT_NAME" in data:
        return data
    start = data.index(b"BOOL _CHAR_makeObjectCString(")
    end = data.index(b"\nvoid CHAR_sendCToArroundCharacter", start)
    original = data[start:end]
    updated = original
    replacements = (
        (b"char Name[32];",
         b"/* STONEAGE_SAFE_OBJECT_NAME: escaped name plus optional VIP prefix. */\n"
         b"\t\tchar Name[sizeof(escapename) + sizeof(VipName)];"),
        (b'sprintf(Name, "%s%s",', b'snprintf(Name, sizeof(Name), "%s%s",'),
        (b'sprintf(Name, "%s",', b'snprintf(Name, sizeof(Name), "%s",'),
    )
    for old, new in replacements:
        if updated.count(old) != 1:
            raise ValueError(f"expected exactly one object-name expression: {old!r}")
        updated = updated.replace(old, new, 1)
    return data[:start] + updated + data[end:]


def main() -> int:
    if len(sys.argv) != 2:
        print(f"usage: {Path(sys.argv[0]).name} PATH/TO/char.c", file=sys.stderr)
        return 2
    target = Path(sys.argv[1])
    original = target.read_bytes()
    updated = rewrite(original)
    if updated != original:
        target.write_bytes(updated)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
