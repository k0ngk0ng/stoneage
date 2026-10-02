#!/usr/bin/env python3
"""Exercise the character-scoped, read-only AI observation response."""

from pathlib import Path
import os
import subprocess
import tempfile


ROOT = Path(__file__).resolve().parents[4]
MODULE = ROOT / "server/legacy/modern/stoneage_ai_observation.c"


HARNESS = r'''
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include "char_base.h"
#include "item.h"
#include "util.h"
#include "stoneage_ai_observation.h"

#define INDEX_MAX 16

static int failures;
static const char *loaded_identity;
const char *StoneAge_CharacterIdentityGetLoadedByIndex(int index)
{ return index == 0 ? loaded_identity : NULL; }
int harness_use[INDEX_MAX];
static int harness_pet[INDEX_MAX][5];
static int harness_level[INDEX_MAX];
static int harness_savepoint[INDEX_MAX];
static int harness_party_mode[INDEX_MAX];
static int harness_standby_mask[INDEX_MAX];
static int harness_summon[INDEX_MAX][5];
static int harness_item_index[INDEX_MAX][20];
static int harness_item_use[64];
static int harness_item_id[64];
static int harness_data[INDEX_MAX][64];
static const char *harness_unique[INDEX_MAX];
static int int_reads;
static int char_reads;
static int pet_reads;
static int item_slot_reads;
static int item_id_reads;
static int forbidden_cdkey_reads;
static int other_role_reads;

char *CHAR_getChar(int index, int element)
{
    char_reads++;
    if( index != 0 && index != 10 && index != 11 ) other_role_reads++;
    if( element == CHAR_CDKEY ) forbidden_cdkey_reads++;
    if( element != CHAR_UNIQUECODE ) return (char *)"account-secret";
    return (char *)harness_unique[index];
}

int CHAR_getInt(int index, int element)
{
    int_reads++;
    if( index != 0 && index != 10 && index != 11 ) other_role_reads++;
    if( element == CHAR_LV ) return harness_level[index];
    if( element == CHAR_SAVEPOINT ) return harness_savepoint[index];
    return harness_data[index][element];
}

int CHAR_getWorkInt(int index, int element)
{
    if( index != 0 ) other_role_reads++;
    if( element >= CHAR_WORK_PET0_STAT && element < CHAR_WORK_PET0_STAT+5 ) return harness_summon[index][element-CHAR_WORK_PET0_STAT];
    if( element == CHAR_WORKSTANDBYPET ) return harness_standby_mask[index];
    if( element != CHAR_WORKPARTYMODE ) return -1;
    return harness_party_mode[index];
}

int CHAR_getCharPet(int index, int slot)
{
    pet_reads++;
    if( index != 0 ) other_role_reads++;
    if( slot < 0 || slot >= 5 ) return -1;
    return harness_pet[index][slot];
}

int CHAR_getItemIndex(int index, int slot)
{
    item_slot_reads++;
    if( index != 0 ) other_role_reads++;
    if( index < 0 || index >= INDEX_MAX || slot < 0 || slot >= 20 ) return -1;
    return harness_item_index[index][slot];
}

int harness_item_check(int index)
{
    return index >= 0 && index < 64 && harness_item_use[index];
}

int harness_item_get_int(int index, int element)
{
    item_id_reads++;
    if( (element != ITEM_ID && element != ITEM_MAGICID) || index < 0 || index >= 64 ||
        !harness_item_use[index] ) return 0;
    return harness_item_id[index];
}

char *makeEscapeString(char *src, char *dest, int sizeofdest)
{
    int i;
    int at = 0;
    if( src == NULL || dest == NULL || sizeofdest <= 0 ) return NULL;
    for( i = 0; src[i] != '\0' && at + 2 < sizeofdest; i++ ) {
        char escaped = '\0';
        if( src[i] == '\n' ) escaped = 'n';
        else if( src[i] == ',' ) escaped = 'c';
        else if( src[i] == '|' ) escaped = 'z';
        else if( src[i] == '\\' ) escaped = 'y';
        if( escaped != '\0' ) {
            dest[at++] = '\\';
            dest[at++] = escaped;
        } else {
            dest[at++] = src[i];
        }
    }
    dest[at] = '\0';
    return dest;
}

static void expect(int condition, const char *message)
{
    if( !condition ) {
        fprintf(stderr, "failed: %s\n", message);
        failures++;
    }
}

static void expect_text(const char *actual, const char *needle,
                        const char *message)
{
    expect(actual != NULL && strstr(actual, needle) != NULL, message);
}

static void configure(void)
{
    int i;
    int j;
    for( i = 0; i < INDEX_MAX; i++ ) {
        harness_use[i] = 1;
        harness_unique[i] = NULL;
        harness_level[i] = 0;
        for( j = 0; j < 20; j++ ) harness_item_index[i][j] = -1;
        for( j = 0; j < 64; j++ ) harness_data[i][j] = 0;
        for( j = 0; j < 5; j++ ) harness_pet[i][j] = -1;
    }
    for( i = 0; i < 64; i++ ) {
        harness_item_use[i] = 0;
        harness_item_id[i] = 0;
    }

    harness_pet[0][0] = 10;
    harness_pet[0][2] = 11;
    /* Deliberately repeat an underlying pet index in another slot.  Slots
     * are still emitted once each and the client can deterministically
     * deduplicate/rebuild by slot. */
    harness_pet[0][4] = 10;
    harness_level[10] = 37;
    harness_level[11] = 8;
    harness_savepoint[0] = 123;
    harness_item_index[0][3] = 22;
    harness_item_use[22] = 1;
    harness_item_id[22] = 701;
    harness_item_index[0][5] = 20;
    harness_item_index[0][6] = 21;
    harness_item_use[20] = 1;
    harness_item_use[21] = 1;
    harness_item_id[20] = 2415;
    harness_item_id[21] = 2414;
    harness_unique[10] = "pet,|\\id";
    harness_unique[11] = NULL;
    harness_unique[2] = "other-role-secret";

    for( i = 0; i < 6; i++ ) {
        harness_data[0][CHAR_ENDEVENT + i] = 100 + i;
        harness_data[0][CHAR_NOWEVENT + i] = 200 + i;
    }
    harness_data[0][CHAR_LEARNRIDE] = 120;
    harness_data[0][CHAR_SKILLUPPOINT] = 7;
}

static int count_text(const char *text, const char *needle)
{
    int count = 0;
    const char *cursor = text;
    size_t length = strlen(needle);
    while( (cursor = strstr(cursor, needle)) != NULL ) {
        count++;
        cursor += length;
    }
    return count;
}

int main(void)
{
    int before_int[64];
    int before_pet[5];
    int before_savepoint;
    int before_item_index[20];
    int i;
    static char long_unique[300];
    static char escape_full_unique[256];
    char first[4096];
    char *observation;

    configure();
    for( i = 0; i < 64; i++ ) before_int[i] = harness_data[0][i];
    for( i = 0; i < 5; i++ ) before_pet[i] = harness_pet[0][i];
    before_savepoint = harness_savepoint[0];
    for( i = 0; i < 20; i++ ) before_item_index[i] = harness_item_index[0][i];

    observation = StoneAge_AIObservationMake(0);
    expect(observation != NULL, "valid character returns observation");
    if( observation != NULL ) {
        strncpy(first, observation, sizeof(first) - 1);
        first[sizeof(first) - 1] = '\0';
        expect_text(first, "AI|v=1|chara=0", "version and safe numeric role id");
        expect_text(first, "|pet=0,pet\\c\\z\\yid,37", "legacy unique escaping");
        expect_text(first, "|pet=2,unknown,8", "missing unique is unknown");
        expect_text(first, "|end=100,101,102,103,104,105", "six end flags");
        expect_text(first, "|now=200,201,202,203,204,205", "six now flags");
        expect_text(first, "|ride=120", "learn ride flag");
        expect_text(first, "|sp=123", "save point value");
        expect_text(first, "|stat_points=7", "caller unspent stat points");
        expect_text(first, "|party_mode=0", "caller solo mode");
        expect_text(first, "|active_pet=0", "caller battle pet selection");
        expect_text(first, "|standby_pet_mask=0", "known empty standby list");
        expect_text(first, "|items=5,2415;6,2414|equipment=3,701", "equipment extension preserves legacy backpack format");
        expect(count_text(first, "|pet=") == 3, "repeated underlying pet has no repeated slot field");
        expect(strstr(first, "account-secret") == NULL, "account secret is absent");
        expect(strstr(first, "other-role-secret") == NULL, "other role data is absent");
    }
    expect(harness_savepoint[0] == before_savepoint,
           "save point remains read-only");
    expect(memcmp(before_item_index, harness_item_index[0], sizeof(before_item_index)) == 0,
           "backpack slots remain read-only");

    observation = StoneAge_AIObservationMakeWithRequest(0, "0123456789abcdef");
    expect_text(observation, "|request=0123456789abcdef|", "request correlation is echoed");
    expect(StoneAge_AIObservationMakeWithRequest(0, "short") == NULL, "short request rejected");
    expect(StoneAge_AIObservationMakeWithRequest(0, "0123456789abcde|") == NULL, "non-hex request rejected");
    observation = StoneAge_AIObservationMake(0);
    expect(strstr(observation, "|request=") == NULL, "legacy query does not retain old request");

    harness_savepoint[0] = 0;
    observation = StoneAge_AIObservationMake(0);
    expect_text(observation, "|sp=0", "zero save point value is emitted");
    harness_data[0][CHAR_SKILLUPPOINT] = 0;
    harness_party_mode[0] = 2;
    observation = StoneAge_AIObservationMake(0);
    expect_text(observation, "|stat_points=0", "zero unspent points are authoritative");
    expect_text(observation, "|party_mode=2", "caller party member mode");
    harness_party_mode[0] = 1;
    observation = StoneAge_AIObservationMake(0);
    expect_text(observation, "|party_mode=1", "caller party leader mode");
    harness_data[0][CHAR_SKILLUPPOINT] = 7;
    harness_standby_mask[0] = 5;
    harness_summon[0][0] = 1;harness_summon[0][3] = 1;harness_summon[0][4] = 2;
    observation = StoneAge_AIObservationMake(0);
    expect_text(observation, "|standby_pet_mask=5", "caller standby mask is refreshed");
    expect(harness_standby_mask[0] == 5, "standby selection remains read-only");
    expect_text(observation, "|summon_pet_mask=9", "only actual PET_STAT_SELECT flags are summonable");
    expect(harness_summon[0][0] == 1 && harness_summon[0][3] == 1 && harness_summon[0][4] == 2, "summon status remains read-only");
    harness_data[0][CHAR_DEFAULTPET] = -1;
    observation = StoneAge_AIObservationMake(0);
    expect_text(observation, "|active_pet=-1", "no battle pet is explicit");
    harness_data[0][CHAR_DEFAULTPET] = 2;
    observation = StoneAge_AIObservationMake(0);
    expect_text(observation, "|active_pet=2", "changed battle pet is refreshed");
    harness_data[0][CHAR_DEFAULTPET] = 0;

    harness_savepoint[0] = -2147483648;
    observation = StoneAge_AIObservationMake(0);
    expect_text(observation, "|sp=-2147483648", "signed bit31 save point value is emitted");

    harness_item_index[0][3] = -1;
    harness_item_index[0][5] = -1;
    harness_item_index[0][6] = -1;
    observation = StoneAge_AIObservationMake(0);
    expect_text(observation, "|items=none", "empty backpack is explicit");

    expect(memcmp(before_int, harness_data[0], sizeof(before_int)) == 0,
           "role integer fields remain read-only");
    expect(memcmp(before_pet, harness_pet[0], sizeof(before_pet)) == 0,
           "role pet slots remain read-only");
    expect(forbidden_cdkey_reads == 0, "CDKEY is never read");
    expect(other_role_reads == 0, "only current role and its pets are read");
    expect(int_reads > 0 && char_reads > 0 && pet_reads >= 5 &&
               item_slot_reads >= 20 && item_id_reads >= 3,
           "reads stay within the expected observation accessors");

    for( i = 0; i < (int)sizeof(long_unique) - 1; i++ ) long_unique[i] = 'x';
    long_unique[sizeof(long_unique) - 1] = '\0';
    harness_unique[10] = long_unique;
    observation = StoneAge_AIObservationMake(0);
    expect_text(observation, "|pet=0,unknown,37",
                "oversized unique code is not truncated into an identity");

    for( i = 0; i < (int)sizeof(escape_full_unique) - 1; i++ ) {
        escape_full_unique[i] = ',';
    }
    escape_full_unique[sizeof(escape_full_unique) - 1] = '\0';
    harness_unique[10] = escape_full_unique;
    observation = StoneAge_AIObservationMake(0);
    expect(observation != NULL && count_text(observation, "\\c") == 255 * 2,
           "escape output is complete at scratch-buffer capacity");

    loaded_identity = NULL;
    observation = StoneAge_AIObservationMake(0);
    expect(strstr(observation, "|character_id=") == NULL, "pending identity omitted");
    loaded_identity = "pc1_0123456789abcdef0123456789abcdef";
    observation = StoneAge_AIObservationMake(0);
    expect_text(observation, "|character_id=pc1_0123456789abcdef0123456789abcdef",
                "loaded identity observed without account disclosure");
    loaded_identity = NULL;

    {
        char spell[128]="J1|1|8|0|1|name|memo|";
        char small[32]="J1|1|8|0|1|name|memo|";
        size_t used=strlen(small);
        harness_item_index[0][1]=1;
        harness_item_use[1]=1;
        harness_item_id[1]=10;
        StoneAge_MagicObservationAppend(0,1,spell,sizeof(spell));
        expect_text(spell,"|id=10|","owned spell ID appended to same J packet");
        StoneAge_MagicObservationAppend(0,1,small,used+4);
        expect(strlen(small)==used,"truncated spell ID omitted completely");
        strcpy(spell,"J1|1|8|0|1|name|memo|");
        StoneAge_MagicObservationAppend(0,5,spell,sizeof(spell));
        expect(strstr(spell,"id=")==NULL,"backpack slot cannot identify equipment spell");
        StoneAge_MagicObservationAppend(-1,1,spell,sizeof(spell));
        expect(strstr(spell,"id=")==NULL,"invalid character cannot identify spell");
    }

    harness_use[0] = 0;
    expect(StoneAge_AIObservationMake(0) == NULL,
           "invalid current role returns no observation");
    if( failures != 0 ) return 1;
    puts("AI observation harness passed");
    return 0;
}
'''


