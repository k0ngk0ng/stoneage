#!/usr/bin/env python3
"""Exercise native C/N person-identity companions and emit a Go fixture."""

from pathlib import Path
import json
import os
import shutil
import subprocess
import tempfile


ROOT = Path(__file__).resolve().parents[4]
MODERN = ROOT / "server/legacy/modern"
SOURCE = ROOT / "server/legacy/source/2.5/gmsv"
BUILD = ROOT / "build/ai/person-identity"
BUILD.mkdir(parents=True, exist_ok=True)


HARNESS = r'''
#include <assert.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include "char.h"
#include "object.h"
#include "util.h"
#include "lssproto_serv.h"
#include "stoneage_character_identity.h"
#include "stoneage_person_identity.h"

#define PERSON_CHAR_MAX 256
#define OBJECT_MAX 256
#define METADATA_MAX 4201
#define METADATA_SLOTS 130

typedef struct {
    int use;
    int type;
    int character;
} ObjectState;

static ObjectState objects[OBJECT_MAX];
static int character_use[PERSON_CHAR_MAX];
static int character_type[PERSON_CHAR_MAX];
static int character_object[PERSON_CHAR_MAX];
static int character_visible[PERSON_CHAR_MAX];
static int party[PERSON_CHAR_MAX][CHAR_PARTYMAX];
static char identities[PERSON_CHAR_MAX][64];
static int receiver_by_fd;
static char metadata[METADATA_SLOTS][METADATA_MAX];
static int metadata_count;
static char fixture_c_metadata[METADATA_MAX];
static char fixture_n_metadata[METADATA_MAX];
static int original_c_calls;
static int original_s_calls;
static int original_fd;
static char *original_data;

static const char base62[] =
    "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ";

char *cnv10to62(int value, char *out, int outlen)
{
    char reverse[64];
    unsigned int number;
    int count;
    int i;
    if (out == NULL || outlen < 2 || value < 0) return NULL;
    number = (unsigned int)value;
    count = 0;
    do {
        if (count >= (int)sizeof(reverse)) return NULL;
        reverse[count++] = base62[number % 62U];
        number /= 62U;
    } while (number != 0U);
    if (count + 1 > outlen) return NULL;
    for (i = 0; i < count; i++) out[i] = reverse[count - i - 1];
    out[count] = '\0';
    return out;
}

int OBJECT_getNum(void) { return OBJECT_MAX; }
int CHECKOBJECTUSE(int index)
{
    return index >= 0 && index < OBJECT_MAX && objects[index].use;
}
int OBJECT_getType(int index)
{
    return index >= 0 && index < OBJECT_MAX ? objects[index].type : 0;
}
int OBJECT_getIndex(int index)
{
    return index >= 0 && index < OBJECT_MAX ? objects[index].character : -1;
}

int CHAR_getInt(int index, int element)
{
    if (index < 0 || index >= PERSON_CHAR_MAX) return 0;
    if (element == CHAR_WHICHTYPE) return character_type[index];
    return 0;
}
int CHAR_getWorkInt(int index, int element)
{
    if (index < 0 || index >= PERSON_CHAR_MAX) return -1;
    if (element == CHAR_WORKOBJINDEX) return character_object[index];
    return -1;
}
int CHAR_getFlg(int index, int element)
{
    if (index < 0 || index >= PERSON_CHAR_MAX) return 0;
    if (element == CHAR_ISVISIBLE) return character_visible[index];
    return 0;
}
int CHAR_getPartyIndex(int index, int slot)
{
    if (index < 0 || index >= PERSON_CHAR_MAX || slot < 0 || slot >= CHAR_PARTYMAX)
        return -1;
    return party[index][slot];
}
int CONNECT_getCharaindex(int fd)
{
    return fd == 8 ? receiver_by_fd : -1;
}

const char *StoneAge_CharacterIdentityGetLoadedByIndex(int index)
{
    if (index < 0 || index >= PERSON_CHAR_MAX || identities[index][0] == '\0')
        return NULL;
    return identities[index];
}

void lssproto_S_send(int fd, char *data)
{
    assert(fd == 8);
    assert(data != NULL && strncmp(data, "AIPERSON|1|", 11) == 0);
    assert(metadata_count < METADATA_SLOTS);
    snprintf(metadata[metadata_count], sizeof(metadata[metadata_count]), "%s", data);
    metadata_count++;
}

static void reset_capture(void)
{
    int i;
    metadata_count = 0;
    original_c_calls = 0;
    original_s_calls = 0;
    original_fd = -1;
    original_data = NULL;
    for (i = 0; i < METADATA_SLOTS; i++) metadata[i][0] = '\0';
}

static void send_c_wrapper(int fd, char *data)
{
    StoneAge_PersonIdentitySendC(fd, data);
    original_c_calls++;
    original_fd = fd;
    original_data = data;
}

static void send_s_wrapper(int fd, char *data)
{
    StoneAge_PersonIdentitySendN(fd, data);
    original_s_calls++;
    original_fd = fd;
    original_data = data;
}

static void require_true(int value, const char *message)
{
    if (!value) {
        fprintf(stderr, "failed: %s\n", message);
        exit(1);
    }
}

static void json_quote(const char *value)
{
    const unsigned char *cursor = (const unsigned char *)value;
    putchar('"');
    while (*cursor != '\0') {
        if (*cursor == '"' || *cursor == '\\') {
            putchar('\\');
            putchar(*cursor);
        } else if (*cursor < 0x20U) {
            printf("\\u%04x", (unsigned int)*cursor);
        } else {
            putchar(*cursor);
        }
        cursor++;
    }
    putchar('"');
}

static void configure(void)
{
    int i;
    int j;
    memset(objects, 0, sizeof(objects));
    memset(character_use, 0, sizeof(character_use));
    memset(character_type, 0, sizeof(character_type));
    memset(character_object, 0xff, sizeof(character_object));
    memset(character_visible, 0, sizeof(character_visible));
    memset(identities, 0, sizeof(identities));
    for (i = 0; i < PERSON_CHAR_MAX; i++)
        for (j = 0; j < CHAR_PARTYMAX; j++) party[i][j] = -1;

    receiver_by_fd = 0;
    character_use[0] = 1;
    character_type[0] = CHAR_TYPEPLAYER;
    character_object[0] = 10;
    character_visible[0] = 1;

    /* Character index and object index are intentionally different. */
    character_use[1] = 1;
    character_type[1] = CHAR_TYPEPLAYER;
    character_object[1] = 42;
    character_visible[1] = 1;
    strcpy(identities[1], "pc1_0123456789abcdef0123456789abcdef");
    party[0][0] = 1;

    character_use[2] = 1;
    character_type[2] = CHAR_TYPEPLAYER;
    character_object[2] = 43;
    character_visible[2] = 1;
    strcpy(identities[2], "pc1_abcdef0123456789abcdef0123456789");
    party[0][1] = 2;

    character_use[3] = 1;
    character_type[3] = 4; /* NPC */
    character_object[3] = 44;
    character_visible[3] = 1;
    party[0][2] = 3;

    character_use[4] = 1;
    character_type[4] = CHAR_TYPEPLAYER;
    character_object[4] = 45;
    character_visible[4] = 1; /* no loaded identity */
    party[0][3] = 4;

    character_use[5] = 1;
    character_type[5] = CHAR_TYPEPLAYER;
    character_object[5] = 46;
    character_visible[5] = 0; /* invisible C actor */
    strcpy(identities[5], "pc1_11111111111111111111111111111111");
    party[0][4] = 5;

    for (i = 0; i < PERSON_CHAR_MAX; i++) {
        if (character_object[i] >= 0 && character_object[i] < OBJECT_MAX) {
            objects[character_object[i]].use = character_use[i];
            objects[character_object[i]].type = OBJTYPE_CHARA;
            objects[character_object[i]].character = i;
        }
    }
}

static void reset_person(int character, int object, int type, int visible,
                         const char *identity)
{
    character_use[character] = 1;
    character_type[character] = type;
    character_object[character] = object;
    character_visible[character] = visible;
    snprintf(identities[character], sizeof(identities[character]), "%s",
             identity == NULL ? "" : identity);
    objects[object].use = 1;
    objects[object].type = OBJTYPE_CHARA;
    objects[object].character = character;
}

int main(void)
{
    const char record42[] = "1|G|10|20|0|100|1|0|Friend||||||0|0";
    const char escaped_record[] =
        "1|G|10|20|0|100|1|0|P\xc4\xe3\\z||||||0|0";
    const char record43[] = "1|H|11|21|1|101|2|0|Other||||||0|0";
    const char record_n[] = "N0|22|42|20|100|80|30|Friend|";
    const char record_partial[] = "N0|g|43|";
    char c_pair[512];
    char many[130 * 256];
    char records[130][256];
    char token[64];
    int i;
    int offset;

    configure();

    reset_capture();
    send_c_wrapper(8, (char *)record42);
    require_true(metadata_count == 1, "valid C identity companion");
    require_true(strcmp(metadata[0],
        "AIPERSON|1|C|42|pc1_0123456789abcdef0123456789abcdef|"
        "317c477c31307c32307c307c3130307c317c307c467269656e647c7c7c7c7c7c307c30") == 0,
        "C companion preserves exact record bytes");
    snprintf(fixture_c_metadata, sizeof(fixture_c_metadata), "%s", metadata[0]);
    require_true(original_c_calls == 1 && original_fd == 8 &&
                 original_data == (char *)record42,
                 "C original packet is sent once unchanged");

    reset_capture();
    send_c_wrapper(8, (char *)escaped_record);
    require_true(metadata_count == 1 && strcmp(metadata[0],
        "AIPERSON|1|C|42|pc1_0123456789abcdef0123456789abcdef|"
        "317c477c31307c32307c307c3130307c317c307c50c4e35c7a7c7c7c7c7c7c307c30") == 0,
        "C companion preserves CP936 and escaped bytes");

    snprintf(c_pair, sizeof(c_pair), "%s,%s", record42, record43);
    reset_capture();
    send_c_wrapper(8, c_pair);
    require_true(metadata_count == 2 && strstr(metadata[0], "|C|42|") != NULL &&
                 strstr(metadata[1], "|C|43|") != NULL,
                 "C multi-record companions retain order");

    reset_capture();
    send_c_wrapper(8, "1|I|1|1|0|100|1|0|Npc||||||0|0");
    require_true(metadata_count == 0, "NPC is never attributed");
    send_c_wrapper(8, "1|J|1|1|0|100|1|0|Unknown||||||0|0");
    require_true(metadata_count == 0, "unknown identity is skipped");
    send_c_wrapper(8, "1|K|1|1|0|100|1|0|Invisible||||||0|0");
    require_true(metadata_count == 0, "invisible actor is skipped");
    send_c_wrapper(8, "1|Z|1|1|0|100|1|0|DeadObject||||||0|0");
    require_true(metadata_count == 0, "stale object is skipped");

    memset(many, 0, sizeof(many));
    offset = 0;
    for (i = 0; i < 129; i++) {
        int character = 10 + i;
        int object = 47 + i;
        reset_person(character, object, CHAR_TYPEPLAYER, 1,
                     "pc1_0123456789abcdef0123456789abcdef");
        cnv10to62(object, token, sizeof(token));
        snprintf(records[i], sizeof(records[i]),
                 "1|%s|1|1|0|100|1|0|P%d||||||0|0", token, i);
        if (i != 0) many[offset++] = ',';
        memcpy(many + offset, records[i], strlen(records[i]));
        offset += (int)strlen(records[i]);
    }
    reset_capture();
    send_c_wrapper(8, many);
    require_true(metadata_count == 0, "129 C records publish no partial prefix");
    memset(many, 0, sizeof(many));
    offset = 0;
    for (i = 0; i < 128; i++) {
        if (i != 0) many[offset++] = ',';
        memcpy(many + offset, records[i], strlen(records[i]));
        offset += (int)strlen(records[i]);
    }
    reset_capture();
    send_c_wrapper(8, many);
    require_true(metadata_count == 128, "128 C records stay within cache bound");

    reset_capture();
    send_s_wrapper(8, (char *)record_n);
    require_true(metadata_count == 1, "full N identity companion");
    require_true(strcmp(metadata[0],
        "AIPERSON|1|N|42|pc1_0123456789abcdef0123456789abcdef|"
        "4e307c32327c34327c32307c3130307c38307c33307c467269656e647c") == 0,
        "N companion preserves exact full row");
    snprintf(fixture_n_metadata, sizeof(fixture_n_metadata), "%s", metadata[0]);
    require_true(original_s_calls == 1 && original_fd == 8 &&
                 original_data == (char *)record_n,
                 "N original packet is sent once unchanged");

    reset_capture();
    send_s_wrapper(8, (char *)record_partial);
    require_true(metadata_count == 1 && strstr(metadata[0], "|N|42|") != NULL,
                 "N partial row resolves receiver party member");
    require_true(strstr(metadata[0], "4e307c677c3433") != NULL,
                 "N partial row keeps the HP value separate from object id");

    reset_capture();
    send_s_wrapper(8, "N0|1|42|20|100|80|30|Friend");
    require_true(metadata_count == 1, "legacy N full sentinel is accepted");
    send_s_wrapper(8, "N0|22|43|20|100|80|30|WrongMember");
    require_true(metadata_count == 1, "stale N object mismatch is rejected");
    send_s_wrapper(8, "N0|2|043|");
    require_true(metadata_count == 1, "noncanonical N object is rejected");
    send_s_wrapper(8, "N5|g|70");
    require_true(metadata_count == 1, "invalid N slot is rejected");

    reset_capture();
    party[0][0] = 3;
    send_s_wrapper(8, (char *)record_partial);
    require_true(metadata_count == 0, "N NPC party member is skipped");
    party[0][0] = 4;
    send_s_wrapper(8, (char *)record_partial);
    require_true(metadata_count == 0, "N pending identity is skipped");
    party[0][0] = 1;

    {
        char long_record[2049];
        memset(long_record, 'x', sizeof(long_record));
        long_record[0] = 'N';
        long_record[1] = '0';
        long_record[2] = '|';
        long_record[3] = 'g';
        long_record[4] = '|';
        long_record[2047] = '|';
        long_record[2048] = '\0';
        reset_capture();
        send_s_wrapper(8, long_record);
        require_true(metadata_count == 0, "overlong N record is skipped");
    }

    fprintf(stderr, "native person identity: C/N exact companions, party deltas, identity filters and bounds passed\n");
    printf("{\"cases\":[{\"function\":\"C\",\"data\":");
    json_quote(record42);
    printf(",\"metadata\":[");
    json_quote(fixture_c_metadata);
    printf("],\"object_id\":42,\"persistent_character_id\":");
    json_quote(identities[1]);
    printf("},{\"function\":\"S\",\"data\":");
    json_quote(record_n);
    printf(",\"metadata\":[");
    json_quote(fixture_n_metadata);
    printf("],\"object_id\":42,\"persistent_character_id\":");
    json_quote(identities[1]);
    puts("}]}");
    return 0;
}
'''


