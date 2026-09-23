/* Training trajectory adapter. All game reads stay on the game thread.
 * Observations derive from the actual per-player BP/BC packets; hidden
 * commands and execution diagnostics are separate server-only events. */
#include "version.h"
#include <ctype.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include "char.h"
#include "char_base.h"
#include "battle.h"
#include "item.h"
#include "pet_skill.h"
#include "stoneage_character_identity.h"
#include "stoneage_battle_record.h"
#include "stoneage_battle_log.h"
#include "stoneage_battle_dataset.h"

typedef struct {
    unsigned long id, seq, dropped, observation[20];
    int active, player[20], exit_side;
    const char *exit_reason;
} RecordedBattle;
static RecordedBattle *matches;
static unsigned long next_match;
static char release[80] = "unknown", ruleset[80] = "unknown";

static void identifier(char *out, size_t size, const char *in)
{
    size_t i;
    if (!in || !*in || strlen(in) >= size) { strcpy(out, "unknown"); return; }
    for (i = 0; in[i]; i++)
        if (!isalnum((unsigned char)in[i]) && in[i] != '-' && in[i] != '_' && in[i] != '.') {
            strcpy(out, "unknown"); return;
        }
    strcpy(out, in);
}

static RecordedBattle *match(int battle)
{
    if (!matches || battle < 0 || battle >= BATTLE_battlenum || !matches[battle].active) return NULL;
    return &matches[battle];
}

static unsigned long emit(int battle, int kind, const char *type, int side, const char *format, ...)
{
    RecordedBattle *m = match(battle);
    char body[BATTLE_LOG_JSON_MAX], json[BATTLE_LOG_JSON_MAX];
    va_list args;
    int n;
    unsigned long seq;
    if (!m) return 0;
    seq = ++m->seq;
    va_start(args, format);
    n = vsnprintf(body, sizeof(body), format, args);
    va_end(args);
    if (n < 0 || n >= sizeof(body)) { m->dropped++; return seq; }
    n = snprintf(json, sizeof(json), "{\"schema_version\":1,\"match_id\":\"%s-%lu\","
        "\"seq\":%lu,\"turn\":%d,\"at\":%ld,\"type\":\"%s\",\"side\":%d,%s}",
        StoneAge_BattleLogSession(), m->id, seq, BattleArray[battle].turn,
        (long)time(NULL), type, side, body);
    if (n < 0 || n >= sizeof(json) || !StoneAge_BattleLogSubmit(battle, m->id, seq, kind, json)) m->dropped++;
    return seq;
}

void StoneAge_BattleRecordInit(void)
{
    if (matches || !StoneAge_BattleLogInit(BATTLE_battlenum)) return;
    matches = (RecordedBattle *)calloc((size_t)BATTLE_battlenum, sizeof(*matches));
    identifier(release, sizeof(release), getenv("STONEAGE_RELEASE_VERSION"));
    identifier(ruleset, sizeof(ruleset), getenv("STONEAGE_BATTLE_RULESET_ID"));
}

static int player_bid(RecordedBattle *m, int c)
{
    int bid;
    for (bid = 0; bid < 20; bid++) if (m->player[bid] == c) return bid;
    return -1;
}

void StoneAge_BattleRecordBegin(int battle)
{
    int side, i, players[2] = {0, 0}, used = 0;
    RecordedBattle *m;
    char identity[80], roster[2400];
    if (!matches || !BATTLE_CHECKINDEX(battle) ||
        (BattleArray[battle].type != BATTLE_TYPE_P_vs_P &&
         BattleArray[battle].type != BATTLE_TYPE_P_vs_E)) return;
    m = &matches[battle];
    memset(m, 0, sizeof(*m));
    for (i = 0; i < 20; i++) m->player[i] = -1;
    m->id = ++next_match;
    m->active = 1;
    m->exit_side = -1;
    roster[0] = 0;
    for (side = 0; side < 2; side++) for (i = 0; i < BATTLE_ENTRY_MAX; i++) {
        BATTLE_ENTRY *entry = &BattleArray[battle].Side[side].Entry[i];
        int c = entry->charaindex, n;
        if (!CHAR_CHECKINDEX(c) || CHAR_getInt(c, CHAR_WHICHTYPE) != CHAR_TYPEPLAYER) continue;
        if (entry->bid < 0 || entry->bid >= 20) { m->dropped++; continue; }
        players[side]++;
        m->player[entry->bid] = c;
        identifier(identity, sizeof(identity), StoneAge_CharacterIdentityGetLoadedByIndex(c));
        n = snprintf(roster+used, sizeof(roster)-used,
            "%s{\"side\":%d,\"bid\":%d,\"character_id\":\"%s\",\"policy_version\":null}",
            used ? "," : "", side, entry->bid, identity);
        if (n < 0 || (size_t)n >= sizeof(roster)-(size_t)used) { m->active = 0; return; }
        used += n;
    }
    emit(battle, BATTLE_LOG_BEGIN, "match_start", -1,
         "\"mode\":\"%s\",\"battle_type\":%d,\"players_per_side\":[%d,%d],"
         "\"release\":\"%s\",\"ruleset_id\":\"%s\","
         "\"field\":%d,\"players\":[%s],\"experiment\":%s,"
         "\"rng_replay_available\":false,\"legal_action_mask_available\":false",
         BattleArray[battle].type == BATTLE_TYPE_P_vs_P ? "pvp" : "pve",
         BattleArray[battle].type, players[0], players[1], release, ruleset,
         BattleArray[battle].field_no, roster, StoneAge_BattleDatasetContext());
}

