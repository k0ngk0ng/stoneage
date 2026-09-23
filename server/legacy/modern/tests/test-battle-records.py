#!/usr/bin/env python3
"""Exercise native recording, durable output, privacy and training extraction."""
from pathlib import Path
import importlib.util
import json
import os
import re
import shutil
import subprocess
import tempfile
from types import SimpleNamespace

ROOT = Path(__file__).resolve().parents[4]
MODERN = ROOT / "server/legacy/modern"
BUILD = ROOT / "build/battle-records/tests"
BUILD.mkdir(parents=True, exist_ok=True)
spec = importlib.util.spec_from_file_location("battle_export", ROOT / "bin/battle-export.py")
exporter = importlib.util.module_from_spec(spec)
spec.loader.exec_module(exporter)

HARNESS = r'''
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include "fixture.h"
#include "stoneage_battle_record.h"
#include "stoneage_battle_log.h"
BATTLE battles[4];
BATTLE *BattleArray = battles;
int BATTLE_battlenum = 4;
static int data[4][256], work[4][256];
int CHAR_getInt(int c,int f) { return data[c][f]; }
int CHAR_getWorkInt(int c,int f) { return work[c][f]; }
int CHAR_getFlg(int c,int f) { (void)f; return data[c][CHAR_HP] <= 0; }
int CHAR_getCharPet(int c,int slot) { (void)c;(void)slot;return -1; }
int CHAR_getItemIndex(int c,int slot) { (void)c;(void)slot;return -1; }
int CHAR_getPetSkill(int c,int slot) { (void)c;(void)slot;return -1; }
int ITEM_getInt(int i,int f) { (void)i;(void)f;return 0; }
int PETSKILL_getInt(int i,int f) { (void)i;(void)f;return 0; }
int PETSKILL_getPetskillArray(int i) { return i; }
const char *StoneAge_CharacterIdentityGetLoadedByIndex(int c) { (void)c;return "pc1_0123456789abcdef0123456789abcdef"; }
const char *StoneAge_BattleDatasetContext(void) { return "null"; }
static void setup(int slot,int pve)
{
 int s,i;
 memset(&battles[slot],0,sizeof(BATTLE));
 battles[slot].type=pve?BATTLE_TYPE_P_vs_E:BATTLE_TYPE_P_vs_P;
 for(s=0;s<2;s++) for(i=0;i<10;i++) battles[slot].Side[s].Entry[i].charaindex=-1;
 for(s=0;s<2;s++) {
   battles[slot].Side[s].Entry[0].charaindex=s;
   battles[slot].Side[s].Entry[0].bid=s*10;
   data[s][CHAR_WHICHTYPE]=(pve&&s==1)?CHAR_TYPEENEMY:CHAR_TYPEPLAYER;
   data[s][CHAR_HP]=100;work[s][CHAR_WORKMAXHP]=100;
   data[s][CHAR_MP]=100;work[s][CHAR_WORKMAXMP]=100;
   data[s][CHAR_VITAL]=3000;data[s][CHAR_STR]=3000;data[s][CHAR_TOUGH]=3000;data[s][CHAR_DEX]=3000;
   data[s][CHAR_LV]=35;data[s][CHAR_DEFAULTPET]=-1;data[s][CHAR_RIDEPET]=-1;
   work[s][CHAR_WORKBATTLEINDEX]=slot;
 }
 StoneAge_BattleRecordBegin(slot);
}
static void observe(int slot,int terminal)
{
 const char *alive="BC|0|0|private-name|secret-title|186A0|23|64|64|1|0||0|0|0|A|\x81\x82|title|186A0|23|64|64|1|0||0|0|0|";
 const char *dead="BC|0|0|private-name|secret-title|186A0|23|64|64|1|0||0|0|0|A|other-name||186A0|23|0|64|3|0||0|0|0|";
 StoneAge_BattleRecordObservation(slot,0,"BP|0|0|64",terminal?dead:alive);
 if(battles[slot].type==BATTLE_TYPE_P_vs_P) StoneAge_BattleRecordObservation(slot,1,"BP|A|0|64",terminal?dead:alive);
}
int main(int argc,char **argv)
{
 unsigned long request;
 int i;
 StoneAge_BattleLogOffline();
 StoneAge_BattleRecordInit();
 setup(0,0);observe(0,0);
 request=StoneAge_BattleRecordRequest(0,"H|A",0);StoneAge_BattleRecordDispatch(0,request);
 request=StoneAge_BattleRecordRequest(1,"I|2|A",0);StoneAge_BattleRecordDispatch(1,request);
 battles[0].turn=1;StoneAge_BattleRecordTurn(0,0);
 data[1][CHAR_HP]=0;StoneAge_BattleRecordTurn(0,1);observe(0,1);
 battles[0].winside=-1;StoneAge_BattleRecordEnd(0,1);StoneAge_BattleRecordEnd(0,0);
 setup(0,1);observe(0,0);battles[0].turn=1;StoneAge_BattleRecordTurn(0,0);
 data[1][CHAR_HP]=0;StoneAge_BattleRecordTurn(0,1);observe(0,1);
 battles[0].winside=-1;StoneAge_BattleRecordEnd(0,1);
 setup(1,0);observe(1,0);
 /* Untrusted payload must not escape into the dataset. */
 StoneAge_BattleRecordRequest(0,"H|password-secret\"\n",0);
 StoneAge_BattleRecordExit(1,0,"timeout");StoneAge_BattleRecordEnd(1,0);
 /* Partial match has no terminal marker and must be excluded by exporters. */
 setup(2,0);observe(2,0);
 /* Exercise bounded queue rollover and offline backpressure under load. */
 if(argc>1 && !strcmp(argv[1],"stress")) for(i=0;i<5000;i++) {
   request=StoneAge_BattleRecordRequest(0,"G",0);StoneAge_BattleRecordDispatch(0,request);
 }
 StoneAge_BattleLogShutdown();
 return 0;
}
'''

