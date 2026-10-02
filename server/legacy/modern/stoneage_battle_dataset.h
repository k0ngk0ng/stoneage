#ifndef STONEAGE_BATTLE_DATASET_H
#define STONEAGE_BATTLE_DATASET_H
int StoneAge_BattleDatasetMain(int argc, char **argv);
int StoneAge_BattleEnvironmentMain(int argc, char **argv);
int StoneAge_BattleRulesMain(int argc, char **argv);
/* Inactive in normal servers. Capture only the synthetic CLI's own B packets. */
int StoneAge_BattleEnvironmentPacket(int character, const char *packet);
/* Only the process-local training CLI can activate this combat-rule scope. */
int StoneAge_BattleEnvironmentIsBattle(int battle);
int StoneAge_BattleEnvironmentMembers(int battle, int *members, int capacity);
/* Synthetic in-memory characters only; called by the offline CLI, never an
 * HTTP/agent route. Normal players still use the original FD dispatcher. */
void StoneAge_BattleDatasetCommand(int character, char *command);
const char *StoneAge_BattleDatasetContext(void);
void StoneAge_BattleDatasetEndLimit(int battle);
#endif
