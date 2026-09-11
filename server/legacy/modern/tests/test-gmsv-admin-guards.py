#!/usr/bin/env python3
"""Compile and exercise the GMSV player-admin target guards and hash."""

import os
from pathlib import Path
import re
import subprocess
import tempfile


def extract_function(source: str, name: str) -> str:
    """Return one complete static function from the production C source."""

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


def harness_source(source: str) -> str:
    check_target = extract_function(source, "StoneAgePA_checkTarget")
    hash_serialized = extract_function(source, "StoneAgePA_hashSerialized")
    return f"""\
#include <stddef.h>
#include <stdio.h>
#include <string.h>

#define TRUE 1
#define FALSE 0
#define BATTLE_CHARMODE_NONE 0
#define CHAR_TRADE_FREE 0
#define CHAR_WORKBATTLEMODE 1
#define CHAR_WORKTRADEMODE 2
#define CHAR_WORKFD 3

typedef struct tagStoneAgePARequest {{
    char expected_revision[32];
    int expected_sequence;
    int has_expected_revision;
    int has_expected_sequence;
}} StoneAgePARequest;

typedef struct tagStoneAgePAResponse {{
    char code[64];
}} StoneAgePAResponse;

static int harness_battle;
static int harness_trade;
static int harness_fd;
static int harness_sequence;
static const char *harness_serialized = "guard-state";

static int CHAR_getWorkInt(int charaindex, int element)
{{
    (void)charaindex;
    if (element == CHAR_WORKBATTLEMODE) return harness_battle;
    if (element == CHAR_WORKTRADEMODE) return harness_trade;
    if (element == CHAR_WORKFD) return harness_fd;
    return 0;
}}

static int CHAR_getCharMakeSequenceNumber(int charaindex)
{{
    (void)charaindex;
    return harness_sequence;
}}

{hash_serialized}

static void StoneAgePA_makeRevision(int charaindex, char *revision,
                                    size_t revision_len)
{{
    (void)charaindex;
    StoneAgePA_hashSerialized(harness_serialized, revision, revision_len);
}}

static void StoneAgePA_responseError(StoneAgePAResponse *response,
                                     const char *code, const char *message)
{{
    (void)message;
    if (response != NULL) {{
        strncpy(response->code, code, sizeof(response->code));
        response->code[sizeof(response->code) - 1] = '\\0';
    }}
}}

{check_target}

static int expect(int condition, const char *message)
{{
    if (condition) return 0;
    fprintf(stderr, "gmsv admin guards: %s\\n", message);
    return 1;
}}

static void reset_response(StoneAgePAResponse *response)
{{
    memset(response, 0, sizeof(*response));
}}

static int test_hash(const char *input, const char *expected)
{{
    char got[32];
    StoneAgePA_hashSerialized(input, got, sizeof(got));
    return expect(strcmp(got, expected) == 0, "FNV-1a known vector mismatch");
}}

static int test_target(const char *name, int charaindex, int mutate,
                       int battle, int trade, int fd, int has_revision,
                       const char *revision, int has_sequence, int sequence,
                       int expected_ok, const char *expected_code)
{{
    StoneAgePARequest request;
    StoneAgePAResponse response;
    int actual;

    memset(&request, 0, sizeof(request));
    reset_response(&response);
    harness_battle = battle;
    harness_trade = trade;
    harness_fd = fd;
    harness_sequence = 7;
    request.has_expected_revision = has_revision;
    if (has_revision) {{
        strncpy(request.expected_revision, revision, sizeof(request.expected_revision));
        request.expected_revision[sizeof(request.expected_revision) - 1] = '\\0';
    }}
    request.has_expected_sequence = has_sequence;
    request.expected_sequence = sequence;
    actual = StoneAgePA_checkTarget(&request, charaindex, &response, mutate);
    if (expect(actual == expected_ok, name)) return 1;
    if (expected_code == NULL) return expect(response.code[0] == '\\0', name);
    return expect(strcmp(response.code, expected_code) == 0, name);
}}

int main(void)
{{
    char revision[32];
    int errors = 0;

    StoneAgePA_hashSerialized(harness_serialized, revision, sizeof(revision));
    errors += test_hash("", "cbf29ce484222325");
    errors += test_hash("hello", "a430d84680aabd0b");
    errors += test_hash("foobar", "85944171f73967e8");

    errors += test_target("online mutation accepted", 4, 1, 0, 0, 42,
                          1, revision, 1, 7, TRUE, NULL);
    errors += test_target("battle mutation rejected", 4, 1, 1, 0, 42,
                          1, revision, 1, 7, FALSE, "busy_battle");
    errors += test_target("trade mutation rejected", 4, 1, 0, 1, 42,
                          1, revision, 1, 7, FALSE, "busy_trade");
    errors += test_target("disconnected mutation rejected", 4, 1, 0, 0, -1,
                          1, revision, 1, 7, FALSE, "not_connected");
    errors += test_target("missing revision rejected", 4, 1, 0, 0, 42,
                          0, "", 1, 7, FALSE, "expected_revision_required");
    errors += test_target("stale revision rejected", 4, 1, 0, 0, 42,
                          1, "0000000000000000", 1, 7, FALSE, "stale_target");
    errors += test_target("stale sequence rejected", 4, 1, 0, 0, 42,
                          1, revision, 1, 8, FALSE, "stale_target");
    errors += test_target("snapshot permits busy target", 4, 0, 1, 1, -1,
                          0, "", 0, 0, TRUE, NULL);
    errors += test_target("missing target rejected", -1, 1, 0, 0, 42,
                          0, "", 0, 0, FALSE, "target_not_online");
    errors += test_target("ambiguous target rejected", -2, 1, 0, 0, 42,
                          0, "", 0, 0, FALSE, "ambiguous_target");

    if (errors != 0) return 1;
    puts("GMSV admin guard harness passed");
    return 0;
}}
"""


def main() -> None:
    root = Path(__file__).resolve().parents[4]
    build_root = root / "build"
    build_root.mkdir(exist_ok=True)
    compiler = os.environ.get("CC", "cc")
    source = (root / "server/legacy/modern/stoneage_player_admin.c").read_text()
    with tempfile.TemporaryDirectory(prefix="gmsv-admin-guards-", dir=build_root) as temp:
        temp_path = Path(temp)
        harness = temp_path / "gmsv_admin_guards.c"
        binary = temp_path / "gmsv-admin-guards"
        harness.write_text(harness_source(source))
        subprocess.run(
            [compiler, "-std=gnu89", "-Wall", "-Wextra", "-Werror", "-o", str(binary), str(harness)],
            cwd=root,
            check=True,
        )
        subprocess.run([str(binary)], cwd=root, check=True)
    print("GMSV admin guard harness passed")


if __name__ == "__main__":
    main()
