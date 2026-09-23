/* Offline equal-budget arena. It initializes tables, not the game service:
 * no listeners, account connection, player saves, NPC loop or Web sessions.
 * BATTLE_Loop and the native command dispatcher resolve every combat round. */
#include "version.h"
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include "char.h"
#include "char_base.h"
#include "char_data.h"
#include "battle.h"
#include "buf.h"
#include "configfile.h"
#include "function.h"
#include "handletime.h"
#include "item.h"
#include "net.h"
#include "object.h"
#include "autil.h"
#include "pet_skill.h"
#include "lssproto_serv.h"
#include "stoneage_battle_record.h"
#include "stoneage_battle_log.h"
#include "stoneage_battle_dataset.h"

static char context[1024];
static unsigned int selection_rng;

const char *StoneAge_BattleDatasetContext(void) { return context[0] ? context : "null"; }
static unsigned int pick(void)
{
    selection_rng ^= selection_rng << 13;
    selection_rng ^= selection_rng >> 17;
    selection_rng ^= selection_rng << 5;
    return selection_rng;
}
static int sink(int fd, char *data, int length) { (void)fd; (void)data; return length; }

static void allocation(int points, int *out, int style)
{
    int i, left = points-4, weight[4], total = 0;
    for (i = 0; i < 4; i++) { out[i] = 1; weight[i] = 1 + pick()%100; }
    /* Mixture includes broad random builds, balanced and stat-heavy builds. */
    if (style < 4) weight[style] += 400;
    if (style == 4) for (i = 0; i < 4; i++) weight[i] = 1;
    for (i = 0; i < 4; i++) total += weight[i];
    for (i = 0; i < 4; i++) { int n = (points-4)*weight[i]/total; out[i] += n; left -= n; }
    while (left-- > 0) out[pick()%4]++;
}

static int character(const int *build, int level)
{
    Char ch;
    int c, i;
    if (!CHAR_getDefaultChar(&ch, 100000)) return -1;
    ch.data[CHAR_WHICHTYPE] = CHAR_TYPEPLAYER;
    ch.data[CHAR_BASEIMAGENUMBER] = 100000;
    ch.data[CHAR_BASEBASEIMAGENUMBER] = 100000;
    ch.data[CHAR_LV] = level;
    ch.data[CHAR_TRANSMIGRATION] = 0;
    ch.data[CHAR_DEFAULTPET] = -1;
    ch.data[CHAR_RIDEPET] = -1;
    ch.data[CHAR_VITAL] = build[0]*100;
    ch.data[CHAR_STR] = build[1]*100;
    ch.data[CHAR_TOUGH] = build[2]*100;
    ch.data[CHAR_DEX] = build[3]*100;
    ch.data[CHAR_SKILLUPPOINT] = 0;
    ch.data[CHAR_LUCK] = 0;
    ch.data[CHAR_CHARM] = 100;
    ch.data[CHAR_EARTHAT] = 100;
    ch.data[CHAR_WATERAT] = ch.data[CHAR_FIREAT] = ch.data[CHAR_WINDAT] = 0;
    ch.workint[CHAR_WORKOBJINDEX] = -1;
    ch.workint[CHAR_WORKBATTLEINDEX] = -1;
    for (i = 0; i < CHAR_MAXPETHAVE; i++) ch.unionTable.indexOfPet[i] = -1;
#ifdef _PETSKILL_BECOMEPIG
    ch.data[CHAR_BECOMEPIG] = -1;
#endif
    c = CHAR_initCharOneArray(&ch);
    if (c < 0) return -1;
    CHAR_complianceParameter(c);
    CHAR_setInt(c, CHAR_HP, CHAR_getWorkInt(c, CHAR_WORKMAXHP));
    CHAR_setInt(c, CHAR_MP, CHAR_getWorkInt(c, CHAR_WORKMAXMP));
    return c;
}

static int integer(const char *value, int min, int max)
{
    char *end;
    long n = strtol(value, &end, 10);
    return *value && !*end && n >= min && n <= max ? (int)n : -1;
}

