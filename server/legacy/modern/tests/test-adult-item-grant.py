#!/usr/bin/env python3
"""Exercise the adult NPC's fifteen-item grant commit boundary."""
from pathlib import Path
import os
import subprocess
import tempfile


ROOT = Path(__file__).resolve().parents[4]
MODERN = ROOT / "server/legacy/modern"
SOURCE = ROOT / "server/legacy/source/2.5/gmsv"

HEADER = r'''
#ifndef ADULT_GRANT_TEST_API_H
#define ADULT_GRANT_TEST_API_H
#define TRUE 1
#define FALSE 0
#define CHAR_STARTITEMARRAY 5
#define CHAR_MAXITEMHAVE 20
enum { CHAR_NAME, CHAR_CDKEY, CHAR_FLOOR, CHAR_X, CHAR_Y };
enum { ITEM_ID, ITEM_USEPILENUMS, ITEM_WORKOBJINDEX, ITEM_WORKCHARAINDEX,
       ITEM_UNIQUECODE, ITEM_NAME };
int CHAR_CHECKINDEX(int);
int ITEM_CHECKINDEX(int);
int CHAR_getItemIndex(int,int);
int CHAR_setItemIndex(int,int,int);
int CHAR_getInt(int,int);
char *CHAR_getChar(int,int);
int ITEM_getInt(int,int);
char *ITEM_getChar(int,int);
void ITEM_setWorkInt(int,int,int);
int ITEM_makeItemAndRegist(int);
void ITEM_endExistItemsOne(int);
void CHAR_sendItemDataOne(int,int);
void LogItem(char*,char*,int,char*,int,int,int,char*,char*,int);
#endif
'''

