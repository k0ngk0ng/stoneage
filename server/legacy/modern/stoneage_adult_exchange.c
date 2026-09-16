/* The reviewed 2.5 adult ceremony grants fifteen physical ritual items and
 * later exchanges them for one helmet. Keep these paths separate from scripts
 * with gold, pets, random rewards or other effects. This is an in-memory commit boundary;
 * character-save/crash reconciliation remains the server's responsibility. */
#include "version.h"
#include <string.h>
#include "char.h"
#include "char_base.h"
#include "item.h"
#include "log.h"
#include "stoneage_adult_exchange.h"

static int StoneAge_AdultScript( const char *script, int grant )
{
    char copy[2048], *token, *next, *colon;
    unsigned int seen = 0, bit;
    const char *expected;
    if( script == NULL || strlen(script) >= sizeof(copy) ) return FALSE;
    strcpy(copy, script);
    token = copy;
    while( token != NULL ) {
        next = strchr(token, '|');
        if( next != NULL ) *next++ = '\0';
        if( *token == '\0' ) { token = next; continue; }
        colon = strchr(token, ':');
        if( colon == NULL ) return FALSE;
        *colon++ = '\0';
        bit = 0; expected = NULL;
        if( strcmp(token, "EventNo") == 0 ) { bit = 1; expected = "4"; }
        else if( strcmp(token, "TYPE") == 0 ) { bit = 2; expected = "ACCEPT"; }
        else if( strcmp(token, "EVENT") == 0 ) { bit = 4; expected = grant ? "NOWEV=4&ITEM!=2417" : "ITEM=2417*15"; }
        else if( strcmp(token, "GetItem") == 0 ) { bit = 8; expected = grant ? "2417*15" : "2418"; }
        else if( !grant && strcmp(token, "DelItem") == 0 ) { bit = 16; expected = "2417*15"; }
        else if( !grant && strcmp(token, "EndSetFlg") == 0 ) { bit = 32; expected = "4"; }
        else if( strcmp(token, "NomalMainMsg") == 0 ) bit = 64;
        else if( strcmp(token, "AcceptMsg") == 0 ) bit = 128;
        else if( strcmp(token, "ThanksMsg") == 0 ) bit = 256;
        else if( strcmp(token, "ItemFullMsg") == 0 ) bit = 512;
        else return FALSE;
        if( bit != 0 ) {
            if( (seen & bit) != 0 || (expected != NULL && strcmp(colon, expected) != 0) ) return FALSE;
            seen |= bit;
        }
        token = next;
    }
    return seen == (grant ? 975 : 1023);
}

static void StoneAge_AdultItemLog( int talker, int item, char *operation )
{
    LogItem(CHAR_getChar(talker, CHAR_NAME), CHAR_getChar(talker, CHAR_CDKEY),
#ifdef _add_item_log_name
            item,
#else
            ITEM_getInt(item, ITEM_ID),
#endif
            operation, CHAR_getInt(talker, CHAR_FLOOR), CHAR_getInt(talker, CHAR_X),
            CHAR_getInt(talker, CHAR_Y), ITEM_getChar(item, ITEM_UNIQUECODE),
            ITEM_getChar(item, ITEM_NAME), ITEM_getInt(item, ITEM_ID));
}

static int StoneAge_AdultHasRitualItem( int talker )
{
    int slot, item;
    for( slot = 0; slot < CHAR_MAXITEMHAVE; slot++ ) {
        item = CHAR_getItemIndex(talker, slot);
        if( ITEM_CHECKINDEX(item) && ITEM_getInt(item, ITEM_ID) == 2417 ) return TRUE;
    }
    return FALSE;
}

