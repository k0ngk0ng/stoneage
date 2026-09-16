#!/usr/bin/env python3
"""Check the patched native treasury callback with NPC and deposit replies."""
from pathlib import Path
import os
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[4]
SOURCE = ROOT / "server/legacy/source/2.5/gmsv"
PATCH = ROOT / "server/legacy/modern/patches/0017-npc-tax-refund.patch"

HARNESS = r'''
#include <assert.h>
#include <stdlib.h>
#include <string.h>
#include <stdio.h>
#define CHAR_GOLD 1
#define CHAR_P_STRING_GOLD 2
static int gold, updates;
#define CHAR_getInt(c,k) gold
#define CHAR_setInt(c,k,v) (gold=(v))
#define CHAR_send_P_StatusString(c,k) (updates++)
#define LogStone(a,b,c,amount,...) ((void)(amount))
static void reply(int ret,char *data1,char *data2) {
    int intdata;
    __BODY__
}
int main(void) {
    gold=0; updates=0; reply(0,"40","npc_tax"); assert(gold==0);
    gold=75; reply(0,"40","npc_tax"); assert(gold==75);
    /* Funding revocation cannot change the persisted request's origin. */
    gold=0; reply(0,"40",""); assert(gold==40);
    gold=0; reply(0,"40","1000"); assert(gold==40);
    gold=0; reply(1,"40","1000"); assert(gold==0);
    gold=0; reply(1,"-40","1000"); assert(gold==40);
    gold=0; reply(0,"-40",""); assert(gold==0);
    puts("NPC tax refund checks passed"); return 0;
}
'''


def main():
    scratch = ROOT / "build/ai"
    scratch.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="tax-refund-", dir=scratch) as tmp:
        stage = Path(tmp)
        patch = PATCH.read_bytes()
        for line in patch.splitlines():
            if line.startswith(b"--- a/"):
                name = line[6:].decode()
                target = stage / name
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes((SOURCE / name).read_bytes())
        subprocess.run(["patch", "-p1", "--fuzz=0"], cwd=stage, input=patch, check=True)
        source = (stage / "callfromac.c").read_bytes().decode("latin1")
        start = source.index("}else if( kindflag == FM_FIX_FMGOLD ) {")
        start = source.index("\n", start)
        end = source.index("\n\t}else if( kindflag == FM_FIX_FMLEADERCHANGE )", start)
        program = stage / "tax.c"
        program.write_text(HARNESS.replace("__BODY__", source[start:end]))
        binary = stage / "tax"
        subprocess.run(["cc", "-std=c99", "-Wall", "-Wextra", "-Werror",
                        str(program), "-o", str(binary)], check=True,
                       env={**os.environ, "TMPDIR": str(stage)})
        subprocess.run([str(binary)], check=True)


if __name__ == "__main__":
    main()
