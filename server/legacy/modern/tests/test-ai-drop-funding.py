#!/usr/bin/env python3
"""Compile the patched native drop helper and exercise its commit boundary."""

from pathlib import Path
import os
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[4]
MODERN = ROOT / "server/legacy/modern"

HARNESS = r'''
#include <assert.h>
#include <stdio.h>
#include <string.h>
#define BOOL int
#define TRUE 1
#define FALSE 0
#define _DEL_DROP_GOLD 1
enum { CHAR_GOLD, CHAR_NAME, CHAR_CDKEY, CHAR_FLOOR, CHAR_X, CHAR_Y };
enum { OBJTYPE_NOUSE, OBJTYPE_GOLD, OBJTYPE_ITEM };
enum { STONEAGE_AI_EXTERNAL_DROP = 3 };
typedef struct { int type, x, y, floor, index; } Object;
typedef struct node { int index; struct node *next; } *OBJECT;
static struct node node = { 4, NULL };
static struct { int tv_sec; } NowTime = { 123 };
static int gold, pile, kind, allowed, walkable, allocation_ok, live;
static int attempts, commits, allocations, cleanups, logs, timestamps;
static int CHAR_getInt(int c, int key) { (void)c; return key == CHAR_GOLD ? gold : 0; }
static int CHAR_getMaxHaveGold(int c) { (void)c; return 100; }
static int MAP_walkAbleFromPoint(int f,int x,int y,int flag) {
    (void)f; (void)x; (void)y; (void)flag; return walkable;
}
static OBJECT MAP_getTopObj(int f,int x,int y) {
    (void)f; (void)x; (void)y; return kind ? &node : NULL;
}
#define NEXT_OBJECT(o) ((o)->next)
#define GET_OBJINDEX(o) ((o)->index)
static int OBJECT_getType(int i) { assert(i==4); return kind; }
static int OBJECT_getIndex(int i) { assert(i==4); return pile; }
static void OBJECT_setIndex(int i,int value) {
    assert(commits==1); assert(i==4 || (i==7 && live)); pile=value;
}
static void CHAR_setInt(int c,int key,int value) {
    assert(c==1 && key==CHAR_GOLD && commits==1); gold=value;
}
static void OBJECT_setTime(int i,int now) {
    assert(commits==1 && (i==4 || i==7) && now==123); timestamps++;
}
static int initObjectOne(Object *o) {
    assert(o->type==OBJTYPE_GOLD && o->index==0 && commits==0);
    allocations++; if(!allocation_ok) return -1; live=1; return 7;
}
static void endObjectOne(int i) {
    assert(i==7 && live && gold==100 && !commits); live=0; cleanups++;
}
static int StoneAge_AIFundingRecordExternal(int c,int k,int amount) {
    assert(c==1 && k==STONEAGE_AI_EXTERNAL_DROP && amount>0);
    assert(gold==100 && !commits && !logs && !timestamps);
    assert((kind==OBJTYPE_GOLD && pile==20) || (live && pile==20));
    attempts++; if(!allowed) return 0; commits++; return 1;
}
#define LogStone(...) (logs++)
/* DROP_HELPER */
static void reset(void) {
    gold=100; pile=20; kind=0; allowed=walkable=allocation_ok=1; live=0;
    attempts=commits=allocations=cleanups=logs=timestamps=0;
}
int main(void) {
    int out, result;
    reset(); out=-1;
    result=CHAR_DropMoneyFXY(1,10,1,2,3,FALSE,&out);
    assert(result==0 && out==7 && gold==90 && pile==10 && live && commits==1 && timestamps==1);
    reset();
    result=CHAR_DropMoneyFXY(1,100,1,2,3,FALSE,&out);
    assert(result==0 && gold==0 && pile==100 && commits==1 && logs==1);
    reset(); kind=OBJTYPE_GOLD;
    result=CHAR_DropMoneyFXY(1,10,1,2,3,FALSE,&out);
    assert(result==0 && out==4 && gold==90 && pile==30 && !allocations && commits==1);
    reset(); allowed=0; out=-1;
    result=CHAR_DropMoneyFXY(1,10,1,2,3,FALSE,&out);
    assert(result==-1 && out==-1 && gold==100 && !live && cleanups==1 && attempts==1 && !commits && !timestamps);
    reset(); kind=OBJTYPE_GOLD; allowed=0;
    result=CHAR_DropMoneyFXY(1,10,1,2,3,FALSE,&out);
    assert(result==-1 && gold==100 && pile==20 && !allocations && attempts==1 && !commits);
    reset(); allocation_ok=0;
    assert(CHAR_DropMoneyFXY(1,10,1,2,3,FALSE,&out)==-3 && !attempts && gold==100);
    reset(); walkable=0;
    assert(CHAR_DropMoneyFXY(1,10,1,2,3,FALSE,&out)==-2 && !attempts && !allocations);
    reset(); kind=OBJTYPE_GOLD; pile=95;
    assert(CHAR_DropMoneyFXY(1,10,1,2,3,FALSE,&out)==-4 && pile==95 && !attempts);
    reset(); kind=OBJTYPE_ITEM;
    assert(CHAR_DropMoneyFXY(1,10,1,2,3,FALSE,&out)==-5 && !attempts);
    assert(CHAR_DropMoneyFXY(1,10,1,2,3,TRUE,&out)==0 && gold==90 && commits==1);
    reset();
    assert(CHAR_DropMoneyFXY(1,0,1,2,3,FALSE,&out)==-6 && !attempts);
    assert(CHAR_DropMoneyFXY(1,-1,1,2,3,FALSE,&out)==-6 && !attempts);
    assert(CHAR_DropMoneyFXY(1,101,1,2,3,FALSE,&out)==-1 && !attempts);
    puts("Native drop funding commit boundaries passed");
    return 0;
}
'''


def main() -> None:
    with tempfile.TemporaryDirectory(prefix="ai-drop-funding-", dir=ROOT / "build") as temp:
        work = Path(temp)
        (work / "char").mkdir()
        source = work / "char/char_item.c"
        source.write_bytes((ROOT / "server/legacy/source/2.5/gmsv/char/char_item.c").read_bytes())
        patch = (MODERN / "patches/0012-ai-funding.patch").read_bytes()
        start = patch.index(b"--- a/char/char_item.c\n")
        end = patch.index(b"--- a/char/family.c\n", start)
        for content in [patch[start:end], (MODERN / "patches/0019-ai-drop-funding.patch").read_bytes()]:
            subprocess.run(["patch", "--fuzz=0", "-p1"], input=content, cwd=work, check=True)
        text = source.read_bytes().decode("gb18030")
        start = text.index("static BOOL CHAR_DropMoneyFXY(")
        end = text.index("void CHAR_DropMoney( ", start)
        helper = text[start:end]
        outer = text[end:text.index("static int CHAR_findEmptyItemBoxNoFromChar", end)]
        assert "StoneAge_AIFundingRecordExternal" not in outer
        assert "case -1:\n\t\t\treturn;" in outer, "uncertain drop must stop, not try another tile"
        harness = work / "drop.c"
        harness.write_text(HARNESS.replace("/* DROP_HELPER */", helper))
        binary = work / "drop"
        subprocess.run([os.environ.get("CC", "cc"), "-std=gnu89", "-Wall", "-Wextra", "-Werror",
                        str(harness), "-o", str(binary)], check=True, cwd=work,
                       env={**os.environ, "TMPDIR": str(work)})
        subprocess.run([str(binary)], check=True, cwd=work)


if __name__ == "__main__":
    main()
