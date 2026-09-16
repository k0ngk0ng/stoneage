#!/usr/bin/env python3
"""Offline identity lifecycle and native named-string save/load compatibility."""
from pathlib import Path
import os
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[4]
MODERN = ROOT / 'server/legacy/modern'
SOURCE = ROOT / 'server/legacy/source/2.5/gmsv'
BUILD = ROOT / 'build/ai/character-identity'
BUILD.mkdir(parents=True, exist_ok=True)

def loop_at(raw, start):
    begin = raw.index(b'{', start)
    depth = 1
    end = begin + 1
    while depth:
        if raw[end:end+1] == b'{': depth += 1
        elif raw[end:end+1] == b'}': depth -= 1
        end += 1
    return raw[start:end]

with tempfile.TemporaryDirectory(prefix='identity-', dir=BUILD) as temporary:
    work = Path(temporary)
    shutil.copytree(SOURCE / 'include', work / 'include')
    (work / 'char').mkdir()
    for rel in ['include/char_base.h', 'char/char_base.c', 'char/char.c', 'makefile']:
        shutil.copyfile(SOURCE / rel, work / rel)
    # The existing adult-exchange patch supplies this predecessor makefile
    # line. Keep the source tree untouched; validate 0022 against that output.
    prior = (MODERN / 'patches/0020-adult-item-exchange.patch').read_bytes()
    predecessor = next(line[1:] for line in prior.splitlines() if line.startswith(b'+$(CLIRPCSRC)'))
    makefile = (work / 'makefile').read_bytes()
    makefile = makefile.replace(b'$(CLIRPCSRC) $(SERVRPCSRC)\n', predecessor + b'\n')
    (work / 'makefile').write_bytes(makefile)
    subprocess.run(['patch', '--batch', '--fuzz=0', '-p1', '-i', str(MODERN / 'patches/0022-character-identity.patch')], cwd=work, check=True)
    subprocess.run(['patch', '--batch', '--fuzz=0', '-p1', '-i', str(MODERN / 'patches/0023-loaded-character-identity.patch')], cwd=work, check=True)
    raw = (work / 'char/char_base.c').read_bytes()
    header = (work / 'include/char_base.h').read_bytes()
    enum_end = header.index(b'}CHAR_DATACHAR;') + len(b'}CHAR_DATACHAR;')
    enum_start = header.rfind(b'typedef enum', 0, enum_end)
    table_start = raw.index(b'static char* CHAR_setchardata[')
    table_end = raw.index(b'\n};', table_start) + 3
    save_start = raw.index(b'char* CHAR_makeStringFromCharData( Char* one )')
    save_loop = loop_at(raw, raw.index(b'for( i = 0 ; i < CHAR_DATACHARNUM ; i ++ )', save_start))
    load_loop = loop_at(raw, raw.index(b'for( i = 0 ; i < CHAR_DATACHARNUM ; i ++ ){\n            if( strcmp( firstToken', save_start))
    assert b'StoneAge_CharacterIdentityPrepareSave(' not in raw
    save_entry = (work / 'char/char.c').read_bytes()
    assert save_entry.count(b'StoneAge_CharacterIdentityPrepareSave(ch);') == 1
    assert save_entry.index(b'BOOL CHAR_charSaveFromConnectAndChar(') < save_entry.index(b'StoneAge_CharacterIdentityPrepareSave(ch);')
    default = (SOURCE / 'char/char_data.c').read_bytes()
    assert b'nc->string[j].string[0] = \'\\0\';' in default
    # Compile the production module against the patched real native headers
    # before replacing them with the focused harness definitions.
    env = dict(os.environ, TMPDIR=str(work))
    subprocess.run(['cc', '-std=gnu89', '-D_FORTIFY_SOURCE=0', '-fsyntax-only', '-I'+str(work/'include'),
                    '-I'+str(SOURCE/'include'), '-I'+str(MODERN),
                    str(MODERN/'stoneage_character_identity.c')], env=env, check=True)
    layout = b'#include "char.h"\n#include <string.h>\n' + raw[table_start:table_end] + b'\nint main(void) { return CHAR_setchardata[CHAR_PERSISTENTID] == 0 || strcmp(CHAR_setchardata[CHAR_PERSISTENTID], "charid") != 0; }\n'
    (work / 'layout.c').write_bytes(layout)
    subprocess.run(['cc', '-std=gnu89', '-D_FORTIFY_SOURCE=0', '-I'+str(work/'include'),
                    str(work/'layout.c'), '-o', str(work/'layout-test')], env=env, check=True)
    subprocess.run([str(work/'layout-test')], env=env, check=True)
    stub = b'''#ifndef IDENTITY_HARNESS_CHAR_BASE
#define IDENTITY_HARNESS_CHAR_BASE
#define _UNIQUE_P_I
''' + header[enum_start:enum_end] + b'''
#define CHAR_WHICHTYPE 0
#define CHAR_TYPEPLAYER 1
#define CHAR_TYPEPET 2
#define CHAR_WORKPERSISTENTID_LOADED 0
#define CHAR_CHECKINDEX(i) ((i) == 0)
typedef struct { char string[64]; } STRING64;
typedef struct tagChar { int use; int data[1]; STRING64 string[CHAR_DATACHARNUM]; STRING64 workchar[1]; } Char;
Char *CHAR_getCharPointer(int);
#endif
'''
    (work / 'include/char_base.h').write_bytes(stub)
    (work / 'include/char.h').write_text('#include "char_base.h"\n')
    shutil.copyfile(MODERN / 'stoneage_character_identity.h', work / 'include/stoneage_character_identity.h')
    harness = r'''
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <errno.h>
#include <fcntl.h>
#include <unistd.h>
#include "char_base.h"
static int entropy_calls, serial, mode, reads, closed;
Char *CHAR_getCharPointer(int index) {(void)index;return NULL;}
static int test_open(const char *p,int f) { (void)p;(void)f;entropy_calls++;reads=0; if(mode==1)return -1;return 7; }
static ssize_t test_read(int fd,void *p,size_t n) {size_t i;(void)fd;reads++;if(mode==2)return 0;if(mode==3&&reads==2){errno=EIO;return -1;}if(mode==4&&reads==1){errno=EINTR;return -1;}if(n>3)n=3;for(i=0;i<n;i++)((unsigned char*)p)[i]=(unsigned char)serial++;return (ssize_t)n;}
static int test_close(int fd){(void)fd;closed++;return 0;}
#define open test_open
#define read test_read
#define close test_close
#include "stoneage_character_identity.c"
#undef open
#undef read
#undef close
static void require(int value,const char *why){if(!value){fprintf(stderr,"FAIL: %s\n",why);exit(1);}}
static void strcpysafe(char *out,size_t cap,const char *in){if(cap){snprintf(out,cap,"%s",in);}}
static char *makeEscapeString(char *in,char *out,size_t cap){strcpysafe(out,cap,in);return out;}
static char *makeStringFromEscaped(char *in){return in;}
#define CHAR_DELIMITER "\n"
#define fprint(...) ((void)0)
#define print(...) ((void)0)
#define TRUE 1
#define FALSE 0
static char CHAR_dataString[8192];
'''.encode() + raw[table_start:table_end] + b'''
/* These loops are extracted verbatim from the patched native serializer and
 * loader. Harness string helpers suffice because IDs contain only safe ASCII. */
static char *save_fields(Char *one){int i;size_t strlength=0;memset(CHAR_dataString,0,sizeof(CHAR_dataString));
''' + save_loop + rb'''
RETURN: return CHAR_dataString;}
static int load_fields(Char *one,const char *text){char copy[8192],*line,*next;int i;strcpysafe(copy,sizeof(copy),text);line=copy;while(line&&*line){char *eq;char firstToken[128],secondToken[256];next=strchr(line,'\n');if(next)*next++=0;eq=strchr(line,'=');if(!eq)return 0;*eq=0;strcpysafe(firstToken,sizeof(firstToken),line);strcpysafe(secondToken,sizeof(secondToken),eq+1);
''' + load_loop + r'''
NEXT: line=next;}return 1;}
static Char player(void){Char p;memset(&p,0,sizeof(p));p.use=1;p.data[CHAR_WHICHTYPE]=CHAR_TYPEPLAYER;strcpy(p.string[CHAR_NAME].string,"same-name");strcpy(p.string[CHAR_UNIQUECODE].string,"existing-pet-code");return p;}
int main(void){Char first=player(),loaded=player(),replacement=player(),p;char saved[8192],id[64];int calls;
 require(CHAR_setchardata[CHAR_PERSISTENTID]&&strcmp(CHAR_setchardata[CHAR_PERSISTENTID],"charid")==0,"field/table index mismatch");
 require(CHAR_PERSISTENTID==CHAR_DATACHARNUM-1,"existing field indices shifted");
 require(load_fields(&first,"name=same-name\nucode=existing-pet-code\n"),"old archive failed");
 require(first.string[CHAR_PERSISTENTID].string[0]==0,"old archive invented identity on read");
 save_fields(&first);require(first.string[CHAR_PERSISTENTID].string[0]==0,"read-only serialization generated identity");
 require(StoneAge_CharacterIdentityPrepareSave(&first),"save entry generation failed");strcpysafe(saved,sizeof(saved),save_fields(&first));strcpy(id,first.string[CHAR_PERSISTENTID].string);
 require(StoneAge_CharacterIdentityValid(id),"new identity missing");
 require(StoneAge_CharacterIdentityGetLoaded(&first)==NULL,"unsaved identity published");
 require(strstr(saved,"charid=pc1_")!=NULL,"serializer omitted dedicated identity");
 require(strcmp(first.string[CHAR_UNIQUECODE].string,"existing-pet-code")==0,"pet code modified");
 require(load_fields(&loaded,saved),"new archive failed");
 require(StoneAge_CharacterIdentityGetLoaded(&loaded)==NULL,"generic parsing established persistence");
 StoneAge_CharacterIdentityMarkLoaded(&loaded);
 require(StoneAge_CharacterIdentityGetLoaded(&loaded)!=NULL,"accepted login archive identity omitted");
 calls=entropy_calls;require(StoneAge_CharacterIdentityPrepareSave(&loaded),"loaded identity rejected");
 require(strcmp(loaded.string[CHAR_PERSISTENTID].string,id)==0&&entropy_calls==calls,"relogin/repeated save rerolled ID");
 require(StoneAge_CharacterIdentityPrepareSave(&replacement),"replacement failed");
 require(strcmp(replacement.string[CHAR_PERSISTENTID].string,id)!=0,"same-name replacement reused ID");
 require(StoneAge_CharacterIdentityGetLoaded(&replacement)==NULL,"replacement inherited loaded identity");
 strcpy(loaded.string[CHAR_PERSISTENTID].string,replacement.string[CHAR_PERSISTENTID].string);
 require(StoneAge_CharacterIdentityGetLoaded(&loaded)==NULL,"changed in-memory identity published without load");
 strcpy(loaded.string[CHAR_PERSISTENTID].string,id);
 for(mode=1;mode<=3;mode++){p=player();require(!StoneAge_CharacterIdentityPrepareSave(&p),"entropy failure accepted");require(p.string[CHAR_PERSISTENTID].string[0]==0,"partial ID published");}
 mode=4;p=player();require(StoneAge_CharacterIdentityPrepareSave(&p),"EINTR/partial reads not handled");mode=0;
 p=player();strcpy(p.string[CHAR_PERSISTENTID].string,"malformed-existing");calls=entropy_calls;require(!StoneAge_CharacterIdentityPrepareSave(&p)&&entropy_calls==calls,"malformed identity regenerated");require(strcmp(p.string[CHAR_PERSISTENTID].string,"malformed-existing")==0,"malformed identity overwritten");
 p=player();p.data[CHAR_WHICHTYPE]=CHAR_TYPEPET;calls=entropy_calls;require(!StoneAge_CharacterIdentityPrepareSave(&p)&&entropy_calls==calls,"pet assigned player ID");
 p.use=0;p.data[CHAR_WHICHTYPE]=CHAR_TYPEPLAYER;require(!StoneAge_CharacterIdentityPrepareSave(&p),"unused character assigned ID");require(!StoneAge_CharacterIdentityPrepareSave(NULL),"null accepted");
 require(!StoneAge_CharacterIdentityValid(NULL)&&!StoneAge_CharacterIdentityValid("pc1_")&&!StoneAge_CharacterIdentityValid("pc1_0000000000000000000000000000000G"),"invalid identity accepted");
 require(closed>0,"entropy descriptors not closed");puts("character identity lifecycle and native field roundtrip passed");return 0;}
'''.encode()
    (work / 'harness.c').write_bytes(harness)
    env = dict(os.environ, TMPDIR=str(work))
    subprocess.run(['cc','-std=gnu89','-Wall','-Wextra','-Werror','-I'+str(work/'include'),'-I'+str(MODERN),str(work/'harness.c'),'-o',str(work/'identity-test')],env=env,check=True)
    subprocess.run([str(work/'identity-test')],env=env,check=True)
