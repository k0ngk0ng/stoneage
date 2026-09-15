/*
 * StoneAge player administration bridge.
 *
 * This file is modern compatibility code.  The archived 2.5 sources are
 * encoded in CP936; keeping this module in a separate ASCII/UTF-8 file means
 * that build.sh can add the bridge without transcoding any archived source.
 * All work happens on the GMSV main-loop thread.  The bridge only consumes
 * atomically-created request files and writes response files; it has no
 * network or command-execution path.
 */
#include "version.h"
#include <dirent.h>
#include <errno.h>
#include <limits.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <sys/types.h>
#include <unistd.h>

#include "battle.h"
#include "char.h"
#include "char_data.h"
#include "enemy.h"
#include "item.h"
#include "pet.h"
#include "pet_skill.h"

#define STONEAGE_PA_VERSION 1
#define STONEAGE_PA_MAX_REQUEST 65535
#define STONEAGE_PA_MAX_RESPONSE 65535
#define STONEAGE_PA_MAX_FIELDS 96
#define STONEAGE_PA_MAX_ID 64
#define STONEAGE_PA_MAX_VALUE 8192
#define STONEAGE_PA_MAX_PATH 1024
#define STONEAGE_PA_MAX_ACTION 32
#define STONEAGE_PA_MAX_LOCATION 16
#define STONEAGE_PA_MAX_FIELD 32
#define STONEAGE_PA_MAX_ACCOUNT 256
#define STONEAGE_PA_MAX_NAME 256
#define STONEAGE_PA_MAX_ATTRS 64
#define STONEAGE_PA_MAX_POSSESSIONS (CHAR_MAXITEMHAVE + CHAR_MAXPOOLITEMHAVE + CHAR_MAXPETHAVE + CHAR_MAXPOOLPETHAVE)
#define STONEAGE_PA_MAX_CHARACTER_SLOTS 2
#define STONEAGE_PA_MAX_NATIVE_NAME 64
#define STONEAGE_PA_MAX_GRANT_QUANTITY 64
#define STONEAGE_PA_MIN_PET_MODAI 0
#define STONEAGE_PA_MAX_PET_MODAI 1000000

typedef struct tagStoneAgePAField {
    char key[64];
    char value[STONEAGE_PA_MAX_VALUE];
} StoneAgePAField;

typedef struct tagStoneAgePARequest {
    char id[STONEAGE_PA_MAX_ID + 1];
    char action[STONEAGE_PA_MAX_ACTION + 1];
    char account[STONEAGE_PA_MAX_ACCOUNT];
    char character[STONEAGE_PA_MAX_NAME];
    char location[STONEAGE_PA_MAX_LOCATION + 1];
    char field[STONEAGE_PA_MAX_FIELD + 1];
    char name[STONEAGE_PA_MAX_NAME];
    char payload[STONEAGE_PA_MAX_VALUE];
    int protocol;
    int character_slot;
    int slot;
    int expected_sequence;
    char expected_revision[32];
    int expected_item_id;
    int expected_pet_id;
    int template_id;
    int item_id;
    int quantity;
    int value;
    int skill_slot;
    int skill_id;
    int has_slot;
    int has_character_slot;
    int has_expected_sequence;
    int has_expected_revision;
    int has_expected_item_id;
    int has_expected_pet_id;
    int has_template_id;
    int has_item_id;
    int has_quantity;
    int has_value;
    int has_skill_slot;
    int has_skill_id;
    int has_field;
    int has_name;
    int has_payload;
    int granted_slots[STONEAGE_PA_MAX_GRANT_QUANTITY];
    int granted_slot_count;
    int field_count;
    StoneAgePAField fields[STONEAGE_PA_MAX_FIELDS];
} StoneAgePARequest;

typedef struct tagStoneAgePAResponse {
    FILE *fp;
    size_t length;
    int failed;
} StoneAgePAResponse;

static void StoneAgePA_writeItemObject( StoneAgePAResponse *response,
                                        int itemindex, const char *prefix );

static char StoneAgePA_dir[STONEAGE_PA_MAX_PATH] = "/run/stoneage/player-admin/gmsv";
static int StoneAgePA_initialized = FALSE;
static unsigned long StoneAgePA_response_serial = 0;

static int StoneAgePA_isHex( int c )
{
    return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') ||
           (c >= 'A' && c <= 'F');
}

static int StoneAgePA_hex( int c )
{
    if( c >= '0' && c <= '9' ) return c - '0';
    if( c >= 'a' && c <= 'f' ) return c - 'a' + 10;
    return c - 'A' + 10;
}

/* Values use percent encoding over raw CP936 bytes.  This prevents a name
 * from introducing a protocol delimiter while preserving its legacy bytes. */
static int StoneAgePA_decode( const char *src, char *dst, size_t dstlen )
{
    size_t i = 0;
    size_t out = 0;
    int c;
    while( src[i] != '\0' ) {
        if( src[i] == '%' ) {
            if( !StoneAgePA_isHex((unsigned char)src[i + 1]) ||
                !StoneAgePA_isHex((unsigned char)src[i + 2]) ) return FALSE;
            c = StoneAgePA_hex((unsigned char)src[i + 1]) * 16 +
                StoneAgePA_hex((unsigned char)src[i + 2]);
            i += 3;
        } else {
            c = (unsigned char)src[i++];
        }
        if( c == 0 || out + 1 >= dstlen ) return FALSE;
        dst[out++] = (char)c;
    }
    dst[out] = '\0';
    return TRUE;
}

