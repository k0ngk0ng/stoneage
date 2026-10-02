/* Offline equal-budget arena. It initializes tables, not the game service:
 * no listeners, account connection, player saves, NPC loop or Web sessions.
 * BATTLE_Loop and the native command dispatcher resolve every combat round. */
#include "version.h"
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
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
#include "magic_base.h"
#include "lssproto_serv.h"
#include "stoneage_battle_record.h"
#include "stoneage_battle_log.h"
#include "stoneage_battle_dataset.h"
#include "stoneage_ai_observation.h"

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

/* Export only allowlisted inputs, using the exact native configuration parser:
 * hostname overrides, whitespace, duplicate keys, compiled table selection,
 * native defaults, path prefixes and getter clamping all matter. Never dump
 * setup.cf or arbitrary configuration strings (account secrets live there).
 * This CLI opens no listener, initializes no characters and writes no files. */
static int rules_table(const char *kind, const char *key, const char *path)
{
    if (!path || !*path || strpbrk(path, "\t\r\n")) return 0;
    printf("%s\t%s\t%s\n", kind, key, path);
    return 1;
}

int StoneAge_BattleRulesMain(int argc, char **argv)
{
#ifdef _GET_BATTLE_EXP
    /* This existing getter is missing from the legacy public header. */
    extern unsigned int getBattleexp(void);
#endif
    if (argc != 4 || strcmp(argv[2], "--config")) {
        fprintf(stderr, "usage: gmsvjt.exe --battle-rules --config setup.cf\n");
        return 2;
    }
    defaultConfig(argv[0]);
    if (!readconfigfile(argv[3])) return 1;
    if (!rules_table("table", "itemfile", getItemfile()) ||
        !rules_table("table", "petskillfile", getPetskillfile()) ||
        !rules_table("table", "magicfile", getMagicfile()) ||
        !rules_table("table", "enemyfile", getEnemyfile()) ||
        !rules_table("table", "enemybasefile", getEnemyBasefile())) return 1;
#ifdef _ATTACK_MAGIC
    if (!rules_table("table", "attmagicfile", getAttMagicfileName())) return 1;
#endif
#ifdef _NEED_ITEM_ENEMY
    /* Native startup permits absence, leaving the static defaults in place. */
    if (!rules_table("optional", "needitemenemy", "data/needitemeneny.txt")) return 1;
#endif
#ifdef _RIDE_CF
    if (!rules_table("optional", "ride", "data/ride.txt")) return 1;
#endif
#ifdef _FM_LEADER_RIDE
    if (!rules_table("optional", "leaderride", "data/leaderride.txt")) return 1;
#endif
#ifdef _USER_EXP_CF
    if (!rules_table("table", "expfile", getEXPfile())) return 1;
#endif
#ifdef _GET_BATTLE_EXP
    printf("option\tbattleexp\t%u\n", getBattleexp());
#endif
#ifdef _SKILLUPPOINT_CF
    printf("option\tskup\t%d\n", getSkup());
#endif
#ifdef _BATTLE_GOLD
    printf("option\tbattlegold\t%d\n", getBattleGold());
#endif
#ifdef _RIDEMODE_20
    printf("option\tridemode\t%d\n", getRideMode());
#endif
#ifdef _TRANS_LEVEL_CF
    printf("option\tchartrans\t%d\n", getChartrans());
    printf("option\tpettrans\t%d\n", getPettrans());
    printf("option\tmaxlevel\t%d\n", getMaxLevel());
    printf("option\tyblevel\t%d\n", getYBLevel());
#endif
    return ferror(stdout) ? 1 : 0;
}

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

