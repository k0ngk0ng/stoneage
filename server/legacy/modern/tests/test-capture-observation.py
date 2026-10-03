#!/usr/bin/env python3
"""Verify capture quotes against native lookup/check functions in both builds."""
from pathlib import Path
import os
import re
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[4]
SOURCE = ROOT / "server/legacy/source/2.5/gmsv/battle/battle_event.c"
MODERN = ROOT / "server/legacy/modern"

def function(source, name):
    match = re.search(r"(?:static\s+)?(?:int|BOOL)\s+" + name + r"\s*\([^{}]*\)\s*\{", source)
    if not match:
        raise RuntimeError(name + " missing from native source")
    opening = source.index("{", match.start())
    depth = 0
    for i in range(opening, len(source)):
        depth += (source[i] == "{") - (source[i] == "}")
        if depth == 0:
            return source[match.start():i+1]
    raise RuntimeError(name + " is unterminated")

PRELUDE = r'''
#include <stdio.h>
#include <string.h>
#include <stdlib.h>
#define TRUE 1
#define FALSE 0
#define BOOL int
#define CHAR_MAXPETHAVE 5
#define CHAR_MAXITEMHAVE 20
#define CHAR_STARTITEMARRAY 5
#define BATTLE_ENTRY_MAX 10
#define BATTLE_TYPE_P_vs_E 1
#define CHAR_TYPEPLAYER 1
#define CHAR_TYPEENEMY 2
#define ITEM_ID 0
#define arraysizeof(x) (sizeof(x)/sizeof((x)[0]))
#define CHAR_CHECKINDEX(c) ((c)>0&&(c)<5)
#define BATTLE_CHECKINDEX(b) ((b)==0)
#define ITEM_CHECKINDEX(i) ((i)>=0&&(i)<20&&items[i]>=0)
enum { CHAR_WHICHTYPE,CHAR_HP,CHAR_PETID,CHAR_LV,CHAR_BASEIMAGENUMBER,
       CHAR_WORKBATTLEINDEX,CHAR_WORKBATTLEMODE,CHAR_WORK_PETFLG,CHAR_PickAllPet };
static int data[5][16], items[20], pets[5], enemies[20], private_reads;
static struct { int type, turn; } BattleArray[1];
static int CHAR_getInt(int c,int k) { if(c==4 && k!=CHAR_WHICHTYPE) private_reads++; return data[c][k]; }
static int CHAR_getWorkInt(int c,int k) { return CHAR_getInt(c,k); }
static int CHAR_getCharPet(int c,int s) { if(c!=1) abort(); return pets[s]; }
static int CHAR_getItemIndex(int c,int s) { if(c!=1) abort(); return items[s]<0?-1:s; }
static int ITEM_getInt(int i,int k) { return items[i]; }
static int BATTLE_Index2No(int b,int c) { return c==1?0:-1; }
static int BATTLE_No2Index(int b,int slot) { return enemies[slot]; }
#ifdef _CAPTURE_FREES
#define MAXCAPTRUEFREE 15
static struct { int EnemyId; int ItemId[15]; } NeedEnemy[1];
#else
static struct { int EnemyId; int ItemId; } NeedEnemy[1];
#endif
'''

