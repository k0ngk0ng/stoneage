#!/usr/bin/env python3
"""Make the legacy SAAC character reader bounded and repeatable.

The archive is not UTF-8. Byte substitutions preserve its localized source
while also accepting the half-applied state left by older patch runs.
"""

from pathlib import Path
import sys


MARKER = b"STONEAGE_SAFE_CHARACTER_FILE_READ"


def replace_once(data: bytes, old: bytes, new: bytes, label: str) -> bytes:
    count = data.count(old)
    if count != 1:
        raise SystemExit(f"{label}: expected one match, found {count}")
    return data.replace(old, new, 1)


def main() -> int:
    if len(sys.argv) != 2:
        print(f"usage: {Path(sys.argv[0]).name} PATH/TO/char.c", file=sys.stderr)
        return 2

    target = Path(sys.argv[1])
    data = target.read_bytes()
    if MARKER in data:
        return 0

    if b"\tchar c_temp,*c_ptr;" in data:
        data = replace_once(
            data,
            b"\tchar c_temp,*c_ptr;",
            b"\tint c_temp;\n\tchar *c_ptr;",
            "fgetc variable type",
        )
    elif b"\tint c_temp;\n\tchar *c_ptr;" not in data:
        raise SystemExit("fgetc variable type: unsupported source state")

    old_loop = (
        b"\tdo{\n"
        b"\t\tc_temp = fgetc(fp);\n"
        b"\t\t*c_ptr=c_temp;\n"
        b"\t\tc_ptr++;\n"
        b"\t}while(c_temp != EOF);\n"
        b"\t*c_ptr='\\0';\n"
        b"\t\n"
        b"\tif( output[0]=='|' && output[1]=='|' ){\n"
        b"\t\treturn -1;\n"
        b"\t}\n"
        b"\tfclose(fp);"
    )
    new_loop = (
        b"\t/* STONEAGE_SAFE_CHARACTER_FILE_READ\n"
        b"\t * Preserve fgetc()'s int return type so EOF stays distinct from 255,\n"
        b"\t * and reject files that do not fit the caller's buffer.\n"
        b"\t */\n"
        b"\twhile( ( c_temp = fgetc( fp )) != EOF ){\n"
        b"\t\tif( c_ptr - output >= outlen - 1 ){\n"
        b"\t\t\tfclose( fp );\n"
        b"\t\t\toutput[0] = '\\0';\n"
        b"\t\t\treturn -1;\n"
        b"\t\t}\n"
        b"\t\t*c_ptr++ = (char)c_temp;\n"
        b"\t}\n"
        b"\t*c_ptr='\\0';\n"
        b"\tif( ferror( fp )){\n"
        b"\t\tfclose( fp );\n"
        b"\t\toutput[0] = '\\0';\n"
        b"\t\treturn -1;\n"
        b"\t}\n"
        b"\tfclose(fp);\n"
        b"\t\n"
        b"\tif( output[0]=='|' && output[1]=='|' ){\n"
        b"\t\treturn -1;\n"
        b"\t}"
    )
    data = replace_once(data, old_loop, new_loop, "bounded character read")
    target.write_bytes(data)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