with tempfile.TemporaryDirectory(prefix="fixture-", dir=BUILD) as temporary:
    work = Path(temporary)
    include = work / "include"
    include.mkdir()
    env = dict(os.environ, TMPDIR=str(work), STONEAGE_BATTLE_RECORD_DIR=str(work / "records"),
               STONEAGE_BATTLE_RECORD_MIN_FREE_BYTES="0", STONEAGE_RELEASE_VERSION="test-v1",
               STONEAGE_BATTLE_RULESET_ID="fixture-rules")
    for module in ("stoneage_battle_record", "stoneage_battle_dataset"):
        subprocess.run(["cc", "-std=gnu89", "-D_FORTIFY_SOURCE=0", "-fsyntax-only",
                        "-I"+str(ROOT/"server/legacy/source/2.5/gmsv/include"), "-I"+str(MODERN),
                        str(MODERN/(module+".c"))], env=env, check=True)
    text = (MODERN/"stoneage_battle_record.c").read_text()
    constants = sorted(set(re.findall(r"\b(?:CHAR|ITEM|PETSKILL)_[A-Z][A-Z0-9_]+\b", text)) -
                       {"CHAR_CHECKINDEX", "ITEM_CHECKINDEX", "PETSKILL_CHECKINDEX", "CHAR_MAXPETHAVE", "CHAR_MAXITEMHAVE", "CHAR_MAXPETSKILLHAVE", "CHAR_STARTITEMARRAY"})
    header = "#ifndef FIXTURE_H\n#define FIXTURE_H\n" + "enum {"+",".join(constants)+"};\n" + r'''
#define CHAR_CHECKINDEX(c) ((c)>=0 && (c)<4)
#define ITEM_CHECKINDEX(c) ((c)>=0)
#define PETSKILL_CHECKINDEX(c) ((c)>=0)
#define CHAR_MAXPETHAVE 5
#define CHAR_MAXITEMHAVE 20
#define CHAR_MAXPETSKILLHAVE 7
#define CHAR_STARTITEMARRAY 5
#define BATTLE_ENTRY_MAX 10
#define BATTLE_TYPE_P_vs_P 2
#define BATTLE_TYPE_P_vs_E 1
#define BATTLE_CHECKINDEX(b) ((b)>=0 && (b)<4)
typedef struct {int charaindex,bid;} BATTLE_ENTRY;
typedef struct {int type,field_no,turn,winside;struct {BATTLE_ENTRY Entry[10];} Side[2];} BATTLE;
extern BATTLE *BattleArray;extern int BATTLE_battlenum;
int CHAR_getInt(int,int);int CHAR_getWorkInt(int,int);int CHAR_getFlg(int,int);
int CHAR_getCharPet(int,int);int CHAR_getItemIndex(int,int);int CHAR_getPetSkill(int,int);
int ITEM_getInt(int,int);int PETSKILL_getInt(int,int);int PETSKILL_getPetskillArray(int);
#endif
'''
    # TYPEENEMY appears in fixture only.
    header = header.replace("enum {", "enum {CHAR_TYPEENEMY=100,")
    (include/"fixture.h").write_text(header)
    for name in ("version.h", "char.h", "char_base.h", "battle.h", "item.h", "pet_skill.h"):
        (include/name).write_text('#include "fixture.h"\n')
    (work/"fixture.c").write_text(HARNESS)
    subprocess.run(["cc", "-std=gnu89", "-pthread", "-Wall", "-Wextra", "-Wno-sign-compare",
                    "-I"+str(include), "-I"+str(MODERN), str(work/"fixture.c"),
                    str(MODERN/"stoneage_battle_record.c"), str(MODERN/"stoneage_battle_log.c"),
                    "-o", str(work/"fixture")], env=env, check=True)
    subprocess.run([str(work/"fixture"), "stress"], env=env, check=True, timeout=60)
    dirs = sorted((work/"records").glob("????-??-??/*"))
    assert len(dirs) == 4
    all_text = "".join(p.read_text() for p in (work/"records").rglob("*.json*"))
    assert not any(secret in all_text for secret in ("private-name", "secret-title", "password-secret", "other-name"))
    args = SimpleNamespace(mode="pvp-1v1", format="transitions", equal_points=True, include_abnormal=False)
    by_mode = {}
    for d in dirs:
        if not (d/"result.json").exists():
            continue
        m, events, result = exporter.load_match(d)
        by_mode.setdefault(m["mode"], []).append((d,m,events,result))
    d,m,events,result = by_mode["pvp"][0]
    rows = exporter.export_match(m,events,result,args)
    assert len(rows)==2 and [r["reward"] for r in rows]==[1,-1]
    assert rows[0]["submitted_actions"][0]["opcode"]=="H" and rows[1]["submitted_actions"][0]["arg1"]==2
    assert all(r["terminated"] and not r["truncated"] for r in rows)
    assert all("resolution_commands" not in r["observation"] for r in rows)
    allocation = exporter.allocation(rows[0]["observation"])
    assert allocation["budget_raw"]==12000 and allocation["strength_raw"]==3000
    args.format="builds"
    builds=exporter.export_match(m,events,result,args)
    assert len(builds)==1 and builds[0]["fairness"]["equal_points"]
    altered=json.loads(json.dumps(events))
    a=next(e for e in altered if e["type"]=="allocation" and e["side"]==1)
    a["strength_raw"]+=100;a["allocated_raw"]+=100;a["budget_raw"]+=100
    assert exporter.export_match(m,altered,result,args)==[]
    # A level advantage is excluded even with the same raw point budget.
    altered=json.loads(json.dumps(events));next(e for e in altered if e["type"]=="allocation" and e["side"]==1)["level"]+=1
    assert exporter.export_match(m,altered,result,args)==[]
    args.format="transitions";args.mode="all";args.equal_points=False
    _,pm,pe,pr=by_mode["pve"][0]
    assert len(exporter.export_match(pm,pe,pr,args))==1
    # Sequence loss must fail closed, including a surviving terminal marker.
    path=d/"events.jsonl";original=path.read_text();path.write_text("\n".join(original.splitlines()[1:])+"\n")
    try: exporter.load_match(d)
    except exporter.InvalidMatch: pass
    else: raise AssertionError("sequence gap accepted")
    path.write_text(original)
    # A second process must not overwrite first-process match IDs.
    subprocess.run([str(work/"fixture")],env=env,check=True,timeout=30)
    assert len(list((work/"records").glob("????-??-??/*")))==8
    # Storage failure must not crash/block gameplay or certify completion.
    blocked=work/"blocked";blocked.write_text("not a directory")
    bad_env=dict(env,STONEAGE_BATTLE_RECORD_DIR=str(blocked))
    subprocess.run([str(work/"fixture")],env=bad_env,check=True,timeout=30)
    low_env=dict(env,STONEAGE_BATTLE_RECORD_DIR=str(work/"low-space"),STONEAGE_BATTLE_RECORD_MIN_FREE_BYTES=str(2**63))
    subprocess.run([str(work/"fixture")],env=low_env,check=True,timeout=30)
    assert not list((work/"low-space").glob("????-??-??/*/result.json"))
print("Battle record, equal-budget filtering and training-export tests passed")
