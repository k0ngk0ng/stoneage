#ifndef STONEAGE_AI_OBSERVATION_H
#define STONEAGE_AI_OBSERVATION_H
#include <stddef.h>

/*
 * Build a read-only, character-scoped observation for the AI game bridge.
 * The returned buffer is owned by the module and is valid until the next
 * call.  It is intended to be passed immediately to lssproto_S_send().
 */
char *StoneAge_AIObservationMake( int charaindex );
char *StoneAge_AIObservationMakeWithRequest( int charaindex, const char *request );
/* Append an optional owned spell ID to a complete native J status packet. */
void StoneAge_MagicObservationAppend(int charaindex, int slot, char *buffer, size_t size);

#endif
