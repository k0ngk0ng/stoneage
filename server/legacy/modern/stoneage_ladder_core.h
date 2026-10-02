#ifndef STONEAGE_LADDER_CORE_H
#define STONEAGE_LADDER_CORE_H
#include <stddef.h>

/* All entry points run on the GMSV main thread. No client supplies player
 * identity, combat statistics, ratings, or a battle participant list. */
#define LADDER_TEAM_MAX 5
#define LADDER_ID_MAX 65
#define LADDER_NAME_MAX 129
#define LADDER_PLAYER_MAX 1024
#define LADDER_ROOM_MAX 256
#define LADDER_MATCH_MAX 128
#define LADDER_INVITE_MAX 1024
/* Legacy util_SendMesg has 32 KiB buffers, including base64 expansion. */
#define LADDER_WIRE_MAX 24000
#define LADDER_CONTACT_IDENTITY_REQUIRED -2
#define LADDER_CONTACT_IDENTITY_CHANGED -3

typedef long long LadderTime;

typedef struct {
    char id[LADDER_ID_MAX], name_hex[LADDER_NAME_MAX];
    int character, online, idle, pet_mask, active_pet, ride_pet;
    const char *busy_reason;
    double character_power, pet_power[5];
    unsigned long long loadout_hash;
} LadderProfile;

typedef struct {
    int player_kills, pet_kills, deaths, assists, revives;
    long long damage, damage_taken, pet_damage, pet_damage_taken, ride_damage_taken, healing;
} LadderStats;

typedef struct {
    char id[LADDER_ID_MAX], name_hex[LADDER_NAME_MAX];
    int online;
} LadderContact;

typedef struct {
    LadderTime (*now)(void);
    int (*profile)(int character, LadderProfile *profile);
    int (*contact)(int character, int address_index);
    void (*send)(int character, const char *wire);
    /* start must either enter EVERY listed player or roll back all entries. */
    int (*start)(const char *match_id, const int *characters, const int *pet_masks, int mode);
    void (*abandon)(int character);
    void (*finish)(int battle, int winner_side, const char *reason);
    /* Persist native pre-match resources during the countdown. Return 1
     * when every save is confirmed, 0 while pending, -1 on failure. */
    int (*prepare)(const char *match_id, const int *characters, const int *pet_masks, int mode);
    void (*cancel_prepare)(const char *match_id);
    /* One atomic card view, including its archived character identity. */
    int (*contact_entry)(int character, int address_index, LadderContact *entry);
} LadderHooks;

int Ladder_Init(const char *database, const LadderHooks *hooks);
void Ladder_Shutdown(void);
int Ladder_AdminSnapshot(char *out,size_t capacity,int offset,int (*battle_turn)(int));
void Ladder_Request(int character, const char *wire);
void Ladder_Tick(void);
void Ladder_Disconnected(int character);
void Ladder_Reconnected(int character);
/* Call before the native character slot is deleted or reused. */
void Ladder_Detached(int character);
/* Voluntary escape and reconnect timeout share the same final responsibility. */
int Ladder_Abandon(int character);
void Ladder_Rejected(int character);
int Ladder_Reserved(int character);
int Ladder_PetAllowed(int character, int slot);
int Ladder_Battle(int battle);
int Ladder_CharacterBattle(int character);
void Ladder_End(int battle, int winner_side, int turns, const char *reason, int rated);
void Ladder_Stats(int battle, int character, const LadderStats *delta);
double Ladder_EquivalentPoints(double hp, double attack, double defense, double speed);
double Ladder_PowerGap(double a, double b);
int Ladder_RatingDelta(double a, double b, double score);

#endif
