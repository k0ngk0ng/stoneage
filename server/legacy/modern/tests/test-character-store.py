#!/usr/bin/env python3
"""Exercise the transformed SAAC writer with real files and injected failures.

The protocol/lock service is not mocked as an end-to-end SAAC test: this tests
the actual saveCharOne call site, atomic writer, and filesystem boundaries.
"""
import hashlib
import importlib.util
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[4]
MODERN = ROOT / "server/legacy/modern"
ARCHIVE = ROOT / "server/legacy/source/2.5/saac"
BUILD = ROOT / "build/ladder/character-store"
BUILD.mkdir(parents=True, exist_ok=True)
spec = importlib.util.spec_from_file_location("character_store_integration", MODERN / "integrate-character-store.py")
integration = importlib.util.module_from_spec(spec)
spec.loader.exec_module(integration)
original = (ARCHIVE / "char.c").read_bytes()
transformed = integration.transform(original)
start = transformed.index(b"int saveCharOne(")
end = transformed.index(b"\nstatic int makeSaveCharString(", start)
HARNESS = BUILD / "harness.c"
BINARY = BUILD / "harness"
HARNESS.write_bytes(rb'''
#include <errno.h>
#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <unistd.h>
static int mode,writes,syncs;
static ssize_t injected_write(int fd,const void *data,size_t size)
{
    ssize_t n;
    writes++;
    if(mode==1 && writes==1){errno=EINTR;return -1;}
    if(mode==2 && writes>1){errno=ENOSPC;return -1;}
    if(size>7 && (mode==1 || mode==2 || mode==6))size=7;
    n=write(fd,data,size);
    if(mode==6)_exit(91);
    return n;
}
static int injected_fsync(int fd)
{
    syncs++;
    if((mode==3 && syncs==1) || (mode==5 && syncs==2)){errno=EIO;return -1;}
    return fsync(fd);
}
static int injected_rename(const char *from,const char *to)
{
    int result;
    if(mode==4){errno=EIO;return -1;}
    result=rename(from,to);
    if(mode==7 && !result)_exit(92);
    return result;
}
#define write injected_write
#define fsync injected_fsync
#define rename injected_rename
#include "stoneage_character_store.c"
#undef write
#undef fsync
#undef rename
#define log(...) ((void)0)
static const char *target;
static void makeCharFileName(char *id,char *out,int size,int slot)
{ (void)id;(void)slot;snprintf(out,size,"%s",target); }
''' + transformed[start:end] + rb'''
int main(int argc,char **argv)
{
    int result;char *input;
    if(argc!=4)return 99;
    mode=atoi(argv[1]);target=argv[2];input=strdup(argv[3]);
    result=saveCharOne("test-account",0,input);free(input);
    printf("%d %d\n",result,syncs);return result?10:0;
}
''')
subprocess.run(["cc", "-std=gnu99", "-Wall", "-Wextra", "-Werror", "-I" + str(MODERN),
                str(HARNESS), "-o", str(BINARY)], check=True,
               env=dict(os.environ, TMPDIR=str(BUILD), CLANG_MODULE_CACHE_PATH=str(BUILD / "module-cache")))


class CharacterStore(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(dir=BUILD)
        self.directory = Path(self.temp.name)
        self.path = self.directory / "test.0.char"
        self.old = b"original complete archive"
        self.payload = "name=Hero\\nhp=17\\nmp=20\\nitem=healing_food\\nDATAEND="
        self.path.write_bytes(self.old)

    def tearDown(self):
        self.temp.cleanup()

    def run_save(self, mode, code=0):
        result = subprocess.run([str(BINARY), str(mode), str(self.path), self.payload], capture_output=True, text=True)
        self.assertEqual(result.returncode, code, result.stderr)
        return result.stdout.strip()

    def test_success_and_interrupted_short_writes_are_complete_and_synced(self):
        for mode in (0, 1):
            self.assertEqual(self.run_save(mode), "0 2")
            self.assertEqual(self.path.read_bytes(), self.payload.encode())
            self.assertEqual(self.path.stat().st_mode & 0o777, 0o600)
            self.assertEqual(list(self.directory.glob("*.tmp.*")), [])

    def test_write_file_sync_and_rename_failures_preserve_old_archive(self):
        for mode in (2, 3, 4):
            self.run_save(mode, 10)
            self.assertEqual(self.path.read_bytes(), self.old)
            self.assertEqual(list(self.directory.glob("*.tmp.*")), [])

    def test_directory_sync_failure_never_acknowledges_durability(self):
        self.assertEqual(self.run_save(5, 10), "-1 2")
        self.assertEqual(self.path.read_bytes(), self.payload.encode())
        self.assertEqual(self.run_save(0), "0 2")

    def test_process_exit_leaves_only_complete_final_archives(self):
        self.run_save(6, 91)
        self.assertEqual(self.path.read_bytes(), self.old)
        self.run_save(7, 92)
        self.assertEqual(self.path.read_bytes(), self.payload.encode())

    def test_legacy_hp_normalization_still_runs_before_atomic_write(self):
        self.payload = "name=Hero\\nhp=-123\\nmp=20\\nDATAEND="
        self.run_save(0)
        self.assertEqual(self.path.read_bytes(), self.payload.replace("hp=-123", "hp=1").encode())

    def test_integration_is_atomic_idempotent_and_preserves_archive(self):
        digest = hashlib.sha256(original).digest()
        root = self.directory / "copy"
        root.mkdir()
        source = root / "char.c"
        makefile = root / "makefile"
        source.write_bytes(original)
        makefile.write_bytes((ARCHIVE / "makefile").read_bytes().replace(b"SRC = main.c", b"SRC = changed.c"))
        with self.assertRaises(ValueError):
            integration.integrate(root)
        self.assertEqual(source.read_bytes(), original)
        makefile.write_bytes((ARCHIVE / "makefile").read_bytes())
        integration.integrate(root)
        updated = (source.read_bytes(), makefile.read_bytes())
        integration.integrate(root)
        self.assertEqual((source.read_bytes(), makefile.read_bytes()), updated)
        self.assertIn(b"stoneage_character_store.c", updated[1])
        source.write_bytes(updated[0].replace(integration.MARKER, b"/* STONEAGE_DURABLE_CHARACTER_STORE_V2 */\n"))
        with self.assertRaisesRegex(ValueError, "regenerate the build copy"):
            integration.integrate(root)
        self.assertEqual(makefile.read_bytes(), updated[1])
        source.write_bytes(updated[0])
        self.assertEqual(hashlib.sha256((ARCHIVE / "char.c").read_bytes()).digest(), digest)
        main = (ARCHIVE / "main.c").read_bytes()
        (root / "main.c").write_bytes(main)
        subprocess.run(["patch", "-p1", "-d", str(root)],
                       input=(MODERN / "patches/0033-saac-restart-address.patch").read_bytes(),
                       capture_output=True, check=True)
        self.assertIn(b"STONEAGE_SAAC_RESTART_ADDRESS", (root / "main.c").read_bytes())
        self.assertEqual((ARCHIVE / "main.c").read_bytes(), main)


if __name__ == "__main__":
    unittest.main()
