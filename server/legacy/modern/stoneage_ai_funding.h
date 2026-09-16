#ifndef STONEAGE_AI_FUNDING_H
#define STONEAGE_AI_FUNDING_H

/*
 * Server-side funding capability for administrator-created AI characters.
 *
 * The capability is matched to the live account and save-file slot.  It is
 * deliberately separate from CHAR_GOLD: the legacy integer remains a real,
 * bounded player balance and is never used as an unlimited sentinel.
 */
enum {
    STONEAGE_AI_EXTERNAL_TRADE = 1,
    STONEAGE_AI_EXTERNAL_BANK = 2,
    STONEAGE_AI_EXTERNAL_DROP = 3
};

int StoneAge_AIFundingCanCharge( int charaindex, int amount );
int StoneAge_AIFundingCharge( int charaindex, int amount );
int StoneAge_AIFundingCanExternal( int charaindex, int kind, int amount );
int StoneAge_AIFundingRecordExternal( int charaindex, int kind, int amount );
/* Validates both real balances and commits both trade quotas together.
 * Does not move gold/items/pets. A failure must prevent the native exchange;
 * a post-rename durability failure is uncertain and must not be replayed. */
int StoneAge_AIFundingRecordTradePair( int first, int first_amount,
                                      int second, int second_amount );

/* Policy files are read on every authorization decision.  This hook keeps
 * the main-loop integration explicit and intentionally has no cache to flush.
 */
void StoneAge_AIFundingRefresh( void );

#endif
