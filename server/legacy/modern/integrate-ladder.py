#!/usr/bin/env python3
"""Checked, byte-preserving ladder integration after patch 0032.

Only the build copy is edited. Every anchor is checked before any file is
written; source drift fails the build instead of silently omitting a hook.
"""
from pathlib import Path
import hashlib
import re
import sys

# A stale build copy must never skip hooks added without a manual version bump.
MARKER = b"/* STONEAGE_LADDER_INTEGRATION_V3_" + hashlib.sha256(Path(__file__).read_bytes()).hexdigest()[:16].encode() + b" */\n"
ARCHIVE = Path(__file__).resolve().parent.parent / "source/2.5/gmsv"


def replace(data, old, new, count=1):
    if data.count(old) != count:
        raise ValueError(f"expected {count} anchors, found {data.count(old)}: {old!r}")
    return data.replace(old, new)


def definition_start(data, name):
    matches = list(re.finditer(rb"(?m)^[ \t]*(?:(?:static|INLINE|ANYTHREAD|int|void|BOOL|char)[ \t*]+)+" +
                              name.encode() + rb"\s*\([^;{}]*\)\s*\{", data))
    if len(matches) != 1:
        raise ValueError(f"expected one definition of {name}, got {len(matches)}")
    return matches[0].end()


def body_range(data, name):
    """Locate a C definition; ignore braces in strings and comments."""
    start = definition_start(data, name)
    depth, i = 1, start
    while i < len(data):
        if data[i:i+2] == b"/*":
            end = data.find(b"*/", i+2)
            if end < 0:
                raise ValueError("unterminated C comment")
            i = end + 2
        elif data[i:i+2] == b"//":
            end = data.find(b"\n", i+2)
            i = end if end >= 0 else len(data)
        elif data[i] in (34, 39):
            quote = data[i]
            i += 1
            while i < len(data):
                if data[i] == 92:
                    i += 2
                elif data[i] == quote:
                    i += 1
                    break
                else:
                    i += 1
        else:
            if data[i] == 123:
                depth += 1
            elif data[i] == 125:
                depth -= 1
                if depth == 0:
                    return start, i
            i += 1
    raise ValueError(f"unterminated definition of {name}")


def entry(data, name, code):
    start = definition_start(data, name)
    return data[:start] + b"\n    " + code.encode() + b"\n" + data[start:]


def within(data, name, old, new, count=1):
    start, end = body_range(data, name)
    return data[:start] + replace(data[start:end], old, new, count) + data[end:]


def between(data, first, following, old, new, count=1):
    # Some preserved functions contain alternate preprocessor branches whose
    # raw braces are unbalanced. Two unique definitions bound those edits.
    start, end = definition_start(data, first), definition_start(data, following)
    if end <= start:
        raise ValueError("unexpected function ordering")
    return data[:start] + replace(data[start:end], old, new, count) + data[end:]


