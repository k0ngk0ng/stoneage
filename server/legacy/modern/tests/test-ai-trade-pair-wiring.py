#!/usr/bin/env python3
"""Verify the real funding patch stack places one quota commit before exchange."""

from pathlib import Path
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[4]


def main() -> None:
    with tempfile.TemporaryDirectory(prefix="trade-pair-patch-", dir=ROOT / "build") as work:
        target = Path(work)
        (target / "char").mkdir()
        source = target / "char/trade.c"
        source.write_bytes((ROOT / "server/legacy/source/2.5/gmsv/char/trade.c").read_bytes())
        patches = ROOT / "server/legacy/modern/patches"
        base = (patches / "0012-ai-funding.patch").read_bytes()
        start = base.index(b"--- a/char/trade.c\n")
        end = base.index(b"--- a/char/char_item.c\n", start)
        for patch in [base[start:end], (patches / "0018-ai-trade-pair-funding.patch").read_bytes()]:
            subprocess.run(["patch", "--fuzz=0", "-p1"], input=patch, cwd=target, check=True)
        content = source.read_bytes()
        assert content.count(b"StoneAge_AIFundingRecordTradePair(") == 1
        assert b"StoneAge_AIFundingRecordExternal(" not in content
        assert b'else if (result == -16)\n   \tstrcpy(msg, "' in content
        assert content.index(b"StoneAge_AIFundingRecordTradePair(") < content.index(
            b"\tTRADE_ChangeItem(meindex, toindex, a, d, item1, item4,"
        )
    print("Trade pair patches apply without fuzz; one pair commit precedes native exchange.")


if __name__ == "__main__":
    main()