/* Numeric own-state fields are explicit. No account, name, mail, IP, or
 * unrestricted legacy character arrays enter this dataset. */
static void own_actor(int battle, int side, unsigned long observation, int c, int pet_slot)
{
    long long allocated;
    if (!CHAR_CHECKINDEX(c)) return;
    allocated = (long long)CHAR_getInt(c, CHAR_VITAL) + CHAR_getInt(c, CHAR_STR) +
                CHAR_getInt(c, CHAR_TOUGH) + CHAR_getInt(c, CHAR_DEX);
    emit(battle, BATTLE_LOG_EVENT, "own_actor", side,
        "\"observation_id\":%lu,\"pet_slot\":%d,\"level\":%d,\"graphic\":%d,"
        "\"hp\":%d,\"max_hp\":%d,\"mp\":%d,\"max_mp\":%d,"
        "\"attack\":%d,\"defense\":%d,\"speed\":%d,"
        "\"elements\":[%d,%d,%d,%d],\"dead\":%s,\"active_pet_slot\":%d,\"ride_pet_slot\":%d",
        observation, pet_slot, CHAR_getInt(c, CHAR_LV), CHAR_getInt(c, CHAR_BASEIMAGENUMBER),
        CHAR_getInt(c, CHAR_HP), CHAR_getWorkInt(c, CHAR_WORKMAXHP),
        CHAR_getInt(c, CHAR_MP), CHAR_getWorkInt(c, CHAR_WORKMAXMP),
        CHAR_getWorkInt(c, CHAR_WORKATTACKPOWER), CHAR_getWorkInt(c, CHAR_WORKDEFENCEPOWER),
        CHAR_getWorkInt(c, CHAR_WORKQUICK), CHAR_getWorkInt(c, CHAR_WORKFIXEARTHAT),
        CHAR_getWorkInt(c, CHAR_WORKFIXWATERAT), CHAR_getWorkInt(c, CHAR_WORKFIXFIREAT),
        CHAR_getWorkInt(c, CHAR_WORKFIXWINDAT), CHAR_getFlg(c, CHAR_ISDIE) ? "true" : "false",
        CHAR_getInt(c, CHAR_DEFAULTPET), CHAR_getInt(c, CHAR_RIDEPET));
    emit(battle, BATTLE_LOG_EVENT, "allocation", side,
        "\"observation_id\":%lu,\"pet_slot\":%d,\"raw_units_per_point\":100,"
        "\"vitality_raw\":%d,\"strength_raw\":%d,\"toughness_raw\":%d,\"dexterity_raw\":%d,"
        "\"unspent_points\":%d,\"allocated_raw\":%lld,\"budget_raw\":%lld,"
        "\"level\":%d,\"rebirths\":%d,\"charm\":%d,\"luck\":%d,\"base_elements\":[%d,%d,%d,%d]",
        observation, pet_slot, CHAR_getInt(c, CHAR_VITAL), CHAR_getInt(c, CHAR_STR),
        CHAR_getInt(c, CHAR_TOUGH), CHAR_getInt(c, CHAR_DEX),
        CHAR_getInt(c, CHAR_SKILLUPPOINT), allocated,
        allocated + (long long)CHAR_getInt(c, CHAR_SKILLUPPOINT)*100,
        CHAR_getInt(c, CHAR_LV), CHAR_getInt(c, CHAR_TRANSMIGRATION),
        CHAR_getInt(c, CHAR_CHARM), CHAR_getInt(c, CHAR_LUCK),
        CHAR_getInt(c, CHAR_EARTHAT), CHAR_getInt(c, CHAR_WATERAT),
        CHAR_getInt(c, CHAR_FIREAT), CHAR_getInt(c, CHAR_WINDAT));
    if (pet_slot >= 0) {
        int slot;
        for (slot = 0; slot < CHAR_MAXPETSKILLHAVE; slot++) {
            int id = CHAR_getPetSkill(c, slot), index = PETSKILL_getPetskillArray(id);
            if (index < 0 || !PETSKILL_CHECKINDEX(index)) continue;
            emit(battle, BATTLE_LOG_EVENT, "pet_skill", side,
                "\"observation_id\":%lu,\"pet_slot\":%d,\"slot\":%d,\"id\":%d,"
                "\"field\":%d,\"target_type\":%d,\"cost\":%d",
                observation, pet_slot, slot, id, PETSKILL_getInt(index, PETSKILL_FIELD),
                PETSKILL_getInt(index, PETSKILL_TARGET), PETSKILL_getInt(index, PETSKILL_COST));
        }
    }
}

