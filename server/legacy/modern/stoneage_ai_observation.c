/*
 * Character-scoped AI observation for the legacy 2.5 S status request.
 *
 * This module is deliberately read-only.  It walks only the authenticated
 * character index supplied by lssproto_S_recv(), then follows that
 * character's five pet slots.  It never scans the global character table,
 * reads account credentials or GM fields, creates a pet identity, or writes
 * a character field.
 */
#include "version.h"
#include <stdarg.h>
#include <stdio.h>
#include <string.h>

#include "char.h"
#include "char_base.h"
#include "item.h"
#include "util.h"
#include "stoneage_ai_observation.h"
#include "stoneage_character_identity.h"

#define STONEAGE_AI_OBSERVATION_BUFFER_SIZE 65500
#define STONEAGE_AI_OBSERVATION_UNIQUE_BUFFER_SIZE 512
#define STONEAGE_AI_OBSERVATION_UNIQUE_RAW_SIZE 256
#define STONEAGE_AI_OBSERVATION_PET_SLOTS 5
#define STONEAGE_AI_OBSERVATION_EVENT_GROUPS 6

static char StoneAge_AIObservationBuffer[STONEAGE_AI_OBSERVATION_BUFFER_SIZE];

static int StoneAge_AIObservationAppend( char **cursor, size_t *remaining,
                                         const char *format, ... )
{
    va_list args;
    int written;

    if( cursor == NULL || remaining == NULL || format == NULL ||
        *cursor == NULL || *remaining == 0 ) return FALSE;

    va_start( args, format );
    written = vsnprintf( *cursor, *remaining, format, args );
    va_end( args );
    if( written < 0 || (size_t)written >= *remaining ) return FALSE;
    *cursor += written;
    *remaining -= (size_t)written;
    return TRUE;
}

static const char *StoneAge_AIObservationUnique( int petindex,
                                                  char *raw,
                                                  size_t raw_size,
                                                  char *escaped,
                                                  size_t escaped_size )
{
    const char *unique;
    size_t length;
    size_t escaped_length;
    size_t i;

    if( raw == NULL || raw_size == 0 || escaped == NULL || escaped_size == 0 ||
        !CHAR_CHECKINDEX( petindex ) ) return "unknown";

    unique = CHAR_getChar( petindex, CHAR_UNIQUECODE );
    if( unique == NULL || unique[0] == '\0' ) return "unknown";

    /* makeEscapeString() has a legacy non-const signature.  Copy first so
     * the observation path cannot ever hand a mutable character buffer to a
     * formatter or accidentally modify the live pet record. */
    length = strlen( unique );
    /* A truncated unique code can alias another pet's code.  Unknown is a
     * safe value when the complete source will not fit in the scratch
     * buffer. */
    if( length >= raw_size ) return "unknown";
    memcpy( raw, unique, length );
    raw[length] = '\0';

    /* The legacy formatter has no truncation indicator.  Account for every
     * escape byte before calling it so its output is always complete. */
    escaped_length = length;
    for( i = 0; i < length; i++ ) {
        if( raw[i] == '\n' || raw[i] == ',' || raw[i] == '|' ||
            raw[i] == '\\' ) escaped_length++;
    }
    if( escaped_length >= escaped_size ) return "unknown";
    escaped[0] = '\0';
    makeEscapeString( raw, escaped, (int)escaped_size );
    if( escaped[0] == '\0' ) return "unknown";
    return escaped;
}

static int StoneAge_AIObservationEndEvent( int charaindex, int group )
{
    switch( group ) {
    case 0: return CHAR_getInt( charaindex, CHAR_ENDEVENT );
    case 1: return CHAR_getInt( charaindex, CHAR_ENDEVENT2 );
    case 2: return CHAR_getInt( charaindex, CHAR_ENDEVENT3 );
#ifdef _NEWEVENT
    case 3: return CHAR_getInt( charaindex, CHAR_ENDEVENT4 );
    case 4: return CHAR_getInt( charaindex, CHAR_ENDEVENT5 );
    case 5: return CHAR_getInt( charaindex, CHAR_ENDEVENT6 );
#else
    case 3:
    case 4:
    case 5: return 0;
#endif
    default: return 0;
    }
}

static int StoneAge_AIObservationNowEvent( int charaindex, int group )
{
    switch( group ) {
    case 0: return CHAR_getInt( charaindex, CHAR_NOWEVENT );
    case 1: return CHAR_getInt( charaindex, CHAR_NOWEVENT2 );
    case 2: return CHAR_getInt( charaindex, CHAR_NOWEVENT3 );
#ifdef _NEWEVENT
    case 3: return CHAR_getInt( charaindex, CHAR_NOWEVENT4 );
    case 4: return CHAR_getInt( charaindex, CHAR_NOWEVENT5 );
    case 5: return CHAR_getInt( charaindex, CHAR_NOWEVENT6 );
#else
    case 3:
    case 4:
    case 5: return 0;
#endif
    default: return 0;
    }
}

