#!/usr/bin/env python3
"""Exercise the native initialize_ai grammar and deterministic stat budget."""

from pathlib import Path
import os
import re
import subprocess
import tempfile


ROOT = Path(__file__).resolve().parents[4]
MODULE = ROOT / "server/legacy/modern/stoneage_player_admin.c"
TMP_ROOT = ROOT / "build/ai/tmp"


def extract_function(source: str, name: str, return_type: str) -> str:
    pattern = re.compile(
        r"static\s+" + re.escape(return_type) + r"\s+" + re.escape(name)
        + r"\s*\([^{}]*\)\s*\{",
        re.MULTILINE | re.DOTALL,
    )
    match = pattern.search(source)
    if match is None:
        raise RuntimeError(f"could not find production function {name}")
    opening = source.find("{", match.start(), match.end())
    depth = 0
    for index in range(opening, len(source)):
        if source[index] == "{":
            depth += 1
        elif source[index] == "}":
            depth -= 1
            if depth == 0:
                return source[match.start() : index + 1]
    raise RuntimeError(f"unterminated production function {name}")


def extract_function_any(source: str, name: str, return_type: str) -> str:
    try:
        return extract_function(source, name, return_type)
    except RuntimeError:
        pattern = re.compile(
            r"static\s+" + re.escape(return_type) + r"\s+" + re.escape(name)
            + r"\s*\([^{}]*\)\s*\{",
            re.MULTILINE | re.DOTALL,
        )
        match = pattern.search(source)
        if match is None:
            raise
        opening = source.find("{", match.start(), match.end())
        depth = 0
        for index in range(opening, len(source)):
            if source[index] == "{":
                depth += 1
            elif source[index] == "}":
                depth -= 1
                if depth == 0:
                    return source[match.start() : index + 1]
        raise RuntimeError(f"unterminated production function {name}")


