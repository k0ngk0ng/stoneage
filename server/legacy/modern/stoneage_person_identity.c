#include <limits.h>
#include <stddef.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "char.h"
#include "net.h"
#include "object.h"
#include "lssproto_serv.h"
#include "util.h"
#include "stoneage_character_identity.h"
#include "stoneage_person_identity.h"

#define STONEAGE_PERSON_IDENTITY_MAX_RECORD 2047
#define STONEAGE_PERSON_IDENTITY_MAX_METADATA 4201

/* Resolve a wire base-62 object token without adding a decoder to the legacy
 * utility surface. Re-encoding the value enforces the canonical spelling, so
 * aliases such as 00 can never identify a live object. */
static int StoneAge_PersonIdentityDecodeBase62(const char *token,
                                               size_t length)
{
    static const char digits[] =
        "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ";
    char encoded[64];
    unsigned long value;
    unsigned int digit;
    size_t i;
    int value_as_int;

    if (token == NULL || length == 0 || length >= sizeof(encoded)) return -1;
    value = 0;
    for (i = 0; i < length; i++) {
        const char *found = strchr(digits, token[i]);
        if (found == NULL) return -1;
        digit = (unsigned int)(found - digits);
        if (value > ((unsigned long)INT_MAX - digit) / 62UL) return -1;
        value = value * 62UL + digit;
    }
    value_as_int = (int)value;
    if (cnv10to62(value_as_int, encoded, sizeof(encoded)) == NULL ||
        strlen(encoded) != length || memcmp(encoded, token, length) != 0)
        return -1;
    return value_as_int;
}

/* Validate a decimal object value emitted by CHAR_make_N_StatusString when
 * the N mask includes CHAR_N_STRING_OBJINDEX. */
static int StoneAge_PersonIdentityDecodeDecimalObject(const char *value,
                                                      size_t length)
{
    char number[32];
    char canonical[32];
    unsigned long parsed;
    size_t i;

    if (value == NULL || length == 0 || length >= sizeof(number)) return -1;
    parsed = 0;
    for (i = 0; i < length; i++) {
        unsigned int digit;
        if (value[i] < '0' || value[i] > '9') return -1;
        digit = (unsigned int)(value[i] - '0');
        if (parsed > ((unsigned long)INT_MAX - digit) / 10UL) return -1;
        parsed = parsed * 10UL + digit;
    }
    if (snprintf(number, sizeof(number), "%lu", parsed) <= 0 ||
        strlen(number) != length || memcmp(number, value, length) != 0 ||
        snprintf(canonical, sizeof(canonical), "%d", (int)parsed) <= 0 ||
        strcmp(number, canonical) != 0 ||
        parsed >= (unsigned long)OBJECT_getNum() ||
        !CHECKOBJECTUSE((int)parsed) ||
        OBJECT_getType((int)parsed) != OBJTYPE_CHARA)
        return -1;
    if (!CHAR_CHECKINDEX(OBJECT_getIndex((int)parsed)) ||
        CHAR_getInt(OBJECT_getIndex((int)parsed), CHAR_WHICHTYPE) != CHAR_TYPEPLAYER ||
        CHAR_getWorkInt(OBJECT_getIndex((int)parsed), CHAR_WORKOBJINDEX) != (int)parsed)
        return -1;
    return (int)parsed;
}

static int StoneAge_PersonIdentityObjectForCharacter(int character)
{
    int object;
    if (!CHAR_CHECKINDEX(character) ||
        CHAR_getInt(character, CHAR_WHICHTYPE) != CHAR_TYPEPLAYER)
        return -1;
    object = CHAR_getWorkInt(character, CHAR_WORKOBJINDEX);
    if (object < 0 || object >= OBJECT_getNum() || !CHECKOBJECTUSE(object) ||
        OBJECT_getType(object) != OBJTYPE_CHARA ||
        OBJECT_getIndex(object) != character)
        return -1;
    return object;
}

