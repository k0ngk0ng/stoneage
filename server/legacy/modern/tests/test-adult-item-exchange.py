#!/usr/bin/env python3
"""Exercise the actual adult exchange module with the preserved quest script."""
from pathlib import Path
import os
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[4]
MODERN = ROOT / "server/legacy/modern"
SOURCE = ROOT / "server/legacy/source/2.5/gmsv"

HEADER = r'''
#ifndef ADULT_TEST_CHAR_H
#define ADULT_TEST_CHAR_H
#define TRUE 1
#define FALSE 0
#define CHAR_STARTITEMARRAY 5
#define CHAR_MAXITEMHAVE 20
enum { CHAR_NAME, CHAR_CDKEY, CHAR_FLOOR, CHAR_X, CHAR_Y };
enum { ITEM_ID, ITEM_USEPILENUMS, ITEM_WORKOBJINDEX, ITEM_WORKCHARAINDEX, ITEM_UNIQUECODE, ITEM_NAME };
int CHAR_CHECKINDEX(int);
int ITEM_CHECKINDEX(int);
int CHAR_getItemIndex(int,int);
int CHAR_setItemIndex(int,int,int);
int CHAR_getInt(int,int);
char *CHAR_getChar(int,int);
int ITEM_getInt(int,int);
char *ITEM_getChar(int,int);
void ITEM_setWorkInt(int,int,int);
int ITEM_makeItemAndRegist(int);
void ITEM_endExistItemsOne(int);
void CHAR_sendItemDataOne(int,int);
void LogItem(char*,char*,int,char*,int,int,int,char*,char*,int);
#endif
'''

HARNESS = r'''
#include <assert.h>
#include <stdio.h>
#include <string.h>
#include "char_base.h"
#include "stoneage_adult_exchange.h"
static int slots[20], alive[64], ids[64], stacks[64], owners[64], objects[64];
static int alloc_ok, allocations, destroyed, cleanup, sends, writes, logs, change_input, wrong_reward;
int CHAR_CHECKINDEX(int c) { return c==1; }
int ITEM_CHECKINDEX(int i) { return i>=0 && i<64 && alive[i]; }
int CHAR_getItemIndex(int c,int s) { assert(c==1 && s>=0 && s<20); return slots[s]; }
int CHAR_setItemIndex(int c,int s,int id) {
    int old=slots[s]; assert(c==1 && s>=5 && s<20 && alive[40]);
    assert(!sends); slots[s]=id; writes++; return old;
}
int CHAR_getInt(int c,int key) { (void)c; (void)key; return 0; }
char *CHAR_getChar(int c,int key) { (void)c; (void)key; return "role"; }
int ITEM_getInt(int i,int key) { assert(ITEM_CHECKINDEX(i)); return key==ITEM_ID ? ids[i] : stacks[i]; }
char *ITEM_getChar(int i,int key) { (void)key; assert(ITEM_CHECKINDEX(i)); return "item"; }
void ITEM_setWorkInt(int i,int key,int value) {
    assert(i==40 && alive[i]); if(key==ITEM_WORKCHARAINDEX) owners[i]=value; else objects[i]=value;
}
int ITEM_makeItemAndRegist(int id) {
    assert(id==2418 && !writes && !sends); allocations++;
    if(!alloc_ok) return -1;
    alive[40]=1; ids[40]=wrong_reward ? 999 : id; if(change_input) slots[5]=-1; return 40;
}
void ITEM_endExistItemsOne(int i) {
    assert(ITEM_CHECKINDEX(i));
    if(i==40) { assert(!writes && !sends); cleanup++; }
    else { assert(slots[5]==40 && owners[40]==1 && !sends); destroyed++; }
    alive[i]=0;
}
void CHAR_sendItemDataOne(int c,int s) {
    assert(c==1 && s>=5 && s<20 && destroyed==15 && slots[5]==40 && owners[40]==1 && objects[40]==-1);
    sends++;
}
void LogItem(char*a,char*b,int c,char*d,int e,int f,int g,char*h,char*i,int j) {
    (void)a;(void)b;(void)c;(void)d;(void)e;(void)f;(void)g;(void)h;(void)i;(void)j;
    assert(slots[5]==40 && !sends); logs++;
}
static void reset(void) {
    int i; memset(slots,-1,sizeof(slots)); memset(alive,0,sizeof(alive));
    memset(stacks,0,sizeof(stacks)); memset(owners,-1,sizeof(owners));
    for(i=0;i<15;i++) { slots[i+5]=i; alive[i]=1; ids[i]=2417; owners[i]=1; }
    alloc_ok=1; allocations=destroyed=cleanup=sends=writes=logs=change_input=wrong_reward=0;
}
static void unchanged(void) {
    int i; for(i=0;i<15;i++) assert(slots[i+5]==i && alive[i]);
    assert(!writes && !sends && !destroyed && !logs);
}
int main(void) {
    int i; char other[2048]; const char *script=SCRIPT;
    reset(); slots[0]=50; alive[50]=1; ids[50]=99;
    assert(StoneAge_AdultExchange(1,script)==1);
    assert(slots[5]==40 && slots[0]==50 && alive[50] && destroyed==15 && sends==15 && logs==16);
    for(i=6;i<20;i++) assert(slots[i]==-1);
    assert(StoneAge_AdultExchange(1,script)==0 && allocations==1);
    reset(); alloc_ok=0; assert(StoneAge_AdultExchange(1,script)==0); unchanged();
    reset(); wrong_reward=1; assert(StoneAge_AdultExchange(1,script)==0 && cleanup==1); unchanged();
    reset(); slots[19]=-1; slots[0]=14;
    assert(StoneAge_AdultExchange(1,script)==0 && !allocations && !writes && alive[14]);
    reset(); slots[19]=0; assert(StoneAge_AdultExchange(1,script)==0 && !allocations && !writes);
    reset(); stacks[0]=2; assert(StoneAge_AdultExchange(1,script)==0 && !allocations); unchanged();
    reset(); change_input=1;
    assert(StoneAge_AdultExchange(1,script)==0 && cleanup==1 && !alive[40] && !destroyed && !writes && !sends);
    reset(); assert(StoneAge_AdultExchange(2,script)==0); unchanged();
    snprintf(other,sizeof(other),"%s|GetStone:100",script);
    assert(StoneAge_AdultExchange(1,other)==-1); unchanged();
    snprintf(other,sizeof(other),"%s|GetItem:2418",script);
    assert(StoneAge_AdultExchange(1,other)==-1); unchanged();
    snprintf(other,sizeof(other),"%s|ThanksMsg:duplicate",script);
    assert(StoneAge_AdultExchange(1,other)==-1); unchanged();
    assert(StoneAge_AdultExchange(1,"EventNo:5|GetItem:2418|DelItem:2417*15")==-1); unchanged();
    puts("Adult ceremony item exchange preparation and commit checks passed");
    return 0;
}
'''