def harness_source(source: str) -> str:
    parse_integer = extract_function(source, "StoneAgePA_parseAIInteger", "int")
    parse_integer_or_end = extract_function(
        source, "StoneAgePA_parseAIIntegerOrEnd", "int"
    )
    parse_plan = extract_function(source, "StoneAgePA_parseAIPlan", "int")
    find_default_mount = extract_function(
        source, "StoneAgePA_findDefaultMountTemplate", "int"
    )
    skill_points = extract_function(source, "StoneAgePA_aiSkillPoints", "int")
    apply_stats = extract_function_any(source, "StoneAgePA_applyAIStats", "void")
    return f"""
#include <limits.h>
#include <stdio.h>
#include <string.h>

#define TRUE 1
#define FALSE 0
#define CHAR_MAXUPLEVEL 140
#define STONEAGE_PA_MAX_AI_PETS 5
#define CHAR_MAXPETHAVE 5
#define CHAR_MAXPOOLPETHAVE 10

typedef int CHAR_DATAINT;
enum {{ CHAR_VITAL = 0, CHAR_STR, CHAR_TOUGH, CHAR_DEX,
        CHAR_LEVELUPPOINT, CHAR_SKILLUPPOINT, CHAR_OLDEXP, CHAR_EXP, CHAR_LV }};
enum {{ E_T_LIMITLEVEL = 0, E_T_IMGNUMBER = 1, E_T_PETFLG = 2 }};
enum {{ ENEMY_TEMPNO = 0 }};

typedef struct tagRidePetTable {{
    int rideNo;
    int charNo;
    int petNo;
    int petId;
}} tagRidePetTable;
static tagRidePetTable ridePetTable[3] = {{
    {{101000, 100000, 100352, 331}},
    {{101001, 100000, 100329, 309}},
    {{101002, 100005, 100352, 331}}
}};

typedef struct tagStoneAgePAResponse {{
    FILE *fp;
    size_t length;
    int failed;
}} StoneAgePAResponse;
typedef struct tagStoneAgePAInitializePet {{
    int template_id;
    int level;
    int array;
}} StoneAgePAInitializePet;
typedef struct tagStoneAgePAInitializePlan {{
    int version;
    int level;
    int weight[4];
    int mount;
    int pet_count;
    StoneAgePAInitializePet pets[STONEAGE_PA_MAX_AI_PETS];
}} StoneAgePAInitializePlan;

static int errors;
static int data[32];
static int enemy_tempnos[5] = {{100, 200, 201, 252, 253}};
static int enemy_petflags[5] = {{0, 0, 0, 1, 1}};
static int template_array(int id) {{
    int i;
    for (i = 0; i < 5; i++) if (enemy_tempnos[i] == id) return i;
    return -1;
}}
static int template_limit(int array) {{ return array == 2 ? 10 : 0; }}
static int ENEMY_getEnemyNum(void) {{ return 5; }}
static int ENEMY_getInt(int array, int element) {{
    if (element == ENEMY_TEMPNO && array >= 0 && array < 5) return enemy_tempnos[array];
    return 0;
}}
static int ENEMY_getEnemyArrayFromTempNo(int id) {{ return template_array(id); }}
static int ENEMY_CHECKINDEX(int array) {{ return array >= 0 && array < 5; }}
static int ENEMYTEMP_getEnemyTempArray(int array) {{ return array; }}
static int ENEMYTEMP_CHECKINDEX(int array) {{ return array >= 0 && array < 5; }}
static int ENEMYTEMP_getInt(int array, int element) {{
    if (element == E_T_LIMITLEVEL) return template_limit(array);
    if (element == E_T_IMGNUMBER) return array == 3 ? 100352 : (array == 4 ? 100329 : 100000);
    if (element == E_T_PETFLG) return enemy_petflags[array];
    return 0;
}}
static void StoneAgePA_responseError(StoneAgePAResponse *response,
                                     const char *code, const char *message) {{
    (void)message;
    if (response != NULL) response->failed = 1;
    (void)code;
}}
static void CHAR_setInt(int index, int element, int value) {{
    (void)index;
    if (element >= 0 && element < (int)(sizeof(data) / sizeof(data[0])))
        data[element] = value;
}}

{parse_integer}

{parse_integer_or_end}

{parse_plan}

{find_default_mount}

{skill_points}

{apply_stats}

static void expect(int condition, const char *message) {{
    if (!condition) {{
        fprintf(stderr, "initialize_ai: %s\\n", message);
        errors++;
    }}
}}

static void expect_valid(const char *payload, int level, int count) {{
    StoneAgePAResponse response;
    StoneAgePAInitializePlan plan;
    memset(&response, 0, sizeof(response));
    expect(StoneAgePA_parseAIPlan(&response, payload, &plan), payload);
    if (!response.failed) {{
        expect(plan.level == level, "level did not parse");
        expect(plan.pet_count == count, "pet count did not parse");
    }}
}}

static void expect_invalid(const char *payload) {{
    StoneAgePAResponse response;
    StoneAgePAInitializePlan plan;
    memset(&response, 0, sizeof(response));
    expect(!StoneAgePA_parseAIPlan(&response, payload, &plan),
           "malformed payload was accepted");
}}

static void test_default_mount_lookup(void) {{
    int graphic = 0;
    int template_id = StoneAgePA_findDefaultMountTemplate(100000, &graphic);
    expect(template_id == 252 && graphic == 101000,
           "default mount lookup did not follow the native ride table");
    enemy_tempnos[4] = 252;
    graphic = 0;
    template_id = StoneAgePA_findDefaultMountTemplate(100000, &graphic);
    expect(template_id == 252 && graphic == 101000,
           "duplicate template row changed the canonical default");
    enemy_tempnos[3] = 999;
    enemy_petflags[3] = 0;
    graphic = 0;
    template_id = StoneAgePA_findDefaultMountTemplate(100000, &graphic);
    expect(template_id == 252 && graphic == 101001,
           "default mount lookup did not use the canonical duplicate row");
    enemy_tempnos[3] = 252;
    enemy_tempnos[4] = 253;
    enemy_petflags[3] = 1;
}}

int main(void) {{
    StoneAgePAInitializePlan plan;
    int birth[4] = {{5, 5, 5, 5}};
    int i;

    test_default_mount_lookup();

    expect_valid("1|60|1|2|0|0|0", 60, 0);
    expect_valid("1|1|1|2|0|0|2|100:1|100:2", 1, 2);
    expect_valid("1|1|1|0|0|0|0", 1, 0);
    expect_valid("2|35|1|0|0|0|1|1|100:30", 35, 1);
    expect_invalid("1|1|0|0|0|0|0");
    expect_invalid("1|01|1|0|0|0|0");
    expect_invalid("1|1|101|0|0|0|0");
    expect_invalid("1|1|1|0|0|0|6");
    expect_invalid("1|1|1|0|0|0|0|");
    expect_invalid("2|1|1|0|0|0|2|1|100:1");
    expect_invalid("2|1|1|0|0|0|1|0");
    expect_invalid("2|1|1|0|0|0|1|1|100:0");
    expect_invalid("1|1|1|0|0|0|1|100:0");
    expect_invalid("1|1|1|0|0|0|1|201:11");
    expect_invalid("1|1|1|0|0|0|1|999:1");
    expect_invalid("1|1|1|0|0|0|1|100:1|extra");
    expect_invalid("1|2147483648|1|0|0|0|0");
    expect_invalid("1|1|1|0|0|0|1|100:\xE4");

    memset(&plan, 0, sizeof(plan));
    plan.level = 1;
    plan.weight[0] = 1;
    plan.weight[1] = 2;
    StoneAgePA_applyAIStats(0, &plan, birth);
    expect(data[CHAR_VITAL] == 700 && data[CHAR_STR] == 1300 &&
           data[CHAR_TOUGH] == 0 && data[CHAR_DEX] == 0,
           "level-one birth budget did not follow largest remainders");
    expect(data[CHAR_SKILLUPPOINT] == 0 && data[CHAR_LEVELUPPOINT] == 0,
           "allocated points were left as spendable points");

    memset(data, 0, sizeof(data));
    memset(&plan, 0, sizeof(plan));
    plan.level = 4;
    for (i = 0; i < 4; i++) plan.weight[i] = 1;
    StoneAgePA_applyAIStats(0, &plan, birth);
    expect(data[CHAR_VITAL] == 800 && data[CHAR_STR] == 700 &&
           data[CHAR_TOUGH] == 700 && data[CHAR_DEX] == 700,
           "configured upgrade budget was not allocated deterministically");
    expect(data[CHAR_LV] == 4 && data[CHAR_EXP] == 0 && data[CHAR_OLDEXP] == 0,
           "level or experience state is inconsistent");

    return errors != 0;
}}
"""