def patch_makefile_for_0025(path: Path) -> None:
    prior = (MODERN / "patches/0024-chat-identity.patch").read_bytes()
    line = next(line[1:] for line in prior.splitlines()
                if line.startswith(b"+$(CLIRPCSRC)"))
    raw = path.read_bytes()
    raw = raw.replace(b"$(CLIRPCSRC) $(SERVRPCSRC)\n", line + b"\n")
    path.write_bytes(raw)


def check_patch_and_headers(work: Path) -> None:
    patch_work = work / "patch"
    patch_work.mkdir()
    shutil.copyfile(SOURCE / "lssproto_serv.c", patch_work / "lssproto_serv.c")
    shutil.copyfile(SOURCE / "makefile", patch_work / "makefile")
    patch_makefile_for_0025(patch_work / "makefile")
    subprocess.run([
        "patch", "--batch", "--fuzz=0", "-p1", "-i",
        str(MODERN / "patches/0025-person-identity.patch")
    ], cwd=patch_work, check=True, stdout=subprocess.PIPE,
       stderr=subprocess.PIPE)
    assert b"StoneAge_PersonIdentitySendC(fd, data);" in \
        (patch_work / "lssproto_serv.c").read_bytes()
    assert b"StoneAge_PersonIdentitySendN(fd, data);" in \
        (patch_work / "lssproto_serv.c").read_bytes()
    assert (patch_work / "makefile").read_bytes().count(
        b"stoneage_person_identity.c") == 1

    real = work / "real"
    shutil.copytree(SOURCE / "include", real / "include")
    (real / "char").mkdir()
    for rel in ["char/char_base.c", "char/char.c", "makefile"]:
        shutil.copyfile(SOURCE / rel, real / rel)
    prior = (MODERN / "patches/0020-adult-item-exchange.patch").read_bytes()
    line = next(line[1:] for line in prior.splitlines()
                if line.startswith(b"+$(CLIRPCSRC)"))
    makefile = (real / "makefile").read_bytes().replace(
        b"$(CLIRPCSRC) $(SERVRPCSRC)\n", line + b"\n")
    (real / "makefile").write_bytes(makefile)
    for patch_name in ["0022-character-identity.patch",
                       "0023-loaded-character-identity.patch"]:
        subprocess.run([
            "patch", "--batch", "--fuzz=0", "-p1", "-i",
            str(MODERN / "patches" / patch_name)
        ], cwd=real, check=True, stdout=subprocess.PIPE,
           stderr=subprocess.PIPE)
    subprocess.run([
        "cc", "-std=gnu89", "-D_FORTIFY_SOURCE=0", "-fsyntax-only",
        "-I" + str(real / "include"), "-I" + str(SOURCE / "include"),
        "-I" + str(MODERN), str(MODERN / "stoneage_person_identity.c")
    ], check=True)