static void own_configuration(int battle, int side, unsigned long observation, int c)
{
    int slot;
    own_actor(battle, side, observation, c, -1);
    for (slot = 0; slot < CHAR_MAXPETHAVE; slot++)
        own_actor(battle, side, observation, CHAR_getCharPet(c, slot), slot);
    for (slot = 0; slot < CHAR_MAXITEMHAVE; slot++) {
        int index = CHAR_getItemIndex(c, slot), quantity = 1;
        if (!ITEM_CHECKINDEX(index)) continue;
#ifdef _ITEMSET4_TXT
        quantity = ITEM_getInt(index, ITEM_USEPILENUMS);
#endif
        emit(battle, BATTLE_LOG_EVENT, "item", side,
            "\"observation_id\":%lu,\"slot\":%d,\"equipped\":%s,\"id\":%d,"
            "\"item_type\":%d,\"field\":%d,\"target_type\":%d,\"quantity\":%d,"
            "\"magic_id\":%d,\"magic_mp\":%d,\"modifiers\":{\"attack\":%d,\"defense\":%d,\"speed\":%d,\"hp\":%d,\"mp\":%d}",
            observation, slot, slot < CHAR_STARTITEMARRAY ? "true" : "false",
            ITEM_getInt(index, ITEM_ID), ITEM_getInt(index, ITEM_TYPE),
            ITEM_getInt(index, ITEM_ABLEUSEFIELD), ITEM_getInt(index, ITEM_TARGET), quantity,
            ITEM_getInt(index, ITEM_MAGICID), ITEM_getInt(index, ITEM_MAGICUSEMP),
            ITEM_getInt(index, ITEM_MODIFYATTACK), ITEM_getInt(index, ITEM_MODIFYDEFENCE),
            ITEM_getInt(index, ITEM_MODIFYQUICK), ITEM_getInt(index, ITEM_MODIFYHP), ITEM_getInt(index, ITEM_MODIFYMP));
    }
}

/* Split BC in-place, retaining empty name/title fields. Never call the
 * original BC builder twice: it clears pet-fall state as a side effect. */
static int token(const char **cursor, char *out, size_t size)
{
    const char *p = *cursor, *end = strchr(p, '|');
    size_t n = end ? (size_t)(end-p) : strlen(p);
    if (!*p && !end) return 0;
    if (n >= size) return 0;
    memcpy(out, p, n); out[n] = 0;
    *cursor = end ? end+1 : p+n;
    return 1;
}

