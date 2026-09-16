#!/usr/bin/env python3
"""Exercise the server-side AI funding policy and ledger boundary."""

from pathlib import Path
import os
import subprocess
import tempfile


ROOT = Path(__file__).resolve().parents[4]
MODULE = ROOT / "server/legacy/modern/stoneage_ai_funding.c"


HARNESS = r'''
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <sys/wait.h>
#include <sys/file.h>
#include <unistd.h>
#include <errno.h>
#include "char_base.h"
#include "stoneage_ai_funding.h"

int harness_use[4] = { 1, 1, 1, 1 };
int harness_slot[4] = { 0, 1, 0, 0 };
int harness_gold[4] = { 0, 0, 100, 0 };
const char *harness_account[4] = { "ai", "ai", "ordinary", "ai" };

static int failures;
static int fail_rename, fail_write, fail_file_sync;
static int fail_directory_sync_after;
static int change_balance_on_lock = -1;
static const char *tighten_policy_on_lock;
static int write_policy(const char *, const char *, int, const char *);
int harness_flock(int fd, int operation)
{
    if( operation == LOCK_EX && change_balance_on_lock >= 0 ) {
        harness_gold[change_balance_on_lock] = 0;
        change_balance_on_lock = -1;
    }
    if( operation == LOCK_EX && tighten_policy_on_lock != NULL ) {
        if( !write_policy(tighten_policy_on_lock, "ai", 1, "trade_limit=0\n") ) return -1;
        tighten_policy_on_lock = NULL;
    }
    return flock(fd, operation);
}
int harness_rename(const char *from, const char *to)
{
    if( fail_rename ) { errno = EIO; return -1; }
    return rename(from, to);
}
ssize_t harness_write(int fd, const void *data, size_t length)
{
    if( fail_write == 2 ) { fail_write = 1; return write(fd, data, length > 1 ? 1 : length); }
    if( fail_write == 1 ) { errno = EIO; return -1; }
    return write(fd, data, length);
}
int harness_fsync(int fd)
{
    struct stat st;
    if( fail_directory_sync_after > 0 && fstat(fd, &st) == 0 && S_ISDIR(st.st_mode) &&
        --fail_directory_sync_after == 0 ) { errno = EIO; return -1; }
    if( fail_file_sync && fstat(fd, &st) == 0 && S_ISREG(st.st_mode) ) {
        errno = EIO; return -1;
    }
    return fsync(fd);
}

static long ledger_size(const char *path)
{
    struct stat st;
    return stat(path, &st) == 0 ? (long)st.st_size : -1;
}
char *CHAR_getChar(int index, int element)
{
    (void)element;
    return (char *)harness_account[index];
}
int CHAR_getInt(int index, int element)
{
    return element == CHAR_SAVEINDEXNUMBER ? harness_slot[index] : harness_gold[index];
}
int CHAR_DelGold(int index, int amount)
{
    /* Match legacy carry-limit clamping, so the wrapper must validate the
     * original quote rather than treating a smaller deduction as success. */
    if( amount > 100 ) amount = 100;
    if( harness_gold[index] < amount ) return 0;
    harness_gold[index] -= amount;
    return 1;
}
static void expect(int actual, int wanted, const char *message)
{
    if( actual != wanted ) {
        fprintf(stderr, "%s: got %d, wanted %d\n", message, actual, wanted);
        failures++;
    }
}
static int write_policy(const char *dir, const char *account, int slot,
                        const char *limits)
{
    char path[1024];
    FILE *fp;
    snprintf(path, sizeof(path), "%s/%s.%d.policy", dir, account, slot);
    fp = fopen(path, "w");
    if( fp == NULL ) return 0;
    fprintf(fp, "version=1\naccount=%s\nslot=%d\nnpc_spend=unlimited\n%s",
            account, slot, limits);
    fclose(fp);
    if( chmod(path, 0600) != 0 ) return 0;
    return 1;
}
int main(int argc, char **argv)
{
    char policy_dir[1024];
    char policy_path[1024];
    char ledger_path[1024];
    long before_pair;
    int fault;
    FILE *tail_file;
    pid_t children[2];
    int status, successes = 0;
    if( argc != 2 ) return 2;
    snprintf(policy_dir, sizeof(policy_dir), "%s/policies", argv[1]);
    if( mkdir(policy_dir, 0700) != 0 ) return 3;
    snprintf(policy_path, sizeof(policy_path), "%s/ai.0.policy", policy_dir);
    snprintf(ledger_path, sizeof(ledger_path), "%s/ledger.log", argv[1]);
    setenv("STONEAGE_AI_FUNDING_POLICY_DIR", policy_dir, 1);
    setenv("STONEAGE_AI_FUNDING_LEDGER", ledger_path, 1);

    expect(StoneAge_AIFundingCanCharge(2, 101), 0, "ordinary balance guard");
    expect(StoneAge_AIFundingCharge(2, 101), 0, "ordinary failed charge");
    expect(harness_gold[2], 100, "ordinary balance unchanged");
    expect(StoneAge_AIFundingCharge(-1, 1), 0, "invalid character rejected");
    expect(StoneAge_AIFundingCharge(-1, 0), 0, "invalid free charge rejected");
    expect(StoneAge_AIFundingCharge(2, 25), 1, "ordinary exact charge");
    expect(harness_gold[2], 75, "ordinary exact balance");
    if( !write_policy(policy_dir, "ai", 0,
                      "trade_limit=50\nbank_limit=20\ndrop_limit=30\n") ) return 4;
    expect(StoneAge_AIFundingCanCharge(0, 1000000), 1, "unlimited charge preflight");
    expect(StoneAge_AIFundingCharge(0, 1000000), 1, "unlimited charge");
    expect(harness_gold[0], 0, "unlimited charge keeps real balance");
    expect(StoneAge_AIFundingCanCharge(1, 1), 0, "slot isolation");
    harness_gold[0] = 100;
    expect(StoneAge_AIFundingCanExternal(0, STONEAGE_AI_EXTERNAL_TRADE, 40), 1,
           "finite trade preflight");
    expect(StoneAge_AIFundingRecordExternal(0, STONEAGE_AI_EXTERNAL_TRADE, 40), 1,
           "finite trade record");
    expect(StoneAge_AIFundingCanExternal(0, STONEAGE_AI_EXTERNAL_TRADE, 11), 0,
           "finite trade quota");
    expect(StoneAge_AIFundingCanExternal(0, STONEAGE_AI_EXTERNAL_DROP, 31), 0,
           "finite drop quota");
    if( !write_policy(policy_dir, "ai", 1,
                      "trade_limit=20\nbank_limit=0\ndrop_limit=0\n") ) return 6;
    harness_gold[1] = 100;
    before_pair = ledger_size(ledger_path);
    change_balance_on_lock = 1;
    expect(StoneAge_AIFundingRecordTradePair(0, 5, 1, 10), 0, "balance rechecked after acquiring lock");
    harness_gold[1] = 100;
    tighten_policy_on_lock = policy_dir;
    expect(StoneAge_AIFundingRecordTradePair(0, 5, 1, 10), 0, "policy reloaded after acquiring lock");
    if( !write_policy(policy_dir, "ai", 1, "trade_limit=20\nbank_limit=0\ndrop_limit=0\n") ) return 12;
    expect(ledger_size(ledger_path) == before_pair, 1, "changed inputs preserve both quotas");
    expect(StoneAge_AIFundingRecordTradePair(0, 5, 1, 21), 0, "second quota rejects whole pair");
    expect(ledger_size(ledger_path) == before_pair, 1, "second quota failure appends nothing");
    expect(StoneAge_AIFundingCanExternal(0, STONEAGE_AI_EXTERNAL_TRADE, 10), 1,
           "first quota preserved after second rejection");
    expect(StoneAge_AIFundingRecordTradePair(0, 5, 2, 76), 0, "ordinary balance rechecked");
    expect(StoneAge_AIFundingRecordTradePair(0, 1, 0, 1), 0, "same character rejected");
    harness_gold[3] = 100;
    expect(StoneAge_AIFundingRecordTradePair(0, 1, 3, 1), 0, "duplicate policy identity rejected");
    expect(StoneAge_AIFundingRecordTradePair(-1, 0, 1, 0), 0, "invalid zero transfer rejected");
    expect(StoneAge_AIFundingRecordTradePair(0, -1, 1, 0), 0, "negative pair amount rejected");
    for( fault = 0; fault < 3; fault++ ) {
        fail_rename = fault == 0;
        fail_write = fault == 1 ? 2 : 0;
        fail_file_sync = fault == 2;
        expect(StoneAge_AIFundingRecordTradePair(0, 5, 1, 10), 0, "prepublication I/O failure rejects pair");
        fail_rename = fail_write = fail_file_sync = 0;
        expect(ledger_size(ledger_path) == before_pair, 1, "I/O failure preserves original ledger size");
        expect(StoneAge_AIFundingCanExternal(0, STONEAGE_AI_EXTERNAL_TRADE, 10), 1, "I/O failure preserves first quota");
        expect(StoneAge_AIFundingCanExternal(1, STONEAGE_AI_EXTERNAL_TRADE, 20), 1, "I/O failure preserves second quota");
    }
    expect(StoneAge_AIFundingRecordTradePair(0, 5, 1, 10), 1, "two funded parties commit");
    expect(StoneAge_AIFundingCanExternal(0, STONEAGE_AI_EXTERNAL_TRADE, 6), 0, "first pair quota accounted");
    expect(StoneAge_AIFundingCanExternal(1, STONEAGE_AI_EXTERNAL_TRADE, 11), 0, "second pair quota accounted");
    expect(harness_gold[0], 100, "record does not mint or move first gold");
    expect(harness_gold[1], 100, "record does not move second gold");
    expect(StoneAge_AIFundingRecordTradePair(0, 5, 2, 20), 1, "funded and ordinary pair");
    expect(harness_gold[2], 75, "ordinary gold transfer belongs to native exchange");
    before_pair = ledger_size(ledger_path);
    expect(StoneAge_AIFundingRecordTradePair(0, 0, 1, 0), 1, "zero pair allowed");
    expect(ledger_size(ledger_path) == before_pair, 1, "zero pair not recorded");
    tail_file = fopen(ledger_path, "r+");
    if( tail_file == NULL || ftruncate(fileno(tail_file), before_pair - 1) != 0 ) return 7;
    fclose(tail_file);
    expect(StoneAge_AIFundingRecordTradePair(0, 0, 1, 1), 0, "unterminated ledger rejects pair");
    expect(ledger_size(ledger_path) == before_pair - 1, 1, "unterminated ledger preserved");
    tail_file = fopen(ledger_path, "a");
    if( tail_file == NULL ) return 8;
    fputc('\n', tail_file);
    fclose(tail_file);
    harness_account[3] = "otherai";
    if( !write_policy(policy_dir, "otherai", 0, "trade_limit=10\n") ) return 9;
    fail_directory_sync_after = 2;
    expect(StoneAge_AIFundingRecordTradePair(1, 1, 3, 1), 0, "postpublication durability failure is uncertain");
    expect(StoneAge_AIFundingCanExternal(1, STONEAGE_AI_EXTERNAL_TRADE, 10), 0, "uncertain pair retains first debit");
    expect(StoneAge_AIFundingCanExternal(3, STONEAGE_AI_EXTERNAL_TRADE, 10), 0, "uncertain pair retains second debit");
    expect(harness_gold[1], 100, "uncertain commit never moves real assets");
    for( fault = 0; fault < 2; fault++ ) {
        children[fault] = fork();
        if( children[fault] < 0 ) return 10;
        if( children[fault] == 0 ) _exit(StoneAge_AIFundingRecordTradePair(1, 1, 3, 8) ? 0 : 1);
    }
    for( fault = 0; fault < 2; fault++ ) {
        if( waitpid(children[fault], &status, 0) != children[fault] ) return 11;
        if( WIFEXITED(status) && WEXITSTATUS(status) == 0 ) successes++;
    }
    expect(successes, 1, "concurrent pairs cannot overspend shared quota");
    expect(StoneAge_AIFundingCanExternal(3, STONEAGE_AI_EXTERNAL_TRADE, 1), 1, "rejected concurrent pair consumed no quota");
    expect(StoneAge_AIFundingCanExternal(3, STONEAGE_AI_EXTERNAL_TRADE, 2), 0, "concurrent debit counted exactly once");
    remove(policy_path);
    expect(StoneAge_AIFundingCanCharge(0, 101), 0, "policy deletion revokes capability");
    if( !write_policy(policy_dir, "ai", 0, "trade_limit=-1\n") ) return 5;
    expect(StoneAge_AIFundingCanCharge(0, 101), 0, "invalid limit falls back");
    if( failures != 0 ) return 1;
    puts("AI funding harness passed");
    return 0;
}
'''


