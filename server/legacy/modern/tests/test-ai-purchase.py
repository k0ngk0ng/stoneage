#!/usr/bin/env python3
"""Compile the patched native purchase handler and exercise commit failures."""
from pathlib import Path
import os
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[4]
MODERN = ROOT / "server/legacy/modern"
SOURCE = ROOT / "server/legacy/source/2.5/gmsv"

HARNESS = r'''
#include <assert.h>
#include <limits.h>
#include <math.h>
#include <stdio.h>
#include <string.h>
#define BOOL int
#define TRUE 1
#define FALSE 0
#define CHAR_MAXITEMHAVE 20
#define CHAR_STARTITEMARRAY 5
#define ITEM_WORKOBJINDEX 0
#define ITEM_WORKCHARAINDEX 1
static int bag[20], owner[20][2], alive[20];
static int cost, gold, unlimited, reject_charge, fail_alloc;
static int allocated, destroyed, charged, published, taxes, wanted;
static int CHAR_CHECKINDEX(int c) { return c == 1; }
static int ITEM_getcostFromITEMtabl(int id) { (void)id; return cost; }
static int StoneAge_AIFundingCanCharge(int c, int n) {
    assert(c == 1); return unlimited || gold >= n;
}
static int StoneAge_AIFundingCharge(int c, int n) {
    int i; assert(c == 1); assert(!published && !taxes);
    for(i=5;i<20;i++) assert(bag[i] < 0 || bag[i] == 99);
    if(reject_charge || (!unlimited && gold < n)) return 0;
    if(!unlimited) gold -= n;
    charged++; return 1;
}
static int CHAR_getItemIndex(int c,int slot) { assert(c==1); return bag[slot]; }
static int ITEM_makeItemAndRegist(int id) {
    (void)id;
    if(allocated == fail_alloc) return -1;
    alive[allocated] = 1; return allocated++;
}
static int ITEM_CHECKINDEX(int id) { return id >= 0 && id < allocated && alive[id]; }
static void ITEM_endExistItemsOne(int id) { assert(alive[id]); alive[id]=0; destroyed++; }
static void CHAR_setItemIndex(int c,int slot,int id) {
    assert(c==1 && charged==1 && bag[slot]==-1); bag[slot]=id;
}
static void ITEM_setWorkInt(int id,int key,int value) { owner[id][key]=value; }
static int addNpcFamilyTax(int npc,int c,double n) {
    (void)npc; (void)c; (void)n; assert(charged==1); taxes++; return 1;
}
#define print(...) ((void)0)
static void CHAR_sendItemDataOne(int c,int slot) {
    int i,count=0; assert(c==1 && charged==1 && taxes==1);
    for(i=5;i<20;i++) if(bag[i]>=0 && bag[i]!=99) count++;
    assert(count==wanted);
    assert(owner[bag[slot]][0]==-1 && owner[bag[slot]][1]==1); published++;
}
__HANDLER__
static void reset(void) {
    int i; for(i=0;i<20;i++) bag[i]=-1;
    memset(owner,0,sizeof(owner)); memset(alive,0,sizeof(alive));
    cost=10; gold=0; unlimited=1; reject_charge=0; fail_alloc=-1;
    allocated=destroyed=charged=published=taxes=0; wanted=3;
}
static void no_effect(void) {
    assert(!charged && !published && !taxes && allocated==destroyed);
}
int main(void) {
    int i;
    reset(); assert(NPC_AddItemBuy(0,1,42,3,1));
    assert(gold==0 && charged==1 && published==3 && !destroyed);
    reset(); unlimited=0; gold=30; assert(NPC_AddItemBuy(0,1,42,3,1));
    assert(gold==0 && published==3);
    reset(); unlimited=0; gold=29; assert(!NPC_AddItemBuy(0,1,42,3,1)); no_effect();
    reset(); reject_charge=1; assert(!NPC_AddItemBuy(0,1,42,3,1));
    no_effect(); assert(destroyed==3);
    for(i=0;i<3;i++) {
        reset(); fail_alloc=i; assert(!NPC_AddItemBuy(0,1,42,3,1));
        no_effect(); assert(destroyed==i);
    }
    reset(); for(i=5;i<18;i++) bag[i]=99;
    assert(!NPC_AddItemBuy(0,1,42,3,1)); no_effect();
    reset(); assert(!NPC_AddItemBuy(0,1,42,0,1)); no_effect();
    assert(!NPC_AddItemBuy(0,1,42,16,1)); no_effect();
    assert(!NPC_AddItemBuy(0,-1,42,3,1)); no_effect();
    cost=INT_MAX; assert(!NPC_AddItemBuy(0,1,42,3,1)); no_effect();
    cost=-1; assert(!NPC_AddItemBuy(0,1,42,3,1)); no_effect();
    cost=10; assert(!NPC_AddItemBuy(0,1,42,3,NAN)); no_effect();
    assert(!NPC_AddItemBuy(0,1,42,3,INFINITY)); no_effect();
    assert(!NPC_AddItemBuy(0,1,42,3,-1)); no_effect();
    puts("native purchase commit checks passed"); return 0;
}
'''


def main():
    scratch = ROOT / "build/ai"
    scratch.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="purchase-", dir=scratch) as tmp:
        stage = Path(tmp)
        transaction = (MODERN / "patches/0015-ai-funding-transactions.patch").read_bytes()
        names = [line[6:].decode() for line in transaction.splitlines() if line.startswith(b"--- a/")]
        for name in names:
            target = stage / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes((SOURCE / name).read_bytes())
        # Apply precisely the prerequisite sections for these native handlers.
        base = (MODERN / "patches/0012-ai-funding.patch").read_bytes()
        sections = base.split(b"--- a/")[1:]
        selected = b"".join(b"--- a/" + part for part in sections
                            if part.splitlines()[0].decode() in names)
        for patch in (selected, transaction):
            result = subprocess.run(["patch", "-p1", "--fuzz=0"], cwd=stage,
                                    input=patch, capture_output=True, check=True)
            print(result.stdout.decode(), end="")
        source = (stage / "npc/npc_itemshop.c").read_bytes().decode("latin1")
        start = source.index("BOOL NPC_AddItemBuy(int meindex, int talker,int itemID,int kosuu,double rate)\n{")
        end = source.index("\n}\n", start) + 3
        program = stage / "purchase.c"
        program.write_text(HARNESS.replace("__HANDLER__", source[start:end]))
        binary = stage / "purchase"
        subprocess.run(["cc", "-std=c99", "-Wall", "-Wextra", "-Werror",
                        str(program), "-o", str(binary)], check=True,
                       env={**os.environ, "TMPDIR": str(stage)})
        subprocess.run([str(binary)], check=True)


if __name__ == "__main__":
    main()
