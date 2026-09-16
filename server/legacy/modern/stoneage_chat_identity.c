#include <stdio.h>
#include <string.h>
#include "char.h"
#include "lssproto_serv.h"
#include "stoneage_character_identity.h"
#include "stoneage_chat_identity.h"

#define CHAT_IDENTITY_MAX_MESSAGE 2047

void StoneAge_ChatIdentitySend(int fd, int sender, int object, char *message, int color)
{
    static const char hex[] = "0123456789abcdef";
    char metadata[4201];
    const char *identity;
    size_t length, i, used;
    int prefix;

    identity = NULL;
    if (fd >= 0 && object >= 0 && CHAR_CHECKINDEX(sender) &&
        CHAR_getWorkInt(sender, CHAR_WORKOBJINDEX) == object)
        identity = StoneAge_CharacterIdentityGetLoadedByIndex(sender);
    if (identity != NULL && message != NULL) {
        /* Long chat still reaches the original TK path, without attribution.
         * Bound scanning as well as hex expansion. Never truncate metadata. */
        for (length = 0; length <= CHAT_IDENTITY_MAX_MESSAGE && message[length]; length++) {}
        if (length > 0 && length <= CHAT_IDENTITY_MAX_MESSAGE) {
            prefix = snprintf(metadata, sizeof(metadata), "AICHAT|1|%d|%d|%s|", object, color, identity);
            if (prefix > 0 && (size_t)prefix + length * 2 < sizeof(metadata)) {
                used = (size_t)prefix;
                for (i = 0; i < length; i++) {
                    unsigned char byte = (unsigned char)message[i];
                    metadata[used++] = hex[byte >> 4];
                    metadata[used++] = hex[byte & 15];
                }
                metadata[used] = '\0';
                lssproto_S_send(fd, metadata);
            }
        }
    }
    /* Keep ordinary TK exactly once, with identical parameters, including
     * system, NPC, unknown/unsaved identities and messages above the bound. */
    lssproto_TK_send(fd, object, message, color);
}
