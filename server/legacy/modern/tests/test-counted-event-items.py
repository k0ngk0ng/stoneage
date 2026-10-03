#!/usr/bin/env python3
"""Compile the patched native DelItem function; verify real slot consumption."""
from pathlib import Path
import os
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[4]
MODERN = ROOT / "server/legacy/modern"
SOURCE = ROOT / "server/legacy/source/2.5/gmsv/npc/npc_exchangeman.c"

HARNESS = r'''
#include <assert.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#define BOOL int
#define TRUE 1
#define FALSE 0
#define CHAR_MAXITEMHAVE 20
#define CHAR_NAME 1
#define CHAR_CDKEY 2
#define CHAR_FLOOR 3
#define CHAR_X 4
#define CHAR_Y 5
#define CHAR_COLORYELLOW 0
#define CHAR_COLORWHITE 0
#define ITEM_ID 0
#define ITEM_NAME 1
#define ITEM_UNIQUECODE 2
#define LogItem(...) ((void)0)
static int slots[20], ids[20], deleted[20], sent[20];
static int CHAR_getItemIndex(int c,int s) { assert(c==1); return slots[s]; }
static int ITEM_CHECKINDEX(int i) { return i>=0 && i<20 && !deleted[i]; }
static int ITEM_getInt(int i,int field) { assert(field==ITEM_ID); return ids[i]; }
static char *ITEM_getChar(int i,int field) { (void)i; (void)field; return "item"; }
static void CHAR_talkToCli(int c,int to,char *s,int color) { (void)to; (void)s; (void)color; assert(c==1); }
static void CHAR_setItemIndex(int c,int s,int i) { assert(c==1&&i==-1); slots[s]=i; }
static void ITEM_endExistItemsOne(int i) { assert(!deleted[i]); deleted[i]=1; }
static void CHAR_sendItemDataOne(int c,int s) { assert(c==1); sent[s]++; }
static int getStringFromIndexWithDelim(const char *s,const char *d,int n,char *out,int size) {
    const char *end; int len;
    while (--n>0) {s=strstr(s,d);if(!s)return FALSE;s+=strlen(d);}
    if(!*s)return FALSE;
    end=strstr(s,d);len=end?(int)(end-s):(int)strlen(s);
    if(len>=size)len=size-1;
    memcpy(out,s,len);out[len]=0;return TRUE;
}
FUNCTION
static void reset(void) {
    int i;memset(deleted,0,sizeof deleted);memset(sent,0,sizeof sent);
    for(i=0;i<20;i++){slots[i]=-1;ids[i]=0;}
    slots[5]=5;ids[5]=20031;slots[6]=6;ids[6]=1234;
    slots[7]=7;ids[7]=20031;slots[8]=8;ids[8]=20031;
}
int main(void) {
    int broken;
    for(broken=0;broken<=1;broken++) {
        reset();assert(NPC_EventDelItem(0,1,"20031*1",broken));
        assert(deleted[5]&&sent[5]==1&&slots[5]==-1);
        assert(!deleted[6]&&!deleted[7]&&!deleted[8]);
        reset();assert(NPC_EventDelItem(0,1,"20031*2,1234*1",broken));
        assert(deleted[5]&&deleted[7]&&deleted[6]&&!deleted[8]);
        reset();assert(!NPC_EventDelItem(0,1,"20031*0",broken));
        assert(!deleted[5]&&!deleted[7]&&!deleted[8]);
        reset();assert(!NPC_EventDelItem(0,1,"20031*-1",broken));
        assert(!deleted[5]&&!deleted[7]&&!deleted[8]);
    }
    reset();assert(NPC_EventDelItem(0,1,"20031",0));
    assert(deleted[5]&&deleted[7]&&deleted[8]&&!deleted[6]);
    puts("native counted ordinary and Break hand-ins consume exactly the requested slots");
    return 0;
}
'''

def main():
    with tempfile.TemporaryDirectory(prefix="counted-items-", dir=ROOT / "build") as tmp:
        work = Path(tmp)
        target = work / "npc/npc_exchangeman.c"
        target.parent.mkdir()
        target.write_bytes(SOURCE.read_bytes())
        with (MODERN / "patches/0056-counted-event-items.patch").open("rb") as patch:
            subprocess.run(["patch", "-p1", "--batch"], cwd=work, stdin=patch, check=True)
        source = target.read_bytes().decode("gb18030")
        start = source.index("BOOL NPC_EventDelItem(int meindex,int talker,char *buf,int breakflg)\n{")
        end = source.index("BOOL NPC_EventDelItemEVDEL(", start)
        function = source[start:end]
        # Drop the comment introducing the following function.
        function = function[:function.rfind("}") + 1]
        harness = work / "test.c"
        harness.write_text(HARNESS.replace("FUNCTION", function))
        binary = work / "test"
        subprocess.run([os.environ.get("CC", "cc"), "-std=gnu89", "-Wall", "-Wextra", "-Wno-unused-parameter", str(harness), "-o", str(binary)], check=True, env={**os.environ, "TMPDIR": str(work)})
        subprocess.run([str(binary)], check=True)

if __name__ == "__main__":
    main()