HARNESS = r'''
#include <assert.h>
#include <stdio.h>
#include <string.h>
#include "char_base.h"
#include "stoneage_adult_exchange.h"

#define SLOT_COUNT 20
#define ITEM_COUNT 128
#define GENERATED_FIRST 40

static int slots[SLOT_COUNT];
static int alive[ITEM_COUNT];
static int ids[ITEM_COUNT];
static int stacks[ITEM_COUNT];
static int owners[ITEM_COUNT];
static int objects[ITEM_COUNT];
static int sent[SLOT_COUNT];
static int fail_at, wrong_at, stacked_at, mutate_after_first;
static int allocations, created, cleanup, writes, sends, logs;

int CHAR_CHECKINDEX(int c) { return c == 1; }

int ITEM_CHECKINDEX(int i) {
    return i >= 0 && i < ITEM_COUNT && alive[i];
}

int CHAR_getItemIndex(int c, int s) {
    assert(c == 1 && s >= 0 && s < SLOT_COUNT);
    return slots[s];
}

int CHAR_setItemIndex(int c, int s, int id) {
    int old;
    assert(c == 1 && s >= CHAR_STARTITEMARRAY && s < CHAR_MAXITEMHAVE);
    assert(allocations == 15 && !sends && ITEM_CHECKINDEX(id));
    old = slots[s];
    slots[s] = id;
    writes++;
    return old;
}

int CHAR_getInt(int c, int key) {
    (void)c;
    (void)key;
    return 0;
}

char *CHAR_getChar(int c, int key) {
    (void)c;
    (void)key;
    return "role";
}

int ITEM_getInt(int i, int key) {
    assert(ITEM_CHECKINDEX(i));
    if (key == ITEM_ID) return ids[i];
    if (key == ITEM_USEPILENUMS) return stacks[i];
    return 0;
}

char *ITEM_getChar(int i, int key) {
    (void)key;
    assert(ITEM_CHECKINDEX(i));
    return "item";
}

void ITEM_setWorkInt(int i, int key, int value) {
    assert(ITEM_CHECKINDEX(i));
    if (key == ITEM_WORKOBJINDEX) objects[i] = value;
    else if (key == ITEM_WORKCHARAINDEX) {
        assert(value == -1 || (value == 1 && allocations == 15));
        owners[i] = value;
    }
}

int ITEM_makeItemAndRegist(int id) {
    int item;
    int call;
    assert(id == 2417 && !writes && !sends);
    call = allocations + 1;
    allocations++;
    if (fail_at == call) return -1;
    item = GENERATED_FIRST + created;
    assert(item < ITEM_COUNT);
    created++;
    alive[item] = 1;
    ids[item] = wrong_at == call ? 99 : 2417;
    stacks[item] = stacked_at == call ? 2 : 0;
    owners[item] = 99;
    objects[item] = 99;
    if (mutate_after_first && call == 1) {
        slots[CHAR_STARTITEMARRAY] = 60;
        alive[60] = 1;
        ids[60] = 99;
        stacks[60] = 0;
        owners[60] = 1;
        objects[60] = -1;
    }
    return item;
}

void ITEM_endExistItemsOne(int i) {
    assert(ITEM_CHECKINDEX(i));
    assert(!writes && !sends && !logs);
    assert(i >= GENERATED_FIRST && i < GENERATED_FIRST + created);
    alive[i] = 0;
    cleanup++;
}

void CHAR_sendItemDataOne(int c, int s) {
    int i;
    assert(c == 1 && s >= CHAR_STARTITEMARRAY && s < CHAR_MAXITEMHAVE);
    assert(writes == 15 && logs > 0);
    assert(sent[s] == 0);
    i = slots[s];
    assert(ITEM_CHECKINDEX(i) && ids[i] == 2417);
    assert(stacks[i] >= 0 && stacks[i] <= 1);
    assert(owners[i] == 1 && objects[i] == -1);
    sent[s]++;
    sends++;
}

void LogItem(char *a, char *b, int c, char *d, int e, int f, int g,
             char *h, char *i, int j) {
    (void)a;
    (void)b;
    (void)c;
    (void)d;
    (void)e;
    (void)f;
    (void)g;
    (void)h;
    (void)i;
    (void)j;
    assert(writes == 15 && sends == 0);
    logs++;
}

static void reset(void) {
    int i;
    memset(slots, 0xff, sizeof(slots));
    memset(alive, 0, sizeof(alive));
    memset(ids, 0, sizeof(ids));
    memset(stacks, 0, sizeof(stacks));
    memset(owners, 0xff, sizeof(owners));
    memset(objects, 0xff, sizeof(objects));
    memset(sent, 0, sizeof(sent));
    fail_at = wrong_at = stacked_at = mutate_after_first = 0;
    allocations = created = cleanup = writes = sends = logs = 0;
    for (i = 0; i < SLOT_COUNT; i++) slots[i] = -1;
}

static void assert_empty_and_clean(void) {
    int i;
    for (i = 0; i < SLOT_COUNT; i++) assert(slots[i] == -1);
    for (i = GENERATED_FIRST; i < GENERATED_FIRST + created; i++) assert(!alive[i]);
    assert(writes == 0 && sends == 0 && logs == 0);
}

static void assert_no_generated(void) {
    int i;
    for (i = GENERATED_FIRST; i < GENERATED_FIRST + created; i++) assert(!alive[i]);
    assert(writes == 0 && sends == 0 && logs == 0);
}

int main(void) {
    int i;
    char extra[2048];
    const char *script = SCRIPT;

    reset();
    assert(StoneAge_AdultExchange(1, script) == 1);
    assert(allocations == 15 && created == 15 && cleanup == 0);
    assert(writes == 15 && sends == 15 && logs == 15);
    for (i = CHAR_STARTITEMARRAY; i < CHAR_MAXITEMHAVE; i++) {
        assert(slots[i] == GENERATED_FIRST + i - CHAR_STARTITEMARRAY);
        assert(sent[i] == 1);
        assert(owners[slots[i]] == 1 && objects[slots[i]] == -1);
    }
    assert(StoneAge_AdultExchange(1, script) == 0);
    assert(allocations == 15 && writes == 15 && sends == 15 && logs == 15);

    reset();
    slots[CHAR_MAXITEMHAVE - 1] = 61;
    alive[61] = 1;
    ids[61] = 99;
    assert(StoneAge_AdultExchange(1, script) == 0);
    assert(allocations == 0);
    assert_no_generated();
    assert(slots[CHAR_MAXITEMHAVE - 1] == 61);

    reset();
    slots[0] = 62;
    alive[62] = 1;
    ids[62] = 2417;
    assert(StoneAge_AdultExchange(1, script) == 0);
    assert(allocations == 0);
    assert_no_generated();
    assert(slots[0] == 62);

    for (i = 1; i <= 15; i++) {
        reset();
        fail_at = i;
        assert(StoneAge_AdultExchange(1, script) == 0);
        assert(allocations == i && created == i - 1 && cleanup == i - 1);
        assert_empty_and_clean();
    }

    reset();
    wrong_at = 8;
    assert(StoneAge_AdultExchange(1, script) == 0);
    assert(allocations == 8 && created == 8 && cleanup == 8);
    assert_empty_and_clean();

    reset();
    stacked_at = 8;
    assert(StoneAge_AdultExchange(1, script) == 0);
    assert(allocations == 8 && created == 8 && cleanup == 8);
    assert_empty_and_clean();

    reset();
    mutate_after_first = 1;
    assert(StoneAge_AdultExchange(1, script) == 0);
    assert(allocations == 15 && created == 15 && cleanup == 15);
    assert(slots[CHAR_STARTITEMARRAY] == 60 && alive[60]);
    assert(writes == 0 && sends == 0 && logs == 0);
    for (i = GENERATED_FIRST; i < GENERATED_FIRST + created; i++) assert(!alive[i]);

    reset();
    snprintf(extra, sizeof(extra), "%s|DelItem:2417*15", script);
    assert(StoneAge_AdultExchange(1, extra) == -1);
    assert_empty_and_clean();

    reset();
    snprintf(extra, sizeof(extra), "%s|ThanksMsg:duplicate", script);
    assert(StoneAge_AdultExchange(1, extra) == -1);
    assert_empty_and_clean();

    puts("Adult ceremony item grant preparation and commit checks passed");
    return 0;
}
'''


def main() -> None:
    raw = (SOURCE / "data/npc/jaruga/event/event04_2").read_bytes().decode("gb18030")
    script = "|".join(
        line.strip() for line in raw.split("EventEnd", 1)[0].splitlines() if line.strip()
    )
    with tempfile.TemporaryDirectory(prefix="adult-grant-", dir=ROOT / "build") as temp:
        work = Path(temp)
        include = work / "include"
        include.mkdir()
        for name in ["version.h", "char.h", "char_base.h", "item.h", "log.h"]:
            (include / name).write_text(HEADER)
        literal = '"' + "".join("\\%03o" % byte for byte in script.encode("gb18030")) + '"'
        harness = work / "grant.c"
        harness.write_text(HARNESS.replace("SCRIPT", literal))
        binary = work / "grant"
        subprocess.run(
            [
                os.environ.get("CC", "cc"),
                "-std=gnu89",
                "-Wall",
                "-Wextra",
                "-Werror",
                "-I" + str(include),
                "-I" + str(MODERN),
                str(harness),
                str(MODERN / "stoneage_adult_exchange.c"),
                "-o",
                str(binary),
            ],
            cwd=work,
            check=True,
            env={**os.environ, "TMPDIR": str(work)},
        )
        subprocess.run([str(binary)], cwd=work, check=True)


if __name__ == "__main__":
    main()
