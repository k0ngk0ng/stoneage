#!/usr/bin/env python3
"""Exercise the production admin save receipt against native identity save."""

from pathlib import Path
import os
import re
import subprocess
import tempfile


ROOT = Path(__file__).resolve().parents[4]
MODERN = ROOT / "server/legacy/modern"
MODULE = MODERN / "stoneage_player_admin.c"
IDENTITY = MODERN / "stoneage_character_identity.c"
BUILD = ROOT / "build/ai/admin-save-identity"


def extract_function(source: str, name: str) -> str:
    pattern = re.compile(
        r"static\s+[^;{]+?\b" + re.escape(name) + r"\s*\([^;{}]*\)\s*\{",
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


def harness_source(module: str, identity: str) -> str:
    hash_serialized = extract_function(module, "StoneAgePA_hashSerialized")
    copy_serialized = extract_function(module, "StoneAgePA_copySerialized")
    preflight = extract_function(module, "StoneAgePA_preflightSave")
    save = extract_function(module, "StoneAgePA_save")
    if "StoneAge_CharacterIdentityPrepareSave" not in save:
        raise AssertionError("admin save does not prepare persistent identity")
    if save.index("StoneAge_CharacterIdentityPrepareSave") > save.index(
        "StoneAgePA_copySerialized"
    ):
        raise AssertionError("admin save hashes before identity preparation")
    if "StoneAge_CharacterIdentityPrepareSave" in preflight:
        raise AssertionError("save preflight must remain identity read-only")

    return f"""
#include <stddef.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include "char.h"

#define TRUE 1
#define FALSE 0
#define CHAR_WORKFD 0
#define CHAR_TYPEPLAYER 1
#define CHAR_WHICHTYPE 0
#define CHAR_PERSISTENTID 0
#define CHAR_WORKPERSISTENTID_LOADED 0
#define CHAR_CHECKINDEX(index) ((index) == 0)

typedef struct tagStoneAgePAResponse {{
    FILE *fp;
    size_t length;
    int failed;
    char save_hash[32];
}} StoneAgePAResponse;

static Char owner;
static char serialized[256];
static char submitted[256];
static int save_calls;
static int acfd = 9;

Char *CHAR_getCharPointer(int index)
{{
    return index == 0 ? &owner : NULL;
}}

static int CHAR_getWorkInt(int index, int element)
{{
    (void)index;
    return element == CHAR_WORKFD ? 7 : 0;
}}

static char *CHAR_makeStringFromCharIndex(int index)
{{
    int length;
    if (index != 0) return NULL;
    length = snprintf(serialized, sizeof(serialized),
                      "name=Hero\\ncharid=%s\\n", owner.string[CHAR_PERSISTENTID].string);
    if (length < 0 || (size_t)length >= sizeof(serialized)) return NULL;
    return serialized;
}}

{identity}

{hash_serialized}

{copy_serialized}

static void StoneAgePA_responseError(StoneAgePAResponse *response,
                                     const char *code, const char *message)
{{
    (void)code;
    (void)message;
    if (response != NULL) response->failed = 1;
}}

static void StoneAgePA_writeEncoded(StoneAgePAResponse *response,
                                    const char *key, const char *value)
{{
    if (response != NULL && strcmp(key, "save_data_hash") == 0) {{
        strncpy(response->save_hash, value, sizeof(response->save_hash) - 1);
        response->save_hash[sizeof(response->save_hash) - 1] = '\\0';
    }}
}}

/* This models the ordinary native save path, which calls the same helper
 * before handing the serialized character to SAAC. */
static int CHAR_charSaveFromConnect(int fd, int unlock)
{{
    char *payload;
    (void)fd;
    (void)unlock;
    save_calls++;
    (void)StoneAge_CharacterIdentityPrepareSave(&owner);
    payload = CHAR_makeStringFromCharIndex(0);
    if (payload == NULL) return FALSE;
    strncpy(submitted, payload, sizeof(submitted) - 1);
    submitted[sizeof(submitted) - 1] = '\\0';
    return TRUE;
}}

{preflight}

{save}

static void reset_owner(const char *identity_value)
{{
    memset(&owner, 0, sizeof(owner));
    owner.use = 1;
    owner.data[CHAR_WHICHTYPE] = CHAR_TYPEPLAYER;
    if (identity_value != NULL) {{
        strncpy(owner.string[CHAR_PERSISTENTID].string, identity_value,
                sizeof(owner.string[CHAR_PERSISTENTID].string) - 1);
        owner.string[CHAR_PERSISTENTID].string[
            sizeof(owner.string[CHAR_PERSISTENTID].string) - 1] = '\\0';
    }}
    memset(serialized, 0, sizeof(serialized));
    memset(submitted, 0, sizeof(submitted));
    save_calls = 0;
}}

static int expect(int condition, const char *message)
{{
    if (condition) return 0;
    fprintf(stderr, "admin save identity: %s\\n", message);
    return 1;
}}

static int receipt_matches_submission(const StoneAgePAResponse *response)
{{
    char expected[32];
    StoneAgePA_hashSerialized(submitted, expected, sizeof(expected));
    return strcmp(response->save_hash, expected) == 0;
}}

static int test_first_save_generates_and_matches(void)
{{
    StoneAgePAResponse response;
    int errors = 0;
    reset_owner(NULL);
    memset(&response, 0, sizeof(response));
    errors += expect(StoneAgePA_preflightSave(0, &response),
                     "save preflight failed for fresh character");
    errors += expect(owner.string[CHAR_PERSISTENTID].string[0] == '\\0',
                     "save preflight generated a persistent identity");
    memset(&response, 0, sizeof(response));
    errors += expect(StoneAgePA_save(0, &response),
                     "fresh admin save failed");
    errors += expect(StoneAge_CharacterIdentityValid(
                         owner.string[CHAR_PERSISTENTID].string),
                     "fresh admin save did not generate a valid identity");
    errors += expect(save_calls == 1, "fresh admin save submitted more than once");
    errors += expect(!response.failed && receipt_matches_submission(&response),
                     "fresh identity save receipt differs from submitted bytes");
    return errors;
}}

static int test_existing_and_invalid_identity_are_retained(void)
{{
    const char *existing = "pc1_0123456789abcdef0123456789abcdef";
    StoneAgePAResponse response;
    int errors = 0;
    reset_owner(existing);
    memset(&response, 0, sizeof(response));
    errors += expect(StoneAgePA_preflightSave(0, &response),
                     "save preflight failed for existing identity");
    errors += expect(strcmp(owner.string[CHAR_PERSISTENTID].string, existing) == 0,
                     "save preflight changed existing identity");
    errors += expect(StoneAgePA_save(0, &response),
                     "existing identity admin save failed");
    errors += expect(strcmp(owner.string[CHAR_PERSISTENTID].string, existing) == 0,
                     "admin save rerolled existing identity");
    errors += expect(!response.failed && receipt_matches_submission(&response),
                     "existing identity save receipt differs from submitted bytes");

    reset_owner("malformed-existing");
    memset(&response, 0, sizeof(response));
    errors += expect(StoneAgePA_save(0, &response),
                     "malformed identity changed ordinary save failure semantics");
    errors += expect(strcmp(owner.string[CHAR_PERSISTENTID].string,
                            "malformed-existing") == 0,
                     "malformed identity was replaced");
    errors += expect(!response.failed && receipt_matches_submission(&response),
                     "malformed identity save receipt differs from submitted bytes");
    return errors;
}}

int main(void)
{{
    return test_first_save_generates_and_matches() ||
           test_existing_and_invalid_identity_are_retained();
}}
"""


def main() -> None:
    BUILD.mkdir(parents=True, exist_ok=True)
    compiler = os.environ.get("CC", "cc")
    module = MODULE.read_text(encoding="utf-8")
    identity = IDENTITY.read_text(encoding="utf-8")
    with tempfile.TemporaryDirectory(prefix="admin-save-identity-", dir=BUILD) as temp:
        temp_path = Path(temp)
        include = temp_path / "include"
        include.mkdir()
        (include / "char.h").write_text(
            """#ifndef ADMIN_SAVE_IDENTITY_CHAR_H
#define ADMIN_SAVE_IDENTITY_CHAR_H
typedef struct tagStoneIdentityString { char string[64]; } StoneIdentityString;
typedef struct tagChar {
    int use;
    int data[1];
    StoneIdentityString string[1];
    StoneIdentityString workchar[1];
} Char;
Char *CHAR_getCharPointer(int);
#endif
""",
            encoding="utf-8",
        )
        harness = temp_path / "admin_save_identity.c"
        binary = temp_path / "admin-save-identity"
        harness.write_text(harness_source(module, identity), encoding="utf-8")
        subprocess.run(
            [
                compiler,
                "-std=gnu89",
                "-Wall",
                "-Wextra",
                "-Werror",
                "-I",
                str(include),
                "-I",
                str(MODERN),
                "-o",
                str(binary),
                str(harness),
            ],
            cwd=ROOT,
            check=True,
        )
        subprocess.run([str(binary)], cwd=ROOT, check=True)
    print("native admin save identity receipt and read-only preflight tests passed")


if __name__ == "__main__":
    main()