static int StoneAge_PersonIdentityRange(const char *start, const char *end,
                                        int fd, int object,
                                        const char *identity)
{
    static const char hex[] = "0123456789abcdef";
    char metadata[STONEAGE_PERSON_IDENTITY_MAX_METADATA];
    int prefix;
    size_t length;
    size_t used;
    size_t i;

    if (start == NULL || end == NULL || end < start || identity == NULL)
        return FALSE;
    length = (size_t)(end - start);
    if (length == 0 || length > STONEAGE_PERSON_IDENTITY_MAX_RECORD)
        return FALSE;
    prefix = snprintf(metadata, sizeof(metadata),
                      "AIPERSON|1|C|%d|%s|", object, identity);
    if (prefix <= 0 || (size_t)prefix + length * 2 >= sizeof(metadata))
        return FALSE;
    used = (size_t)prefix;
    for (i = 0; i < length; i++) {
        unsigned char byte = (unsigned char)start[i];
        metadata[used++] = hex[byte >> 4];
        metadata[used++] = hex[byte & 15];
    }
    metadata[used] = '\0';
    lssproto_S_send(fd, metadata);
    return TRUE;
}

/* The caller supplies the recipient fd. Kept separate from the range parser
 * so a central lssproto_C_send wrapper can emit one companion per visible
 * player record while preserving the exact aggregate C payload. */
static int StoneAge_PersonIdentityResolveCRange(const char *start,
                                                const char *end, int *object_out,
                                                const char **identity_out)
{
    char type[16];
    const char *first;
    const char *second;
    char *type_end;
    long type_value;
    int object;
    int character;
    const char *identity;
    size_t length;
    if (start == NULL || end == NULL || end <= start || object_out == NULL ||
        identity_out == NULL) return FALSE;
    first = memchr(start, '|', (size_t)(end - start));
    if (first == NULL || first == start || (size_t)(first - start) >= sizeof(type))
        return FALSE;
    memcpy(type, start, (size_t)(first - start));
    type[first - start] = '\0';
    type_value = strtol(type, &type_end, 10);
    if (*type_end != '\0' || type_value != CHAR_TYPEPLAYER) return FALSE;
    second = memchr(first + 1, '|', (size_t)(end - first - 1));
    if (second == NULL || second == first + 1) return FALSE;
    length = (size_t)(second - (first + 1));
    object = StoneAge_PersonIdentityDecodeBase62(first + 1, length);
    if (object < 0 || object >= OBJECT_getNum() || !CHECKOBJECTUSE(object) ||
        OBJECT_getType(object) != OBJTYPE_CHARA)
        return FALSE;
    character = OBJECT_getIndex(object);
    if (!CHAR_CHECKINDEX(character) || !CHAR_getFlg(character, CHAR_ISVISIBLE))
        return FALSE;
    if (CHAR_getInt(character, CHAR_WHICHTYPE) != CHAR_TYPEPLAYER ||
        CHAR_getWorkInt(character, CHAR_WORKOBJINDEX) != object)
        return FALSE;
    identity = StoneAge_CharacterIdentityGetLoadedByIndex(character);
    if (identity == NULL) return FALSE;
    *object_out = object;
    *identity_out = identity;
    return TRUE;
}

static int StoneAge_PersonIdentitySendCRange(int fd, const char *start,
                                             const char *end)
{
    int object;
    const char *identity;
    if (!StoneAge_PersonIdentityResolveCRange(start, end, &object, &identity))
        return FALSE;
    return StoneAge_PersonIdentityRange(start, end, fd, object, identity);
}

