#ifndef STONEAGE_PERSON_IDENTITY_H
#define STONEAGE_PERSON_IDENTITY_H

/* Publish loaded persistent identities only for records that are about to be
 * sent through the ordinary, character-scoped C/N status paths. The helpers
 * send AIPERSON companions and leave the original protocol packet untouched. */
void StoneAge_PersonIdentitySendC(int fd, char *data);
void StoneAge_PersonIdentitySendN(int fd, char *data);

#endif