def main() -> None:
    raw = (SOURCE / "data/npc/jaruga/event/event04_1").read_bytes().decode("gb18030")
    script = "|".join(line.strip() for line in raw.split("EventEnd", 1)[0].splitlines() if line.strip())
    with tempfile.TemporaryDirectory(prefix="adult-exchange-", dir=ROOT / "build") as temp:
        work = Path(temp)
        include = work / "include"
        include.mkdir()
        for name in ["version.h", "char.h", "char_base.h", "item.h", "log.h"]:
            (include / name).write_text(HEADER)
        harness = work / "adult.c"
        literal = '"' + ''.join("\\%03o" % byte for byte in script.encode("gb18030")) + '"'
        harness.write_text(HARNESS.replace("SCRIPT", literal))
        binary = work / "adult"
        subprocess.run([os.environ.get("CC", "cc"), "-std=gnu89", "-Wall", "-Wextra", "-Werror",
                        "-I"+str(include), "-I"+str(MODERN), str(harness),
                        str(MODERN / "stoneage_adult_exchange.c"), "-o", str(binary)],
                       cwd=work, check=True, env={**os.environ,"TMPDIR":str(work)})
        subprocess.run([str(binary)], cwd=work, check=True)
        (work / "npc").mkdir()
        (work / "npc/npc_exchangeman.c").write_bytes((SOURCE / "npc/npc_exchangeman.c").read_bytes())
        (work / "makefile").write_bytes((SOURCE / "makefile").read_bytes())
        for name, target in [
            ("0009-player-admin-bridge.patch", "makefile"),
            ("0012-ai-funding.patch", "makefile"),
            ("0013-ai-observation.patch", "makefile"),
            ("0012-ai-funding.patch", "npc/npc_exchangeman.c"),
        ]:
            content=(MODERN / "patches" / name).read_bytes()
            start=content.index(("--- a/"+target+"\n").encode())
            end=content.find(b"\n--- a/",start+1)
            selected=content[start:] if end < 0 else content[start:end+1]
            subprocess.run(["patch","--fuzz=0","-p1"],cwd=work,check=True,input=selected)
        subprocess.run(["patch","--fuzz=0","-p1"],cwd=work,check=True,
                       input=(MODERN / "patches/0020-adult-item-exchange.patch").read_bytes())
        patched=(work / "npc/npc_exchangeman.c").read_bytes()
        start=patched.index(b"BOOL NPC_AcceptDel(int meindex,int talker,int mode )\n{")
        tail=patched[start:]
        assert tail.index(b"StoneAge_AdultExchange(talker, buf)") < tail.index(b"NPC_ItemFullCheck(")
        assert b"if( result >= 0 ) return result;" in tail
        assert b"stoneage_adult_exchange.c" in (work / "makefile").read_bytes()
    print("Adult exchange native wiring applies without fuzz")


if __name__ == "__main__":
    main()