void StoneAge_PersonIdentitySendC(int fd, char *data)
{
    const char *start;
    const char *separator;
    int object;
    const char *identity;
    int valid;

    if (fd < 0 || data == NULL || data[0] == '\0') return;
    /* A single C packet can contain many records. Count first so a packet
     * over the bounded companion cache never leaves a misleading prefix. */
    valid = 0;
    start = data;
    while (*start != '\0') {
        separator = strchr(start, ',');
        if (separator == NULL) separator = start + strlen(start);
        if (StoneAge_PersonIdentityResolveCRange(start, separator, &object,
                                                  &identity)) {
            valid++;
            if (valid > 128) return;
        }
        if (*separator == '\0') break;
        start = separator + 1;
    }

    start = data;
    while (*start != '\0') {
        separator = strchr(start, ',');
        if (separator == NULL) separator = start + strlen(start);
        StoneAge_PersonIdentitySendCRange(fd, start, separator);
        if (*separator == '\0') break;
        start = separator + 1;
    }
}

static int StoneAge_PersonIdentitySendNRange(int fd, char *data)
{
    char slot[16];
    const char *first;
    const char *second;
    const char *third;
    const char *end;
    int object;
    int character;
    int receiver;
    int member_object;
    int mask;
    const char *identity;
    char *slot_end;
    long slot_value;
    static const char hex[] = "0123456789abcdef";
    char metadata[STONEAGE_PERSON_IDENTITY_MAX_METADATA];
    size_t length;
    size_t used;
    size_t i;
    int prefix;

    if (fd < 0 || data == NULL || data[0] != 'N') return FALSE;
    end = data + strlen(data);
    first = strchr(data, '|');
    if (first == NULL || first == data + 1 ||
        (size_t)(first - (data + 1)) >= sizeof(slot)) return FALSE;
    memcpy(slot, data + 1, (size_t)(first - (data + 1)));
    slot[first - (data + 1)] = '\0';
    slot_value = strtol(slot, &slot_end, 10);
    if (*slot_end != '\0' || slot_value < 0 || slot_value >= CHAR_PARTYMAX)
        return FALSE;
    second = strchr(first + 1, '|');
    if (second == NULL) return FALSE;
    third = strchr(second + 1, '|');
    if (third == NULL) return FALSE;
    mask = StoneAge_PersonIdentityDecodeBase62(first + 1,
                                               (size_t)(second - first - 1));
    if (mask == 1) mask = 126; /* legacy full-party sentinel */
    if (mask < 0 || (mask & ~126) != 0) return FALSE;
    if (mask == 0) return FALSE;
    receiver = CONNECT_getCharaindex(fd);
    if (!CHAR_CHECKINDEX(receiver)) return FALSE;
    character = CHAR_getPartyIndex(receiver, (int)slot_value);
    member_object = StoneAge_PersonIdentityObjectForCharacter(character);
    if (member_object < 0) return FALSE;
    object = member_object;
    if (mask & 2) {
        size_t object_length;
        int encoded_object;
        object_length = (size_t)(third - second - 1);
        encoded_object = StoneAge_PersonIdentityDecodeDecimalObject(
            second + 1, object_length);
        if (encoded_object < 0 || encoded_object != object) return FALSE;
    }
    identity = StoneAge_CharacterIdentityGetLoadedByIndex(character);
    if (identity == NULL) return FALSE;
    length = (size_t)(end - data);
    if (length == 0 || length > STONEAGE_PERSON_IDENTITY_MAX_RECORD) return FALSE;
    prefix = snprintf(metadata, sizeof(metadata),
                      "AIPERSON|1|N|%d|%s|", object, identity);
    if (prefix <= 0 || (size_t)prefix + length * 2 >= sizeof(metadata))
        return FALSE;
    used = (size_t)prefix;
    for (i = 0; i < length; i++) {
        unsigned char byte = (unsigned char)data[i];
        metadata[used++] = hex[byte >> 4];
        metadata[used++] = hex[byte & 15];
    }
    metadata[used] = '\0';
    lssproto_S_send(fd, metadata);
    return TRUE;
}

void StoneAge_PersonIdentitySendN(int fd, char *data)
{
    (void)StoneAge_PersonIdentitySendNRange(fd, data);
}