def main() -> None:
    ai_build = ROOT / "build/ai"
    ai_build.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="ai-observation-", dir=ai_build) as temp:
        temp_path = Path(temp)
        include = temp_path / "include"
        include.mkdir()
        (include / "version.h").write_text(
            "#define TRUE 1\n#define FALSE 0\n#define _UNIQUE_P_I 1\n#define _NEWEVENT 1\n"
        )
        (include / "char.h").write_text('#include "char_base.h"\n')
        (include / "util.h").write_text(
            "char *makeEscapeString(char *, char *, int);\n"
        )
        (include / "char_base.h").write_text(
            """#ifndef OBSERVATION_TEST_CHAR_BASE_H
#define OBSERVATION_TEST_CHAR_BASE_H
#define TRUE 1
#define FALSE 0
#define CHAR_MAXPETHAVE 5
#define CHAR_STARTITEMARRAY 5
#define CHAR_MAXITEMHAVE 20
enum {
    CHAR_LV = 1,
    CHAR_ENDEVENT = 2,
    CHAR_ENDEVENT2 = 3,
    CHAR_ENDEVENT3 = 4,
    CHAR_ENDEVENT4 = 5,
    CHAR_ENDEVENT5 = 6,
    CHAR_ENDEVENT6 = 7,
    CHAR_NOWEVENT = 8,
    CHAR_NOWEVENT2 = 9,
    CHAR_NOWEVENT3 = 10,
    CHAR_NOWEVENT4 = 11,
    CHAR_NOWEVENT5 = 12,
    CHAR_NOWEVENT6 = 13,
    CHAR_LEARNRIDE = 14,
    CHAR_SAVEPOINT = 15,
    CHAR_SKILLUPPOINT = 16,
    CHAR_WORKPARTYMODE = 17,
    CHAR_DEFAULTPET = 18,
    CHAR_WORKSTANDBYPET = 19,
    CHAR_WORK_PET0_STAT = 20,
    PET_STAT_SELECT = 1,
    CHAR_CDKEY = 90
};
enum { CHAR_UNIQUECODE = 1 };
extern int harness_use[16];
#define CHAR_CHECKINDEX(i) ((i) >= 0 && (i) < 16 && harness_use[(i)])
char *CHAR_getChar(int, int);
int CHAR_getInt(int, int);
int CHAR_getWorkInt(int, int);
int CHAR_getCharPet(int, int);
int CHAR_getItemIndex(int, int);
#endif
"""
        )
        (include / "item.h").write_text(
            """#ifndef OBSERVATION_TEST_ITEM_H
#define OBSERVATION_TEST_ITEM_H
#define ITEM_ID 0
#define ITEM_MAGICID 1
int harness_item_check(int);
int harness_item_get_int(int, int);
#define ITEM_CHECKINDEX(index) harness_item_check(index)
#define ITEM_getInt(index, element) harness_item_get_int(index, element)
#endif
"""
        )
        harness = temp_path / "observation-harness.c"
        binary = temp_path / "observation-harness"
        harness.write_text(HARNESS)
        compiler = os.environ.get("CC", "cc")
        subprocess.run(
            [
                compiler,
                "-std=gnu89",
                "-Wall",
                "-Wextra",
                "-Werror",
                "-I" + str(include),
                "-I" + str(ROOT / "server/legacy/modern"),
                "-o",
                str(binary),
                str(harness),
                str(MODULE),
            ],
            cwd=ROOT,
            check=True,
        )
        subprocess.run([str(binary)], cwd=ROOT, check=True)
    print("AI observation scope, escaping and read-only tests passed")


if __name__ == "__main__":
    main()