def main() -> None:
    with tempfile.TemporaryDirectory(prefix="person-identity-", dir=BUILD) as temporary:
        work = Path(temporary)
        check_patch_and_headers(work)
        include = work / "include"
        include.mkdir()
        (include / "char.h").write_text(r'''
#ifndef PERSON_IDENTITY_CHAR_H
#define PERSON_IDENTITY_CHAR_H
#define TRUE 1
#define FALSE 0
#define CHAR_TYPEPLAYER 1
#define CHAR_WHICHTYPE 0
#define CHAR_WORKOBJINDEX 1
#define CHAR_PARTYMAX 5
#define CHAR_ISVISIBLE 0
#define PERSON_CHAR_MAX 256
#define CHAR_CHECKINDEX(i) ((i) >= 0 && (i) < PERSON_CHAR_MAX)
int CHAR_getInt(int, int);
int CHAR_getWorkInt(int, int);
int CHAR_getFlg(int, int);
int CHAR_getPartyIndex(int, int);
#endif
''')
        (include / "object.h").write_text(r'''
#ifndef PERSON_IDENTITY_OBJECT_H
#define PERSON_IDENTITY_OBJECT_H
#define OBJTYPE_CHARA 1
int OBJECT_getNum(void);
int CHECKOBJECTUSE(int);
int OBJECT_getType(int);
int OBJECT_getIndex(int);
#endif
''')
        (include / "util.h").write_text(r'''
#ifndef PERSON_IDENTITY_UTIL_H
#define PERSON_IDENTITY_UTIL_H
char *cnv10to62(int, char *, int);
#endif
''')
        (include / "net.h").write_text(r'''
#ifndef PERSON_IDENTITY_NET_H
#define PERSON_IDENTITY_NET_H
int CONNECT_getCharaindex(int);
#endif
''')
        (include / "lssproto_serv.h").write_text(
            "void lssproto_S_send(int, char *);\n")
        (include / "stoneage_character_identity.h").write_text(
            "const char *StoneAge_CharacterIdentityGetLoadedByIndex(int);\n")
        shutil.copyfile(MODERN / "stoneage_person_identity.h",
                        include / "stoneage_person_identity.h")
        (work / "harness.c").write_text(HARNESS)
        binary = work / "person-identity-test"
        subprocess.run([
            "cc", "-std=c99", "-Wall", "-Wextra", "-Werror",
            "-D_FORTIFY_SOURCE=0", "-I" + str(include),
            str(work / "harness.c"),
            str(MODERN / "stoneage_person_identity.c"), "-o", str(binary)
        ], check=True)
        output = subprocess.check_output([str(binary)], text=True)
        fixture_line = next(line for line in output.splitlines()
                            if line.startswith('{'))
        fixture = json.loads(fixture_line)
        assert len(fixture.get("cases", [])) >= 2
        assert {case.get("function") for case in fixture["cases"]} >= {"C", "S"}
        print("native person identity: harness output captured")
    fixture_path = BUILD / "native-fixture.json"
    fixture_path.write_text(json.dumps(fixture, indent=2) + "\n")
    print("wrote", fixture_path)


if __name__ == "__main__":
    main()
