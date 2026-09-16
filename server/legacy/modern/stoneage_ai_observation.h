#ifndef STONEAGE_AI_OBSERVATION_H
#define STONEAGE_AI_OBSERVATION_H

/*
 * Build a read-only, character-scoped observation for the AI game bridge.
 * The returned buffer is owned by the module and is valid until the next
 * call.  It is intended to be passed immediately to lssproto_S_send().
 */
char *StoneAge_AIObservationMake( int charaindex );
char *StoneAge_AIObservationMakeWithRequest( int charaindex, const char *request );

#endif
