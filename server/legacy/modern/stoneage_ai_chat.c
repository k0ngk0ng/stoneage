/*
 * Publish the identity of the character which produced one native TK packet.
 *
 * The companion is deliberately built from the speaker character index while
 * the normal chat path still has that index.  Object indexes are reusable and
 * are therefore only copied into the record; they are never used to discover
 * a character after the fact.  A missing or uncommitted identity simply
 * suppresses the companion and leaves the original TK untouched.
 */
#include <stddef.h>
#include <stdio.h>

#include "char.h"
#include "char_base.h"
#include "lssproto_serv.h"
#include "stoneage_ai_chat.h"
#include "stoneage_character_identity.h"

#define STONEAGE_AI_CHAT_MAX_MESSAGE_BYTES 2047
#define STONEAGE_AI_CHAT_MAX_PAYLOAD_BYTES 4200

static int StoneAge_AIChatMessageLength(const char *message, size_t *length)
{
    size_t used;

    if (message == NULL || length == NULL) return FALSE;
    used = 0;
    /* Include the upper-bound byte in the scan so a 2048-byte message is
     * rejected without ever constructing a truncated companion record. */
    while (used <= STONEAGE_AI_CHAT_MAX_MESSAGE_BYTES &&
           message[used] != '\0') used++;
    if (used == 0 || used > STONEAGE_AI_CHAT_MAX_MESSAGE_BYTES) return FALSE;
    *length = used;
    return TRUE;
}

void StoneAge_AIChatSend(int fd, int speaker_charaindex,
                         int color, const char *message)
{
    static const char hex[] = "0123456789abcdef";
    const char *character_id;
    char payload[STONEAGE_AI_CHAT_MAX_PAYLOAD_BYTES + 1];
    int object_id;
    int prefix_length;
    size_t message_length;
    size_t used;
    size_t i;

    if (fd < 0 || !StoneAge_AIChatMessageLength(message, &message_length))
        return;
    if (!CHAR_CHECKINDEX(speaker_charaindex) ||
        CHAR_getInt(speaker_charaindex, CHAR_WHICHTYPE) != CHAR_TYPEPLAYER)
        return;

    character_id = StoneAge_CharacterIdentityGetLoadedByIndex(
        speaker_charaindex);
    if (character_id == NULL ||
        !StoneAge_CharacterIdentityValid(character_id)) return;

    object_id = CHAR_getWorkInt(speaker_charaindex, CHAR_WORKOBJINDEX);
    if (object_id < 0) return;

    prefix_length = snprintf(payload, sizeof(payload),
                             "AICHAT|1|%d|%d|%s|",
                             object_id, color, character_id);
    if (prefix_length < 0 ||
        (size_t)prefix_length > STONEAGE_AI_CHAT_MAX_PAYLOAD_BYTES)
        return;
    used = (size_t)prefix_length;
    if (message_length > (STONEAGE_AI_CHAT_MAX_PAYLOAD_BYTES - used) / 2)
        return;

    for (i = 0; i < message_length; i++) {
        unsigned char byte = (unsigned char)message[i];
        payload[used++] = hex[byte >> 4];
        payload[used++] = hex[byte & 0x0f];
    }
    if (used > STONEAGE_AI_CHAT_MAX_PAYLOAD_BYTES) return;
    payload[used] = '\0';
    lssproto_S_send(fd, payload);
}