void StoneAge_BattleRecordObservation(int battle, int c, const char *bp, const char *bc)
{
    RecordedBattle *m = match(battle);
    int side, field, bid, flags, mp, i, owner;
    unsigned long obs;
    const char *cursor;
    char value[256];
    if (!m) return;
    owner = player_bid(m, c);
    side = owner < 0 ? -1 : owner / 10;
    if (side < 0) { m->dropped++; return; }
    if (sscanf(bp, "BP|%X|%X|%X", &bid, &flags, &mp) != 3 || strncmp(bc, "BC|", 3)) {
        m->dropped++; return;
    }
    cursor = bc+3;
    if (!token(&cursor, value, sizeof(value))) { m->dropped++; return; }
    field = (int)strtoul(value, NULL, 16);
    obs = emit(battle, BATTLE_LOG_EVENT, "observation", side,
        "\"my_bid\":%d,\"menu_flags\":%d,\"mp\":%d,\"field_attribute\":%d,"
        "\"legal_action_mask\":null", bid, flags, mp, field);
    m->observation[owner] = obs;
    while (*cursor) {
        unsigned long v[13];
        for (i = 0; i < 13; i++) {
            if (!token(&cursor, value, sizeof(value))) { m->dropped++; return; }
            /* Names/titles are deliberately skipped, including non-UTF8 GBK. */
            v[i] = (i == 1 || i == 2 || i == 9) ? 0 : strtoul(value, NULL, 16);
        }
        emit(battle, BATTLE_LOG_EVENT, "visible_actor", side,
            "\"observation_id\":%lu,\"bid\":%lu,\"graphic\":%lu,\"level\":%lu,"
            "\"hp\":%lu,\"max_hp\":%lu,\"flags\":%lu,\"ride_flag\":%d,"
            "\"ride_pet_level\":%lu,\"ride_pet_hp\":%lu,\"ride_pet_max_hp\":%lu",
            obs, v[0], v[3], v[4], v[5], v[6], v[7], (int)v[8], v[10], v[11], v[12]);
    }
    own_configuration(battle, side, obs, c);
    emit(battle, BATTLE_LOG_EVENT, "observation_end", side, "\"observation_id\":%lu", obs);
}

unsigned long StoneAge_BattleRecordRequest(int c, const char *command, int depth)
{
    int battle, side, owner, a = -1, b = -1, parsed = 1;
    unsigned long request;
    char opcode[3] = "?";
    RecordedBattle *m;
    size_t len, i;
    if (!matches || !CHAR_CHECKINDEX(c)) return 0;
    battle = CHAR_getWorkInt(c, CHAR_WORKBATTLEINDEX);
    m = match(battle);
    if (!m || !command || command[0] == '@') return 0; /* animation acknowledgement */
    owner = player_bid(m, c);
    side = owner < 0 ? -1 : owner / 10;
    if (side < 0) return 0;
    len = strlen(command);
    /* Never store arbitrary untrusted input, even for rejected commands. */
    if (len == 0 || len > 32 || !strchr("U E H G N T S W J I", command[0]) || command[0] == ' ') parsed = 0;
    for (i = 1; parsed && i < len; i++)
        if (!isxdigit((unsigned char)command[i]) && command[i] != '|' && command[i] != '-') parsed = 0;
    if (parsed) {
        opcode[0] = command[0]; opcode[1] = 0;
        if (len > 1 && command[1] == '|') {
            if (opcode[0] == 'S') sscanf(command+2, "%d", &a);
            else sscanf(command+2, "%X|%X", &a, &b);
        }
    }
    request = emit(battle, BATTLE_LOG_EVENT, "action_request", side,
         "\"observation_id\":%lu,\"origin\":\"%s\",\"opcode\":\"%s\","
         "\"arg1\":%d,\"arg2\":%d,\"parsed\":%s,\"mp_before\":%d",
         m->observation[owner], depth ? "server_fallback" : "client", opcode, a, b,
         parsed ? "true" : "false", CHAR_getInt(c, CHAR_MP));
    return request;
}

