#ifndef STONEAGE_BATTLE_DATASET_H
#define STONEAGE_BATTLE_DATASET_H
int StoneAge_BattleDatasetMain(int argc, char **argv);
/* Synthetic in-memory characters only; called by the offline CLI, never an
 * HTTP/agent route. Normal players still use the original FD dispatcher. */
void StoneAge_BattleDatasetCommand(int character, char *command);
const char *StoneAge_BattleDatasetContext(void);
void StoneAge_BattleDatasetEndLimit(int battle);
#endif
