#ifndef STONEAGE_ADULT_EXCHANGE_H
#define STONEAGE_ADULT_EXCHANGE_H

/* -1: a different script, 0: preparation failed, 1: grant/exchange committed.
 * The caller retains responsibility for event flags and dialog flow. */
int StoneAge_AdultExchange( int talker, const char *script );

#endif
