#ifndef STONEAGE_CHARACTER_STORE_H
#define STONEAGE_CHARACTER_STORE_H

/* Success means the complete archive and its directory entry are durable.
 * A failed call may have completed the rename but never leaves a partial
 * archive at the final path. Retrying the same payload is safe. */
int StoneAge_CharacterStoreWrite(const char *path,const char *data);

#endif