def main() -> None:
    build = ROOT / "build"
    build.mkdir(exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="ai-funding-", dir=build) as temp:
        temp_path = Path(temp)
        include = temp_path / "include"
        include.mkdir()
        (include / "version.h").write_text("#define TRUE 1\n#define FALSE 0\n")
        (include / "char.h").write_text('#include "char_base.h"\n')
        (include / "char_base.h").write_text(
            """#ifndef FUNDING_TEST_CHAR_BASE_H
#define FUNDING_TEST_CHAR_BASE_H
#define TRUE 1
#define FALSE 0
enum { CHAR_GOLD = 0, CHAR_CDKEY = 0, CHAR_SAVEINDEXNUMBER = 1 };
extern int harness_use[4];
#define CHAR_CHECKINDEX(i) ((i) >= 0 && (i) < 4 && harness_use[(i)])
char *CHAR_getChar(int, int);
int CHAR_getInt(int, int);
int CHAR_DelGold(int, int);
#endif
"""
        )
        harness = temp_path / "funding-harness.c"
        binary = temp_path / "funding-harness"
        harness.write_text(HARNESS)
        compiler = os.environ.get("CC", "cc")
        hooks = include / "funding-hooks.h"
        hooks.write_text("#include <sys/types.h>\nint harness_rename(const char *, const char *);\nssize_t harness_write(int, const void *, size_t);\nint harness_fsync(int);\nint harness_flock(int, int);\n")
        module_object = temp_path / "funding.o"
        subprocess.run([
            compiler, "-std=gnu89", "-Wall", "-Wextra", "-Werror",
            "-I" + str(include), "-I" + str(ROOT / "server/legacy/modern"),
            "-include", str(hooks), "-Drename=harness_rename",
            "-Dwrite=harness_write", "-Dfsync=harness_fsync", "-Dflock=harness_flock",
            "-c", str(MODULE), "-o", str(module_object),
        ], cwd=ROOT, check=True, env={**os.environ, "TMPDIR": str(temp_path)})
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
                str(module_object),
            ],
            cwd=ROOT,
            check=True,
            env={**os.environ, "TMPDIR": str(temp_path)},
        )
        subprocess.run([str(binary), str(temp_path)], cwd=ROOT, check=True)
        assert not list(temp_path.glob("ledger.log.pair.*")), "pair temporary files leaked"
    print("AI funding policy and ledger tests passed")


if __name__ == "__main__":
    main()
