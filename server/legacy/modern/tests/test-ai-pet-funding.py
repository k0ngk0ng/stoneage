#!/usr/bin/env python3
"""Exercise the patched pet storage handler and lost-pet commit block."""
from pathlib import Path
import os
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[4]
MODERN = ROOT / "server/legacy/modern"
SOURCE = ROOT / "server/legacy/source/2.5/gmsv"

HARNESS = r'''
#include <assert.h>
#include <stdio.h>
#include <string.h>
#define FALSE 0
#define TRUE 1
#define CHAR_MAXPETHAVE 5
enum { CHAR_WORKSHOPRELEVANT, CHAR_RIDEPET, CHAR_DEFAULTPET, CHAR_HP,
       CHAR_WORKMAXHP, CHAR_WORKPLAYERINDEX, CHAR_OWNERCDKEY, CHAR_OWNERCHARANAME,
       CHAR_P_STRING_GOLD, CHAR_COLORYELLOW, NPC_PETSHOP_MSG_POOLTHANKS };
static int pet, pool, defaultpet, riding, space, accepted, charges, alive;
static int destroyed, updates, parse_ok, alloc_ok, backup_ok, backups;
static char petstring[1][64];
static int getfdFromCharaIndex(int c) { (void)c; return 1; }
static int CHAR_getWorkInt(int c,int k) { (void)c; return k==CHAR_WORKSHOPRELEVANT ? 1 : 100; }
static int CHAR_getInt(int c,int k) { (void)c; return k==CHAR_RIDEPET ? riding : defaultpet; }
static int CHAR_getCharPet(int c,int slot) { (void)c; return slot==0 ? pet : -1; }
static int CHAR_CHECKINDEX(int c) { return c==1 || (c==2 && alive); }
static int CHAR_getCharPoolPetElement(int c) { (void)c; return space; }
#define NPC_GETPOOLCOST(c) 100
static int StoneAge_AIFundingCharge(int c,int cost) {
    (void)c; assert(cost==100); assert(pool==-1 && defaultpet==0 && !updates);
    assert(petstring[0][0]);
    if(!accepted) return 0;
    charges++; return 1;
}
static void CHAR_setInt(int c,int k,int v) {
    (void)c; assert(charges==1); if(k==CHAR_DEFAULTPET) defaultpet=v;
}
static void CHAR_setCharPoolPet(int c,int slot,int id) {
    (void)c; (void)slot; assert(charges==1); pool=id;
}
static void CHAR_setCharPet(int c,int slot,int id) {
    (void)c; (void)slot; assert(charges==1); pet=id;
}
#define lssproto_KS_send(...) (updates++)
#define CHAR_send_P_StatusString(...) (updates++)
#define CHAR_sendStatusString(...) (updates++)
#define NPC_MaxGoldOver(...) ((void)0)
#define fprint(...) ((void)0)
#define print(...) ((void)0)
#define LogPet(...) ((void)0)
#define CHAR_talkToCli(...) ((void)0)
#define CHAR_complianceParameter(...) ((void)0)
#define CHAR_setWorkInt(...) ((void)0)
#define CHAR_setChar(...) ((void)0)
#define CHAR_getUseName(...) "pet"
static int CHAR_makePetFromStringToArg(char *s,int *ch,int n) {
    (void)s; (void)ch; (void)n; return parse_ok;
}
static int PET_initCharOneArray(int *ch) { (void)ch; alive=alloc_ok; return alloc_ok ? 2 : -1; }
static void CHAR_endCharOneArray(int c) { assert(c==2 && alive); alive=0; destroyed++; }
static int NPC_backupLostPetString(int c) {
    (void)c; assert(charges==1 && pet==2 && !petstring[0][0]); backups++; return backup_ok;
}
__STORAGE__
static void recover_pet(void) {
    int ret, ch=0, toindex=1, ti=1, cost=100, havepetelement=0, i;
    char petstring1[64]="pet", msgbuf[64];
    __RECOVERY__
}
static void reset(void) {
    pet=2; pool=-1; defaultpet=0; riding=-1; space=0; accepted=1;
    charges=destroyed=updates=backups=0; alive=parse_ok=alloc_ok=backup_ok=1;
    strcpy(petstring[0],"recoverable original record");
}
int main(void) {
    char token[256];
    reset(); accepted=0; NPC_PetDel2(0,1,0,token);
    assert(pet==2 && pool==-1 && defaultpet==0 && !updates && !charges);
    reset(); NPC_PetDel2(0,1,0,token);
    assert(pet==-1 && pool==2 && defaultpet==-1 && charges==1 && updates);
    reset(); riding=0; NPC_PetDel2(0,1,0,token); assert(!charges && pet==2);
    reset(); space=-1; NPC_PetDel2(0,1,0,token); assert(!charges && pet==2);
    reset(); pet=-1; accepted=0; recover_pet();
    assert(pet==-1 && !alive && destroyed==1 && !updates && !backups && petstring[0][0]);
    reset(); pet=-1; parse_ok=0; recover_pet();
    assert(pet==-1 && !charges && !backups && petstring[0][0]);
    reset(); pet=-1; alloc_ok=0; recover_pet();
    assert(pet==-1 && !charges && !backups && petstring[0][0]);
    reset(); pet=-1; recover_pet();
    assert(pet==2 && alive && charges==1 && backups==1 && !petstring[0][0] && updates);
    puts("pet funding commit checks passed"); return 0;
}
'''


def main():
    scratch = ROOT / "build/ai"
    scratch.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="pet-funding-", dir=scratch) as tmp:
        stage = Path(tmp)
        patch = (MODERN / "patches/0016-ai-pet-funding.patch").read_bytes()
        names = [line[6:].decode() for line in patch.splitlines() if line.startswith(b"--- a/")]
        for name in names:
            target = stage / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes((SOURCE / name).read_bytes())
        sections = (MODERN / "patches/0012-ai-funding.patch").read_bytes().split(b"--- a/")[1:]
        base = b"".join(b"--- a/" + part for part in sections if part.splitlines()[0].decode() in names)
        for data in (base, patch):
            subprocess.run(["patch", "-p1", "--fuzz=0"], cwd=stage, input=data, check=True)
        shop = (stage / "npc/npc_petshop.c").read_bytes().decode("latin1")
        start = shop.index("void NPC_PetDel2( int meindex, int talker, int select, char *token)\n{")
        end = shop.index("\nvoid NPC_PetDel3(", start)
        lost = (stage / "npc/npc_newnpcman.c").read_bytes().decode("latin1")
        first = lost.index("ret = CHAR_makePetFromStringToArg( petstring1, &ch, -2);")
        last = lost.index("\n \t\t}\n#endif", first)
        program = stage / "pet.c"
        program.write_text(HARNESS.replace("__STORAGE__", shop[start:end])
                           .replace("__RECOVERY__", lost[first:last]))
        binary = stage / "pet"
        subprocess.run(["cc", "-std=c99", "-Wall", "-Wextra", "-Werror",
                        "-Wno-unused-parameter", str(program), "-o", str(binary)],
                       check=True, env={**os.environ, "TMPDIR": str(stage)})
        subprocess.run([str(binary)], check=True)


if __name__ == "__main__":
    main()