static int synthetic_character(const int *build, int level, int type, int owner)
{
    Char ch;
    int c, i;
    if (!CHAR_getDefaultChar(&ch, 100000)) return -1;
    ch.data[CHAR_WHICHTYPE] = type;
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
    if (type == CHAR_TYPEPET) {
        ch.workint[CHAR_WORKPLAYERINDEX] = owner;
        ch.data[CHAR_BASEIMAGENUMBER] = ch.data[CHAR_BASEBASEIMAGENUMBER] = 100266;
        ch.data[CHAR_MODAI] = 100;
        /* Native command validation checks capacity independently of the W
         * skill list. A synthetic pet with CHAR_SLOT=0 silently waits. */
        ch.data[CHAR_SLOT] = CHAR_MAXPETSKILLHAVE;
    }
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

static int character(const int *build, int level)
{ return synthetic_character(build, level, CHAR_TYPEPLAYER, -1); }

static int integer(const char *value, int min, int max)
{
    char *end;
    long n = strtol(value, &end, 10);
    return *value && !*end && n >= min && n <= max ? (int)n : -1;
}

static int initialize(const char *program, const char *config)
{
    setNewTime();
    if (!util_Init()) return 0;
    defaultConfig((char *)program);
    return readconfigfile((char *)config) && configmem(getMemoryunit(), getMemoryunitnum()) && memInit() &&
        initConnect(32) && initObjectArray(64) && CHAR_initCharArray(32, 32, 32) &&
        ITEM_readItemConfFile(getItemfile()) && ITEM_initExistItemsArray(256) &&
        initFunctionTable() && PETSKILL_initPetskill(getPetskillfile()) &&
        lssproto_InitServer(sink, 65536) >= 0 && BATTLE_initBattleArray(4);
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
    if (!initialize(argv[0], config)) {
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

/* Versioned, process-local training transport. It never starts listeners or
 * reaches login, saves, experience awards, matchmaking or rating callbacks.
 * stdout is reserved before initialization; legacy diagnostics go to stderr.
 * Request grammar is deliberately bounded ASCII; responses contain the actual
 * client packet bytes as hex (legacy names can be GBK, not JSON UTF-8).
 */
#define ENV_MEMBERS 10
#define ENV_PACKETS 256
#define ENV_BYTES 262144
static int env_active, env_count, env_char[ENV_MEMBERS], env_battle = -1;
static int env_pets, env_pet[ENV_MEMBERS];
static int env_packets[ENV_MEMBERS], env_bytes[ENV_MEMBERS], env_failed;
static char *env_packet[ENV_MEMBERS][ENV_PACKETS];

int StoneAge_BattleEnvironmentIsBattle(int battle)
{ return env_active && battle>=0 && battle==env_battle; }

int StoneAge_BattleEnvironmentMembers(int battle, int *members, int capacity)
{
    if(!StoneAge_BattleEnvironmentIsBattle(battle) || !members || capacity<env_count)return 0;
    memcpy(members,env_char,sizeof(int)*env_count);return env_count;
}

int StoneAge_BattleEnvironmentPacket(int c, const char *packet)
{
    int i, n;
    if (!env_active) return 0;
    for (i = 0; i < env_count; i++) if (env_char[i] == c) {
        n = (int)strlen(packet);
        if (env_packets[i] == ENV_PACKETS || n > ENV_BYTES-env_bytes[i]) {
            env_failed = 1;
            return 1;
        }
        env_packet[i][env_packets[i]] = malloc((size_t)n+1);
        if (!env_packet[i][env_packets[i]]) { env_failed = 1; return 1; }
        memcpy(env_packet[i][env_packets[i]++], packet, (size_t)n+1);
        env_bytes[i] += n;
        return 1;
    }
    return 0;
}

static void environment_clear_packets(void)
{
    int i, j;
    for (i = 0; i < ENV_MEMBERS; i++) {
        for (j = 0; j < env_packets[i]; j++) free(env_packet[i][j]);
        env_packets[i] = env_bytes[i] = 0;
    }
    env_failed = 0;
}

static void environment_dispose(void)
{
    int i, slot, item;
    if (env_battle >= 0) BATTLE_DeleteBattle(env_battle);
    env_battle = -1;
    for (i = 0; i < env_count; i++) if (CHAR_CHECKINDEX(env_char[i])) {
        for(slot=0;slot<CHAR_MAXITEMHAVE;slot++) {
            item = CHAR_getItemIndex(env_char[i], slot);
            if (ITEM_CHECKINDEX(item)) {
                CHAR_setItemIndex(env_char[i], slot, -1);
                ITEM_endExistItemsOne(item);
            }
        }
        CHAR_setWorkInt(env_char[i], CHAR_WORKBATTLEMODE, BATTLE_CHARMODE_NONE);
        CHAR_setWorkInt(env_char[i], CHAR_WORKBATTLEINDEX, -1);
        for(slot=0;slot<CHAR_MAXPETHAVE;slot++) {
            item=CHAR_getCharPet(env_char[i],slot);
            if(CHAR_CHECKINDEX(item)) {
                CHAR_setCharPet(env_char[i],slot,-1);
                CHAR_endCharOneArray(item);
            }
        }
        CHAR_endCharOneArray(env_char[i]);
    }
    env_count = 0;
    environment_clear_packets();
}

static void environment_hex(FILE *out, const char *s)
{
    const unsigned char *p = (const unsigned char *)(s ? s : "");
    fputc('"', out);
    for (; *p; p++) fprintf(out, "%02x", *p);
    fputc('"', out);
}

static int environment_response(FILE *out, int mode, int limit)
{
    int i, j, k, ended, turn, winner;
    char pet_status[16];
    if (env_failed) return 0;
    ended = BattleArray[env_battle].mode == BATTLE_MODE_FINISH;
    turn = BattleArray[env_battle].turn;
    winner = ended ? (BattleArray[env_battle].winside == -1 ? 0 : BattleArray[env_battle].winside == 1 ? 1 : -1) : -1;
    fprintf(out, "{\"schema_version\":1,\"ok\":true,\"mode\":%d,\"turn\":%d,\"terminated\":%s,\"truncated\":%s,\"winner_side\":%d,\"members\":[",
        mode, turn, ended ? "true":"false", !ended && turn >= limit ? "true":"false", winner);
    for (i = 0; i < env_count; i++) {
        fprintf(out, "%s{\"side\":%d,\"seat\":%d,\"packets\":[", i ? ",":"", i/mode, i%mode);
        for (j = 0; j < env_packets[i]; j++) {
            fprintf(out, "%s{\"function\":\"B\",\"hex\":", j ? ",":"");
            environment_hex(out, env_packet[i][j]); fputc('}', out);
        }
        fprintf(out, "%s{\"function\":\"S\",\"hex\":", j ? ",":"");
        environment_hex(out, CHAR_makeStatusString(env_char[i], "P"));
        fputs("},{\"function\":\"S\",\"hex\":", out);
        environment_hex(out, CHAR_makeStatusString(env_char[i], "I"));
        fputs("},{\"function\":\"S\",\"hex\":", out);
        environment_hex(out, StoneAge_AIObservationMake(env_char[i]));
        fputs("},{\"function\":\"S\",\"hex\":", out);
        environment_hex(out, CHAR_makeStatusString(env_char[i], "J1"));
        for(k=0;k<CHAR_MAXPETHAVE;k++) if(CHAR_CHECKINDEX(CHAR_getCharPet(env_char[i],k))) {
            fputs("},{\"function\":\"S\",\"hex\":", out);
            snprintf(pet_status,sizeof(pet_status),"K%d",k);
            environment_hex(out, CHAR_makeStatusString(env_char[i], pet_status));
            fputs("},{\"function\":\"S\",\"hex\":", out);
            snprintf(pet_status,sizeof(pet_status),"W%d",k);
            environment_hex(out, CHAR_makeStatusString(env_char[i], pet_status));
        }
        fputs("}]}", out);
    }
    fputs("]}\n", out);
    return fflush(out) == 0 && !ferror(out);
}

static int environment_pet_command(const char *s)
{
    unsigned int slot,target; int n=0;
    if (!strcmp(s,"-")) return 1;
    if (sscanf(s,"W|%X|%X%n",&slot,&target,&n)!=2 || s[n]) return 0;
    return (slot==255 && target==255) || (slot<7 && target<20);
}

/* Explicit controlled loadout. Validate the actual loaded table before using
 * the policy's curated semantics; a changed table must never keep the same
 * poison/stone/confusion/sleep feature interpretation by ID alone. Options
 * use the table's GB bytes, not localized display names or descriptions. */
static const int environment_skills[8]={1,2,3,60,80,90,110,20};
static int environment_skill_mask(int mask)
{
    int count=0,bit;
    if(mask<1 || mask>255)return 0;
    for(bit=0;bit<8;bit++)if(mask&(1<<bit))count++;
    return count<=CHAR_MAXPETSKILLHAVE;
}
static int environment_pet_skills(int pet,int mask)
{
    int bit,slot=0;
    if(!environment_skill_mask(mask))return 0;
    for(bit=0;bit<CHAR_MAXPETSKILLHAVE;bit++)CHAR_setPetSkill(pet,bit,-1);
    for(bit=0;bit<8;bit++)if(mask&(1<<bit))CHAR_setPetSkill(pet,slot++,environment_skills[bit]);
    return 1;
}
static int environment_skill_contract(void)
{
    static const char *functions[8]={"PETSKILL_NormalAttack","PETSKILL_NormalGuard","PETSKILL_GuardBreak",
        "PETSKILL_StatusChange","PETSKILL_StatusChange","PETSKILL_StatusChange","PETSKILL_StatusChange","PETSKILL_Guardian"};
    static const char *options[8]={"","","","\266\276 turn 3  \271\245%-30",
        "\312\257 turn 3  \271\245%-30","\302\322 turn 3 \271\245%-30","\303\337 turn 3  \271\245%-30",
        "\271\245%-20  COM:\271\245\273\367"};
    int i,array;
    for(i=0;i<8;i++) {
        array=PETSKILL_getPetskillArray(environment_skills[i]);
        if(!PETSKILL_CHECKINDEX(array) || PETSKILL_getInt(array,PETSKILL_FIELD)!=1 ||
            PETSKILL_getInt(array,PETSKILL_TARGET)!=(i==1?5:(i==7?7:6)) ||
            strcmp(PETSKILL_getChar(array,PETSKILL_FUNCNAME),functions[i]) ||
            strcmp(PETSKILL_getChar(array,PETSKILL_OPTION),options[i]))return 0;
    }
    return 1;
}

static int environment_command(const char *s)
{
    char *end;
    long target;
    unsigned int slot, to; int n=0;
    if (!strcmp(s,"G") || !strcmp(s,"N")) return 1;
    if (!strncmp(s,"S|",2)) return !strcmp(s+2,"-1") ||
        (s[2]>='0' && s[2]<='4' && !s[3]);
    if (!strncmp(s,"J|",2)) {
        return sscanf(s,"J|%X|%X%n",&slot,&to,&n)==2 && !s[n] && slot==CHAR_BODY && to<22;
    }
    if (!strncmp(s,"I|",2)) {
        return sscanf(s,"I|%X|%X%n",&slot,&to,&n)==2 && !s[n] && slot>=5 && slot<20 && to<20;
    }
    if (strncmp(s,"H|",2) || !s[2] || strlen(s)>4) return 0;
    target = strtol(s+2,&end,16);
    return !*end && target>=0 && target<20;
}

/* Kept as separate operations so the native parity harness can exercise the
 * exact training initializer/step against the normal arena entry path. */
static int environment_characters(int mode, int level, int pets, int points[][4], int pet_points[][4], int healing, int items)
{
    int i,j,c,pet,item,magic;
    environment_dispose();
    if(!environment_skill_contract())return 0;
    if(items<0 || items>15)return 0;
    if(healing!=0 && healing!=10 && healing!=20)return 0;
    if(healing) {
        magic=MAGIC_getMagicArray(healing);
        if(magic<0 || MAGIC_getInt(magic,MAGIC_FIELD)!=(healing==10?0:1) ||
           MAGIC_getInt(magic,MAGIC_TARGET)!=(healing==10?1:8) ||
           MAGIC_getInt(magic,MAGIC_TARGET_DEADFLG)!=0 ||
           strcmp(MAGIC_getChar(magic,MAGIC_FUNCNAME),healing==10?"MAGIC_OtherRecovery":"MAGIC_Recovery") ||
           strcmp(MAGIC_getChar(magic,MAGIC_OPTION),healing==10?"65":"50"))return 0;
    }
    env_pets=pets;
    for(i=0;i<ENV_MEMBERS;i++) env_pet[i]=-1;
    for(i=0;i<mode*2;i++) {
        c=character(points[i],level);
        if(c<0)return 0;
        env_char[env_count++]=c;
        for(j=0;j<items;j++) {
            item=ITEM_makeItemAndRegist(1234);
            if(!ITEM_CHECKINDEX(item))return 0;
            CHAR_setItemIndex(c,5+j,item);
            /* Actual loaded item semantics, never inferred from its name.
             * Native _CHAR_DelItem consumes the entire occupied slot. */
            if(ITEM_getInt(item,ITEM_ID)!=1234 || ITEM_getInt(item,ITEM_ABLEUSEFIELD)!=0 ||
               ITEM_getInt(item,ITEM_TARGET)!=1 || ITEM_getInt(item,ITEM_LEVEL)!=0 ||
               strcmp(ITEM_getChar(item,ITEM_USEFUNC),"ITEM_useRecovery") ||
               strcmp(ITEM_getChar(item,ITEM_ARGUMENT),"\314\34520"))return 0;
        }
        if(healing) {
            /* Real fixed armour, with the same stats/budget on both sides.
             * MP belongs to this explicit loadout, not to allocated points. */
            item=ITEM_makeItemAndRegist(healing==10?1002:1003);
            if(!ITEM_CHECKINDEX(item))return 0;
            CHAR_setItemIndex(c,CHAR_BODY,item);
            if(ITEM_getInt(item,ITEM_MAGICID)!=healing || ITEM_getInt(item,ITEM_MAGICUSEMP)!=(healing==10?8:20))return 0;
            CHAR_setInt(c,CHAR_MAXMP,100);
            CHAR_complianceParameter(c);
            CHAR_setInt(c,CHAR_MP,CHAR_getWorkInt(c,CHAR_WORKMAXMP));
            CHAR_setInt(c,CHAR_HP,CHAR_getWorkInt(c,CHAR_WORKMAXHP));
        }
        if(pets) {
            pet=synthetic_character(pet_points[i],level,CHAR_TYPEPET,c);
            if(pet<0)return 0;
            env_pet[i]=pet;
            CHAR_setChar(pet,CHAR_NAME,"training-pet");
            if(!environment_pet_skills(pet,127))return 0;
            CHAR_setCharPet(c,0,pet);
            CHAR_setInt(c,CHAR_DEFAULTPET,0);
            CHAR_setWorkInt(c,CHAR_WORKSTANDBYPET,1);
            CHAR_setWorkInt(c,CHAR_WORK_PET0_STAT,PET_STAT_SELECT);
        }
    }
    return 1;
}

/* Called only after all budgets/skill masks have been validated. A maximum
 * of three selectable pets mirrors the native SPET limit. */
static int environment_reserves(int count,int level,int points[ENV_MEMBERS][2][4],int masks[ENV_MEMBERS][2])
{
    int i,j,k,c,pet,skill;
    char name[32];
    if(count<0 || count>2 || (count && !env_pets))return 0;
    for(i=0;i<env_count;i++) {
        c=env_char[i];
        for(j=0;j<count;j++) {
            pet=synthetic_character(points[i][j],level,CHAR_TYPEPET,c);
            if(pet<0)return 0;
            CHAR_setCharPet(c,j+1,pet);
            snprintf(name,sizeof(name),"reserve-pet-%d",j+1);CHAR_setChar(pet,CHAR_NAME,name);
            if(!environment_pet_skills(pet,masks[i][j]))return 0;
            CHAR_setWorkInt(c,CHAR_WORK_PET0_STAT+j+1,PET_STAT_SELECT);
        }
        if(count)CHAR_setWorkInt(c,CHAR_WORKSTANDBYPET,(1<<(count+1))-1);
    }
    return 1;
}

static int environment_enter(int mode, int seed)
{
    int i;
    snprintf(context,sizeof(context),"{\"generator\":\"native-step-v1\",\"seed\":%d,\"mode\":%d}",seed,mode);
    srand((unsigned int)seed);
    env_battle=BATTLE_CreateBattle();
    if(env_battle<0)return 0;
    BattleArray[env_battle].type=BATTLE_TYPE_P_vs_P;
    BattleArray[env_battle].field_no=0;
    BattleArray[env_battle].leaderindex=env_char[0];
    BattleArray[env_battle].winside=0;
    for(i=0;i<2;i++){BattleArray[env_battle].Side[i].type=BATTLE_S_TYPE_PLAYER;BattleArray[env_battle].Side[i].flg=0;}
    for(i=0;i<env_count;i++)if(BATTLE_NewEntry(env_char[i],env_battle,i/mode))return 0;
    if(env_pets)for(i=0;i<env_count;i++)if(BATTLE_NewEntry(env_pet[i],env_battle,i/mode))return 0;
    BATTLE_Loop();
    return 1;
}

static int environment_step(char **commands)
{
    int i,turn,loops,stride=1+env_pets;
    environment_clear_packets();
    for(i=0;i<env_count;i++) {
        /* Removed members still occupy a fixed-width transport slot. Their
         * N/- placeholders are not decisions or client requests: dispatching
         * them records a stale observation even though the arena guard rejects
         * the command. Keep the living/dead-but-present paths unchanged. */
        if(BATTLE_Index2No(env_battle,env_char[i])<0)continue;
        StoneAge_BattleDatasetCommand(env_char[i],commands[i*stride]);
        if(env_pets && strcmp(commands[1+i*stride],"-"))StoneAge_BattleDatasetCommand(env_char[i],commands[1+i*stride]);
    }
    turn=BattleArray[env_battle].turn;
    for(loops=0;loops<8 && BattleArray[env_battle].turn==turn && BattleArray[env_battle].mode!=BATTLE_MODE_FINISH;loops++)BATTLE_Loop();
    return BattleArray[env_battle].turn!=turn || BattleArray[env_battle].mode==BATTLE_MODE_FINISH;
}

int StoneAge_BattleEnvironmentMain(int argc, char **argv)
{
    FILE *out;
    char line[4096], *token[256], *p;
    int n, i, j, mode=0, limit=0, seed, level, points[ENV_MEMBERS][4];
    int budget, total, valid, finished=0, status=0, pets, stride, healing, items, loadout;
    int pet_points[ENV_MEMBERS][4], reserve_points[ENV_MEMBERS][2][4], reserve_masks[ENV_MEMBERS][2];
    int roster,reserves,offset,k,custom_skills,active_masks[ENV_MEMBERS];
    const char *config, *rules;
#if defined(__aarch64__)
    const char *platform = "linux-arm64";
#elif defined(__x86_64__)
    const char *platform = "linux-amd64";
#else
    const char *platform = "linux-other";
#endif
    if (argc != 4 || strcmp(argv[2],"--config")) return 2;
    config = argv[3];
    out = fdopen(dup(STDOUT_FILENO),"w");
    if (!out || dup2(STDERR_FILENO,STDOUT_FILENO)<0) return 1;
    env_active = 1;
    /* Like --battle-dataset, this explicit offline transport must wait for
     * archival writes when the bounded queue fills. Online play remains
     * nonblocking; dropping the terminal record here would lose training data. */
    StoneAge_BattleLogOffline();
    if (!initialize(argv[0],config) || !MAGIC_initMagic(getMagicfile())) { fclose(out); return 1; }
    rules = getenv("STONEAGE_BATTLE_RULESET_ID");
    if (!rules) rules = "";
    if (*rules && (strlen(rules)!=64 || strspn(rules,"0123456789abcdef")!=64)) { fclose(out); return 2; }
    fprintf(out,"{\"schema_version\":1,\"ok\":true,\"ready\":true,\"scenario\":\"controlled-battle-v8\",\"rules_digest\":\"%s\",\"platform\":\"%s\"}\n",rules,platform);
    fflush(out);
    while (fgets(line,sizeof(line),stdin)) {
        if (!strchr(line,'\n')) { status=2; break; }
        n=0; p=strtok(line," \t\r\n");
        while (p && n<256) { token[n++]=p; p=strtok(NULL," \t\r\n"); }
        if (p || !n) { status=2; break; }
        if (!strcmp(token[0],"close") && n==1) break;
        if (!strcmp(token[0],"reset") || !strcmp(token[0],"reset-pets") ||
            !strcmp(token[0],"reset-healing") || !strcmp(token[0],"reset-pets-healing") ||
            !strcmp(token[0],"reset-loadout") || !strcmp(token[0],"reset-pets-loadout") || !strcmp(token[0],"reset-pets-roster") || !strcmp(token[0],"reset-pets-skills-roster")) {
            pets=!strncmp(token[0],"reset-pets",10);
            healing=strstr(token[0],"-healing")!=NULL;
            loadout=strstr(token[0],"-loadout")!=NULL;items=0;
            custom_skills=!strcmp(token[0],"reset-pets-skills-roster");
            roster=!strcmp(token[0],"reset-pets-roster") || custom_skills;reserves=0;
            /* reset seed level max_turns mode [four integer stats per member]
             * All members have the same point budget in this initial scenario.
             */
            valid=n>=5;
            seed=valid?integer(token[1],1,2147483647):-1;
            level=valid?integer(token[2],1,200):-1;
            limit=valid?integer(token[3],1,10000):-1;
            mode=valid?integer(token[4],1,5):-1;
            valid=seed>0 && level>0 && limit>0 && mode>0;
            offset=5+mode*8*(1+pets);
            if(roster && valid) {
                valid=n>=offset+3;
                if(valid) {
                    healing=integer(token[offset],0,20);items=integer(token[offset+1],0,15);reserves=integer(token[offset+2],custom_skills?0:1,2);
                    valid=(healing==0 || healing==10 || healing==20) && items>=0 && reserves>=0 && n==offset+3+mode*2*(reserves*5+custom_skills);
                }
            } else valid=valid && n==offset+healing+2*loadout;
            if(valid && healing && !roster) { healing=integer(token[n-1],10,20); valid=healing==10 || healing==20; }
            if(valid && loadout) {
                healing=integer(token[n-2],0,20);items=integer(token[n-1],1,15);
                valid=(healing==0 || healing==10 || healing==20) && items>0;
            }
            budget=0;
            for (i=0; valid && i<mode*2; i++) {
                total=0;
                for (j=0;j<4;j++) { points[i][j]=integer(token[5+i*4+j],1,10000); if(points[i][j]<0) valid=0; total+=points[i][j]; }
                if (i==0) budget=total;
                if (total!=budget || total>10000) valid=0;
            }
            budget=0;
            for (i=0; valid && pets && i<mode*2; i++) {
                total=0;
                for (j=0;j<4;j++) { pet_points[i][j]=integer(token[5+mode*8+i*4+j],1,10000); if(pet_points[i][j]<0) valid=0; total+=pet_points[i][j]; }
                if(i==0) budget=total;
                if(total!=budget || total>10000) valid=0;
            }
            offset+=3;
            for(i=0;valid && custom_skills && i<mode*2;i++) {
                active_masks[i]=integer(token[offset++],1,255);
                if(!environment_skill_mask(active_masks[i]))valid=0;
            }
            for(i=0;valid && i<mode*2 && reserves;i++)for(k=0;k<reserves;k++) {
                total=0;
                for(j=0;j<4;j++) {reserve_points[i][k][j]=integer(token[offset++],1,10000);if(reserve_points[i][k][j]<0)valid=0;total+=reserve_points[i][k][j];}
                reserve_masks[i][k]=integer(token[offset++],1,255);
                if(total!=budget || !environment_skill_mask(reserve_masks[i][k]))valid=0;
            }
            if (!valid) { status=2; break; }
            if(!environment_characters(mode,level,pets,points,pet_points,healing,items)) { status=1; break; }
            for(i=0;custom_skills && i<mode*2;i++)if(!environment_pet_skills(env_pet[i],active_masks[i]))status=1;
            if(status || !environment_reserves(reserves,level,reserve_points,reserve_masks) || !environment_enter(mode,seed)) { status=1; break; }
            finished=0;
        } else if (!strcmp(token[0],"step")) {
            stride=1+env_pets;
            if(env_battle<0 || finished || n!=2+env_count*stride || integer(token[1],0,10000)!=BattleArray[env_battle].turn) { status=2; break; }
            valid=1;
            for(i=0;i<env_count;i++) {
                if(!environment_command(token[2+i*stride])) valid=0;
                if(env_pets && !environment_pet_command(token[3+i*stride])) valid=0;
            }
            if(!valid) { status=2; break; }
            /* Validate every request before changing either side. Neither side
             * observes the other's pending commands before joint resolution. */
            if(!environment_step(token+2)) { status=1; break; }
        } else { status=2; break; }
        if(!environment_response(out,mode,limit)) { status=1; break; }
        if(BattleArray[env_battle].mode!=BATTLE_MODE_FINISH && BattleArray[env_battle].turn>=limit)
            StoneAge_BattleDatasetEndLimit(env_battle);
        finished=BattleArray[env_battle].mode==BATTLE_MODE_FINISH || BattleArray[env_battle].turn>=limit;
    }
    if(status) { fprintf(out,"{\"schema_version\":1,\"ok\":false,\"error\":\"%s\"}\n",status==2?"invalid_request":"engine_failure"); fflush(out); }
    environment_dispose();
    StoneAge_BattleLogShutdown();
    /* Check after draining the writer, including its final fsync. The native
     * transport may also be used without an optional archive directory. */
    if (!status && getenv("STONEAGE_BATTLE_RECORD_DIR") &&
        *getenv("STONEAGE_BATTLE_RECORD_DIR") && !StoneAge_BattleLogHealthy())
        status = 1;
    fclose(out);
    return status;
}