int StoneAge_BattleDatasetMain(int argc, char **argv)
{
    int matches = 100, points = 120, seed = 1, max_turns = 200, level = 35, repeats = 4;
    int i, j, side, battle, c[2], builds[2][4], winners[3] = {0,0,0};
    const char *config = "setup.cf", *output = getenv("STONEAGE_BATTLE_RECORD_DIR");
    for (i = 2; i < argc; i += 2) {
        int value;
        if (i+1 >= argc) goto usage;
        if (!strcmp(argv[i], "--config")) { config = argv[i+1]; continue; }
        value = integer(argv[i+1], 1, 1000000);
        if (value < 0) goto usage;
        if (!strcmp(argv[i], "--matches")) matches = value;
        else if (!strcmp(argv[i], "--points") && value >= 4 && value <= 10000) points = value;
        else if (!strcmp(argv[i], "--seed")) seed = value;
        else if (!strcmp(argv[i], "--max-turns") && value <= 10000) max_turns = value;
        else if (!strcmp(argv[i], "--level") && value <= 200) level = value;
        else if (!strcmp(argv[i], "--repeats") && value <= 1000) repeats = value;
        else goto usage;
    }
    if (!output || !*output) { fprintf(stderr, "STONEAGE_BATTLE_RECORD_DIR is required\n"); return 2; }
    /* Only explicit CLI mode enables backpressure. Production always uses
     * bounded, nonblocking submission; offline generation must lose nothing. */
    StoneAge_BattleLogOffline();
    setNewTime();
    if (!util_Init()) return 1;
    defaultConfig(argv[0]);
    if (!readconfigfile((char *)config) || !configmem(getMemoryunit(), getMemoryunitnum()) || !memInit() ||
        !initConnect(16) || !initObjectArray(32) || !CHAR_initCharArray(8, 8, 8) ||
        !ITEM_readItemConfFile(getItemfile()) || !ITEM_initExistItemsArray(32) ||
        !initFunctionTable() || !PETSKILL_initPetskill(getPetskillfile()) ||
        lssproto_InitServer(sink, 65536) < 0 || !BATTLE_initBattleArray(4)) {
        fprintf(stderr, "Offline arena initialization failed\n"); return 1;
    }
    selection_rng = (unsigned int)seed;
    for (i = 0; i < matches; i++) {
        unsigned int combat_seed = (unsigned int)seed + (unsigned int)(i/2)*7919U;
        int swap = i%2, policy[2], pair = i/(repeats*2);
        if (i%(repeats*2) == 0) for (side = 0; side < 2; side++) allocation(points, builds[side], pick()%8);
        for (side = 0; side < 2; side++) {
            /* Match tactics within a pair so allocation labels are not
             * confounded by a stronger baseline policy on one side. */
            policy[side] = pair%3;
            c[side] = character(builds[side^swap], level);
            if (c[side] < 0) return 1;
        }
        snprintf(context, sizeof(context), "{\"generator\":\"native-equal-points-v1\","
            "\"run_seed\":%d,\"combat_seed\":%u,\"match_index\":%d,\"pair_index\":%d,"
            "\"repetition\":%d,\"side_swap\":%s,\"points\":%d,\"level\":%d,"
            "\"scenario\":\"bare-character-no-pets-v1\",\"allocation_generator\":\"mixture-v1\","
            "\"policy_ids\":[%d,%d],\"max_turns\":%d}",
            seed, combat_seed, i, pair, (i/2)%repeats, swap ? "true" : "false", points, level,
            policy[0], policy[1], max_turns);
        srand(combat_seed);
        battle = BATTLE_CreateBattle();
        if (battle < 0) return 1;
        BattleArray[battle].type = BATTLE_TYPE_P_vs_P;
        BattleArray[battle].field_no = 0;
        BattleArray[battle].leaderindex = c[0];
        BattleArray[battle].winside = 0;
        for (side = 0; side < 2; side++) {
            BattleArray[battle].Side[side].type = BATTLE_S_TYPE_PLAYER;
            BattleArray[battle].Side[side].flg = 0;
            if (BATTLE_NewEntry(c[side], battle, side)) return 1;
        }
        BATTLE_Loop(); /* Native initial observation/menu and turn setup. */
        for (j = 0; j < max_turns && BattleArray[battle].mode != BATTLE_MODE_FINISH; j++) {
            for (side = 0; side < 2; side++) {
                char command[32];
                int guard = policy[side] == 1 ? (pick()%5 == 0) :
                            policy[side] == 2 && (j%4 == 0);
                snprintf(command, sizeof(command), guard ? "G" : "H|%X", (1-side)*10);
                StoneAge_BattleDatasetCommand(c[side], command);
            }
            BATTLE_Loop();
        }
        if (BattleArray[battle].mode == BATTLE_MODE_FINISH)
            winners[BattleArray[battle].winside == -1 ? 0 : 1]++;
        else { winners[2]++; StoneAge_BattleDatasetEndLimit(battle); }
        /* No account rewards, rankings, network saves or post-fight NPC
         * callbacks: dispose only these synthetic in-memory characters. */
        BATTLE_DeleteBattle(battle);
        for (side = 0; side < 2; side++) {
            CHAR_setWorkInt(c[side], CHAR_WORKBATTLEMODE, BATTLE_CHARMODE_NONE);
            CHAR_setWorkInt(c[side], CHAR_WORKBATTLEINDEX, -1);
            CHAR_endCharOneArray(c[side]);
        }
    }
    StoneAge_BattleLogShutdown();
    fprintf(stderr, "Offline arena: %d matches; side0=%d side1=%d turn_limit=%d\n", matches, winners[0], winners[1], winners[2]);
    return StoneAge_BattleLogHealthy() ? 0 : 1;
usage:
    fprintf(stderr, "Usage: gmsvjt.exe --battle-dataset [--config FILE] [--matches N] [--points N] [--seed N] [--max-turns N] [--level N] [--repeats N]\n");
    return 2;
}