def mutation_harness_source(source: str) -> str:
    parse_integer = extract_function(source, "StoneAgePA_parseAIInteger", "int")
    parse_integer_or_end = extract_function(
        source, "StoneAgePA_parseAIIntegerOrEnd", "int"
    )
    parse_plan = extract_function(source, "StoneAgePA_parseAIPlan", "int")
    get_ride_graphic = extract_function(source, "StoneAgePA_getRideGraphic", "int")
    validate_mount = extract_function(source, "StoneAgePA_validateAIMount", "int")
    fresh = extract_function(source, "StoneAgePA_checkAIFresh", "int")
    skill_points = extract_function(source, "StoneAgePA_aiSkillPoints", "int")
    apply_stats = extract_function_any(source, "StoneAgePA_applyAIStats", "void")
    discard = extract_function_any(source, "StoneAgePA_discardAIPets", "void")
    release = extract_function_any(source, "StoneAgePA_releaseOldAIPets", "void")
    initialize = extract_function(source, "StoneAgePA_initializeAI", "int")
    return f"""
#include <limits.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#define TRUE 1
#define FALSE 0
#define CHAR_MAXUPLEVEL 140
#define CHAR_MAXPETHAVE 5
#define CHAR_MAXPOOLPETHAVE 10
#define STONEAGE_PA_MAX_AI_PETS 5

typedef int CHAR_DATAINT;
enum {{ CHAR_VITAL = 0, CHAR_STR, CHAR_TOUGH, CHAR_DEX,
        CHAR_LEVELUPPOINT, CHAR_SKILLUPPOINT, CHAR_OLDEXP, CHAR_EXP, CHAR_LV,
        CHAR_HP, CHAR_MP, CHAR_MAXMP, CHAR_DEFAULTPET, CHAR_RIDEPET,
        CHAR_WHICHTYPE, CHAR_BASEBASEIMAGENUMBER, CHAR_BASEIMAGENUMBER,
        CHAR_LEARNRIDE, CHAR_VARIABLEAI }};
enum {{ CHAR_WORKPETFOLLOW = 0, CHAR_WORKMAXHP, CHAR_WORKMAXMP, CHAR_WORKFIXAI }};
enum {{ CHAR_TYPEPLAYER = 1, CHAR_TYPEPET = 2 }};
enum {{ E_T_LIMITLEVEL = 0, E_T_IMGNUMBER = 1, E_T_PETFLG = 2 }};
#define CHAR_MAXVARIABLEAI 10000

typedef struct tagRidePetTable {{
    int rideNo;
    int charNo;
    int petNo;
    int petId;
}} tagRidePetTable;
static tagRidePetTable ridePetTable[1] = {{ {{101000, 100000, 100352, 331}} }};

typedef struct tagStoneAgePAResponse {{
    FILE *fp;
    size_t length;
    int failed;
}} StoneAgePAResponse;
typedef struct tagStoneAgePAInitializePet {{
    int template_id;
    int level;
    int array;
}} StoneAgePAInitializePet;
typedef struct tagStoneAgePAInitializePlan {{
    int version;
    int level;
    int weight[4];
    int mount;
    int pet_count;
    StoneAgePAInitializePet pets[STONEAGE_PA_MAX_AI_PETS];
}} StoneAgePAInitializePlan;
typedef struct tagStoneAgePARequest {{
    int has_payload;
    char payload[8192];
}} StoneAgePARequest;
typedef struct tagChar {{
    int data[32];
    int pet[CHAR_MAXPETHAVE];
    int pool[CHAR_MAXPOOLPETHAVE];
    int work[4];
}} Char;

static Char owner;
static Char pets[64];
static int pet_use[64];
static int pet_end_count[64];
static int create_calls;
static int fail_on_create;
static int next_pet;
static int force_bad_ai;
static int errors;

static Char *char_pointer(int index) {{
    if (index == 0) return &owner;
    if (index >= 0 && index < 64) return &pets[index];
    return NULL;
}}
static int char_check(int index) {{
    return index == 0 || (index > 0 && index < 64 && pet_use[index]);
}}
#define CHAR_CHECKINDEX(index) char_check(index)
static int *char_data(int index) {{ return char_pointer(index)->data; }}
static int *char_work(int index) {{ return char_pointer(index)->work; }}
static int CHAR_getInt(int index, int element) {{ return char_data(index)[element]; }}
static void CHAR_setInt(int index, int element, int value) {{ char_data(index)[element] = value; }}
static int CHAR_getWorkInt(int index, int element) {{ return char_work(index)[element]; }}
static void CHAR_setWorkInt(int index, int element, int value) {{ char_work(index)[element] = value; }}
static Char *CHAR_getCharPointer(int index) {{ return char_pointer(index); }}
static int CHAR_getCharPet(int index, int slot) {{
    if (index != 0 || slot < 0 || slot >= CHAR_MAXPETHAVE) return -1;
    return owner.pet[slot];
}}
static int CHAR_setCharPet(int index, int slot, int value) {{
    int old;
    if (index != 0 || slot < 0 || slot >= CHAR_MAXPETHAVE) return -1;
    old = owner.pet[slot];
    owner.pet[slot] = value;
    return old;
}}
static int CHAR_getCharPoolPet(int index, int slot) {{
    if (index != 0 || slot < 0 || slot >= CHAR_MAXPOOLPETHAVE) return -1;
    return owner.pool[slot];
}}
static int CHAR_setCharPoolPet(int index, int slot, int value) {{
    int old;
    if (index != 0 || slot < 0 || slot >= CHAR_MAXPOOLPETHAVE) return -1;
    old = owner.pool[slot];
    owner.pool[slot] = value;
    return old;
}}
static int CHAR_getCharPetElement(int index) {{
    int i;
    if (index != 0) return -1;
    for (i = 0; i < CHAR_MAXPETHAVE; i++) if (owner.pet[i] == -1) return i;
    return -1;
}}
static void CHAR_endCharOneArray(int index) {{
    if (index > 0 && index < 64 && pet_use[index]) {{
        pet_use[index] = 0;
        pet_end_count[index]++;
    }}
}}
static void CHAR_complianceParameter(int index) {{
    int maxhp;
    int maxmp;
    if (!CHAR_CHECKINDEX(index)) return;
    maxhp = (CHAR_getInt(index, CHAR_VITAL) * 4 +
             CHAR_getInt(index, CHAR_STR) +
             CHAR_getInt(index, CHAR_TOUGH) +
             CHAR_getInt(index, CHAR_DEX)) / 100;
    maxmp = CHAR_getInt(index, CHAR_MAXMP);
    CHAR_setWorkInt(index, CHAR_WORKMAXHP, maxhp);
    CHAR_setWorkInt(index, CHAR_WORKMAXMP, maxmp);
    if (CHAR_getInt(index, CHAR_WHICHTYPE) == CHAR_TYPEPET)
        CHAR_setWorkInt(index, CHAR_WORKFIXAI, force_bad_ai ? 0 : 100);
    if (CHAR_getInt(index, CHAR_HP) > maxhp) CHAR_setInt(index, CHAR_HP, maxhp);
    if (CHAR_getInt(index, CHAR_MP) > maxmp) CHAR_setInt(index, CHAR_MP, maxmp);
}}
static int ENEMY_getEnemyArrayFromTempNo(int id) {{
    if (id == 100) return 0;
    if (id == 101) return 1;
    return -1;
}}
static int ENEMY_CHECKINDEX(int array) {{ return array >= 0 && array < 2; }}
static int ENEMYTEMP_getEnemyTempArray(int array) {{ return array; }}
static int ENEMYTEMP_CHECKINDEX(int array) {{ return array >= 0 && array < 2; }}
static int ENEMYTEMP_getInt(int array, int element) {{
    (void)array;
    if (element == E_T_IMGNUMBER) return 100352;
    if (element == E_T_PETFLG) return 1;
    return 0;
}}
static int ENEMY_createPetFromEnemyIndexAtLevel(int index, int array, int level) {{
    int slot;
    int petindex;
    (void)array;
    create_calls++;
    if (create_calls == fail_on_create) return -1;
    slot = CHAR_getCharPetElement(index);
    if (slot < 0) return -1;
    petindex = next_pet++;
    pet_use[petindex] = 1;
    memset(&pets[petindex], 0, sizeof(pets[petindex]));
    pets[petindex].data[CHAR_WHICHTYPE] = CHAR_TYPEPET;
    pets[petindex].data[CHAR_LV] = level;
    pets[petindex].data[CHAR_BASEBASEIMAGENUMBER] = 100352;
    pets[petindex].data[CHAR_BASEIMAGENUMBER] = 100352;
    pets[petindex].data[CHAR_MAXMP] = 100;
    pets[petindex].data[CHAR_HP] = 1;
    pets[petindex].data[CHAR_MP] = 1;
    owner.pet[slot] = petindex;
    CHAR_complianceParameter(petindex);
    return petindex;
}}
static void StoneAgePA_responseError(StoneAgePAResponse *response,
                                     const char *code, const char *message) {{
    (void)code;
    (void)message;
    if (response != NULL) response->failed = 1;
}}

{parse_integer}

{parse_integer_or_end}

{parse_plan}

{get_ride_graphic}

{validate_mount}

{fresh}

{skill_points}

{apply_stats}

{discard}

{release}

{initialize}

static void expect(int condition, const char *message) {{
    if (!condition) {{
        fprintf(stderr, "initialize_ai mutation: %s\\n", message);
        errors++;
    }}
}}
static void setup_fresh(void) {{
    int i;
    memset(&owner, 0, sizeof(owner));
    memset(pets, 0, sizeof(pets));
    memset(pet_use, 0, sizeof(pet_use));
    memset(pet_end_count, 0, sizeof(pet_end_count));
    for (i = 0; i < CHAR_MAXPETHAVE; i++) owner.pet[i] = -1;
    for (i = 0; i < CHAR_MAXPOOLPETHAVE; i++) owner.pool[i] = -1;
    owner.data[CHAR_WHICHTYPE] = CHAR_TYPEPLAYER;
    owner.data[CHAR_LV] = 1;
    owner.data[CHAR_HP] = 7;
    owner.data[CHAR_MP] = 3;
    owner.data[CHAR_MAXMP] = 100;
    owner.data[CHAR_BASEBASEIMAGENUMBER] = 100000;
    owner.data[CHAR_BASEIMAGENUMBER] = 100000;
    owner.data[CHAR_LEARNRIDE] = 0;
    owner.data[CHAR_VITAL] = 500;
    owner.data[CHAR_STR] = 500;
    owner.data[CHAR_TOUGH] = 500;
    owner.data[CHAR_DEX] = 500;
    owner.data[CHAR_DEFAULTPET] = 0;
    owner.data[CHAR_RIDEPET] = 3;
    owner.work[CHAR_WORKPETFOLLOW] = 1;
    pet_use[1] = 1;
    pets[1].data[CHAR_WHICHTYPE] = CHAR_TYPEPET;
    pets[1].data[CHAR_LV] = 1;
    next_pet = 2;
    create_calls = 0;
    fail_on_create = 0;
    force_bad_ai = 0;
    owner.pet[0] = 1;
}}
static StoneAgePARequest request_for(const char *payload) {{
    StoneAgePARequest request;
    memset(&request, 0, sizeof(request));
    request.has_payload = 1;
    strncpy(request.payload, payload, sizeof(request.payload) - 1);
    return request;
}}
static void test_second_pet_failure(void) {{
    StoneAgePARequest request;
    StoneAgePAResponse response;
    setup_fresh();
    fail_on_create = 2;
    request = request_for("1|10|1|1|1|1|2|100:3|101:4");
    memset(&response, 0, sizeof(response));
    expect(!StoneAgePA_initializeAI(&response, &request, 0),
           "second pet failure must fail action");
    expect(owner.data[CHAR_LV] == 1 && owner.data[CHAR_VITAL] == 500 &&
           owner.data[CHAR_STR] == 500 && owner.data[CHAR_TOUGH] == 500 &&
           owner.data[CHAR_DEX] == 500 && owner.data[CHAR_HP] == 7 &&
           owner.data[CHAR_MP] == 3, "failed action changed character state");
    expect(owner.pet[0] == 1 && owner.pet[1] == -1,
           "failed action did not restore original pet slots");
    expect(pet_use[1] && pet_end_count[1] == 0,
           "failed action released the original pet");
    expect(!pet_use[2] && pet_end_count[2] == 1,
           "failed action did not release the first new pet");
}}
static void test_success_releases_once(void) {{
    StoneAgePARequest request;
    StoneAgePAResponse response;
    setup_fresh();
    request = request_for("1|10|1|1|1|1|2|100:3|101:4");
    memset(&response, 0, sizeof(response));
    expect(StoneAgePA_initializeAI(&response, &request, 0),
           "valid action must succeed");
    expect(owner.pet[0] == 2 && owner.pet[1] == 3 && owner.pet[2] == -1,
           "valid action did not fill deterministic pet slots");
    expect(!pet_use[1] && pet_end_count[1] == 1,
           "valid action did not release original pet exactly once");
    expect(pet_use[2] && pet_use[3] && pet_end_count[2] == 0 &&
           pet_end_count[3] == 0, "valid action released a new pet");
    expect(owner.data[CHAR_DEFAULTPET] == 0 && owner.data[CHAR_RIDEPET] == -1 &&
           owner.work[CHAR_WORKPETFOLLOW] == -1,
           "valid action left stale pet references");
    expect(owner.data[CHAR_HP] == owner.work[CHAR_WORKMAXHP] &&
           owner.data[CHAR_MP] == owner.work[CHAR_WORKMAXMP],
           "valid action did not restore full character health");
    expect(owner.data[CHAR_VITAL] + owner.data[CHAR_STR] +
           owner.data[CHAR_TOUGH] + owner.data[CHAR_DEX] == 4700 &&
           owner.data[CHAR_SKILLUPPOINT] == 0,
           "valid action did not consume the configured point budget");
}}
static void test_zero_pets(void) {{
    StoneAgePARequest request;
    StoneAgePAResponse response;
    setup_fresh();
    request = request_for("1|1|1|2|0|0|0");
    memset(&response, 0, sizeof(response));
    expect(StoneAgePA_initializeAI(&response, &request, 0),
           "zero-pet action must succeed");
    expect(owner.pet[0] == -1 && owner.pet[1] == -1 &&
           owner.data[CHAR_DEFAULTPET] == -1,
           "zero-pet action left an inventory pet");
    expect(!pet_use[1] && pet_end_count[1] == 1,
           "zero-pet action did not release original pet exactly once");
    expect(owner.data[CHAR_VITAL] == 700 && owner.data[CHAR_STR] == 1300 &&
           owner.data[CHAR_TOUGH] == 0 && owner.data[CHAR_DEX] == 0,
           "zero-pet level-one allocation is incorrect");
}}
static void test_mounted_single_pet(void) {{
    StoneAgePARequest request;
    StoneAgePAResponse response;
    setup_fresh();
    request = request_for("2|10|1|1|1|1|1|1|100:3");
    memset(&response, 0, sizeof(response));
    expect(StoneAgePA_initializeAI(&response, &request, 0),
           "mounted action must succeed for a mapped pet");
    expect(owner.pet[0] == 2 && owner.data[CHAR_RIDEPET] == 0 &&
           owner.data[CHAR_DEFAULTPET] == -1 && owner.data[CHAR_BASEIMAGENUMBER] == 101000,
           "mounted action did not set the native ride state");
    expect(owner.data[CHAR_LEARNRIDE] >= 3 &&
           pets[2].data[CHAR_VARIABLEAI] == CHAR_MAXVARIABLEAI &&
           pets[2].work[CHAR_WORKFIXAI] >= 100,
           "mounted pet does not satisfy the native ride prerequisites");
}}
static void test_mounted_second_pet_is_default(void) {{
    StoneAgePARequest request;
    StoneAgePAResponse response;
    setup_fresh();
    request = request_for("2|10|1|1|1|1|1|2|100:3|101:4");
    memset(&response, 0, sizeof(response));
    expect(StoneAgePA_initializeAI(&response, &request, 0),
           "mounted two-pet action must succeed");
    expect(owner.data[CHAR_RIDEPET] == 0 && owner.data[CHAR_DEFAULTPET] == 1,
           "mounted action did not choose a non-mounted default pet");
    expect(pets[2].data[CHAR_VARIABLEAI] == CHAR_MAXVARIABLEAI &&
           pets[3].data[CHAR_VARIABLEAI] == 0,
           "mounted action changed the non-mounted pet loyalty");
}}
static void test_invalid_mount_is_rejected_before_creation(void) {{
    StoneAgePARequest request;
    StoneAgePAResponse response;
    setup_fresh();
    request = request_for("2|1|1|1|1|1|1|1|100:7");
    memset(&response, 0, sizeof(response));
    expect(!StoneAgePA_initializeAI(&response, &request, 0),
           "pet above the native ride level must fail");
    expect(create_calls == 0 && owner.pet[0] == 1 && owner.data[CHAR_RIDEPET] == 3,
           "invalid mounted plan created a pet or changed state");
}}
static void test_unmapped_mount_is_rejected_before_creation(void) {{
    StoneAgePARequest request;
    StoneAgePAResponse response;
    int pet_no = ridePetTable[0].petNo;
    setup_fresh();
    ridePetTable[0].petNo = 999999;
    request = request_for("2|10|1|1|1|1|1|1|100:3");
    memset(&response, 0, sizeof(response));
    expect(!StoneAgePA_initializeAI(&response, &request, 0),
           "pet without a ride sprite must fail");
    expect(create_calls == 0 && owner.pet[0] == 1,
           "unmapped mounted plan created a pet");
    ridePetTable[0].petNo = pet_no;
}}
static void test_mount_verification_rolls_back(void) {{
    StoneAgePARequest request;
    StoneAgePAResponse response;
    setup_fresh();
    force_bad_ai = 1;
    request = request_for("2|10|1|1|1|1|1|1|100:3");
    memset(&response, 0, sizeof(response));
    expect(!StoneAgePA_initializeAI(&response, &request, 0),
           "failed native mount verification must fail action");
    expect(owner.pet[0] == 1 && owner.data[CHAR_LV] == 1 &&
           owner.data[CHAR_RIDEPET] == 3 && owner.data[CHAR_DEFAULTPET] == 0 &&
           owner.data[CHAR_BASEIMAGENUMBER] == 100000,
           "failed native mount verification did not restore character state");
    expect(pet_use[1] && pet_end_count[1] == 0 && !pet_use[2] &&
           pet_end_count[2] == 1,
           "failed native mount verification did not clean up pets");
}}
int main(void) {{
    test_second_pet_failure();
    test_success_releases_once();
    test_zero_pets();
    test_mounted_single_pet();
    test_mounted_second_pet_is_default();
    test_invalid_mount_is_rejected_before_creation();
    test_unmapped_mount_is_rejected_before_creation();
    test_mount_verification_rolls_back();
    return errors != 0;
}}
"""


