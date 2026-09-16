#!/usr/bin/env python3
"""Offline native sender wiring, exact byte companion, and fallback checks."""
from pathlib import Path
import os
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[4]
MODERN = ROOT / 'server/legacy/modern'
SOURCE = ROOT / 'server/legacy/source/2.5/gmsv'
BUILD = ROOT / 'build/ai/chat-identity'
BUILD.mkdir(parents=True, exist_ok=True)
with tempfile.TemporaryDirectory(prefix='native-', dir=BUILD) as temporary:
    work = Path(temporary)
    (work / 'char').mkdir()
    for rel in ['char/char_event.c', 'char/char_talk.c', 'makefile']:
        shutil.copyfile(SOURCE / rel, work / rel)
    prior = (MODERN / 'patches/0022-character-identity.patch').read_bytes()
    line = next(line[1:] for line in prior.splitlines() if line.startswith(b'+$(CLIRPCSRC)'))
    makefile = (work / 'makefile').read_bytes().replace(b'$(CLIRPCSRC) $(SERVRPCSRC)\n', line + b'\n')
    (work / 'makefile').write_bytes(makefile)
    subprocess.run(['patch', '--batch', '--fuzz=0', '-p1', '-i', str(MODERN / 'patches/0024-chat-identity.patch')], cwd=work, check=True)
    talk = (work / 'char/char_talk.c').read_bytes()
    event = (work / 'char/char_event.c').read_bytes()
    assert talk.count(b'StoneAge_ChatIdentitySend( fd, talkcharaindex, talkchar, lastbuf, color);') == 1
    assert event.count(b'StoneAge_ChatIdentitySend( fd, talkindex, CHAR_getWorkInt( talkindex, CHAR_WORKOBJINDEX ),lastbuf, color);') == 1
    assert b'lssproto_TK_send(' not in talk + event
    assert (work / 'makefile').read_bytes().count(b'stoneage_chat_identity.c') == 1
    env = dict(os.environ, TMPDIR=str(work))
    subprocess.run(['cc', '-std=gnu89', '-D_FORTIFY_SOURCE=0', '-fsyntax-only', '-I'+str(SOURCE/'include'), '-I'+str(MODERN), str(MODERN/'stoneage_chat_identity.c')], env=env, check=True)
    # Use the production helper with observable send stubs. The real loaded-ID
    # getter lifecycle is covered separately by test-character-identity.py.
    (work / 'char.h').write_text('''#define CHAR_WORKOBJINDEX 0
#define CHAR_CHECKINDEX(i) ((i) >= 0 && (i) < 4)
int CHAR_getWorkInt(int index, int field);
''')
    (work / 'lssproto_serv.h').write_text('void lssproto_S_send(int fd, char *data);\nvoid lssproto_TK_send(int fd, int index, char *message, int color);\n')
    (work / 'harness.c').write_text(r'''
#include <assert.h>
#include <stdio.h>
#include <string.h>
#include "stoneage_chat_identity.h"
static const char *id = "pc1_0123456789abcdef0123456789abcdef";
static char metadata[4201], order[4];
static int count, expected_fd, expected_object, expected_color;
static char *expected_message;
int CHAR_getWorkInt(int index, int field) { (void)index; (void)field; return 42; }
const char *StoneAge_CharacterIdentityGetLoadedByIndex(int index) { return index == 1 ? id : NULL; }
void lssproto_S_send(int fd, char *data) {
    assert(fd == expected_fd); assert(count == 0); assert(strlen(data) <= 4200);
    strcpy(metadata, data); order[count++] = 'S';
}
void lssproto_TK_send(int fd, int object, char *message, int color) {
    assert(fd == expected_fd && object == expected_object && color == expected_color);
    assert(message == expected_message); assert(count < 2); order[count++] = 'T';
}
static void send_chat(int fd, int sender, int object, char *message, int color, int identified) {
    memset(order, 0, sizeof(order)); metadata[0] = 0; count = 0;
    expected_fd=fd; expected_object=object; expected_color=color; expected_message=message;
    StoneAge_ChatIdentitySend(fd, sender, object, message, color);
    assert(strcmp(order, identified ? "ST" : "T") == 0);
}
int main(void) {
    char message[] = {'P','|',(char)0xc4,(char)0xe3,(char)0xba,(char)0xc3,'\\','z',0};
    char long_message[2049];
    send_chat(8,1,42,message,3,1);
    assert(strcmp(metadata,"AICHAT|1|42|3|pc1_0123456789abcdef0123456789abcdef|507cc4e3bac35c7a") == 0);
    send_chat(8,-1,-1,message,3,0); /* system */
    send_chat(8,2,42,message,3,0); /* NPC or missing/pending loaded identity */
    send_chat(8,1,43,message,3,0); /* inconsistent sender/object */
    send_chat(-1,1,42,message,3,0);
    send_chat(8,1,42,"",3,0);
    send_chat(8,1,42,NULL,3,0);
    memset(long_message,'x',sizeof(long_message)); long_message[2047]=0;
    send_chat(8,1,42,long_message,-2147483647-1,1);
    long_message[2047]='x'; long_message[2048]=0;
    send_chat(8,1,42,long_message,3,0);
    puts("native chat identity: sender wiring, exact CP936/escaped bytes, order, bounds and fallback passed");
    return 0;
}
''')
    subprocess.run(['cc', '-std=c99', '-Wall', '-Wextra', '-Werror', '-I'+str(work), '-I'+str(MODERN), str(work/'harness.c'), str(MODERN/'stoneage_chat_identity.c'), '-o', str(work/'chat-test')], env=env, check=True)
    subprocess.run([str(work/'chat-test')], env=env, check=True)
