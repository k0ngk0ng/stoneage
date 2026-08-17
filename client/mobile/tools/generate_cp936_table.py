#!/usr/bin/env python3
"""Build the small CP936 lookup table used by the Android client.

The legacy server sends names and dialogue as Windows code page 936 bytes.
Android does not expose an iconv/GBK codec to a Godot script, so the build
ships a two-byte lookup table (one UTF-16 code unit per possible byte pair).
"""

from __future__ import annotations

import argparse
from pathlib import Path


def build_table() -> bytes:
    table = bytearray(256 * 256 * 2)
    for lead in range(0x100):
        for trail in range(0x100):
            try:
                text = bytes((lead, trail)).decode("cp936")
            except UnicodeDecodeError:
                continue
            if len(text) != 1:
                continue
            codepoint = ord(text)
            # CP936 maps to the BMP; store little-endian UTF-16 code units.
            offset = ((lead << 8) | trail) * 2
            table[offset] = codepoint & 0xFF
            table[offset + 1] = codepoint >> 8
    return bytes(table)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_bytes(build_table())


if __name__ == "__main__":
    main()