def main() -> None:
    TMP_ROOT.mkdir(parents=True, exist_ok=True)
    compiler = os.environ.get("CC", "cc")
    with tempfile.TemporaryDirectory(prefix="ai-initialize-", dir=TMP_ROOT) as temp:
        temp_path = Path(temp)
        harness = temp_path / "ai_initialize.c"
        binary = temp_path / "ai-initialize"
        harness.write_text(harness_source(MODULE.read_text()), encoding="utf-8")
        subprocess.run(
            [compiler, "-std=gnu89", "-Wall", "-Wextra", "-Werror", "-o", str(binary), str(harness)],
            cwd=ROOT,
            check=True,
        )
        result = subprocess.run([str(binary)], cwd=ROOT, capture_output=True, text=True)
        if result.returncode != 0:
            raise RuntimeError(result.stderr or result.stdout or "native fixture failed")

        mutation_harness = temp_path / "ai_initialize_mutation.c"
        mutation_binary = temp_path / "ai-initialize-mutation"
        mutation_harness.write_text(
            mutation_harness_source(MODULE.read_text()), encoding="utf-8"
        )
        subprocess.run(
            [
                compiler,
                "-std=gnu89",
                "-Wall",
                "-Wextra",
                "-Werror",
                "-o",
                str(mutation_binary),
                str(mutation_harness),
            ],
            cwd=ROOT,
            check=True,
        )
        result = subprocess.run(
            [str(mutation_binary)], cwd=ROOT, capture_output=True, text=True
        )
        if result.returncode != 0:
            raise RuntimeError(
                result.stderr or result.stdout or "native mutation fixture failed"
            )

    text = MODULE.read_text()
    initialize = extract_function(text, "StoneAgePA_initializeAI", "int")
    parse_request = extract_function(text, "StoneAgePA_parseRequest", "int")
    execute = extract_function(text, "StoneAgePA_execute", "int")
    if "memcpy(current, before, sizeof(Char))" not in initialize:
        raise AssertionError("initialize_ai must restore the character snapshot on pet failure")
    if "StoneAgePA_discardAIPets(index, created, created_count)" not in initialize:
        raise AssertionError("initialize_ai must clean every newly-created pet on failure")
    if execute.count("StoneAgePA_save(index, response)") != 1:
        raise AssertionError("initialize_ai must use the single common save call")
    if "StoneAgePA_preflightSave(index, response)" not in execute:
        raise AssertionError("mutations must retain save preflight")
    required_target = parse_request.index('StoneAgePA_requiredString(request, "account"')
    if parse_request.index('"ai_mount_default"') > required_target or \
            parse_request.index('"validate_ai"') > required_target:
        raise AssertionError("catalog validation actions must not require an online target")
    target_lookup = execute.index("StoneAgePA_findTarget(request)")
    if execute.index("StoneAgePA_aiMountDefault(response, request)") > target_lookup or \
            execute.index("StoneAgePA_validateAI(response, request)") > target_lookup:
        raise AssertionError("catalog validation actions must run before target lookup")
    if "StoneAgePA_findDefaultMountTemplate" not in text or \
            "StoneAgePA_getRideGraphic" not in initialize:
        raise AssertionError("initialize_ai must use the native ride mapping")
    print("native initialize_ai parser, allocation, rollback, and save wiring tests passed")


if __name__ == "__main__":
    main()