def transform(name, data):
    if MARKER in data:
        return data
    if b"STONEAGE_LADDER_INTEGRATION_" in data:
        raise ValueError("outdated ladder integration; regenerate the build copy from historical sources")
    if name == "include/addressbook.h":
        data = replace(data, b"} ADDRESSBOOK_entry;",
                       b"    char persistent_character_id[37];\n} ADDRESSBOOK_entry;")
        return MARKER + data
    if name == "main.c":
        data = replace(data, b"BATTLE_Loop();", b"StoneAge_LadderTick();\n        BATTLE_Loop();")
    elif name == "callfromcli.c":
        # ClientLogin is already authenticated by the Go gateway. SAAC's
        # reply token must identify this exact connection generation.
        data = within(data, "lssproto_ClientLogin_recv", b"CONNECT_setCdkey( fd, cdkey );",
                       b"StoneAge_LadderAuthenticated(fd, 0);\n    CONNECT_setCdkey( fd, cdkey );")
        data = within(data, "lssproto_ClientLogin_recv", b"saacproto_ACCharLogin_send( acfd, fd,",
                       b"saacproto_ACCharLogin_send( acfd, CONNECT_getFdid(fd),")
        data = within(data, "lssproto_CharLogin_recv", b"CONNECT_setCharname( fd, charname );",
                       b"if (StoneAge_LadderResume(fd, charname)) return;\n    CONNECT_setCharname( fd, charname );")
        data = entry(data, "lssproto_CharLogout_recv",
                     'if (StoneAge_LadderDisconnect(fd)) { lssproto_CharLogout_send(fd, SUCCESSFUL, "retained"); return; }')
        data = entry(data, "lssproto_S_recv", "if (StoneAge_LadderRequest(fd, category)) return;")
        data = within(data, "lssproto_WN_recv", b"fd_charaindex = CONNECT_getCharaindex( fd );",
                       b"fd_charaindex = CONNECT_getCharaindex( fd );\n    StoneAge_LadderWindow(fd_charaindex, 0);")
        exempt = {"ClientLogin", "CharLogin", "CharLogout", "CharList", "CreateNewChar", "CharDelete"}
        handlers = re.findall(rb"void\s+lssproto_(\w+)_recv\s*\(\s*int\s+fd\b", data)
        if len(handlers) < 30:
            raise ValueError("client operation inventory unexpectedly small")
        for op in handlers:
            text = op.decode()
            if text not in exempt:
                data = entry(data, "lssproto_" + text + "_recv",
                             f'if (StoneAge_LadderGuard(fd, "{text}")) return;')
    elif name == "callfromac.c":
        # A cold or unavailable presence directory is not proof that the
        # character was deleted. Only an explicit deletion broadcast may
        # turn a missing entry into destructive address-book removal.
        data = within(data, "saacproto_DBGetEntryString_recv", b"mode = 0;",
                      b"if (msgid2 != 1) return;\n            mode = 0;")
        data = within(data, "saacproto_Broadcast_recv",
                      b"saacproto_DBGetEntryString_send( acfd, DB_ADDRESSBOOK, escapebuf, 0,0);",
                      b'saacproto_DBGetEntryString_send( acfd, DB_ADDRESSBOOK, escapebuf, 0, !strcmp(message, "chardelete") ? 1 : 0);')
        data = entry(data, "saacproto_ACCharLogin_recv", """clifd = getfdFromFdid(clifd);
    if (!CONNECT_checkfd(clifd) || !CONNECT_isNOTLOGIN(clifd)) return;
    StoneAge_LadderAuthenticated(clifd, flag == 1);""")
        data = entry(data, "saacproto_ACCharSave_recv",
                     "if (StoneAge_LadderOfflineSaveReply(retfd, result)) return;")
    elif name == "net.c":
        data = entry(data, "CONNECT_beginEOFLogoutSave",
                     "if (StoneAge_LadderDisconnect(sockfd)) return FALSE;")
    elif name == "char/char.c":
        data = entry(data, "_CHAR_warpToSpecificPoint",
                     "if (StoneAge_LadderReserved(charaindex)) return FALSE;")
        data = within(data, "check_TimeTicket", b"if( !CHAR_CHECKINDEX(i) )\tcontinue;",
                      b"if( !CHAR_CHECKINDEX(i) )\tcontinue;\n        if (StoneAge_LadderReserved(i)) continue;")
        data = within(data, "_CHAR_logout", b"charindex = CONNECT_getCharaindex( clifd );",
                       b"if (StoneAge_LadderDisconnect(clifd)) return TRUE;\n\tcharindex = CONNECT_getCharaindex( clifd );")
        data = replace(data, b'lssproto_CharLogin_send( clifd, SUCCESSFUL,"" );',
                       b'lssproto_CharLogin_send( clifd, SUCCESSFUL,"" );\n\tStoneAge_LadderCharacterLoaded(charaindex);')
        data = entry(data, "CHAR_CharaDelete", "StoneAge_LadderCharacterDeleted(charaindex);")
        data = entry(data, "CHAR_makeOptionString", "char *ladder_saved = StoneAge_LadderSavedOptions(ch);\n    if (ladder_saved) return ladder_saved;")
        data = within(data, "CHAR_charSaveFromConnectAndChar", b'if( chardata == "\\0" )return FALSE;',
                      b'if (!chardata || !*chardata) return FALSE;')
        data = replace(data, b"CHAR_JoinParty_Main( charaindex, toindex);",
                       b"if (!StoneAge_LadderInteractionAllowed(charaindex, toindex)) break;\n            CHAR_JoinParty_Main( charaindex, toindex);")
    elif name == "char/char_party.c":
        data = entry(data, "CHAR_JoinParty",
                     "if (!StoneAge_LadderInteractionAllowed(charaindex, charaindex)) return FALSE;")
        data = within(data, "CHAR_JoinParty", b"parray = CHAR_getEmptyPartyArray( targetindex) ;",
                       b"if (!StoneAge_LadderInteractionAllowed(charaindex, targetindex)) continue;\n\t\tparray = CHAR_getEmptyPartyArray( targetindex) ;")
        data = entry(data, "CHAR_JoinParty_Main",
                     "if (!StoneAge_LadderInteractionAllowed(charaindex, targetindex)) return;")
        # A stale party-choice window may name a member rather than its
        # leader. Check the resolved leader before either roster is changed.
        data = within(data, "CHAR_JoinParty_Main", b"parray = CHAR_getEmptyPartyArray( toindex) ;",
                       b"if (!StoneAge_LadderInteractionAllowed(charaindex, toindex)) return;\n\tparray = CHAR_getEmptyPartyArray( toindex) ;")
    elif name == "char/trade.c":
        data = entry(data, "TRADE_Search",
                     "if (!StoneAge_LadderInteractionAllowed(meindex, meindex)) return FALSE;")
        data = within(data, "TRADE_Search", b"if (index == meindex)\tcontinue;",
                       b"if (index == meindex)\tcontinue;\n        if (!StoneAge_LadderInteractionAllowed(meindex, index)) continue;")
        for function in ("TRADE_ShowItem", "TRADE_Close"):
            data = within(data, function, b"toindex = CONNECT_getCharaindex(tofd);",
                           b"toindex = CONNECT_getCharaindex(tofd);\n    if (!StoneAge_LadderInteractionAllowed(meindex, toindex)) return;")
    elif name == "char/petmail.c":
        data = entry(data, "PETMAIL_Loopfunc", """int ladder_recipient = -1, ladder_sender = -1;
    if (PETMAIL_getOffmsg(CHAR_getInt(index, CHAR_PETMAILBUFINDEX)))
        ladder_recipient = PETMAIL_CheckPlayerExist(index, 0);
    if (*CHAR_getChar(index, CHAR_OWNERCDKEY) && *CHAR_getChar(index, CHAR_OWNERCHARANAME))
        ladder_sender = PETMAIL_CheckPlayerExist(index, 1);
    if (StoneAge_LadderReserved(ladder_recipient) || StoneAge_LadderReserved(ladder_sender)) {
        CHAR_setInt(index, CHAR_PETMAILIDLETIME, NowTime.tv_sec);
        return;
    }""")
    elif name == "char/char_base.c":
        data = entry(data, "CHAR_makeStringFromCharData", "char *ladder_saved = StoneAge_LadderSavedCharacter(one);\n    if (ladder_saved) return ladder_saved;")
        data = entry(data, "CHAR_makePetStringFromPetIndex", "char *ladder_saved = StoneAge_LadderSavedPet(petindex);\n    if (ladder_saved) return ladder_saved;")
        data = within(data, "CHAR_makeStringFromCharData", b'if( petstring == "\\0" ) continue;',
                      b'if (!petstring || !*petstring) goto MAKESTRINGERR;', 2)
        data = within(data, "CHAR_makePetStringFromPetIndex", b"\treturn CHAR_petdataString;",
                      b'\tif (!StoneAge_LadderAppendPetItems(petindex, CHAR_petdataString, sizeof(CHAR_petdataString))) return "\\0";\n\treturn CHAR_petdataString;')
        data = within(data, "CHAR_makePetFromStringToArg", b"\t\t\tfound = FALSE;",
                      b"""            {
                int pet_item = StoneAge_LadderParsePetItem(ch, petfirstToken, petsecondToken);
                if (pet_item < 0) { CHAR_endCharData(ch); return -1; }
                if (pet_item > 0) continue;
            }
\t\t\tfound = FALSE;""")
        data = replace(data, b"CHAR_chara[index].data[element] = data;",
                       b"CHAR_chara[index].data[element] = data;\n    if (element == CHAR_HP) StoneAge_LadderHPChanged(index, buf, data);", 2)
        data = replace(data, b"CHAR_chara[index].workint[element] = data;",
                       b"CHAR_chara[index].workint[element] = data;\n    StoneAge_LadderStatusChanged(index, element, buf, data);")
    elif name == "char/addressbook.c":
        data = within(data, "ADDRESSBOOK_makeEntryFromCharaindex", b"\treturn TRUE;",
                      b"\tStoneAge_LadderBindContact(charaindex, ae);\n\treturn TRUE;")
        data = within(data, "ADDRESSBOOK_makeAddressbookString",
                      b'"%s|%s|%d|%d|%d|%d",', b'"%s|%s|%d|%d|%d|%d|%s",')
        data = within(data, "ADDRESSBOOK_makeAddressbookString", b"a->graphicsno,a->transmigration);",
                      b"a->graphicsno,a->transmigration,StoneAge_LadderContactIdentity(a));")
        data = within(data, "ADDRESSBOOK_makeAddressbookEntry", b"\treturn FALSE;",
                      b"\tStoneAge_LadderParseContact(a, in);\n\treturn FALSE;")
        # Existing cards are refreshed only by another explicit, native card
        # exchange with the live character, never by matching an old name.
        data = within(data, "ADDRESSBOOK_addEntry",
                      b"CHAR_getChar(meindex, CHAR_NAME) )  >= 0  )\t{\n\t\t\tcontinue;",
                      b"CHAR_getChar(meindex, CHAR_NAME) )  >= 0  )\t{\n            StoneAge_LadderRefreshContacts(meindex, index);\n\t\t\tcontinue;")
        # The successful path ends with an explicit return. Inserting before
        # the closing brace would leave the identity refresh unreachable.
        data = within(data, "ADDRESSBOOK_addAddressBook", b"\n\treturn;\n",
                      b"\n    StoneAge_LadderRefreshContacts(meindex, toindex);\n\treturn;\n")
    elif name == "item/item.c":
        data = entry(data, "_ITEM_endExistItemsOne", "if (StoneAge_LadderRetainsItem(index)) return;")
    elif name == "lssproto_serv.c":
        data = entry(data, "lssproto_WN_send", "StoneAge_LadderWindowSent(CONNECT_getCharaindex(fd), windowtype, seqno, objindex, data);")
    elif name == "battle/battle.c":
        data = entry(data, "BATTLE_Battling", "int stoneage_delivered[BATTLE_ENTRY_MAX*2], stoneage_delivered_count=0;")
        data = replace(data,
                       b"\t\tif( BATTLE_CommandSend( charaindex, szAllBattleString ) == TRUE ){\n\t\t}\n\t}\n\tpWatchBattle = pBattle->pNext;",
                       b"\t\tif( BATTLE_CommandSend( charaindex, szAllBattleString ) == TRUE ){\n"
                       b"            stoneage_delivered[stoneage_delivered_count++]=charaindex;\n\t\t}\n\t}\n"
                       b"    StoneAge_LadderRemovedMovie(battleindex,szAllBattleString,stoneage_delivered,stoneage_delivered_count);\n"
                       b"\tpWatchBattle = pBattle->pNext;")
        data = entry(data, "BATTLE_GetProfit", "if (StoneAge_LadderIsBattle(battleindex)) return 0;")
        data = entry(data, "BATTLE_UltimateExtra", "if (StoneAge_LadderKnockout(battleindex, enemyindex)) return;")
        data = entry(data, "BATTLE_NewEntry",
                     "if (!StoneAge_LadderEntryAllowed(charaindex, battleindex)) return BATTLE_ERR_PARAM;")
        data = entry(data, "BATTLE_CommandWait", """int ladder_wait = StoneAge_LadderCommandWait(battleindex, side);
    if (ladder_wait >= 0) return ladder_wait;""")
        data = entry(data, "BATTLE_Finish", "if (StoneAge_LadderFinish(battleindex)) return 0;")
        data = replace(data, b"StoneAge_BattleRecordExecuting(battleindex, attackNo, COM);",
                       b"StoneAge_BattleRecordExecuting(battleindex, attackNo, COM);\n        StoneAge_LadderExecuting(battleindex, attackNo);")
        data = replace(data, b"BATTLE_Battling( battleindex );",
                       b"BATTLE_Battling( battleindex );\n    StoneAge_LadderExecutionEnd();")
        data = entry(data, "BATTLE_StatusSeq", "StoneAge_LadderExecutionEnd();")
        data = between(data, "BATTLE_StatusSeq", "BATTLE_CanMoveCheck", b"switch( StatusTbl[i] )",
                       b"StoneAge_LadderPeriodicSource(charaindex, StatusTbl[i]);\n\t\tswitch( StatusTbl[i] )")
        data = between(data, "BATTLE_StatusSeq", "BATTLE_CanMoveCheck", b"\n#ifdef _SUIT_ITEM\n",
                       b"\n    StoneAge_LadderPeriodicSource(charaindex, -1);\n#ifdef _SUIT_ITEM\n")
        data = between(data, "BATTLE_StatusSeq", "BATTLE_CanMoveCheck", b"\treturn 0;",
                       b"    StoneAge_LadderExecutionEnd();\n\treturn 0;")
    elif name == "battle/battle_command.c":
        data = entry(data, "StoneAge_NativeBattleDispatch",
                     "if (!StoneAge_LadderCommandAllowed(charaindex, command)) return;")
        data = within(data, "BATTLE_CharSendAll",
                       b"if( CHAR_CHECKINDEX( charaindex ) == FALSE )continue;",
                       b"if( CHAR_CHECKINDEX( charaindex ) == FALSE )continue;\n            if (!StoneAge_LadderSendRecipient(charaindex)) continue;")
        data = within(data, "BATTLE_CharSendAll", b'\t\t\tsprintf( szBp, "BP|%X|%X|%X",',
                       b'\t\t\tflg = StoneAge_LadderResumeFlags(charaindex, flg);\n\t\t\tsprintf( szBp, "BP|%X|%X|%X",')
        data = within(data, "BATTLE_CharSendAll", b"pBattle = BattleArray[battleindex].pNext;",
                       b"StoneAge_LadderRemovedObservation(battleindex,szAllBattleString,szAllBattleVitals);\n"
                       b"    if (!StoneAge_LadderSendRecipient(-1)) return;\n\tpBattle = BattleArray[battleindex].pNext;")
    elif name == "battle/battle_event.c":
        data = entry(data, "BATTLE_Escape", "if (StoneAge_LadderEscape(battleindex, attackNo)) return TRUE;")
        # Pass the actual physical attacker, including counters/combos. At a
        # reflection swap the previous defender becomes the damage source.
        for function, writes, swaps in (("BATTLE_DamageSub", 6, 2),
                                       ("BATTLE_DamageSub_FIREKILL", 6, 2),
                                       ("BATTLE_DamageSub2", 2, 1)):
            data = entry(data, function, "int ladder_source = attackindex;")
            data = within(data, function, b"defindex = attackindex;",
                          b"ladder_source = defindex;\n            defindex = attackindex;", swaps)
            start, end = body_range(data, function)
            section, count = re.subn(rb"CHAR_setInt\(\s*(\w+)\s*,\s*CHAR_HP\s*,",
                                     rb"StoneAge_LadderSetHP(ladder_source, \1,", data[start:end])
            if count != writes:
                raise ValueError(f"unexpected HP setters in {function}: {count}")
            data = data[:start] + section + data[end:]
    else:
        raise ValueError(f"unknown source {name}")
    return MARKER + b'#include "stoneage_ladder.h"\n' + data