static int StoneAge_AIObservationAppendItems( int charaindex,
                                              char **cursor,
                                              size_t *remaining )
{
    int slot;
    int itemindex;
    int item_id;
    int count = 0;

    if( !StoneAge_AIObservationAppend(cursor, remaining, "|items=") ) {
        return FALSE;
    }
    /* Inspect only the authenticated character's backpack slots.  In
     * particular, do not walk the global item table to infer ownership. */
    for( slot = CHAR_STARTITEMARRAY; slot < CHAR_MAXITEMHAVE; slot++ ) {
        itemindex = CHAR_getItemIndex( charaindex, slot );
        if( !ITEM_CHECKINDEX(itemindex) ) continue;
        item_id = ITEM_getInt(itemindex, ITEM_ID);
        if( item_id <= 0 ) return FALSE;
        if( !StoneAge_AIObservationAppend(
                cursor, remaining, "%s%d,%d", count == 0 ? "" : ";",
                slot, item_id ) ) return FALSE;
        count++;
    }
    if( count == 0 &&
        !StoneAge_AIObservationAppend(cursor, remaining, "none") ) {
        return FALSE;
    }
    return TRUE;
}

char *StoneAge_AIObservationMake( int charaindex )
{
    return StoneAge_AIObservationMakeWithRequest(charaindex, NULL);
}

char *StoneAge_AIObservationMakeWithRequest( int charaindex, const char *request )
{
    char *cursor;
    size_t remaining;
    char raw_unique[STONEAGE_AI_OBSERVATION_UNIQUE_RAW_SIZE];
    char escaped_unique[STONEAGE_AI_OBSERVATION_UNIQUE_BUFFER_SIZE];
    int slot;
    int petindex;
    int group;
    const char *persistent_id;

    if( request != NULL ) {
        size_t i;
        if( strlen(request) != 16 ) return NULL;
        for( i = 0; i < 16; i++ ) {
            if( !((request[i] >= '0' && request[i] <= '9') ||
                  (request[i] >= 'a' && request[i] <= 'f')) ) return NULL;
        }
    }

    if( !CHAR_CHECKINDEX( charaindex ) ) return NULL;

    cursor = StoneAge_AIObservationBuffer;
    remaining = sizeof( StoneAge_AIObservationBuffer );
    if( !StoneAge_AIObservationAppend( &cursor, &remaining,
                                       "AI|v=1|chara=%d", charaindex ) ) {
        return NULL;
    }
    persistent_id = StoneAge_CharacterIdentityGetLoadedByIndex(charaindex);
    if( persistent_id != NULL && !StoneAge_AIObservationAppend(
            &cursor, &remaining, "|character_id=%s", persistent_id) ) return NULL;
    if( request != NULL && !StoneAge_AIObservationAppend(
            &cursor, &remaining, "|request=%s", request) ) return NULL;

    for( slot = 0; slot < STONEAGE_AI_OBSERVATION_PET_SLOTS; slot++ ) {
        petindex = CHAR_getCharPet( charaindex, slot );
        if( !CHAR_CHECKINDEX( petindex ) ) continue;
        if( !StoneAge_AIObservationAppend(
                &cursor, &remaining, "|pet=%d,%s,%d", slot,
                StoneAge_AIObservationUnique( petindex, raw_unique,
                                               sizeof(raw_unique),
                                               escaped_unique,
                                               sizeof(escaped_unique) ),
                CHAR_getInt( petindex, CHAR_LV ) ) ) return NULL;
    }

    if( !StoneAge_AIObservationAppend( &cursor, &remaining, "|end=" ) ) {
        return NULL;
    }
    for( group = 0; group < STONEAGE_AI_OBSERVATION_EVENT_GROUPS; group++ ) {
        if( !StoneAge_AIObservationAppend(
                &cursor, &remaining, "%s%d", group == 0 ? "" : ",",
                StoneAge_AIObservationEndEvent( charaindex, group ) ) ) {
            return NULL;
        }
    }

    if( !StoneAge_AIObservationAppend( &cursor, &remaining, "|now=" ) ) {
        return NULL;
    }
    for( group = 0; group < STONEAGE_AI_OBSERVATION_EVENT_GROUPS; group++ ) {
        if( !StoneAge_AIObservationAppend(
                &cursor, &remaining, "%s%d", group == 0 ? "" : ",",
                StoneAge_AIObservationNowEvent( charaindex, group ) ) ) {
            return NULL;
        }
    }

    if( !StoneAge_AIObservationAppend(
            &cursor, &remaining, "|ride=%d",
            CHAR_getInt( charaindex, CHAR_LEARNRIDE ) ) ) return NULL;
    if( !StoneAge_AIObservationAppend(
            &cursor, &remaining, "|sp=%d",
            CHAR_getInt( charaindex, CHAR_SAVEPOINT ) ) ) return NULL;
    /* Legacy SKUP allocation does not echo the remaining point count.
       Expose the caller's current value through the existing read-only query. */
    if( !StoneAge_AIObservationAppend(
            &cursor, &remaining, "|stat_points=%d",
            CHAR_getInt( charaindex, CHAR_SKILLUPPOINT ) ) ) return NULL;
    if( !StoneAge_AIObservationAppend(
            &cursor, &remaining, "|party_mode=%d",
            CHAR_getWorkInt( charaindex, CHAR_WORKPARTYMODE ) ) ) return NULL;
    if( !StoneAge_AIObservationAppendItems(charaindex, &cursor, &remaining) ) {
        return NULL;
    }
    return StoneAge_AIObservationBuffer;
}
