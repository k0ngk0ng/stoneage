#ifndef STONEAGE_BATTLE_RECORD_H
#define STONEAGE_BATTLE_RECORD_H
void StoneAge_BattleRecordInit(void);
void StoneAge_BattleRecordBegin(int battle);
void StoneAge_BattleRecordObservation(int battle, int character, const char *bp, const char *bc);
unsigned long StoneAge_BattleRecordRequest(int character, const char *command, int depth);
void StoneAge_BattleRecordDispatch(int character, unsigned long request);
void StoneAge_BattleRecordTurn(int battle, int after);
void StoneAge_BattleRecordExecuting(int battle, int bid, int command);
void StoneAge_BattleRecordExit(int battle, int character, const char *reason);
void StoneAge_BattleRecordEnd(int battle, int normal);
#endif