void StoneAge_BattleRecordDispatch(int c, unsigned long request)
{
    int battle, side, owner, pet;
    RecordedBattle *m;
    if (!request || !CHAR_CHECKINDEX(c)) return;
    battle = CHAR_getWorkInt(c, CHAR_WORKBATTLEINDEX);
    m = match(battle);
    if (!m) return;
    owner = player_bid(m, c);
    side = owner < 0 ? -1 : owner / 10;
    if (side < 0) return;
    pet = CHAR_getCharPet(c, CHAR_getInt(c, CHAR_DEFAULTPET));
    /* Void native dispatcher has no acceptance return value. Preserve its
     * selected state, never pretend C_OK proves this particular request won. */
    emit(battle, BATTLE_LOG_EVENT, "action_dispatch_state", side,
         "\"request_id\":%lu,\"player_mode\":%d,\"player_command\":[%d,%d,%d],"
         "\"pet_mode\":%d,\"pet_command\":[%d,%d,%d],\"mp_after\":%d",
         request, CHAR_getWorkInt(c, CHAR_WORKBATTLEMODE),
         CHAR_getWorkInt(c, CHAR_WORKBATTLECOM1), CHAR_getWorkInt(c, CHAR_WORKBATTLECOM2), CHAR_getWorkInt(c, CHAR_WORKBATTLECOM3),
         CHAR_CHECKINDEX(pet) ? CHAR_getWorkInt(pet, CHAR_WORKBATTLEMODE) : -1,
         CHAR_CHECKINDEX(pet) ? CHAR_getWorkInt(pet, CHAR_WORKBATTLECOM1) : -1,
         CHAR_CHECKINDEX(pet) ? CHAR_getWorkInt(pet, CHAR_WORKBATTLECOM2) : -1,
         CHAR_CHECKINDEX(pet) ? CHAR_getWorkInt(pet, CHAR_WORKBATTLECOM3) : -1,
         CHAR_getInt(c, CHAR_MP));
}

void StoneAge_BattleRecordTurn(int battle, int after)
{
    int side, i;
    if (!match(battle)) return;
    emit(battle, BATTLE_LOG_EVENT, after ? "turn_resolved" : "turn_begin", -1,
         "\"decision_turn\":%d", BattleArray[battle].turn-1);
    for (side = 0; side < 2; side++) for (i = 0; i < BATTLE_ENTRY_MAX; i++) {
        BATTLE_ENTRY *entry = &BattleArray[battle].Side[side].Entry[i];
        int c = entry->charaindex;
        if (!CHAR_CHECKINDEX(c)) continue;
        emit(battle, BATTLE_LOG_EVENT, after ? "resolved_actor" : "execution_command", side,
             "\"visibility\":\"server_only\",\"decision_turn\":%d,\"bid\":%d,"
             "\"command\":[%d,%d,%d],\"hp\":%d,\"mp\":%d,\"dead\":%s",
             BattleArray[battle].turn-1, entry->bid,
             CHAR_getWorkInt(c, CHAR_WORKBATTLECOM1), CHAR_getWorkInt(c, CHAR_WORKBATTLECOM2),
             CHAR_getWorkInt(c, CHAR_WORKBATTLECOM3), CHAR_getInt(c, CHAR_HP), CHAR_getInt(c, CHAR_MP),
             CHAR_getFlg(c, CHAR_ISDIE) ? "true" : "false");
    }
}

void StoneAge_BattleRecordExecuting(int battle, int bid, int command)
{
    if (!match(battle)) return;
    emit(battle, BATTLE_LOG_EVENT, "action_resolution", bid / 10,
         "\"visibility\":\"server_only\",\"decision_turn\":%d,\"bid\":%d,\"command\":%d",
         BattleArray[battle].turn-1, bid, command);
}

void StoneAge_BattleRecordExit(int battle, int c, const char *reason)
{
    RecordedBattle *m = match(battle);
    int side, owner;
    if (!m) return;
    owner = player_bid(m, c);
    side = owner < 0 ? -1 : owner / 10;
    if (side < 0) return; /* Recalling/replacing pets is not a forfeiture. */
    if (m->exit_side < 0) {
        m->exit_side = side;
        m->exit_reason = reason;
    }
    emit(battle, BATTLE_LOG_EVENT, "participant_exit", side, "\"reason\":\"%s\"", reason);
}

void StoneAge_BattleRecordEnd(int battle, int normal)
{
    RecordedBattle *m = match(battle);
    int winner;
    if (!m) return;
    winner = normal ? (BattleArray[battle].winside == -1 ? 0 : BattleArray[battle].winside == 1 ? 1 : -1) : -1;
    emit(battle, BATTLE_LOG_END, "match_end", -1,
         "\"winner_side\":%d,\"end_reason\":\"%s\",\"exit_side\":%d,"
         "\"dropped_events\":%lu,\"trajectory_complete\":%s",
         winner, m->exit_reason ? m->exit_reason : normal ? "defeat" : "interrupted",
         m->exit_side, m->dropped, m->dropped ? "false" : "true");
    m->active = 0;
}

void StoneAge_BattleDatasetEndLimit(int battle)
{
    RecordedBattle *m = match(battle);
    if (!m) return;
    m->exit_reason = "turn_limit";
    StoneAge_BattleRecordEnd(battle, 0);
}
