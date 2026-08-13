#!/usr/bin/env python3
"""Remove plaintext credentials from non-UTF-8 StoneAge 2.5 C sources."""

from __future__ import annotations

import argparse
from pathlib import Path


def replace_unique_line(path: Path, needle: bytes, replacement: bytes) -> None:
    data = path.read_bytes()
    if replacement in data:
        return

    matches = [line for line in data.splitlines(keepends=True) if needle in line]
    if len(matches) != 1:
        raise RuntimeError(
            f"expected exactly one line containing {needle!r} in {path}, "
            f"found {len(matches)}"
        )
    original = matches[0]
    newline = b"\r\n" if original.endswith(b"\r\n") else b"\n"
    updated = data.replace(original, replacement + newline, 1)
    if updated == data:
        raise RuntimeError(f"failed to update {path}")
    path.write_bytes(updated)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("gmsv_root", type=Path)
    parser.add_argument("saac_root", type=Path, nargs="?")
    args = parser.parse_args()

    replace_unique_line(
        args.gmsv_root / "callfromcli.c",
        b",cdkey,passwd,ip);",
        b'    /* STONEAGE_REDACT_PASSWORD_LOGS */ '
        b'print("\\nClient login account=%s source=%s\\n", cdkey, ip);',
    )
    replace_unique_line(
        args.gmsv_root / "init.c",
        b"getAccountserverpasswd());",
        b'        /* STONEAGE_REDACT_PASSWORD_LOGS */ '
        b'print("Account server credential: [redacted]\\n");',
    )
    replace_unique_line(
        args.gmsv_root / "init.c",
        b"getChatMagicPasswd() );",
        b'        /* STONEAGE_REDACT_PASSWORD_LOGS */ '
        b'print("GM command credential: [redacted]\\n");',
    )
    replace_unique_line(
        args.gmsv_root / "lssproto_serv.c",
        b"LSSPROTO_CLIENTLOGIN_RECV-cdkey:",
        b'        /* STONEAGE_REDACT_PASSWORD_LOGS */ '
        b'printf("[debug] client login account=%s\\n", cdkey);',
    )
    replace_unique_line(
        args.gmsv_root / "lssproto_serv.c",
        b"LSSPROTO_SHUTDOWN_RECV-passwd:",
        b'        /* STONEAGE_REDACT_PASSWORD_LOGS */ '
        b'printf("[debug] shutdown password=[redacted],min=%d\\n", min);',
    )

    if args.saac_root is not None:
        replace_unique_line(
            args.saac_root / "main.c",
            b'log( "\xc3\xdc\xc2\xeb:%s\\n",param );',
            b'            /* STONEAGE_REDACT_PASSWORD_LOGS */ '
            b'log("Account server credential: [redacted]\\n");',
        )
        replace_unique_line(
            args.saac_root / "recv.c",
            b",id , pas );",
            b'    /* STONEAGE_REDACT_PASSWORD_LOGS */ '
            b'log("Character delete account=%s\\n", id);',
        )


if __name__ == "__main__":
    main()
