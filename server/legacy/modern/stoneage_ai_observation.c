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
extern const char *StoneAge_EncounterPolicy(void);

void StoneAge_MagicObservationAppend(int charaindex, int slot, char *buffer, size_t size)
{
    int itemindex, id, written;
    size_t used;
    if (!CHAR_CHECKINDEX(charaindex) || slot < 0 || slot >= CHAR_STARTITEMARRAY || !buffer || !size) return;
    itemindex = CHAR_getItemIndex(charaindex, slot);
    if (!ITEM_CHECKINDEX(itemindex)) return;
    id = ITEM_getInt(itemindex, ITEM_MAGICID);
    if (id < 0) return;
    used = strnlen(buffer, size);
    if (!used || used >= size || buffer[used-1] != '|') return;
    written = snprintf(buffer+used, size-used, "id=%d|", id);
    /* Never leave a truncated ID that could identify a different spell. */
    if (written < 0 || (size_t)written >= size-used) buffer[used] = '\0';
}

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
                                              size_t *remaining, const char *field,
                                              int first_slot, int end_slot )
{
    int slot;
    int itemindex;
    int item_id;
    int count = 0;

    if( !StoneAge_AIObservationAppend(cursor, remaining, "|%s=", field) ) {
        return FALSE;
    }
    /* Inspect only the authenticated character's equipment and backpack slots.  In
     * particular, do not walk the global item table to infer ownership. */
    for( slot = first_slot; slot < end_slot; slot++ ) {
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

static int StoneAge_AIObservationAppendPetSpecies(int charaindex, char **cursor, size_t *remaining)
{
    int slot, petindex, species, count = 0;
    if (!StoneAge_AIObservationAppend(cursor, remaining, "|pet_species=")) return FALSE;
    for (slot = 0; slot < STONEAGE_AI_OBSERVATION_PET_SLOTS; slot++) {
        petindex = CHAR_getCharPet(charaindex, slot);
        if (!CHAR_CHECKINDEX(petindex)) continue;
        species = CHAR_getInt(petindex, CHAR_PETID);
        if (species < 0) continue;
        if (!StoneAge_AIObservationAppend(cursor, remaining, "%s%d,%d", count ? ";" : "", slot, species)) return FALSE;
        count++;
    }
    return count || StoneAge_AIObservationAppend(cursor, remaining, "-");
}

static int StoneAge_AIObservationAppendPetEvent(int charaindex, char **cursor, size_t *remaining)
{
    int slot, petindex, flag, count = 0;
    if (!StoneAge_AIObservationAppend(cursor, remaining, "|pet_event=")) return FALSE;
    for (slot = 0; slot < STONEAGE_AI_OBSERVATION_PET_SLOTS; slot++) {
        petindex = CHAR_getCharPet(charaindex, slot);
        if (!CHAR_CHECKINDEX(petindex)) continue;
        flag = CHAR_getInt(petindex, CHAR_ENDEVENT);
        if (flag < 0) continue;
        if (!StoneAge_AIObservationAppend(cursor, remaining, "%s%d,%d", count ? ";" : "", slot, flag)) return FALSE;
        count++;
    }
    return count || StoneAge_AIObservationAppend(cursor, remaining, "-");
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
    int group, summon_mask = 0;
    const char *persistent_id;
    const char *encounter_policy;

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
    encounter_policy = StoneAge_EncounterPolicy();
    if( encounter_policy != NULL && !StoneAge_AIObservationAppend(
            &cursor, &remaining, "|encounter_policy=%s", encounter_policy) ) return NULL;
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
    /* Separate optional token preserves the existing three-column pet field
       for older clients. This reads only pets owned by the caller. */
    if (!StoneAge_AIObservationAppendPetSpecies(charaindex, &cursor, &remaining)) return NULL;
    if (!StoneAge_AIObservationAppendPetEvent(charaindex, &cursor, &remaining)) return NULL;

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
            &cursor, &remaining, "|gold_limit=%d",
            CHAR_getMaxHaveGold( charaindex ) ) ) return NULL;
    if( !StoneAge_AIObservationAppend(
            &cursor, &remaining, "|party_mode=%d",
            CHAR_getWorkInt( charaindex, CHAR_WORKPARTYMODE ) ) ) return NULL;
    /* The same own-character selection sent by KS at login. A read-only
       refresh must also expose it: skill lists alone do not identify which
       owned pet is selected for battle. This is not a command acknowledgement. */
    if( !StoneAge_AIObservationAppend(
            &cursor, &remaining, "|active_pet=%d",
            CHAR_getInt( charaindex, CHAR_DEFAULTPET ) ) ) return NULL;
    /* Same five-bit own-character selection list as SPET. A reconnect or
       offline client must not infer summon eligibility from occupied K slots. */
    if( !StoneAge_AIObservationAppend(
            &cursor, &remaining, "|standby_pet_mask=%d",
            CHAR_getWorkInt( charaindex, CHAR_WORKSTANDBYPET ) & 31 ) ) return NULL;
    /* PETST eligibility is independent of SPET membership. The battle
       executor checks it before summoning, after recalling the old pet. */
    for( slot = 0; slot < STONEAGE_AI_OBSERVATION_PET_SLOTS; slot++ )
        if( CHAR_getWorkInt(charaindex, CHAR_WORK_PET0_STAT + slot) == PET_STAT_SELECT )
            summon_mask |= 1 << slot;
    if( !StoneAge_AIObservationAppend(
            &cursor, &remaining, "|summon_pet_mask=%d", summon_mask ) ) return NULL;
    if( !StoneAge_AIObservationAppendItems(charaindex, &cursor, &remaining, "items", CHAR_STARTITEMARRAY, CHAR_MAXITEMHAVE) ||
        !StoneAge_AIObservationAppendItems(charaindex, &cursor, &remaining, "equipment", 0, CHAR_STARTITEMARRAY) ) {
        return NULL;
    }
    return StoneAge_AIObservationBuffer;
}
