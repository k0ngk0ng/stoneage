#ifndef STONEAGE_CHAT_IDENTITY_H
#define STONEAGE_CHAT_IDENTITY_H

/* sender is the character index available at the original chat call site,
 * object is the TK object index. Neither is a persistent player identity. */
void StoneAge_ChatIdentitySend(int fd, int sender, int object, char *message, int color);

#endif
