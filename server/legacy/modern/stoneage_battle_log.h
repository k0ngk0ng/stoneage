#ifndef STONEAGE_BATTLE_LOG_H
#define STONEAGE_BATTLE_LOG_H

/* Single game-thread producer. The worker only sees immutable JSON, never
 * character pointers. Submission is bounded and never waits for disk I/O. */
#define BATTLE_LOG_JSON_MAX 3584
enum { BATTLE_LOG_BEGIN = 1, BATTLE_LOG_EVENT = 2, BATTLE_LOG_END = 3 };
int StoneAge_BattleLogInit(int slots);
const char *StoneAge_BattleLogSession(void);
int StoneAge_BattleLogSubmit(int slot, unsigned long match, unsigned long sequence,
                         int kind, const char *json);
void StoneAge_BattleLogShutdown(void);
void StoneAge_BattleLogOffline(void);
int StoneAge_BattleLogHealthy(void);

#endif