static int StoneAgePA_writeEncoded( StoneAgePAResponse *response,
                                    const char *key, const char *value )
{
    static const char hex[] = "0123456789ABCDEF";
    size_t i;
    int c;
    int n;
    if( response == NULL || response->fp == NULL || response->failed ) return FALSE;
    n = fprintf( response->fp, "%s=", key );
    if( n < 0 ) {
        response->failed = TRUE;
        return FALSE;
    }
    response->length += (size_t)n;
    for( i = 0; value[i] != '\0'; i++ ) {
        c = (unsigned char)value[i];
        if( (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
            (c >= '0' && c <= '9') || c == '-' || c == '_' ||
            c == '.' || c == '~' ) {
            if( fputc(c, response->fp) == EOF ) {
                response->failed = TRUE;
                return FALSE;
            }
            response->length++;
        } else {
            if( fprintf(response->fp, "%%%c%c", hex[(c >> 4) & 15],
                        hex[c & 15]) < 0 ) {
                response->failed = TRUE;
                return FALSE;
            }
            response->length += 3;
        }
        if( response->length > STONEAGE_PA_MAX_RESPONSE ) {
            response->failed = TRUE;
            return FALSE;
        }
    }
    if( fputc('\n', response->fp) == EOF ) {
        response->failed = TRUE;
        return FALSE;
    }
    response->length++;
    return TRUE;
}

static int StoneAgePA_writeInt( StoneAgePAResponse *response,
                                const char *key, int value )
{
    char text[32];
    snprintf( text, sizeof(text), "%d", value );
    return StoneAgePA_writeEncoded( response, key, text );
}

static int StoneAgePA_validID( const char *id )
{
    size_t i;
    if( id == NULL || id[0] == '\0' || strlen(id) > STONEAGE_PA_MAX_ID ) return FALSE;
    for( i = 0; id[i] != '\0'; i++ ) {
        if( !((id[i] >= 'a' && id[i] <= 'z') ||
              (id[i] >= 'A' && id[i] <= 'Z') ||
              (id[i] >= '0' && id[i] <= '9') || id[i] == '-' ||
              id[i] == '_' || id[i] == '.') ) return FALSE;
    }
    return TRUE;
}

static int StoneAgePA_validKey( const char *key )
{
    size_t i;
    if( key == NULL || key[0] == '\0' || strlen(key) >= 64 ) return FALSE;
    for( i = 0; key[i] != '\0'; i++ ) {
        if( !((key[i] >= 'a' && key[i] <= 'z') ||
              (key[i] >= 'A' && key[i] <= 'Z') ||
              (key[i] >= '0' && key[i] <= '9') || key[i] == '_' ||
              key[i] == '.') ) return FALSE;
    }
    return TRUE;
}

static int StoneAgePA_parseInt( const char *text, int *value )
{
    char *end;
    long parsed;
    if( text == NULL || text[0] == '\0' ) return FALSE;
    errno = 0;
    parsed = strtol( text, &end, 10 );
    if( errno != 0 || *end != '\0' || parsed < INT_MIN || parsed > INT_MAX ) return FALSE;
    *value = (int)parsed;
    return TRUE;
}

static int StoneAgePA_getField( const StoneAgePARequest *request,
                                const char *key, char *value, size_t value_len )
{
    int i;
    if( request == NULL || key == NULL ) return FALSE;
    for( i = 0; i < request->field_count; i++ ) {
        if( strcmp(request->fields[i].key, key) == 0 ) {
            if( value != NULL ) {
                strncpy( value, request->fields[i].value, value_len );
                value[value_len - 1] = '\0';
            }
            return TRUE;
        }
    }
    return FALSE;
}

static int StoneAgePA_requiredString( const StoneAgePARequest *request,
                                      const char *key, char *value,
                                      size_t value_len )
{
    if( !StoneAgePA_getField(request, key, value, value_len) || value[0] == '\0' ) return FALSE;
    return TRUE;
}

static int StoneAgePA_parseRequest( char *data, size_t length,
                                    const char *filename_id,
                                    StoneAgePARequest *request )
{
    char *line;
    char *cursor;
    char *equals;
    char encoded[STONEAGE_PA_MAX_VALUE];
    int i;
    int value;
    memset( request, 0, sizeof(*request) );
    request->protocol = -1;
    request->character_slot = -1;
    request->slot = -1;
    request->expected_sequence = -1;
    request->expected_item_id = -1;
    request->expected_pet_id = -1;
    request->template_id = -1;
    request->item_id = -1;
    request->quantity = 1;
    request->skill_slot = -1;
    request->skill_id = -1;
    if( length == 0 || length > STONEAGE_PA_MAX_REQUEST || data[length] != '\0' ) return FALSE;
    if( !StoneAgePA_validID(filename_id) ) return FALSE;
    strncpy( request->id, filename_id, sizeof(request->id) );
    cursor = data;
    while( cursor < data + length ) {
        line = cursor;
        while( cursor < data + length && *cursor != '\n' ) cursor++;
        if( cursor == line ) {
            if( cursor < data + length ) {
                cursor++;
                continue;
            }
            break;
        }
        if( cursor >= data + length ) return FALSE;
        *cursor++ = '\0';
        if( line[0] != '\0' && line[strlen(line) - 1] == '\r' ) line[strlen(line) - 1] = '\0';
        equals = strchr( line, '=' );
        if( equals == NULL ) return FALSE;
        *equals++ = '\0';
        if( !StoneAgePA_validKey(line) || strlen(equals) >= sizeof(encoded) ) return FALSE;
        for( i = 0; i < request->field_count; i++ ) {
            if( strcmp(request->fields[i].key, line) == 0 ) return FALSE;
        }
        if( request->field_count >= STONEAGE_PA_MAX_FIELDS ) return FALSE;
        if( !StoneAgePA_decode(equals, encoded, sizeof(encoded)) ) return FALSE;
        strncpy(request->fields[request->field_count].key, line,
                sizeof(request->fields[request->field_count].key));
        strncpy(request->fields[request->field_count].value, encoded,
                sizeof(request->fields[request->field_count].value));
        request->field_count++;
    }
    if( !StoneAgePA_requiredString(request, "id", encoded, sizeof(encoded)) ||
        strcmp(encoded, request->id) != 0 ) return FALSE;
    if( !StoneAgePA_requiredString(request, "action", request->action,
                                   sizeof(request->action)) ) return FALSE;
    if( StoneAgePA_getField(request, "protocol", encoded, sizeof(encoded)) ) {
        if( !StoneAgePA_parseInt(encoded, &request->protocol) ) return FALSE;
    }
    if( request->protocol != STONEAGE_PA_VERSION ) return FALSE;
    if( strcmp(request->action, "export_item") == 0 ||
        strcmp(request->action, "export_pet") == 0 ) {
        if( !StoneAgePA_getField(request, "template_id", encoded, sizeof(encoded)) ||
            !StoneAgePA_parseInt(encoded, &request->template_id) ||
            request->template_id < 0 ) return FALSE;
        request->has_template_id = TRUE;
        return TRUE;
    }
    if( strcmp(request->action, "inspect_item") == 0 ) {
        if( !StoneAgePA_getField(request, "payload", request->payload,
                                 sizeof(request->payload)) ||
            request->payload[0] == '\0' ) return FALSE;
        request->has_payload = TRUE;
        return TRUE;
    }
    if( !StoneAgePA_requiredString(request, "account", request->account,
                                   sizeof(request->account)) ||
        !StoneAgePA_requiredString(request, "character", request->character,
                                    sizeof(request->character)) ) return FALSE;
    if( !StoneAgePA_getField(request, "character_slot", encoded, sizeof(encoded)) ||
        !StoneAgePA_parseInt(encoded, &request->character_slot) ||
        request->character_slot < 0 ||
        request->character_slot >= STONEAGE_PA_MAX_CHARACTER_SLOTS ) return FALSE;
    request->has_character_slot = TRUE;
    if( StoneAgePA_getField(request, "slot", encoded, sizeof(encoded)) ) {
        if( !StoneAgePA_parseInt(encoded, &request->slot) ) return FALSE;
        request->has_slot = TRUE;
    }
    if( StoneAgePA_getField(request, "expected_sequence", encoded, sizeof(encoded)) ) {
        if( !StoneAgePA_parseInt(encoded, &request->expected_sequence) ) return FALSE;
        request->has_expected_sequence = TRUE;
    }
    if( StoneAgePA_getField(request, "expected_revision", request->expected_revision,
                            sizeof(request->expected_revision)) ) {
        if( request->expected_revision[0] == '\0' ||
            strlen(request->expected_revision) >= sizeof(request->expected_revision) ) return FALSE;
        request->has_expected_revision = TRUE;
    }
    if( StoneAgePA_getField(request, "expected_item_id", encoded, sizeof(encoded)) ) {
        if( !StoneAgePA_parseInt(encoded, &request->expected_item_id) ) return FALSE;
        request->has_expected_item_id = TRUE;
    }
    if( StoneAgePA_getField(request, "expected_pet_id", encoded, sizeof(encoded)) ) {
        if( !StoneAgePA_parseInt(encoded, &request->expected_pet_id) ) return FALSE;
        request->has_expected_pet_id = TRUE;
    }
    if( StoneAgePA_getField(request, "location", request->location,
                            sizeof(request->location)) ) {
        if( strcmp(request->location, "inventory") != 0 &&
            strcmp(request->location, "warehouse") != 0 ) return FALSE;
    } else {
        strcpy(request->location, "inventory");
    }
    if( StoneAgePA_getField(request, "template_id", encoded, sizeof(encoded)) ) {
        if( !StoneAgePA_parseInt(encoded, &request->template_id) ) return FALSE;
        request->has_template_id = TRUE;
    }
    if( StoneAgePA_getField(request, "item_id", encoded, sizeof(encoded)) ) {
        if( !StoneAgePA_parseInt(encoded, &request->item_id) ) return FALSE;
        request->has_item_id = TRUE;
    }
    if( StoneAgePA_getField(request, "quantity", encoded, sizeof(encoded)) ) {
        if( !StoneAgePA_parseInt(encoded, &request->quantity) ) return FALSE;
        request->has_quantity = TRUE;
    }
    if( StoneAgePA_getField(request, "field", request->field, sizeof(request->field)) ) {
        request->has_field = TRUE;
    }
    if( StoneAgePA_getField(request, "value", encoded, sizeof(encoded)) ) {
        if( !StoneAgePA_parseInt(encoded, &request->value) ) return FALSE;
        request->has_value = TRUE;
    }
    if( StoneAgePA_getField(request, "name", request->name, sizeof(request->name)) ) {
        request->has_name = TRUE;
    }
    if( StoneAgePA_getField(request, "payload", request->payload,
                            sizeof(request->payload)) ) {
        request->has_payload = TRUE;
    }
    if( StoneAgePA_getField(request, "skill_slot", encoded, sizeof(encoded)) ) {
        if( !StoneAgePA_parseInt(encoded, &request->skill_slot) ) return FALSE;
        request->has_skill_slot = TRUE;
    }
    if( StoneAgePA_getField(request, "skill_id", encoded, sizeof(encoded)) ) {
        if( !StoneAgePA_parseInt(encoded, &request->skill_id) ) return FALSE;
        request->has_skill_id = TRUE;
    }
    return TRUE;
}

static int StoneAgePA_readRequest( const char *path, const char *id,
                                   StoneAgePARequest *request )
{
    FILE *fp;
    char *data;
    long size;
    size_t got;
    int ok;
    fp = fopen(path, "rb");
    if( fp == NULL ) return FALSE;
    if( fseek(fp, 0, SEEK_END) != 0 ) {
        fclose(fp);
        return FALSE;
    }
    size = ftell(fp);
    if( size <= 0 || size > STONEAGE_PA_MAX_REQUEST ) {
        fclose(fp);
        return FALSE;
    }
    rewind(fp);
    data = (char *)malloc((size_t)size + 1);
    if( data == NULL ) {
        fclose(fp);
        return FALSE;
    }
    got = fread(data, 1, (size_t)size, fp);
    fclose(fp);
    data[got] = '\0';
    ok = got == (size_t)size && memchr(data, '\0', got) == NULL &&
         StoneAgePA_parseRequest(data, got, id, request);
    free(data);
    return ok;
}

static void StoneAgePA_responseBegin( StoneAgePAResponse *response, FILE *fp,
                                      const char *id )
{
    response->fp = fp;
    response->length = 0;
    response->failed = FALSE;
    StoneAgePA_writeEncoded(response, "protocol", "1");
    StoneAgePA_writeEncoded(response, "id", id);
}

static void StoneAgePA_responseError( StoneAgePAResponse *response,
                                      const char *code, const char *message )
{
    if( response->failed ) return;
    StoneAgePA_writeInt(response, "ok", 0);
    StoneAgePA_writeEncoded(response, "code", code);
    StoneAgePA_writeEncoded(response, "message", message);
}

static void StoneAgePA_hashSerialized( const char *serialized, char *hash_text,
                                       size_t hash_len )
{
    unsigned long long hash = 14695981039346656037ULL;
    unsigned long long prime = 1099511628211ULL;
    size_t i;
    if( serialized == NULL ) serialized = "";
    for( i = 0; serialized[i] != '\0'; i++ ) {
        hash ^= (unsigned long long)(unsigned char)serialized[i];
        hash *= prime;
    }
    snprintf(hash_text, hash_len, "%016llx", hash);
}

/* CHAR_makeStringFromCharIndex returns a process-global buffer. Copy it before
 * the save call so the acknowledgement identifies the exact bytes that were
 * handed to the native serializer, even if another serializer reuses that
 * buffer during the save path. */
static int StoneAgePA_copySerialized( int charaindex, char **copy )
{
    char *serialized;
    size_t length;
    *copy = NULL;
    serialized = CHAR_makeStringFromCharIndex(charaindex);
    if( serialized == NULL || serialized[0] == '\0' ) return FALSE;
    length = strlen(serialized);
    *copy = (char *)malloc(length + 1);
    if( *copy == NULL ) return FALSE;
    memcpy(*copy, serialized, length + 1);
    return TRUE;
}

static void StoneAgePA_makeRevision( int charaindex, char *revision,
                                     size_t revision_len )
{
    char *serialized;
    if( !StoneAgePA_copySerialized(charaindex, &serialized) ) {
        StoneAgePA_hashSerialized("", revision, revision_len);
        return;
    }
    StoneAgePA_hashSerialized(serialized, revision, revision_len);
    free(serialized);
}

static void StoneAgePA_responseOK( StoneAgePAResponse *response,
                                   const StoneAgePARequest *request,
                                   int charaindex )
{
    char revision[32];
    StoneAgePA_writeInt(response, "ok", 1);
    StoneAgePA_writeEncoded(response, "code", "ok");
    StoneAgePA_writeInt(response, "character_slot", request->character_slot);
    StoneAgePA_writeInt(response, "sequence", CHAR_getCharMakeSequenceNumber(charaindex));
    StoneAgePA_makeRevision(charaindex, revision, sizeof(revision));
    StoneAgePA_writeEncoded(response, "revision", revision);
}

static void StoneAgePA_responseSimpleOK( StoneAgePAResponse *response,
                                         const StoneAgePARequest *request )
{
    StoneAgePA_writeInt(response, "ok", 1);
    StoneAgePA_writeEncoded(response, "code", "ok");
}

static int StoneAgePA_exportTemplate( StoneAgePAResponse *response,
                                      const StoneAgePARequest *request )
{
    int index;
    int array;
    char *serialized;
    if( strcmp(request->action, "export_item") == 0 ) {
        index = ITEM_makeItemAndRegist(request->template_id);
        if( index < 0 ) {
            StoneAgePA_responseError(response, "invalid_template", "item template does not exist");
            return FALSE;
        }
        serialized = ITEM_makeStringFromItemIndex(index, 0);
        if( serialized == NULL || serialized[0] == '\0' ) {
            ITEM_endExistItemsOne(index);
            StoneAgePA_responseError(response, "export_failed", "item template could not be serialized");
            return FALSE;
        }
        StoneAgePA_writeEncoded(response, "kind", "item");
        StoneAgePA_writeInt(response, "template_id", request->template_id);
        StoneAgePA_writeEncoded(response, "payload", serialized);
        ITEM_endExistItemsOne(index);
    } else {
        array = ENEMY_getEnemyArrayFromTempNo(request->template_id);
        if( array < 0 ) {
            StoneAgePA_responseError(response, "invalid_template", "pet template does not exist");
            return FALSE;
        }
#ifdef _TEST_DROPITEMS
        index = ENEMY_TEST_createPetIndex(array);
#else
        index = -1;
#endif
        if( index < 0 ) {
            StoneAgePA_responseError(response, "export_failed", "pet template could not be serialized");
            return FALSE;
        }
        serialized = CHAR_makePetStringFromPetIndex(index);
        if( serialized == NULL || serialized[0] == '\0' ) {
            CHAR_endCharOneArray(index);
            StoneAgePA_responseError(response, "export_failed", "pet template could not be serialized");
            return FALSE;
        }
        StoneAgePA_writeEncoded(response, "kind", "pet");
        StoneAgePA_writeInt(response, "template_id", request->template_id);
        StoneAgePA_writeEncoded(response, "payload", serialized);
        CHAR_endCharOneArray(index);
    }
    StoneAgePA_responseSimpleOK(response, request);
    return TRUE;
}

static int StoneAgePA_inspectItem( StoneAgePAResponse *response,
                                   const StoneAgePARequest *request )
{
    ITEM_Item item;
    int index;
    memset(&item, 0, sizeof(item));
    if( !request->has_payload ||
        !ITEM_makeExistItemsFromStringToArg(request->payload, &item, 0) ) {
        StoneAgePA_responseError(response, "invalid_payload", "item payload could not be decoded");
        return FALSE;
    }
    index = ITEM_initExistItemsOne(&item);
    if( index < 0 || !ITEM_CHECKINDEX(index) ) {
        StoneAgePA_responseError(response, "inspect_failed", "item payload could not be registered");
        return FALSE;
    }
    StoneAgePA_writeEncoded(response, "kind", "item");
    StoneAgePA_writeInt(response, "template_id", ITEM_getInt(index, ITEM_ID));
    StoneAgePA_writeItemObject(response, index, "item.inspected");
    StoneAgePA_responseSimpleOK(response, request);
    ITEM_endExistItemsOne(index);
    return TRUE;
}

static int StoneAgePA_findTarget( const StoneAgePARequest *request )
{
    int i;
    int found = -1;
    int players = CHAR_getPlayerMaxNum();
    for( i = 0; i < players; i++ ) {
        if( !CHAR_getCharUse(i) ) continue;
        if( CHAR_getInt(i, CHAR_WHICHTYPE) != CHAR_TYPEPLAYER ) continue;
        if( strcmp(CHAR_getChar(i, CHAR_CDKEY), request->account) != 0 ) continue;
        if( strcmp(CHAR_getChar(i, CHAR_NAME), request->character) != 0 ) continue;
        if( CHAR_getInt(i, CHAR_SAVEINDEXNUMBER) != request->character_slot ) continue;
        if( found != -1 ) return -2;
        found = i;
    }
    return found;
}

static int StoneAgePA_checkTarget( const StoneAgePARequest *request,
                                   int charaindex, StoneAgePAResponse *response,
                                   int mutate )
{
    if( charaindex == -2 ) {
        StoneAgePA_responseError(response, "ambiguous_target", "more than one online character matched");
        return FALSE;
    }
    if( charaindex < 0 ) {
        StoneAgePA_responseError(response, "target_not_online", "character is not online");
        return FALSE;
    }
    if( mutate && !request->has_expected_revision ) {
        StoneAgePA_responseError(response, "expected_revision_required", "mutation requires snapshot revision");
        return FALSE;
    }
    if( mutate ) {
        char revision[32];
        StoneAgePA_makeRevision(charaindex, revision, sizeof(revision));
        if( strcmp(request->expected_revision, revision) != 0 ||
            (request->has_expected_sequence &&
             request->expected_sequence != CHAR_getCharMakeSequenceNumber(charaindex)) ) {
            StoneAgePA_responseError(response, "stale_target", "online character changed; refresh snapshot");
            return FALSE;
        }
    }
    if( mutate && CHAR_getWorkInt(charaindex, CHAR_WORKBATTLEMODE) != BATTLE_CHARMODE_NONE ) {
        StoneAgePA_responseError(response, "busy_battle", "character is in battle");
        return FALSE;
    }
    if( mutate && CHAR_getWorkInt(charaindex, CHAR_WORKTRADEMODE) != CHAR_TRADE_FREE ) {
        StoneAgePA_responseError(response, "busy_trade", "character is trading");
        return FALSE;
    }
    if( mutate && CHAR_getWorkInt(charaindex, CHAR_WORKFD) < 0 ) {
        StoneAgePA_responseError(response, "not_connected", "character has no client connection");
        return FALSE;
    }
    return TRUE;
}

static int StoneAgePA_characterAttr( const char *field, CHAR_DATAINT *element )
{
    if( strcmp(field, "gld") == 0 ) *element = CHAR_GOLD;
    else if( strcmp(field, "bankgld") == 0 ) *element = CHAR_BANKGOLD;
#ifdef _GAMBLE_BANK
    else if( strcmp(field, "personaglod") == 0 ) *element = CHAR_PERSONAGOLD;
#endif
    else if( strcmp(field, "lv") == 0 ) *element = CHAR_LV;
    else if( strcmp(field, "nexp") == 0 ) *element = CHAR_EXP;
    else if( strcmp(field, "hp") == 0 ) *element = CHAR_HP;
    else if( strcmp(field, "mp") == 0 ) *element = CHAR_MP;
    else if( strcmp(field, "mmp") == 0 ) *element = CHAR_MAXMP;
    else if( strcmp(field, "vi") == 0 ) *element = CHAR_VITAL;
    else if( strcmp(field, "str") == 0 ) *element = CHAR_STR;
    else if( strcmp(field, "tou") == 0 ) *element = CHAR_TOUGH;
    else if( strcmp(field, "dx") == 0 ) *element = CHAR_DEX;
    else if( strcmp(field, "lvup") == 0 ) *element = CHAR_LEVELUPPOINT;
    else if( strcmp(field, "skup") == 0 ) *element = CHAR_SKILLUPPOINT;
    else if( strcmp(field, "chr") == 0 ) *element = CHAR_CHARM;
    else if( strcmp(field, "luc") == 0 ) *element = CHAR_LUCK;
    else if( strcmp(field, "aea") == 0 ) *element = CHAR_EARTHAT;
    else if( strcmp(field, "awa") == 0 ) *element = CHAR_WATERAT;
    else if( strcmp(field, "afi") == 0 ) *element = CHAR_FIREAT;
    else if( strcmp(field, "awi") == 0 ) *element = CHAR_WINDAT;
    else if( strcmp(field, "trn") == 0 ) *element = CHAR_TRANSMIGRATION;
    else if( strcmp(field, "duel") == 0 ) *element = CHAR_DUELPOINT;
#ifdef _PERSONAL_FAME
    else if( strcmp(field, "fame") == 0 ) *element = CHAR_FAME;
#endif
#ifdef _VIP_SERVER
    else if( strcmp(field, "memberpoint") == 0 ) *element = CHAR_AMPOINT;
#endif
    else return FALSE;
    return TRUE;
}

static int StoneAgePA_characterAttrValue( int index, const char *field,
                                          int *value )
{
    CHAR_DATAINT element;
    if( !StoneAgePA_characterAttr(field, &element) ) return FALSE;
    *value = CHAR_getInt(index, element);
    return TRUE;
}

static int StoneAgePA_itemField( const char *field, ITEM_DATAINT *element )
{
    if( strcmp(field, "id") == 0 ) *element = ITEM_ID;
    else if( strcmp(field, "bi") == 0 ) *element = ITEM_BASEIMAGENUMBER;
    else if( strcmp(field, "cs") == 0 ) *element = ITEM_COST;
    else if( strcmp(field, "ep") == 0 ) *element = ITEM_TYPE;
    else if( strcmp(field, "ft") == 0 ) *element = ITEM_ABLEUSEFIELD;
    else if( strcmp(field, "tg") == 0 ) *element = ITEM_TARGET;
    else if( strcmp(field, "lv") == 0 ) *element = ITEM_LEVEL;
    else if( strcmp(field, "upin") == 0 || strcmp(field, "quantity") == 0 ) *element = ITEM_USEPILENUMS;
    else if( strcmp(field, "dmce") == 0 ) *element = ITEM_DAMAGECRUSHE;
    else if( strcmp(field, "mdmce") == 0 ) *element = ITEM_MAXDAMAGECRUSHE;
    else if( strcmp(field, "otdmag") == 0 ) *element = ITEM_OTHERDAMAGE;
    else if( strcmp(field, "otdefc") == 0 ) *element = ITEM_OTHERDEFC;
    else if( strcmp(field, "nsuit") == 0 ) *element = ITEM_SUITCODE;
    else if( strcmp(field, "ann") == 0 ) *element = ITEM_ATTACKNUM_MIN;
    else if( strcmp(field, "anx") == 0 ) *element = ITEM_ATTACKNUM_MAX;
    else if( strcmp(field, "ma") == 0 ) *element = ITEM_MODIFYATTACK;
    else if( strcmp(field, "md") == 0 ) *element = ITEM_MODIFYDEFENCE;
    else if( strcmp(field, "mh") == 0 ) *element = ITEM_MODIFYQUICK;
    else if( strcmp(field, "mm") == 0 ) *element = ITEM_MODIFYHP;
    else if( strcmp(field, "mq") == 0 ) *element = ITEM_MODIFYMP;
    else if( strcmp(field, "ml") == 0 ) *element = ITEM_MODIFYLUCK;
    else if( strcmp(field, "mc") == 0 ) *element = ITEM_MODIFYCHARM;
    else if( strcmp(field, "mv") == 0 ) *element = ITEM_MODIFYAVOID;
    else if( strcmp(field, "mat") == 0 ) *element = ITEM_MODIFYATTRIB;
    else if( strcmp(field, "mav") == 0 ) *element = ITEM_MODIFYATTRIBVALUE;
    else if( strcmp(field, "mid") == 0 ) *element = ITEM_MAGICID;
    else if( strcmp(field, "mpr") == 0 ) *element = ITEM_MAGICPROB;
    else if( strcmp(field, "mu") == 0 ) *element = ITEM_MAGICUSEMP;
    else if( strcmp(field, "arr") == 0 ) *element = ITEM_MODIFYARRANGE;
    else if( strcmp(field, "seqce") == 0 ) *element = ITEM_MODIFYSEQUENCE;
    else if( strcmp(field, "iapi") == 0 ) *element = ITEM_ATTACHPILE;
    else if( strcmp(field, "hirt") == 0 ) *element = ITEM_HITRIGHT;
    else if( strcmp(field, "neguard") == 0 ) *element = ITEM_NEGLECTGUARD;
    else if( strcmp(field, "poison") == 0 ) *element = ITEM_POISON;
    else if( strcmp(field, "paralysis") == 0 ) *element = ITEM_PARALYSIS;
    else if( strcmp(field, "sleep") == 0 ) *element = ITEM_SLEEP;
    else if( strcmp(field, "stone") == 0 ) *element = ITEM_STONE;
    else if( strcmp(field, "drunk") == 0 ) *element = ITEM_DRUNK;
    else if( strcmp(field, "confusion") == 0 ) *element = ITEM_CONFUSION;
    else if( strcmp(field, "critical") == 0 ) *element = ITEM_CRITICAL;
    else if( strcmp(field, "useaction") == 0 ) *element = ITEM_USEACTION;
    else if( strcmp(field, "dropatlogout") == 0 ) *element = ITEM_DROPATLOGOUT;
    else if( strcmp(field, "vanishatdrop") == 0 ) *element = ITEM_VANISHATDROP;
    else if( strcmp(field, "isovered") == 0 ) *element = ITEM_ISOVERED;
    else if( strcmp(field, "canpetmail") == 0 ) *element = ITEM_CANPETMAIL;
    else if( strcmp(field, "canmergefrom") == 0 ) *element = ITEM_CANMERGEFROM;
    else if( strcmp(field, "canmergeto") == 0 ) *element = ITEM_CANMERGETO;
    else if( strcmp(field, "ingvalue0") == 0 ) *element = ITEM_INGVALUE0;
    else if( strcmp(field, "ingvalue1") == 0 ) *element = ITEM_INGVALUE1;
    else if( strcmp(field, "ingvalue2") == 0 ) *element = ITEM_INGVALUE2;
    else if( strcmp(field, "ingvalue3") == 0 ) *element = ITEM_INGVALUE3;
    else if( strcmp(field, "ingvalue4") == 0 ) *element = ITEM_INGVALUE4;
    else if( strcmp(field, "puttime") == 0 ) *element = ITEM_PUTTIME;
    else if( strcmp(field, "leaklevel") == 0 ) *element = ITEM_LEAKLEVEL;
    else if( strcmp(field, "mergeflg") == 0 ) *element = ITEM_MERGEFLG;
    else if( strcmp(field, "crushlevel") == 0 ) *element = ITEM_CRUSHLEVEL;
    else if( strcmp(field, "var1") == 0 ) *element = ITEM_VAR1;
    else if( strcmp(field, "var2") == 0 ) *element = ITEM_VAR2;
    else if( strcmp(field, "var3") == 0 ) *element = ITEM_VAR3;
    else if( strcmp(field, "var4") == 0 ) *element = ITEM_VAR4;
    else return FALSE;
    return TRUE;
}

static int StoneAgePA_validateItemFieldValue( const char *field, int value )
{
    if( strcmp(field, "dmce") == 0 || strcmp(field, "mdmce") == 0 ||
        strcmp(field, "cs") == 0 ) {
        return value >= 0 && value <= 100000000;
    }
    if( strcmp(field, "upin") == 0 || strcmp(field, "quantity") == 0 ) {
        return value >= 1 && value <= 9999;
    }
    if( strcmp(field, "lv") == 0 ) return value >= 0 && value <= CHAR_MAXUPLEVEL;
    if( strcmp(field, "ma") == 0 || strcmp(field, "md") == 0 ||
        strcmp(field, "mh") == 0 || strcmp(field, "mm") == 0 ||
        strcmp(field, "mq") == 0 || strcmp(field, "ml") == 0 ||
        strcmp(field, "mc") == 0 || strcmp(field, "mv") == 0 ) {
        return value >= -100000000 && value <= 100000000;
    }
    if( strcmp(field, "ann") == 0 || strcmp(field, "anx") == 0 ) {
        return value >= 1 && value <= 20;
    }
    if( strcmp(field, "mat") == 0 ) return value >= 0 && value <= 4;
    if( strcmp(field, "mav") == 0 || strcmp(field, "mpr") == 0 ) {
        return value >= 0 && value <= 100;
    }
    if( strcmp(field, "mid") == 0 ) return value >= -1 && value <= 100000000;
    if( strcmp(field, "mu") == 0 ) return value >= 0 && value <= 100000000;
    return FALSE;
}

static int StoneAgePA_petField( const char *field, CHAR_DATAINT *element )
{
    if( strcmp(field, "lv") == 0 ) *element = CHAR_LV;
    else if( strcmp(field, "nexp") == 0 ) *element = CHAR_EXP;
    else if( strcmp(field, "hp") == 0 ) *element = CHAR_HP;
    else if( strcmp(field, "mp") == 0 ) *element = CHAR_MP;
    else if( strcmp(field, "mmp") == 0 ) *element = CHAR_MAXMP;
    else if( strcmp(field, "vi") == 0 ) *element = CHAR_VITAL;
    else if( strcmp(field, "str") == 0 ) *element = CHAR_STR;
    else if( strcmp(field, "tou") == 0 ) *element = CHAR_TOUGH;
    else if( strcmp(field, "dx") == 0 ) *element = CHAR_DEX;
    else if( strcmp(field, "chr") == 0 ) *element = CHAR_MODAI;
    else if( strcmp(field, "luc") == 0 ) *element = CHAR_VARIABLEAI;
    else if( strcmp(field, "aea") == 0 ) *element = CHAR_EARTHAT;
    else if( strcmp(field, "awa") == 0 ) *element = CHAR_WATERAT;
    else if( strcmp(field, "afi") == 0 ) *element = CHAR_FIREAT;
    else if( strcmp(field, "awi") == 0 ) *element = CHAR_WINDAT;
    else if( strcmp(field, "trn") == 0 ) *element = CHAR_TRANSMIGRATION;
    else if( strcmp(field, "slt") == 0 ) *element = CHAR_SLOT;
    else if( strcmp(field, "llt") == 0 ) *element = CHAR_PETRANK;
    else if( strcmp(field, "lvup") == 0 ) *element = CHAR_ALLOCPOINT;
    else return FALSE;
    return TRUE;
}

static int StoneAgePA_petGrowthShift( const char *field )
{
    if( strcmp(field, "growth_vi") == 0 ) return 24;
    if( strcmp(field, "growth_str") == 0 ) return 16;
    if( strcmp(field, "growth_tou") == 0 ) return 8;
    if( strcmp(field, "growth_dx") == 0 ) return 0;
    return -1;
}

static int StoneAgePA_validateCharacterValue( const char *field, int value )
{
    if( strcmp(field, "gld") == 0 ) return value >= 0 && value <= CHAR_MAXGOLDHAVE;
    if( strcmp(field, "bankgld") == 0 ) return value >= 0 && value <= CHAR_MAXBANKGOLDHAVE;
#ifdef _GAMBLE_BANK
    if( strcmp(field, "personaglod") == 0 ) return value >= 0 && value <= CHAR_MAXPERSONAGOLD;
#endif
    if( strcmp(field, "lv") == 0 ) return value >= 1 && value <= CHAR_MAXUPLEVEL;
    if( strcmp(field, "nexp") == 0 || strcmp(field, "hp") == 0 ||
        strcmp(field, "mp") == 0 || strcmp(field, "mmp") == 0 ||
        strcmp(field, "vi") == 0 || strcmp(field, "str") == 0 ||
        strcmp(field, "tou") == 0 || strcmp(field, "dx") == 0 ||
        strcmp(field, "lvup") == 0 || strcmp(field, "skup") == 0 ||
        strcmp(field, "trn") == 0 || strcmp(field, "memberpoint") == 0 ) {
        return value >= 0;
    }
    if( strcmp(field, "chr") == 0 || strcmp(field, "luc") == 0 ||
        strcmp(field, "aea") == 0 || strcmp(field, "awa") == 0 ||
        strcmp(field, "afi") == 0 || strcmp(field, "awi") == 0 ) {
        return value >= 0 && value <= CHAR_MAXATTRIB;
    }
    if( strcmp(field, "duel") == 0 ) return value >= 0 && value <= CHAR_MAXDUELPOINT;
#ifdef _PERSONAL_FAME
    if( strcmp(field, "fame") == 0 ) return value >= 0 && value <= MAX_PERSONALFAME;
#endif
    return FALSE;
}

static int StoneAgePA_validatePetValue( const char *field, int value )
{
    if( strcmp(field, "lv") == 0 ) return value >= 1 && value <= CHAR_MAXUPLEVEL;
    if( strcmp(field, "nexp") == 0 || strcmp(field, "hp") == 0 ||
        strcmp(field, "mp") == 0 || strcmp(field, "mmp") == 0 ||
        strcmp(field, "vi") == 0 || strcmp(field, "str") == 0 ||
        strcmp(field, "tou") == 0 || strcmp(field, "dx") == 0 ||
        strcmp(field, "trn") == 0 || strcmp(field, "lvup") == 0 ) {
        return value >= 0;
    }
    if( strcmp(field, "chr") == 0 ) {
        return value >= STONEAGE_PA_MIN_PET_MODAI &&
               value <= STONEAGE_PA_MAX_PET_MODAI;
    }
    if( strcmp(field, "luc") == 0 ) {
        return value >= CHAR_MINVARIABLEAI && value <= CHAR_MAXVARIABLEAI;
    }
    if( strcmp(field, "aea") == 0 || strcmp(field, "awa") == 0 ||
        strcmp(field, "afi") == 0 || strcmp(field, "awi") == 0 ) {
        return value >= 0 && value <= CHAR_MAXATTRIB;
    }
    if( strcmp(field, "slt") == 0 ) return value >= 1 && value <= CHAR_MAXPETSKILLHAVE;
    if( strcmp(field, "llt") == 0 ) return value >= 0 && value <= 5;
    return FALSE;
}

static int StoneAgePA_poolPetLimit( int index )
{
    long limit;
    int transmigration = CHAR_getInt(index, CHAR_TRANSMIGRATION);
    if( transmigration < 0 ) return 0;
    limit = (long)transmigration * 2L + 5L;
    if( limit > CHAR_MAXPOOLPETHAVE ) limit = CHAR_MAXPOOLPETHAVE;
    return (int)limit;
}

static int StoneAgePA_petHasSkillAtOrAbove( int petindex, int slot_count )
{
    int i;
    for( i = slot_count; i < CHAR_MAXPETSKILLHAVE; i++ ) {
        if( CHAR_getPetSkill(petindex, i) >= 0 ) return TRUE;
    }
    return FALSE;
}

static void StoneAgePA_writeCharacterAttributes( StoneAgePAResponse *response,
                                                  int index )
{
    static const char *names[] = {
        "gld", "bankgld", "personaglod", "lv", "nexp", "hp", "mp", "mmp",
        "vi", "str", "tou", "dx", "lvup", "skup", "chr", "luc", "aea",
        "awa", "afi", "awi", "trn", "duel", "fame", "memberpoint"
    };
    int i;
    int value;
    for( i = 0; i < (int)(sizeof(names) / sizeof(names[0])); i++ ) {
        if( StoneAgePA_characterAttrValue(index, names[i], &value) ) {
            char key[64];
            snprintf(key, sizeof(key), "attribute.%s", names[i]);
            StoneAgePA_writeInt(response, key, value);
        }
    }
}

static void StoneAgePA_writeItemObject( StoneAgePAResponse *response,
                                        int itemindex, const char *prefix )
{
    char key[192];
    char *name;
    int field_value;
    int item_fields[] = {
        ITEM_ID, ITEM_BASEIMAGENUMBER, ITEM_COST, ITEM_TYPE, ITEM_ABLEUSEFIELD,
        ITEM_TARGET, ITEM_LEVEL, ITEM_USEPILENUMS, ITEM_CANBEPILE, ITEM_ATTACKNUM_MIN,
        ITEM_ATTACKNUM_MAX, ITEM_MODIFYATTACK, ITEM_MODIFYDEFENCE,
        ITEM_MODIFYQUICK, ITEM_MODIFYHP, ITEM_MODIFYMP, ITEM_MODIFYLUCK,
        ITEM_MODIFYCHARM, ITEM_MODIFYAVOID, ITEM_MODIFYATTRIB,
        ITEM_MODIFYATTRIBVALUE, ITEM_MAGICID, ITEM_MAGICPROB, ITEM_MAGICUSEMP,
        ITEM_POISON, ITEM_PARALYSIS, ITEM_SLEEP, ITEM_STONE, ITEM_DRUNK,
        ITEM_CONFUSION, ITEM_CRITICAL, ITEM_USEACTION, ITEM_DROPATLOGOUT,
        ITEM_VANISHATDROP, ITEM_ISOVERED, ITEM_CANPETMAIL, ITEM_CANMERGEFROM,
        ITEM_CANMERGETO, ITEM_INGVALUE0, ITEM_INGVALUE1, ITEM_INGVALUE2,
        ITEM_INGVALUE3, ITEM_INGVALUE4, ITEM_PUTTIME, ITEM_LEAKLEVEL,
        ITEM_MERGEFLG, ITEM_CRUSHLEVEL, ITEM_VAR1, ITEM_VAR2, ITEM_VAR3, ITEM_VAR4
    };
    const char *field_names[] = {
        "id", "bi", "cs", "ep", "ft", "tg", "lv", "upin", "canpile", "ann", "anx",
        "ma", "md", "mh", "mm", "mq", "ml", "mc", "mv", "mat", "mav",
        "mid", "mpr", "mu", "poison", "paralysis", "sleep", "stone", "drunk",
        "confusion", "critical", "useaction", "dropatlogout", "vanishatdrop",
        "isovered", "canpetmail", "canmergefrom", "canmergeto", "ingvalue0",
        "ingvalue1", "ingvalue2", "ingvalue3", "ingvalue4", "puttime", "leaklevel",
        "mergeflg", "crushlevel", "var1", "var2", "var3", "var4"
    };
    snprintf(key, sizeof(key), "%s.id", prefix);
    StoneAgePA_writeInt(response, key, ITEM_getInt(itemindex, ITEM_ID));
    snprintf(key, sizeof(key), "%s.template_id", prefix);
    StoneAgePA_writeInt(response, key, ITEM_getInt(itemindex, ITEM_ID));
    snprintf(key, sizeof(key), "%s.graphic_id", prefix);
    StoneAgePA_writeInt(response, key, ITEM_getInt(itemindex, ITEM_BASEIMAGENUMBER));
    name = ITEM_getChar(itemindex, ITEM_NAME);
    snprintf(key, sizeof(key), "%s.name", prefix);
    StoneAgePA_writeEncoded(response, key, name == NULL ? "" : name);
    for( field_value = 0; field_value < (int)(sizeof(item_fields) / sizeof(item_fields[0])); field_value++ ) {
        snprintf(key, sizeof(key), "%s.field.%s", prefix, field_names[field_value]);
        StoneAgePA_writeInt(response, key, ITEM_getInt(itemindex, item_fields[field_value]));
    }
}

static void StoneAgePA_writeItem( StoneAgePAResponse *response, int index,
                                  const char *location, int slot )
{
    char prefix[128];
    int itemindex;
    itemindex = strcmp(location, "warehouse") == 0
        ? CHAR_getPoolItemIndex(index, slot) : CHAR_getItemIndex(index, slot);
    if( !ITEM_CHECKINDEX(itemindex) ) return;
    snprintf(prefix, sizeof(prefix), "item.%s.%d", location, slot);
    StoneAgePA_writeItemObject(response, itemindex, prefix);
}

static void StoneAgePA_writePet( StoneAgePAResponse *response, int index,
                                 const char *location, int slot )
{
    char prefix[128];
    char key[192];
    int petindex;
    int i;
    const char *name;
    static const char *attrs[] = {
        "lv", "nexp", "hp", "mp", "mmp", "vi", "str", "tou", "dx", "lvup",
        "chr", "luc", "slt", "llt", "aea", "awa", "afi", "awi"
    };
    petindex = strcmp(location, "warehouse") == 0
        ? CHAR_getCharPoolPet(index, slot) : CHAR_getCharPet(index, slot);
    if( !CHAR_CHECKINDEX(petindex) ) return;
    snprintf(prefix, sizeof(prefix), "pet.%s.%d", location, slot);
    snprintf(key, sizeof(key), "%s.id", prefix);
    StoneAgePA_writeInt(response, key, CHAR_getInt(petindex, CHAR_PETID));
    snprintf(key, sizeof(key), "%s.template_id", prefix);
    StoneAgePA_writeInt(response, key, CHAR_getInt(petindex, CHAR_PETID));
    snprintf(key, sizeof(key), "%s.graphic_id", prefix);
    StoneAgePA_writeInt(response, key, CHAR_getInt(petindex, CHAR_BASEIMAGENUMBER));
    name = CHAR_getChar(petindex, CHAR_NAME);
    snprintf(key, sizeof(key), "%s.name", prefix);
    StoneAgePA_writeEncoded(response, key, name == NULL ? "" : name);
    snprintf(key, sizeof(key), "%s.user_name", prefix);
    StoneAgePA_writeEncoded(response, key, CHAR_getChar(petindex, CHAR_USERPETNAME));
    for( i = 0; i < (int)(sizeof(attrs) / sizeof(attrs[0])); i++ ) {
        CHAR_DATAINT element;
        if( StoneAgePA_petField(attrs[i], &element) ) {
            snprintf(key, sizeof(key), "%s.field.%s", prefix, attrs[i]);
            StoneAgePA_writeInt(response, key, CHAR_getInt(petindex, element));
        }
    }
    snprintf(key, sizeof(key), "%s.field.bi", prefix);
    StoneAgePA_writeInt(response, key, CHAR_getInt(petindex, CHAR_BASEIMAGENUMBER));
    for( i = 0; i < CHAR_MAXPETSKILLHAVE; i++ ) {
        snprintf(key, sizeof(key), "%s.skill.%d", prefix, i);
        StoneAgePA_writeInt(response, key, CHAR_getPetSkill(petindex, i));
    }
}

static void StoneAgePA_snapshot( StoneAgePAResponse *response,
                                 const StoneAgePARequest *request, int index )
{
    int i;
    char key[64];
    StoneAgePA_responseOK(response, request, index);
    StoneAgePA_writeEncoded(response, "account", CHAR_getChar(index, CHAR_CDKEY));
    StoneAgePA_writeEncoded(response, "character", CHAR_getChar(index, CHAR_NAME));
    StoneAgePA_writeInt(response, "character.graphic_id", CHAR_getInt(index, CHAR_BASEIMAGENUMBER));
    StoneAgePA_writeEncoded(response, "save_status", "not_requested");
    StoneAgePA_writeCharacterAttributes(response, index);
    for( i = 0; i < CHAR_MAXITEMHAVE; i++ ) {
        if( i < CHAR_EQUIPPLACENUM || CHAR_getItemIndex(index, i) >= 0 ) {
            StoneAgePA_writeItem(response, index, "inventory", i);
        }
    }
    for( i = 0; i < CHAR_MAXPOOLITEMHAVE; i++ ) {
        if( CHAR_getPoolItemIndex(index, i) >= 0 ) {
            StoneAgePA_writeItem(response, index, "warehouse", i);
        }
    }
    for( i = 0; i < CHAR_MAXPETHAVE; i++ ) {
        if( CHAR_getCharPet(index, i) >= 0 ) {
            StoneAgePA_writePet(response, index, "inventory", i);
        }
    }
    for( i = 0; i < CHAR_MAXPOOLPETHAVE; i++ ) {
        if( CHAR_getCharPoolPet(index, i) >= 0 ) {
            StoneAgePA_writePet(response, index, "warehouse", i);
        }
    }
    snprintf(key, sizeof(key), "default_pet_slot");
    StoneAgePA_writeInt(response, key, CHAR_getInt(index, CHAR_DEFAULTPET));
}

static int StoneAgePA_save( int index, StoneAgePAResponse *response )
{
    int fd = CHAR_getWorkInt(index, CHAR_WORKFD);
    char *serialized;
    char save_hash[32];
    if( fd < 0 || acfd < 0 ) {
        StoneAgePA_responseError(response, "mutation_applied_save_failed",
                                 "mutation was applied in memory but GMSV or SAAC is disconnected");
        return FALSE;
    }
    if( !StoneAgePA_copySerialized(index, &serialized) ) {
        StoneAgePA_responseError(response, "mutation_applied_save_failed",
                                 "mutation was applied in memory but character could not be serialized");
        return FALSE;
    }
    StoneAgePA_hashSerialized(serialized, save_hash, sizeof(save_hash));
    if( fd < 0 || !CHAR_charSaveFromConnect(fd, FALSE) ) {
        free(serialized);
        StoneAgePA_responseError(response, "mutation_applied_save_failed",
                                 "mutation was applied in memory but GMSV could not queue character save");
        return FALSE;
    }
    free(serialized);
    StoneAgePA_writeEncoded(response, "save_data_hash", save_hash);
    return TRUE;
}

static int StoneAgePA_preflightSave( int index, StoneAgePAResponse *response )
{
    int fd = CHAR_getWorkInt(index, CHAR_WORKFD);
    char *serialized;
    if( fd < 0 || acfd < 0 ) {
        StoneAgePA_responseError(response, "save_unavailable",
                                 "GMSV or SAAC is disconnected; refresh and retry");
        return FALSE;
    }
    if( !StoneAgePA_copySerialized(index, &serialized) ) {
        StoneAgePA_responseError(response, "save_unavailable",
                                 "character could not be serialized; no mutation was applied");
        return FALSE;
    }
    free(serialized);
    return TRUE;
}

static int StoneAgePA_itemAt( int index, const char *location, int slot )
{
    if( strcmp(location, "warehouse") == 0 ) {
        if( slot < 0 || slot >= CHAR_MAXPOOLITEMHAVE ) return -1;
        return CHAR_getPoolItemIndex(index, slot);
    }
    if( slot < 0 || slot >= CHAR_MAXITEMHAVE ) return -1;
    return CHAR_getItemIndex(index, slot);
}

static int StoneAgePA_petAt( int index, const char *location, int slot )
{
    if( strcmp(location, "warehouse") == 0 ) {
        if( slot < 0 || slot >= CHAR_MAXPOOLPETHAVE ) return -1;
        return CHAR_getCharPoolPet(index, slot);
    }
    if( slot < 0 || slot >= CHAR_MAXPETHAVE ) return -1;
    return CHAR_getCharPet(index, slot);
}

static int StoneAgePA_collectItemSlots( int index, const char *location,
                                        const StoneAgePARequest *request,
                                        int count, int *slots )
{
    int start;
    int limit;
    int i;
    int found = 0;
    int occupied;
    if( strcmp(location, "warehouse") == 0 ) {
        start = 0;
        limit = CHAR_MAXPOOLITEMHAVE;
    } else {
        start = CHAR_STARTITEMARRAY;
        limit = CHAR_MAXITEMHAVE;
    }
    if( request->has_slot ) {
        if( request->slot < start || request->slot >= limit ) return -1;
        occupied = strcmp(location, "warehouse") == 0
            ? CHAR_getPoolItemIndex(index, request->slot)
            : CHAR_getItemIndex(index, request->slot);
        if( occupied != -1 ) return -1;
        slots[found++] = request->slot;
    }
    for( i = start; i < limit && found < count; i++ ) {
        if( request->has_slot && i == request->slot ) continue;
        occupied = strcmp(location, "warehouse") == 0
            ? CHAR_getPoolItemIndex(index, i) : CHAR_getItemIndex(index, i);
        if( occupied == -1 ) slots[found++] = i;
    }
    return found;
}

static int StoneAgePA_grantItem( StoneAgePAResponse *response,
                                 const StoneAgePARequest *request, int index )
{
    int slots[STONEAGE_PA_MAX_GRANT_QUANTITY];
    int created[STONEAGE_PA_MAX_GRANT_QUANTITY];
    int count;
    int i;
    int itemindex;
    if( !request->has_template_id || request->template_id < 0 ) {
        StoneAgePA_responseError(response, "template_required", "grant_item requires template_id");
        return FALSE;
    }
    if( request->quantity < 1 || request->quantity > STONEAGE_PA_MAX_GRANT_QUANTITY ) {
        StoneAgePA_responseError(response, "invalid_quantity", "quantity is outside the supported range");
        return FALSE;
    }
    count = StoneAgePA_collectItemSlots(index, request->location, request,
                                        request->quantity, slots);
    if( count < request->quantity ) {
        StoneAgePA_responseError(response,
                                 strcmp(request->location, "warehouse") == 0
                                     ? "warehouse_full" : "inventory_full",
                                 "not enough empty item slots");
        return FALSE;
    }
    for( i = 0; i < request->quantity; i++ ) {
        itemindex = ITEM_makeItemAndRegist(request->template_id);
        if( itemindex < 0 ) {
            while( i > 0 ) {
                i--;
                ITEM_endExistItemsOne(created[i]);
            }
            StoneAgePA_responseError(response, "invalid_template", "item template does not exist");
            return FALSE;
        }
        created[i] = itemindex;
    }
    for( i = 0; i < request->quantity; i++ ) {
        itemindex = created[i];
        if( strcmp(request->location, "warehouse") == 0 ) {
            CHAR_setPoolItemIndex(index, slots[i], itemindex);
        } else {
            CHAR_setItemIndex(index, slots[i], itemindex);
        }
        ITEM_setWorkInt(itemindex, ITEM_WORKCHARAINDEX, index);
        ITEM_setWorkInt(itemindex, ITEM_WORKOBJINDEX, -1);
    }
    StoneAgePA_writeInt(response, "slot", slots[0]);
    StoneAgePA_writeInt(response, "item_id", ITEM_getInt(created[0], ITEM_ID));
    StoneAgePA_writeInt(response, "quantity", request->quantity);
    return TRUE;
}

static int StoneAgePA_deleteItem( StoneAgePAResponse *response,
                                  const StoneAgePARequest *request, int index )
{
    int itemindex = StoneAgePA_itemAt(index, request->location, request->slot);
    int empty;
    if( itemindex < 0 || !ITEM_CHECKINDEX(itemindex) ) {
        StoneAgePA_responseError(response, "item_not_found", "item slot is empty");
        return FALSE;
    }
    if( request->has_expected_item_id && request->expected_item_id != ITEM_getInt(itemindex, ITEM_ID) ) {
        StoneAgePA_responseError(response, "stale_item", "item changed; refresh snapshot");
        return FALSE;
    }
    if( strcmp(request->location, "inventory") == 0 && request->slot < CHAR_EQUIPPLACENUM ) {
        empty = CHAR_findEmptyItemBox(index);
        if( empty < CHAR_STARTITEMARRAY ) {
            StoneAgePA_responseError(response, "inventory_full", "unequipping this item needs an empty inventory slot");
            return FALSE;
        }
        CHAR_moveEquipItem(index, request->slot, empty);
        itemindex = CHAR_getItemIndex(index, empty);
        if( itemindex < 0 || !ITEM_CHECKINDEX(itemindex) ) {
            StoneAgePA_responseError(response, "equipped_item", "equipment could not be unequipped");
            return FALSE;
        }
        CHAR_setItemIndex(index, empty, -1);
    } else if( strcmp(request->location, "warehouse") == 0 ) {
        CHAR_setPoolItemIndex(index, request->slot, -1);
    } else {
        CHAR_setItemIndex(index, request->slot, -1);
    }
    ITEM_endExistItemsOne(itemindex);
    return TRUE;
}

static int StoneAgePA_setItem( StoneAgePAResponse *response,
                               const StoneAgePARequest *request, int index )
{
    ITEM_DATAINT element;
    int itemindex;
    if( !request->has_field || strcmp(request->field, "id") == 0 ) {
        StoneAgePA_responseError(response, "invalid_item_field", "set_item requires a mutable field and value");
        return FALSE;
    }
    itemindex = StoneAgePA_itemAt(index, request->location, request->slot);
    if( itemindex < 0 || !ITEM_CHECKINDEX(itemindex) ) {
        StoneAgePA_responseError(response, "item_not_found", "item slot is empty");
        return FALSE;
    }
    if( request->has_expected_item_id && request->expected_item_id != ITEM_getInt(itemindex, ITEM_ID) ) {
        StoneAgePA_responseError(response, "stale_item", "item changed; refresh snapshot");
        return FALSE;
    }
    if( strcmp(request->field, "name") == 0 ) {
        if( !request->has_name || request->name[0] == '\0' ||
            strlen(request->name) >= STONEAGE_PA_MAX_NATIVE_NAME ) {
            StoneAgePA_responseError(response, "invalid_item_name", "item name is empty or too long");
            return FALSE;
        }
        ITEM_setChar(itemindex, ITEM_NAME, request->name);
        return TRUE;
    }
    if( !request->has_value || !StoneAgePA_itemField(request->field, &element) ||
        !StoneAgePA_validateItemFieldValue(request->field, request->value) ) {
        StoneAgePA_responseError(response, "invalid_item_field", "item field is not supported");
        return FALSE;
    }
    if( element == ITEM_USEPILENUMS &&
        (ITEM_getInt(itemindex, ITEM_CANBEPILE) != 1 ||
         request->value < 1 || request->value > 9999) ) {
        StoneAgePA_responseError(response, "invalid_item_quantity", "only stackable items accept this quantity");
        return FALSE;
    }
    ITEM_setInt(itemindex, element, request->value);
    return TRUE;
}

static int StoneAgePA_grantPet( StoneAgePAResponse *response,
                                StoneAgePARequest *request, int index )
{
    int slots[STONEAGE_PA_MAX_GRANT_QUANTITY];
    int created[STONEAGE_PA_MAX_GRANT_QUANTITY];
    int array;
    int slot;
    int count;
    int i;
    int j;
    int petindex;
    int temporary_slot;
    int created_slot;
    int displaced;
    int pool_limit;
    request->granted_slot_count = 0;
    if( !request->has_template_id || request->template_id < 0 ) {
        StoneAgePA_responseError(response, "template_required", "grant_pet requires template_id");
        return FALSE;
    }
    if( request->quantity < 1 || request->quantity > STONEAGE_PA_MAX_GRANT_QUANTITY ) {
        StoneAgePA_responseError(response, "invalid_quantity", "quantity is outside the supported range");
        return FALSE;
    }
    if( request->has_name &&
        (request->name[0] == '\0' || strlen(request->name) >= STONEAGE_PA_MAX_NATIVE_NAME) ) {
        StoneAgePA_responseError(response, "invalid_pet_name", "pet name is empty or too long");
        return FALSE;
    }
    /* Catalog template_id is enemybase.txt's TempNo, not enemy.txt's
     * encounter ID.  The distinction matters for templates that are not
     * present in an encounter group. */
    array = ENEMY_getEnemyArrayFromTempNo(request->template_id);
    if( array < 0 ) {
        StoneAgePA_responseError(response, "invalid_template", "pet template does not exist");
        return FALSE;
    }
    if( strcmp(request->location, "warehouse") == 0 ) {
        pool_limit = StoneAgePA_poolPetLimit(index);
        count = 0;
        if( request->has_slot ) {
            if( request->slot < 0 || request->slot >= pool_limit ||
                CHAR_getCharPoolPet(index, request->slot) != -1 ) count = -1;
            else slots[count++] = request->slot;
        }
        for( slot = 0; count >= 0 && slot < pool_limit && count < request->quantity; slot++ ) {
            if( request->has_slot && slot == request->slot ) continue;
            if( CHAR_getCharPoolPet(index, slot) == -1 ) slots[count++] = slot;
        }
    } else {
        count = 0;
        if( request->has_slot ) {
            if( request->slot < 0 || request->slot >= CHAR_MAXPETHAVE ||
                CHAR_getCharPet(index, request->slot) != -1 ) count = -1;
            else slots[count++] = request->slot;
        }
        for( slot = 0; count >= 0 && slot < CHAR_MAXPETHAVE && count < request->quantity; slot++ ) {
            if( request->has_slot && slot == request->slot ) continue;
            if( CHAR_getCharPet(index, slot) == -1 ) slots[count++] = slot;
        }
    }
    if( count < request->quantity ) {
        StoneAgePA_responseError(response,
                                 strcmp(request->location, "warehouse") == 0
                                     ? "warehouse_full" : "pet_full",
                                 "not enough empty pet slots");
        return FALSE;
    }

    for( i = 0; i < request->quantity; i++ ) {
        petindex = -1;
        if( strcmp(request->location, "warehouse") == 0 ) {
            temporary_slot = CHAR_getCharPetElement(index);
            displaced = -1;
            if( temporary_slot < 0 ) {
                temporary_slot = 0;
                displaced = CHAR_getCharPet(index, temporary_slot);
                CHAR_setCharPet(index, temporary_slot, -1);
            }
            petindex = ENEMY_createPetFromEnemyIndex(index, array);
            if( petindex >= 0 ) {
                created_slot = -1;
                for( j = 0; j < CHAR_MAXPETHAVE; j++ ) {
                    if( CHAR_getCharPet(index, j) == petindex ) {
                        created_slot = j;
                        break;
                    }
                }
                if( created_slot >= 0 ) {
                    CHAR_setCharPet(index, created_slot, -1);
                    CHAR_setCharPoolPet(index, slots[i], petindex);
                }
            }
            if( displaced >= 0 ) CHAR_setCharPet(index, temporary_slot, displaced);
        } else {
            petindex = ENEMY_createPetFromEnemyIndex(index, array);
            if( petindex >= 0 ) {
                created_slot = -1;
                for( j = 0; j < CHAR_MAXPETHAVE; j++ ) {
                    if( CHAR_getCharPet(index, j) == petindex ) {
                        created_slot = j;
                        break;
                    }
                }
                if( created_slot >= 0 && created_slot != slots[i] ) {
                    CHAR_setCharPet(index, created_slot, -1);
                    CHAR_setCharPet(index, slots[i], petindex);
                }
            }
        }
        if( petindex < 0 ||
            (strcmp(request->location, "warehouse") == 0
                ? CHAR_getCharPoolPet(index, slots[i]) != petindex
                : CHAR_getCharPet(index, slots[i]) != petindex) ) {
            if( petindex >= 0 ) CHAR_endCharOneArray(petindex);
            for( j = 0; j < i; j++ ) {
                if( strcmp(request->location, "warehouse") == 0 ) {
                    CHAR_setCharPoolPet(index, slots[j], -1);
                } else {
                    CHAR_setCharPet(index, slots[j], -1);
                }
                CHAR_endCharOneArray(created[j]);
            }
            StoneAgePA_responseError(response, "pet_create_failed", "not all pets could be created");
            return FALSE;
        }
        if( request->has_name ) CHAR_setChar(petindex, CHAR_USERPETNAME, request->name);
        CHAR_complianceParameter(petindex);
        created[i] = petindex;
        request->granted_slots[i] = slots[i];
    }
    request->granted_slot_count = request->quantity;
    StoneAgePA_writeInt(response, "slot", slots[0]);
    StoneAgePA_writeInt(response, "pet_id", CHAR_getInt(created[0], CHAR_PETID));
    StoneAgePA_writeInt(response, "quantity", request->quantity);
    return TRUE;
}

static int StoneAgePA_deletePet( StoneAgePAResponse *response,
                                 const StoneAgePARequest *request, int index )
{
    int petindex = StoneAgePA_petAt(index, request->location, request->slot);
    if( petindex < 0 || !CHAR_CHECKINDEX(petindex) ||
        CHAR_getInt(petindex, CHAR_WHICHTYPE) != CHAR_TYPEPET ) {
        StoneAgePA_responseError(response, "pet_not_found", "pet slot is empty");
        return FALSE;
    }
    if( request->has_expected_pet_id && request->expected_pet_id != CHAR_getInt(petindex, CHAR_PETID) ) {
        StoneAgePA_responseError(response, "stale_pet", "pet changed; refresh snapshot");
        return FALSE;
    }
    if( CHAR_getWorkInt(index, CHAR_WORKPETFOLLOW) == petindex ) CHAR_setWorkInt(index, CHAR_WORKPETFOLLOW, -1);
    if( strcmp(request->location, "warehouse") == 0 ) {
        CHAR_setCharPoolPet(index, request->slot, -1);
    } else {
        if( CHAR_getInt(index, CHAR_DEFAULTPET) == request->slot ) CHAR_setInt(index, CHAR_DEFAULTPET, -1);
        if( CHAR_getInt(index, CHAR_RIDEPET) == request->slot ) CHAR_setInt(index, CHAR_RIDEPET, -1);
        CHAR_setCharPet(index, request->slot, -1);
    }
    CHAR_endCharOneArray(petindex);
    return TRUE;
}

static int StoneAgePA_setPet( StoneAgePAResponse *response,
                              const StoneAgePARequest *request, int index )
{
    int petindex = StoneAgePA_petAt(index, request->location, request->slot);
    CHAR_DATAINT element;
    int growth_shift;
    unsigned int packed;
    if( petindex < 0 || !CHAR_CHECKINDEX(petindex) ) {
        StoneAgePA_responseError(response, "pet_not_found", "pet slot is empty");
        return FALSE;
    }
    if( request->has_expected_pet_id && request->expected_pet_id != CHAR_getInt(petindex, CHAR_PETID) ) {
        StoneAgePA_responseError(response, "stale_pet", "pet changed; refresh snapshot");
        return FALSE;
    }
    if( request->has_field && strcmp(request->field, "name") == 0 ) {
        if( !request->has_name || request->name[0] == '\0' ||
            strlen(request->name) >= STONEAGE_PA_MAX_NATIVE_NAME ) {
            StoneAgePA_responseError(response, "invalid_pet_name", "pet name is empty or too long");
            return FALSE;
        }
        CHAR_setChar(petindex, CHAR_USERPETNAME, request->name);
        return TRUE;
    }
    if( !request->has_field || !request->has_value ) {
        StoneAgePA_responseError(response, "invalid_pet_field", "pet field is not supported");
        return FALSE;
    }
    growth_shift = StoneAgePA_petGrowthShift(request->field);
    if( growth_shift >= 0 ) {
        if( request->value < 0 || request->value > 255 ) {
            StoneAgePA_responseError(response, "invalid_pet_growth", "pet growth values must be between 0 and 255");
            return FALSE;
        }
        packed = (unsigned int)CHAR_getInt(petindex, CHAR_ALLOCPOINT);
        packed &= ~((unsigned int)0xff << growth_shift);
        packed |= ((unsigned int)request->value & 0xffU) << growth_shift;
        CHAR_setInt(petindex, CHAR_ALLOCPOINT, (int)packed);
        CHAR_complianceParameter(petindex);
        return TRUE;
    }
    if( !StoneAgePA_petField(request->field, &element) ||
        !StoneAgePA_validatePetValue(request->field, request->value) ) {
        StoneAgePA_responseError(response, "invalid_pet_value", "pet value is outside the safe range");
        return FALSE;
    }
    if( strcmp(request->field, "slt") == 0 &&
        StoneAgePA_petHasSkillAtOrAbove(petindex, request->value) ) {
        StoneAgePA_responseError(response, "pet_skill_slots_in_use", "remove skills outside the new skill-slot count first");
        return FALSE;
    }
    CHAR_setInt(petindex, element, request->value);
    CHAR_complianceParameter(petindex);
    return TRUE;
}

static int StoneAgePA_setCharacter( StoneAgePAResponse *response,
                                    const StoneAgePARequest *request, int index )
{
    CHAR_DATAINT element;
    if( !request->has_field || !request->has_value ||
        !StoneAgePA_characterAttr(request->field, &element) ) {
        StoneAgePA_responseError(response, "invalid_character_field", "character field is not supported");
        return FALSE;
    }
    if( !StoneAgePA_validateCharacterValue(request->field, request->value) ) {
        StoneAgePA_responseError(response, "invalid_character_value", "character value is outside the supported range");
        return FALSE;
    }
    CHAR_setInt(index, element, request->value);
    CHAR_complianceParameter(index);
    return TRUE;
}

static int StoneAgePA_setPetSkill( StoneAgePAResponse *response,
                                   const StoneAgePARequest *request, int index,
                                   int delete_skill )
{
    int petindex = StoneAgePA_petAt(index, request->location, request->slot);
    int skill;
    int skill_slots;
    if( petindex < 0 || !CHAR_CHECKINDEX(petindex) ) {
        StoneAgePA_responseError(response, "pet_not_found", "pet slot is empty");
        return FALSE;
    }
    if( request->has_expected_pet_id && request->expected_pet_id != CHAR_getInt(petindex, CHAR_PETID) ) {
        StoneAgePA_responseError(response, "stale_pet", "pet changed; refresh snapshot");
        return FALSE;
    }
    if( !request->has_skill_slot || request->skill_slot < 0 ||
        request->skill_slot >= CHAR_MAXPETSKILLHAVE ) {
        StoneAgePA_responseError(response, "invalid_skill_slot", "skill slot is outside the pet range");
        return FALSE;
    }
    if( !delete_skill ) {
        skill_slots = CHAR_getInt(petindex, CHAR_SLOT);
        if( skill_slots < 1 || request->skill_slot >= skill_slots ) {
            StoneAgePA_responseError(response, "invalid_skill_slot", "skill slot is outside the pet's configured skill count");
            return FALSE;
        }
    }
    skill = delete_skill ? -1 : request->skill_id;
    if( !delete_skill && (!request->has_skill_id || skill < 0 ||
                          PETSKILL_getPetskillArray(skill) < 0) ) {
        StoneAgePA_responseError(response, "invalid_skill", "pet skill does not exist");
        return FALSE;
    }
    CHAR_setPetSkill(petindex, request->skill_slot, skill);
    return TRUE;
}

static int StoneAgePA_execute( StoneAgePAResponse *response,
                               StoneAgePARequest *request )
{
    int index;
    int mutate;
    int ok = FALSE;
    if( strcmp(request->action, "export_item") == 0 ||
        strcmp(request->action, "export_pet") == 0 ) {
        return StoneAgePA_exportTemplate(response, request);
    }
    if( strcmp(request->action, "inspect_item") == 0 ) {
        return StoneAgePA_inspectItem(response, request);
    }
    if( (strcmp(request->action, "set_item") == 0 ||
         strcmp(request->action, "delete_item") == 0 ||
         strcmp(request->action, "set_pet") == 0 ||
         strcmp(request->action, "delete_pet") == 0 ||
         strcmp(request->action, "set_pet_skill") == 0 ||
         strcmp(request->action, "delete_pet_skill") == 0) &&
        !request->has_slot ) {
        StoneAgePA_responseError(response, "slot_required", "this action requires an item or pet slot");
        return FALSE;
    }
    index = StoneAgePA_findTarget(request);
    if( strcmp(request->action, "snapshot") == 0 ) mutate = FALSE;
    else if( strcmp(request->action, "set_character") == 0 ||
             strcmp(request->action, "grant_item") == 0 ||
             strcmp(request->action, "set_item") == 0 ||
             strcmp(request->action, "delete_item") == 0 ||
             strcmp(request->action, "grant_pet") == 0 ||
             strcmp(request->action, "set_pet") == 0 ||
             strcmp(request->action, "delete_pet") == 0 ||
             strcmp(request->action, "set_pet_skill") == 0 ||
             strcmp(request->action, "delete_pet_skill") == 0 ) mutate = TRUE;
    else {
        StoneAgePA_responseError(response, "unknown_action", "action is not supported");
        return FALSE;
    }
    if( !StoneAgePA_checkTarget(request, index, response, mutate) ) return FALSE;
    if( mutate && !StoneAgePA_preflightSave(index, response) ) return FALSE;
    if( !mutate ) {
        StoneAgePA_snapshot(response, request, index);
        return TRUE;
    }
    if( strcmp(request->action, "set_character") == 0 ) ok = StoneAgePA_setCharacter(response, request, index);
    else if( strcmp(request->action, "grant_item") == 0 ) ok = StoneAgePA_grantItem(response, request, index);
    else if( strcmp(request->action, "set_item") == 0 ) ok = StoneAgePA_setItem(response, request, index);
    else if( strcmp(request->action, "delete_item") == 0 ) ok = StoneAgePA_deleteItem(response, request, index);
    else if( strcmp(request->action, "grant_pet") == 0 ) ok = StoneAgePA_grantPet(response, request, index);
    else if( strcmp(request->action, "set_pet") == 0 ) ok = StoneAgePA_setPet(response, request, index);
    else if( strcmp(request->action, "delete_pet") == 0 ) ok = StoneAgePA_deletePet(response, request, index);
    else if( strcmp(request->action, "set_pet_skill") == 0 ) ok = StoneAgePA_setPetSkill(response, request, index, FALSE);
    else if( strcmp(request->action, "delete_pet_skill") == 0 ) ok = StoneAgePA_setPetSkill(response, request, index, TRUE);
    if( !ok ) return FALSE;
    CHAR_complianceParameter(index);
    if( !StoneAgePA_save(index, response) ) return FALSE;
    CHAR_sendStatusString(index, "P");
    CHAR_sendStatusString(index, "I");
    if( strcmp(request->action, "grant_pet") == 0 ) {
        int i;
        for( i = 0; i < request->granted_slot_count; i++ ) {
            char category[8];
            snprintf(category, sizeof(category), "%s%d",
                     strcmp(request->location, "warehouse") == 0 ? "W" : "K",
                     request->granted_slots[i]);
            CHAR_sendStatusString(index, category);
        }
    } else if( strcmp(request->action, "set_pet") == 0 ||
               strcmp(request->action, "delete_pet") == 0 ||
               strcmp(request->action, "set_pet_skill") == 0 ||
               strcmp(request->action, "delete_pet_skill") == 0 ) {
        char category[8];
        snprintf(category, sizeof(category), "%s%d",
                 strcmp(request->location, "warehouse") == 0 ? "W" : "K",
                 request->slot);
        CHAR_sendStatusString(index, category);
    }
    StoneAgePA_responseOK(response, request, index);
    StoneAgePA_writeEncoded(response, "save_status", "queued");
    return TRUE;
}

static int StoneAgePA_requestIDFromName( const char *name, char *id, size_t idlen )
{
    size_t n;
    if( name == NULL ) return FALSE;
    n = strlen(name);
    if( n < 5 || strcmp(name + n - 4, ".req") != 0 ) return FALSE;
    n -= 4;
    if( n == 0 || n >= idlen ) return FALSE;
    memcpy(id, name, n);
    id[n] = '\0';
    return StoneAgePA_validID(id);
}

static FILE *StoneAgePA_openResponse( const char *id, char *temp_path,
                                      size_t temp_path_len, char *final_path,
                                      size_t final_path_len )
{
    FILE *fp;
    StoneAgePA_response_serial++;
    snprintf(final_path, final_path_len, "%s/responses/%s.resp", StoneAgePA_dir, id);
    snprintf(temp_path, temp_path_len, "%s/responses/.%s.%lu.%ld.tmp",
             StoneAgePA_dir, id, StoneAgePA_response_serial, (long)getpid());
    fp = fopen(temp_path, "wb");
    return fp;
}

static void StoneAgePA_processOne( const char *id )
{
    char request_path[STONEAGE_PA_MAX_PATH];
    char temp_path[STONEAGE_PA_MAX_PATH];
    char response_path[STONEAGE_PA_MAX_PATH];
    StoneAgePARequest request;
    StoneAgePAResponse response;
    FILE *fp;
    int index;
    snprintf(request_path, sizeof(request_path), "%s/requests/%s.req", StoneAgePA_dir, id);
    fp = StoneAgePA_openResponse(id, temp_path, sizeof(temp_path),
                                 response_path, sizeof(response_path));
    if( fp == NULL ) return;
    StoneAgePA_responseBegin(&response, fp, id);
    if( !StoneAgePA_readRequest(request_path, id, &request) ) {
        StoneAgePA_responseError(&response, "invalid_request", "request is malformed or too large");
    } else {
        StoneAgePA_execute(&response, &request);
    }
    fflush(fp);
    fclose(fp);
    if( !response.failed && rename(temp_path, response_path) != 0 ) unlink(temp_path);
    else if( response.failed ) unlink(temp_path);
    unlink(request_path);
}

static void StoneAgePA_init( void )
{
    const char *configured = getenv("STONEAGE_PLAYER_ADMIN_DIR");
    char path[STONEAGE_PA_MAX_PATH];
    if( configured != NULL && configured[0] != '\0' && strlen(configured) < sizeof(StoneAgePA_dir) ) {
        strncpy(StoneAgePA_dir, configured, sizeof(StoneAgePA_dir));
        StoneAgePA_dir[sizeof(StoneAgePA_dir) - 1] = '\0';
    }
    mkdir(StoneAgePA_dir, 0750);
    snprintf(path, sizeof(path), "%s/requests", StoneAgePA_dir);
    mkdir(path, 0750);
    snprintf(path, sizeof(path), "%s/responses", StoneAgePA_dir);
    mkdir(path, 0750);
    StoneAgePA_initialized = TRUE;
}

void STONEAGE_PlayerAdminProcess( void )
{
    DIR *dir;
    struct dirent *entry;
    char id[STONEAGE_PA_MAX_ID + 1];
    char request_path[STONEAGE_PA_MAX_PATH];
    int processed = 0;
    if( !StoneAgePA_initialized ) StoneAgePA_init();
    snprintf(request_path, sizeof(request_path), "%s/requests", StoneAgePA_dir);
    dir = opendir(request_path);
    if( dir == NULL ) return;
    while( processed < 8 && (entry = readdir(dir)) != NULL ) {
        if( StoneAgePA_requestIDFromName(entry->d_name, id, sizeof(id)) ) {
            StoneAgePA_processOne(id);
            processed++;
        }
    }
    closedir(dir);
}
