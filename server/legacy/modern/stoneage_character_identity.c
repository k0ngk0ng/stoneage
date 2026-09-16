#include <errno.h>
#include <fcntl.h>
#include <stddef.h>
#include <string.h>
#include <unistd.h>

#include "char.h"
#include "stoneage_character_identity.h"

#define STONEAGE_CHARACTER_ID_PREFIX "pc1_"
#define STONEAGE_CHARACTER_ID_BYTES 16
#define STONEAGE_CHARACTER_ID_LENGTH 36

int StoneAge_CharacterIdentityValid(const char *value)
{
    size_t i;
    if (value == NULL || strncmp(value, STONEAGE_CHARACTER_ID_PREFIX, 4) != 0)
        return 0;
    for (i = 4; i < STONEAGE_CHARACTER_ID_LENGTH; i++) {
        if (!((value[i] >= '0' && value[i] <= '9') ||
              (value[i] >= 'a' && value[i] <= 'f'))) return 0;
    }
    return value[STONEAGE_CHARACTER_ID_LENGTH] == '\0';
}

int StoneAge_CharacterIdentityPrepareSave(Char *character)
{
    unsigned char entropy[STONEAGE_CHARACTER_ID_BYTES];
    char encoded[STONEAGE_CHARACTER_ID_LENGTH + 1];
    static const char hex[] = "0123456789abcdef";
    char *destination;
    int fd;
    ssize_t count;
    size_t used, i;

    if (character == NULL || !character->use ||
        character->data[CHAR_WHICHTYPE] != CHAR_TYPEPLAYER) return 0;
    destination = character->string[CHAR_PERSISTENTID].string;
    if (sizeof(character->string[CHAR_PERSISTENTID].string) < sizeof(encoded))
        return 0;
    /* Nonempty values are immutable here, including malformed ones. Never
     * silently replace an old identity and merge/divide historical people. */
    if (destination[0] != '\0')
        return StoneAge_CharacterIdentityValid(destination);

    fd = open("/dev/urandom", O_RDONLY);
    if (fd < 0) return 0;
    used = 0;
    while (used < sizeof(entropy)) {
        count = read(fd, entropy + used, sizeof(entropy) - used);
        if (count < 0 && errno == EINTR) continue;
        if (count <= 0) { close(fd); return 0; }
        used += (size_t)count;
    }
    close(fd);
    memcpy(encoded, STONEAGE_CHARACTER_ID_PREFIX, 4);
    for (i = 0; i < sizeof(entropy); i++) {
        encoded[4 + i * 2] = hex[entropy[i] >> 4];
        encoded[5 + i * 2] = hex[entropy[i] & 15];
    }
    encoded[STONEAGE_CHARACTER_ID_LENGTH] = '\0';
    memcpy(destination, encoded, sizeof(encoded));
    return 1;
}

void StoneAge_CharacterIdentityMarkLoaded(Char *character)
{
    const char *value;
    if (character == NULL) return;
    character->workchar[CHAR_WORKPERSISTENTID_LOADED].string[0] = '\0';
    if (!character->use || character->data[CHAR_WHICHTYPE] != CHAR_TYPEPLAYER)
        return;
    value = character->string[CHAR_PERSISTENTID].string;
    if (StoneAge_CharacterIdentityValid(value))
        memcpy(character->workchar[CHAR_WORKPERSISTENTID_LOADED].string,
               value, STONEAGE_CHARACTER_ID_LENGTH + 1);
}

const char *StoneAge_CharacterIdentityGetLoaded(const Char *character)
{
    const char *value;
    if (character == NULL || !character->use ||
        character->data[CHAR_WHICHTYPE] != CHAR_TYPEPLAYER) return NULL;
    value = character->string[CHAR_PERSISTENTID].string;
    if (!StoneAge_CharacterIdentityValid(value) ||
        strcmp(value, character->workchar[CHAR_WORKPERSISTENTID_LOADED].string) != 0)
        return NULL;
    return value;
}

const char *StoneAge_CharacterIdentityGetLoadedByIndex(int index)
{
    if (!CHAR_CHECKINDEX(index)) return NULL;
    return StoneAge_CharacterIdentityGetLoaded(CHAR_getCharPointer(index));
}
