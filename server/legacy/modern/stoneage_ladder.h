#ifndef STONEAGE_LADDER_H
#define STONEAGE_LADDER_H
#include <stddef.h>

/* Integration boundary for the preserved native server. */
void StoneAge_LadderTick(void);
int StoneAge_LadderRequest(int fd,const char *category);
int StoneAge_LadderGuard(int fd,const char *operation);
int StoneAge_LadderInteractionAllowed(int character,int target);
int StoneAge_LadderReserved(int character);
int StoneAge_LadderEntryAllowed(int character,int battle);
int StoneAge_LadderPetAllowed(int character,int slot);
int StoneAge_LadderDisconnect(int fd);
void StoneAge_LadderAuthenticated(int fd,int accepted);
int StoneAge_LadderResume(int fd,const char *name);
int StoneAge_LadderOfflineSaveReply(int request,const char *result);
void StoneAge_LadderCharacterLoaded(int character);
void StoneAge_LadderCharacterDeleted(int character);
int StoneAge_LadderEscape(int battle,int bid);
int StoneAge_LadderCommandAllowed(int character,const char *command);
int StoneAge_LadderFinish(int battle);
int StoneAge_LadderCommandWait(int battle,int side);
int StoneAge_LadderIsBattle(int battle);
/* Serializers return the pre-match record while temporary resources are in
 * use. Item slots remain allocated so consumption cannot recycle originals. */
char *StoneAge_LadderSavedCharacter(void *character);
char *StoneAge_LadderSavedOptions(void *character);
char *StoneAge_LadderSavedPet(int character);
int StoneAge_LadderRetainsItem(int item);
/* Extend native pet archives using the existing nested item encoding. */
int StoneAge_LadderAppendPetItems(int character,char *data,size_t capacity);
int StoneAge_LadderParsePetItem(void *character,const char *key,char *value);
int StoneAge_LadderKnockout(int battle,int character);
void StoneAge_LadderExecuting(int battle,int bid);
void StoneAge_LadderExecutionEnd(void);
void StoneAge_LadderHPChanged(int character,int before,int after);
int StoneAge_LadderSetHP(int source,int target,int hp);
void StoneAge_LadderStatusChanged(int character,int field,int before,int after);
void StoneAge_LadderPeriodicSource(int character,int field);
int StoneAge_LadderSendRecipient(int character);
int StoneAge_LadderResumeFlags(int character,int flags);
void StoneAge_LadderWindow(int character,int open);
void StoneAge_LadderWindowSent(int character,int type,int sequence,int object,const char *data);
/* Archive-backed contact identity, independent of account/name reuse. */
void StoneAge_LadderBindContact(int character,void *entry);
void StoneAge_LadderRefreshContacts(int left,int right);
void StoneAge_LadderParseContact(void *entry,const char *record);
const char *StoneAge_LadderContactIdentity(const void *entry);

#endif