CHECKS = r'''
#include "stoneage_capture_observation_impl.h"
static void expect(int ok,const char *message) { if(!ok) { fprintf(stderr,"%s\n",message); exit(1); } }
int main(void) {
    char *quote, saved_data[sizeof(data)], saved_items[sizeof(items)], saved_pets[sizeof(pets)];
    int i;
    for(i=0;i<20;i++) { items[i]=-1; enemies[i]=-1; }
    for(i=0;i<5;i++) pets[i]=-1;
    pets[0]=3;
    data[1][CHAR_WHICHTYPE]=CHAR_TYPEPLAYER; data[1][CHAR_LV]=1;
    data[1][CHAR_WORKBATTLEMODE]=2;
    BattleArray[0].type=BATTLE_TYPE_P_vs_E; BattleArray[0].turn=7;
    enemies[10]=2; enemies[11]=4;
    data[2][CHAR_WHICHTYPE]=CHAR_TYPEENEMY; data[2][CHAR_PETID]=113;
    data[2][CHAR_LV]=1; data[2][CHAR_HP]=20; data[2][CHAR_BASEIMAGENUMBER]=100113;
    data[2][CHAR_WORK_PETFLG]=1; data[4][CHAR_WHICHTYPE]=CHAR_TYPEPLAYER;
    NeedEnemy[0].EnemyId=113;
#ifdef _CAPTURE_FREES
    for(i=0;i<15;i++) NeedEnemy[0].ItemId[i]=-1;
    NeedEnemy[0].ItemId[0]=1810;
#else
    NeedEnemy[0].ItemId=1810;
#endif
    items[1]=1810; items[5]=1810; items[7]=1810;
    memcpy(saved_data,data,sizeof(data)); memcpy(saved_items,items,sizeof(items)); memcpy(saved_pets,pets,sizeof(pets));
    quote=StoneAge_CaptureObservation(1,"0123456789abcdef");
    expect(quote&&strstr(quote,"|request=0123456789abcdef|active=1|battle=0|turn=7|self=0|free=4"),"scope/clock missing");
#ifdef _CAPTURE_FREES
    expect(strstr(quote,"|target=10,113,1,100113,1,1810,1:1810;5:1810;7:1810")!=NULL,"all equipped/backpack matching slots must be quoted");
#else
    expect(strstr(quote,"|target=10,113,1,100113,1,1810,5:1810;7:1810")!=NULL,"legacy capture consumes matching backpack slots");
#endif
    expect(!strstr(quote,"|target=11,")&&!private_reads,"other player private data read");
    expect(!memcmp(saved_data,data,sizeof(data))&&!memcmp(saved_items,items,sizeof(items))&&!memcmp(saved_pets,pets,sizeof(pets)),"observation mutated game state");
    items[1]=items[5]=items[7]=-1;
    quote=StoneAge_CaptureObservation(1,NULL);
    expect(strstr(quote,"|target=10,113,1,100113,0,1810,-")!=NULL,"missing required items reported eligible");
    NeedEnemy[0].EnemyId=999; /* live configuration differs from defaults */
    quote=StoneAge_CaptureObservation(1,NULL);
    expect(strstr(quote,"|target=10,113,1,100113,1,-,-")!=NULL,"quote ignored the actual loaded requirement table");
    data[2][CHAR_LV]=7;
    quote=StoneAge_CaptureObservation(1,NULL);
    expect(strstr(quote,"|target=10,113,7,100113,0,-,-")!=NULL,"capture level limit ignored");
    data[1][CHAR_PickAllPet]=TRUE;
    quote=StoneAge_CaptureObservation(1,NULL);
    expect(strstr(quote,"|target=10,113,7,100113,1,-,-")!=NULL,"native level privilege mismatched");
    for(i=0;i<5;i++) pets[i]=3;
    quote=StoneAge_CaptureObservation(1,NULL);
    expect(strstr(quote,"|free=0")&&strstr(quote,"|target=10,113,7,100113,0,-,-"),"full pet slots ignored");
    BattleArray[0].type=2;
    expect(!strcmp(StoneAge_CaptureObservation(1,NULL),"BCAP|v=1|active=0"),"PvP leaked capture state");
    expect(!StoneAge_CaptureObservation(1,"bad")&&!StoneAge_CaptureObservation(0,NULL),"invalid identity or request accepted");
    puts("native capture quote parity and read-only checks passed");
    return 0;
}
'''

def main():
    source = SOURCE.read_bytes().decode("latin1")
    code = PRELUDE + function(source, "IsNeedCaptureItem") + "\n" + function(source, "BATTLE_CaptureItemCheck") + CHECKS
    with tempfile.TemporaryDirectory(prefix="stoneage-capture-") as directory:
        path = Path(directory)
        (path / "test.c").write_text(code)
        for flags in [[], ["-D_CAPTURE_FREES"]]:
            binary = path / "test"
            subprocess.run([os.environ.get("CC", "cc"), "-std=c99", *flags, "-I", str(MODERN), str(path / "test.c"), "-o", str(binary)], check=True)
            subprocess.run([str(binary)], check=True)

if __name__ == "__main__":
    main()
