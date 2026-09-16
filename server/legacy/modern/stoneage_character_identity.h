#ifndef STONEAGE_CHARACTER_IDENTITY_H
#define STONEAGE_CHARACTER_IDENTITY_H

struct tagChar;

/* Dedicated player identity, independent from account/slot/name and pet ucode.
 * Only the save path may fill a missing value. Failure leaves it unknown and
 * must not prevent the ordinary gameplay save. A generated in-memory value
 * is NOT proof that SAAC has committed it; observers must wait for a confirmed
 * save or a later load from persisted character data before publishing it. */
int StoneAge_CharacterIdentityValid(const char *value);
int StoneAge_CharacterIdentityPrepareSave(struct tagChar *character);

/* Called only for character data accepted from a SAAC login response. */
void StoneAge_CharacterIdentityMarkLoaded(struct tagChar *character);
const char *StoneAge_CharacterIdentityGetLoaded(const struct tagChar *character);
const char *StoneAge_CharacterIdentityGetLoadedByIndex(int index);

#endif
