#ifndef STONEAGE_AI_CHAT_H
#define STONEAGE_AI_CHAT_H

/* Send one character-scoped companion record immediately before its TK. */
void StoneAge_AIChatSend(int fd, int speaker_charaindex,
                         int color, const char *message);

#endif