SOURCES = ("main.c", "callfromcli.c", "callfromac.c", "net.c", "char/char.c",
           "char/char_base.c", "item/item.c", "lssproto_serv.c", "battle/battle.c",
           "battle/battle_command.c", "battle/battle_event.c", "include/addressbook.h", "char/addressbook.c",
           "char/char_party.c", "char/trade.c", "char/petmail.c")


def integrate(root):
    root = Path(root).resolve()
    if root == ARCHIVE.resolve():
        raise ValueError("refusing to edit the historical source; use a build copy")
    pending = {}
    for name in SOURCES:
        try:
            pending[root / name] = transform(name, (root / name).read_bytes())
        except ValueError as exc:
            raise ValueError(f"{name}: {exc}") from exc
    makefile = root / "makefile"
    data = makefile.read_bytes()
    if b"stoneage_ladder_core.c" not in data:
        data = replace(data, b"\n$(CLIRPCSRC) $(SERVRPCSRC)",
                       b"\n$(CLIRPCSRC) $(SERVRPCSRC) stoneage_ladder.c stoneage_ladder_core.c")
        data = replace(data, b"LDFLAGS=-lm -lpthread", b"LDFLAGS=-lm -lpthread -lsqlite3")
    pending[makefile] = data
    for path, data in pending.items():
        if path.read_bytes() != data:
            path.write_bytes(data)


if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit("usage: integrate-ladder.py <build-copy-of-gmsv>")
    integrate(sys.argv[1])