static int StoneAge_AdultGrant( int talker )
{
    int slots[15], items[15];
    int count = 0, allocated = 0, slot, item, i;
    if( !CHAR_CHECKINDEX(talker) || StoneAge_AdultHasRitualItem(talker) ) return FALSE;
    for( slot = CHAR_STARTITEMARRAY; slot < CHAR_MAXITEMHAVE && count < 15; slot++ ) {
        if( CHAR_getItemIndex(talker, slot) == -1 ) slots[count++] = slot;
    }
    if( count != 15 ) return FALSE;
    for( i = 0; i < count; i++ ) {
        item = ITEM_makeItemAndRegist(2417);
        if( !ITEM_CHECKINDEX(item) ) goto failed;
        items[allocated++] = item;
        if( ITEM_getInt(item, ITEM_ID) != 2417 || ITEM_getInt(item, ITEM_USEPILENUMS) < 0 ||
            ITEM_getInt(item, ITEM_USEPILENUMS) > 1 ) goto failed;
        ITEM_setWorkInt(item, ITEM_WORKOBJINDEX, -1);
        ITEM_setWorkInt(item, ITEM_WORKCHARAINDEX, -1);
    }
    if( !CHAR_CHECKINDEX(talker) || StoneAge_AdultHasRitualItem(talker) ) goto failed;
    for( i = 0; i < count; i++ ) {
        if( CHAR_getItemIndex(talker, slots[i]) != -1 ) goto failed;
    }
    /* All fallible allocations precede slot writes and all network updates.
     * NOWEV eligibility and dialog progression remain with the native caller. */
    for( i = 0; i < count; i++ ) {
        ITEM_setWorkInt(items[i], ITEM_WORKCHARAINDEX, talker);
        CHAR_setItemIndex(talker, slots[i], items[i]);
    }
    for( i = 0; i < count; i++ ) StoneAge_AdultItemLog(talker, items[i], "AdultRitualGrant");
    for( i = 0; i < count; i++ ) CHAR_sendItemDataOne(talker, slots[i]);
    return TRUE;
failed:
    for( i = 0; i < allocated; i++ ) ITEM_endExistItemsOne(items[i]);
    return FALSE;
}

int StoneAge_AdultExchange( int talker, const char *script )
{
    int slots[15], items[15];
    int count = 0, slot, item, i, j, reward;
    if( !StoneAge_AdultScript(script, FALSE) ) {
        if( StoneAge_AdultScript(script, TRUE) ) return StoneAge_AdultGrant(talker);
        return -1;
    }
    if( !CHAR_CHECKINDEX(talker) ) return FALSE;
    for( slot = CHAR_STARTITEMARRAY; slot < CHAR_MAXITEMHAVE; slot++ ) {
        item = CHAR_getItemIndex(talker, slot);
        if( !ITEM_CHECKINDEX(item) || ITEM_getInt(item, ITEM_ID) != 2417 ) continue;
        /* The reviewed task grants fifteen individual items, not stacks.
         * Do not destroy a larger stack or double-count an aliased item. */
        if( ITEM_getInt(item, ITEM_USEPILENUMS) < 0 ||
            ITEM_getInt(item, ITEM_USEPILENUMS) > 1 || count >= 15 ) return FALSE;
        for( j = 0; j < count; j++ ) if( items[j] == item ) return FALSE;
        slots[count] = slot; items[count++] = item;
    }
    if( count != 15 ) return FALSE;
    reward = ITEM_makeItemAndRegist(2418);
    if( !ITEM_CHECKINDEX(reward) ) return FALSE;
    if( ITEM_getInt(reward, ITEM_ID) != 2418 ) {
        ITEM_endExistItemsOne(reward);
        return FALSE;
    }
    ITEM_setWorkInt(reward, ITEM_WORKOBJINDEX, -1);
    ITEM_setWorkInt(reward, ITEM_WORKCHARAINDEX, -1);
    /* Creation can fail without consuming a single ingredient. Recheck all
     * reserved inputs before the first slot mutation. */
    for( i = 0; i < count; i++ ) {
        if( CHAR_getItemIndex(talker, slots[i]) != items[i] ||
            !ITEM_CHECKINDEX(items[i]) || ITEM_getInt(items[i], ITEM_ID) != 2417 ||
            ITEM_getInt(items[i], ITEM_USEPILENUMS) < 0 || ITEM_getInt(items[i], ITEM_USEPILENUMS) > 1 ) {
            ITEM_endExistItemsOne(reward);
            return FALSE;
        }
    }
    /* Validated slot writes and ownership setters do not allocate. Reuse a
     * consumed slot so a full fifteen-slot backpack can complete the task. */
    for( i = 0; i < count; i++ ) CHAR_setItemIndex(talker, slots[i], -1);
    ITEM_setWorkInt(reward, ITEM_WORKCHARAINDEX, talker);
    CHAR_setItemIndex(talker, slots[0], reward);
    for( i = 0; i < count; i++ ) {
        StoneAge_AdultItemLog(talker, items[i], "AdultExchangeOut");
        ITEM_endExistItemsOne(items[i]);
    }
    StoneAge_AdultItemLog(talker, reward, "AdultExchangeIn");
    for( i = 0; i < count; i++ ) CHAR_sendItemDataOne(talker, slots[i]);
    return TRUE;
}
