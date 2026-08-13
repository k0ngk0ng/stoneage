#!/usr/bin/env python3
"""Rewrite the legacy quiz module so it is safe on 64-bit hosts.

The 2.5 archive uses a historic non-UTF-8 source encoding. Keeping the broad
state rewrite here as deterministic byte substitutions avoids transcoding or
silently damaging the original localized strings.
"""

from pathlib import Path
import sys


def replace_once(data: bytes, old: bytes, new: bytes, label: str) -> bytes:
    count = data.count(old)
    if count != 1:
        raise SystemExit(f"{label}: expected one match, found {count}")
    return data.replace(old, new, 1)


def main() -> int:
    if len(sys.argv) != 2:
        print(f"usage: {Path(sys.argv[0]).name} PATH/TO/npc_quiz.c", file=sys.stderr)
        return 2

    target = Path(sys.argv[1])
    data = target.read_bytes()

    if b"STONEAGE_SAFE_QUIZ_STATE" in data:
        return 0

    data = replace_once(
        data,
        b"int *NPC_GetQuestion(int meindex);",
        b"BOOL NPC_GetQuestion(int meindex, int *tbl, int capacity);",
        "question helper prototype",
    )
    data = replace_once(
        data,
        b"\tint *tbl;\n\tint i;\n\n\tNPC_Util_GetArgStr( meindex, argstr, sizeof( argstr));",
        b"\tint tbl[quizcnt + 1];\n\tint i;\n\n\tNPC_Util_GetArgStr( meindex, argstr, sizeof( argstr));",
        "quiz init question buffer",
    )
    data = replace_once(
        data,
        b"\ttbl =  NPC_GetQuestion( meindex);\n\n"
        b"\tif( CHAR_getWorkInt( meindex, CHAR_WORK_QUIZNUM) > ( tbl[0] - 1)){",
        b"\tif( !NPC_GetQuestion( meindex, tbl, arraysizeof(tbl)) ) return FALSE;\n\n"
        b"\tif( CHAR_getWorkInt( meindex, CHAR_WORK_QUIZNUM) > OLDNO ||\n"
        b"\t\tCHAR_getWorkInt( meindex, CHAR_WORK_QUIZNUM) > ( tbl[0] - 1)){",
        "quiz init question lookup",
    )
    data = replace_once(
        data,
        b"\t\t\tint *tbl;\n\t\t\tint point;\n\t\t\tint *pl_ptr;\n",
        b"\t\t\tint tbl[quizcnt + 1];\n\t\t\tint point;\n\t\t\tint *pl_ptr;\n",
        "quiz window question buffer",
    )
    data = replace_once(
        data,
        b"\t\t\ttbl =  NPC_GetQuestion( meindex);\n\t\t\t\n\t\t\t\n",
        b"\t\t\tif( !NPC_GetQuestion( meindex, tbl, arraysizeof(tbl)) ) {\n"
        b"\t\t\t\tfree(PLAYER.ptr);\n\t\t\t\treturn;\n\t\t\t}\n\t\t\t\n",
        "quiz window question lookup",
    )

    question_start = data.index(b"int *NPC_GetQuestion(int meindex)\n{")
    question_end = data.index(b"\nBOOL NPC_QuizItemFullCheck(int meindex,int talker)", question_start)
    question_function = b"""BOOL NPC_GetQuestion(int meindex, int *tbl, int capacity)
{
	char argstr[NPC_UTIL_GETARGSTR_BUFSIZE];
	int i,j;
	int type=0;
	int answer=0;
	int level=0;
	char buf[16];
	NPC_Util_GetArgStr( meindex, argstr, sizeof( argstr));

	if(NPC_Util_GetStrFromStrWithDelim(argstr,"Type",buf, sizeof( buf)) != NULL )
		type = atoi(buf);
	if( type <= 0) type = 0xffff;
	if(NPC_Util_GetStrFromStrWithDelim(argstr,"Answer",buf, sizeof( buf)) != NULL )
		answer = atoi(buf);
	if( answer <= 0) answer = 0xffff;
	if(NPC_Util_GetStrFromStrWithDelim(argstr,"Level",buf, sizeof( buf)) != NULL )
		level = atoi(buf);
	if( level <= 0) level = 0xffff;

	for(j=0,i=0; i < quizcnt ;i++){
		if( (type & (1 << (Quiz[i].type-1))) != (1 << (Quiz[i].type-1))) continue;
		if( (answer & Quiz[i].answertype) != Quiz[i].answertype) continue;
		if( (level & Quiz[i].level) != Quiz[i].level) continue;
		j++;
	}
	if( tbl == NULL || capacity < j + 1 ) return FALSE;

	tbl[0] = j+1;
	for(j=1,i=0; i < quizcnt ;i++){
		if( (type & (1 << (Quiz[i].type-1))) != (1 << (Quiz[i].type-1))) continue;
		if( (answer & Quiz[i].answertype) != Quiz[i].answertype) continue;
		if( (level & Quiz[i].level) != Quiz[i].level) continue;
		tbl[j++] = i;
	}
	return TRUE;
}
"""
    data = data[:question_start] + question_function + data[question_end:]

    data = replace_once(
        data,
        b"struct pl{\n"
        b"\tint talkerindex;\n"
        b"\tint quizno;\n"
        b"\tint answer;\n"
        b"\tint ansno;\n"
        b"\tint oldno[OLDNO];\n"
        b"\tint *ptr;\n"
        b"};\n\n\n"
        b"static int quizcnt = 0;\n",
        b"struct pl{\n"
        b"\tint in_use;\n"
        b"\tint meindex;\n"
        b"\tint slot;\n"
        b"\tint talkerindex;\n"
        b"\tint quizno;\n"
        b"\tint answer;\n"
        b"\tint ansno;\n"
        b"\tint oldno[OLDNO];\n"
        b"};\n\n\n"
        b"static int quizcnt = 0;\n"
        b"static struct pl *QuizPlayer = NULL;\n"
        b"static int QuizPlayerCount = 0;\n",
        "player state declaration",
    )

    data = replace_once(
        data,
        b"static void NPC_Quiz_selectWindow( int meindex, int talker, int num);\n",
        b"static void NPC_Quiz_selectWindow( int meindex, int talker, int num);\n"
        b"static struct pl *NPC_QuizGetPlayer(int meindex, int slot);\n"
        b"static void NPC_QuizReleasePlayer(int meindex, int slot);\n"
        b"static void NPC_QuizReleaseTalker(int talker);\n",
        "helper prototypes",
    )

    marker = b"BOOL NPC_QUIZPARTY_CHAECK(int meindex,int talker);\n\n"
    helpers = b"""BOOL NPC_QUIZPARTY_CHAECK(int meindex,int talker);

/* STONEAGE_SAFE_QUIZ_STATE: never store a pointer in a 32-bit work int. */
static struct pl *NPC_QuizGetPlayer(int meindex, int slot)
{
	int talker;
	struct pl *player;

	if( QuizPlayer == NULL || slot < 0 || slot >= MEPLAYER ) return NULL;
	talker = CHAR_getWorkInt( meindex, CHAR_WORK_PLAYER1 + slot);
	if( talker < 0 || talker >= QuizPlayerCount ) {
		CHAR_setWorkInt( meindex, CHAR_WORK_PLAYER1 + slot, -1);
		return NULL;
	}
	player = &QuizPlayer[talker];
	if( !player->in_use || player->talkerindex != talker ||
		player->meindex != meindex || player->slot != slot ) {
		CHAR_setWorkInt( meindex, CHAR_WORK_PLAYER1 + slot, -1);
		return NULL;
	}
	return player;
}

static void NPC_QuizReleasePlayer(int meindex, int slot)
{
	struct pl *player;

	player = NPC_QuizGetPlayer(meindex, slot);
	if( player != NULL ) memset(player, 0, sizeof(*player));
	if( slot >= 0 && slot < MEPLAYER )
		CHAR_setWorkInt( meindex, CHAR_WORK_PLAYER1 + slot, -1);
}

static void NPC_QuizReleaseTalker(int talker)
{
	struct pl *player;
	int meindex;
	int slot;

	if( QuizPlayer == NULL || talker < 0 || talker >= QuizPlayerCount ) return;
	player = &QuizPlayer[talker];
	if( !player->in_use ) return;
	meindex = player->meindex;
	slot = player->slot;
	if( CHAR_CHECKINDEX(meindex) && slot >= 0 && slot < MEPLAYER &&
		CHAR_getWorkInt(meindex, CHAR_WORK_PLAYER1 + slot) == talker )
		CHAR_setWorkInt(meindex, CHAR_WORK_PLAYER1 + slot, -1);
	memset(player, 0, sizeof(*player));
}

"""
    data = replace_once(data, marker, helpers, "helper implementations")

    data = replace_once(
        data,
        b"\t\t\tint tbl[quizcnt + 1];\n"
        b"\t\t\tint point;\n"
        b"\t\t\tint *pl_ptr;\n",
        b"\t\t\tint tbl[quizcnt + 1];\n"
        b"\t\t\tstruct pl *pl_ptr;\n",
        "quiz window locals",
    )
    data = replace_once(
        data,
        b"\t\t\tp_no = CHAR_getWorkInt( talker, CHAR_WORKSHOPRELEVANT);\n"
        b"\t\t\tpoint = CHAR_getWorkInt( meindex, CHAR_WORK_PLAYER1 + p_no);\n"
        b"\t\t\tpl_ptr = (int *)point;\n\t\t\t\n"
        b"\t\t\tmemcpy(&PLAYER, pl_ptr, sizeof(struct pl));\n",
        b"\t\t\tp_no = CHAR_getWorkInt( talker, CHAR_WORKSHOPRELEVANT);\n"
        b"\t\t\tpl_ptr = NPC_QuizGetPlayer(meindex, p_no);\n"
        b"\t\t\tif( pl_ptr == NULL || pl_ptr->talkerindex != talker ) return;\n"
        b"\t\t\tmemcpy(&PLAYER, pl_ptr, sizeof(PLAYER));\n",
        "quiz window lookup",
    )

    completion_start = data.index(
        b"\t\t\t\tCHAR_setWorkInt( meindex, CHAR_WORK_PLAYER1 + \n",
        data.index(b"static void NPC_Quiz_selectWindow"),
    )
    completion_end = data.index(b"\t\t\t\tif(warp_flg != -1){", completion_start)
    data = (
        data[:completion_start]
        + b"\t\t\t\tNPC_QuizReleasePlayer(meindex,\n"
          b"\t\t\t\t\tCHAR_getWorkInt(talker, CHAR_WORKSHOPRELEVANT));\n\t\t\t\t\n"
        + data[completion_end:]
    )
    data = replace_once(
        data,
        b"\t\t\tif( !NPC_GetQuestion( meindex, tbl, arraysizeof(tbl)) ) {\n"
        b"\t\t\t\tfree(PLAYER.ptr);\n"
        b"\t\t\t\treturn;\n"
        b"\t\t\t}\n",
        b"\t\t\tif( !NPC_GetQuestion( meindex, tbl, arraysizeof(tbl)) ) {\n"
        b"\t\t\t\tNPC_QuizReleasePlayer(meindex, p_no);\n"
        b"\t\t\t\treturn;\n"
        b"\t\t\t}\n",
        "question lookup failure",
    )
    data = replace_once(
        data,
        b"\t\t\tif( CHAR_getWorkInt( meindex, CHAR_WORK_QUIZNUM) > ( tbl[0] - 1))\n"
        b"\t\t\t{\n\t\t\t\tfree(PLAYER.ptr);\n",
        b"\t\t\tif( CHAR_getWorkInt( meindex, CHAR_WORK_QUIZNUM) > ( tbl[0] - 1))\n"
        b"\t\t\t{\n\t\t\t\tNPC_QuizReleasePlayer(meindex, p_no);\n",
        "question count failure",
    )

    data = data.replace(
        b"memcpy(pl_ptr,&PLAYER,sizeof(struct pl));",
        b"memcpy(pl_ptr,&PLAYER,sizeof(PLAYER));",
    )

    data = replace_once(
        data,
        b"\t  \tint point;\n\t\tint *pl_ptr;\n",
        b"\t  \tstruct pl *pl_ptr;\n",
        "window reply locals",
    )
    data = replace_once(
        data,
        b"\t\t\tp_no = CHAR_getWorkInt( talkerindex, CHAR_WORKSHOPRELEVANT);\n"
        b"\t\t\tpoint = CHAR_getWorkInt( meindex, CHAR_WORK_PLAYER1 + p_no);\n"
        b"\t\t\tpl_ptr = (int *)point;\n\t\t\n\n"
        b"\t\t\tmemcpy(&PLAYER,pl_ptr,sizeof(struct pl));\n",
        b"\t\t\tp_no = CHAR_getWorkInt( talkerindex, CHAR_WORKSHOPRELEVANT);\n"
        b"\t\t\tpl_ptr = NPC_QuizGetPlayer(meindex, p_no);\n"
        b"\t\t\tif( pl_ptr == NULL || pl_ptr->talkerindex != talkerindex ) return;\n"
        b"\t\t\tmemcpy(&PLAYER,pl_ptr,sizeof(PLAYER));\n",
        "window reply lookup",
    )
    data = replace_once(
        data,
        b"\t\t\told_no = CHAR_getWorkInt( talkerindex, CHAR_WORKSHOPRELEVANTSEC)-1;\n"
        b"\t\t\ti=\tPLAYER.oldno[old_no];\n",
        b"\t\t\told_no = CHAR_getWorkInt( talkerindex, CHAR_WORKSHOPRELEVANTSEC)-1;\n"
        b"\t\t\tif( old_no < 0 || old_no >= OLDNO ) return;\n"
        b"\t\t\ti=\tPLAYER.oldno[old_no];\n",
        "old question bounds",
    )
    cancel_start = data.index(
        b"\t\t\t}else if(select == WINDOW_BUTTONTYPE_CANCEL){\n",
        data.index(b"void NPC_QuizWindowTalked"),
    )
    cancel_end = data.index(b"\t\t\t}else if( atoi( data) == 0){", cancel_start)
    data = (
        data[:cancel_start]
        + b"\t\t\t}else if(select == WINDOW_BUTTONTYPE_CANCEL){\n"
          b"\t\t\t\tNPC_QuizReleasePlayer(meindex, p_no);\n"
        + data[cancel_end:]
    )

    function_start = data.index(b"\nBOOL NPC_PlayerCheck(int meindex,int talker)\n\t{") + 1
    function_end = data.index(b"\nint NPC_RealyCheack(int meindex,int talker)", function_start)
    player_check = b"""BOOL NPC_PlayerCheck(int meindex,int talker)
	{
	int i;
	int k;
	struct pl *Player;

	NPC_RealyCheack( meindex, talker);
	for(i = 0 ; i < MEPLAYER ; i++){
		if( CHAR_getWorkInt( meindex, CHAR_WORK_PLAYER1 + i ) == -1 ) break;
	}
	if(i == MEPLAYER) return FALSE;
	if( QuizPlayer == NULL || talker < 0 || talker >= QuizPlayerCount ) return FALSE;

	NPC_QuizReleaseTalker(talker);
	Player = &QuizPlayer[talker];
	memset(Player, 0, sizeof(*Player));
	Player->in_use = TRUE;
	Player->meindex = meindex;
	Player->slot = i;
	Player->talkerindex = talker;
	for(k=0 ; k < OLDNO; k++) Player->oldno[k] = -1;

	CHAR_setWorkInt( talker, CHAR_WORKSHOPRELEVANTSEC,0);
	CHAR_setWorkInt( talker, CHAR_WORKSHOPRELEVANT, i);
	CHAR_setWorkInt( talker, CHAR_WORKSHOPRELEVANTTRD,0);
	CHAR_setWorkInt( meindex, CHAR_WORK_PLAYER1 + i, talker );
	return TRUE;

}
"""
    data = data[:function_start] + player_check + data[function_end:]

    function_start = data.index(b"\nint NPC_RealyCheack(int meindex,int talker)\n{") + 1
    function_end = data.index(b"\nBOOL QUIZ_initQuiz( char *filename)", function_start)
    real_check = b"""int NPC_RealyCheack(int meindex,int talker)
{
	int fl, x, y;
	int i, j;
	int px[10] = {0, 1, 0,-1, 1,-1, 1, 0,-1};
	int py[10] = {0,-1,-1,-1, 0, 0, 1, 1, 1};
	int objindex;
	OBJECT object;
	int talkerindex;
	BOOL okflg;
	struct pl *player;

	fl = CHAR_getInt( meindex, CHAR_FLOOR);
	for(j = 0 ; j < MEPLAYER ; j++) {
		if( CHAR_getWorkInt(meindex, CHAR_WORK_PLAYER1 + j) == -1 ) continue;
		player = NPC_QuizGetPlayer(meindex, j);
		if( player == NULL ) continue;
		talkerindex = player->talkerindex;
		okflg = FALSE;
		for(i=0 ; i < 10 ; i++) {
			x = px[i] + CHAR_getInt( meindex, CHAR_X);
			y = py[i] + CHAR_getInt( meindex, CHAR_Y);
			for(object = MAP_getTopObj(fl,x,y) ; object; object = NEXT_OBJECT(object)) {
				objindex = GET_OBJINDEX(object);
				if( OBJECT_getType(objindex) == OBJTYPE_CHARA &&
					OBJECT_getIndex(objindex) == talkerindex ) {
					okflg = TRUE;
					break;
				}
			}
			if(okflg == TRUE) break;
		}
		if(okflg == FALSE || talkerindex == talker)
			NPC_QuizReleasePlayer(meindex, j);
	}
	return -1;
}
"""
    data = data[:function_start] + real_check + data[function_end:]

    allocation_marker = b"\tQuiz = allocateMemory( sizeof(NPC_QUIZ) * (quizcnt+1) );\n"
    allocation_end = data.index(b"    linenum = 0;", data.index(allocation_marker))
    state_allocation = b"""	QuizPlayerCount = CHAR_getPlayerMaxNum();
	QuizPlayer = allocateMemory( sizeof(struct pl) * QuizPlayerCount );
	if( QuizPlayer == NULL ){
		fprint( "Can't allocate quiz player state %d\\n",
				sizeof(struct pl) * QuizPlayerCount );
		fclose( f );
		return FALSE;
	}
	memset(QuizPlayer, 0, sizeof(struct pl) * QuizPlayerCount);
"""
    data = data[:allocation_end] + state_allocation + data[allocation_end:]

    if b"(int)ptr" in data or b"free(PLAYER.ptr)" in data or b"PLAYER.ptr" in data:
        raise SystemExit("unsafe quiz pointer operation remains after rewrite")

    target.write_bytes(data)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
