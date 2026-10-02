/* Native adapter: authenticated game sessions, live battle participants and
 * authoritative HP deltas. No privileged client action or synthetic player. */
#include "version.h"
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/time.h>
#include <sys/stat.h>
#include <limits.h>
#include "char.h"
#include "char_base.h"
#include "char_data.h"
#include "battle.h"
#include "battle_command.h"
#include "battle_event.h"
#include "addressbook.h"
#include "net.h"
#include "item.h"
#include "lssproto_serv.h"
#include "saacproto_cli.h"
#include "stoneage_character_identity.h"
#include "stoneage_ai_observation.h"
#include "stoneage_battle_record.h"
#include "stoneage_battle_dataset.h"
#include "stoneage_ladder_core.h"
#include "stoneage_ladder.h"

#define AUTH_SLOTS 65536
#define OFFLINE_SAVE_BASE 1000000
#define TURN_TIMEOUT_MS 30000
/* SAAC's CHARDATASIZE in saac/include/main.h also bounds its RPC buffers. */
#define SAAC_ARCHIVE_MAX (64*1024)

typedef struct {
    int index;
    ITEM_Item value;
} SavedItem;
typedef struct {
    int character;
    Char *before;
    SavedItem *items;
    int item_count;
    int save_request,save_confirmed;
    char *save_data,*save_options,*save_pet;
} SavedActor;
typedef struct {
    int used,battle,count,characters[10],masks[10],last_turn,forced_winner;
    const char *forced_reason;
    char id[65];
    LadderTime deadline;
    SavedActor actors[60];int actor_count;
    unsigned short contributors[60];
} NativeMatch;
typedef struct { int fdid;char account[CDKEYLEN]; } Authenticated;
typedef struct { int retained,pending_save,window_open,save_request;LadderTime save_at,retry_at; } NativePlayer;

static NativeMatch native_matches[LADDER_MATCH_MAX];
static Authenticated authenticated[AUTH_SLOTS];
static NativePlayer native_players[LADDER_PLAYER_MAX];
static int initialized,enabled,entering,restoring,resync_target=-1,current_source=-1;
static int next_save_request=-OFFLINE_SAVE_BASE;
static int (*status_sources)[BATTLE_ST_END];
extern int StatusTbl[];
extern int BATTLE_ClearGetExp(int character);

static LadderTime now_ms(void)
{ struct timeval t;gettimeofday(&t,NULL);return (LadderTime)t.tv_sec*1000+t.tv_usec/1000; }
static int valid_player(int c)
{ return c>=0 && c<LADDER_PLAYER_MAX && CHAR_CHECKINDEX(c) && CHAR_getInt(c,CHAR_WHICHTYPE)==CHAR_TYPEPLAYER; }
static int online(int c)
{
    int fd=CHAR_getWorkInt(c,CHAR_WORKFD);
    return fd>=0 && CONNECT_checkfd(fd) && CONNECT_isLOGIN(fd) && CONNECT_getCharaindex(fd)==c;
}
static unsigned long long hash_number(unsigned long long hash,int value)
{ return (hash^(unsigned int)value)*1099511628211ULL; }
static unsigned long long hash_text(unsigned long long hash,const char *value,size_t capacity)
{
    size_t i;for(i=0;i<capacity && value[i];i++)hash=hash_number(hash,(unsigned char)value[i]);
    return hash_number(hash,0);
}
static unsigned long long hash_actor(unsigned long long hash,int c)
{
    int i,j,index;ITEM_Item *item;CHAR_HaveSkill *skill;
    static const int fields[]={CHAR_VITAL,CHAR_STR,CHAR_TOUGH,CHAR_DEX,CHAR_LV,CHAR_TRANSMIGRATION,CHAR_SKILLUPPOINT,CHAR_CHARM,CHAR_LUCK,CHAR_EARTHAT,CHAR_WATERAT,CHAR_FIREAT,CHAR_WINDAT,
        CHAR_POISON,CHAR_PARALYSIS,CHAR_SLEEP,CHAR_STONE,CHAR_DRUNK,CHAR_CONFUSION
#ifdef _ATTACK_MAGIC
        ,CHAR_EARTH_EXP,CHAR_WATER_EXP,CHAR_FIRE_EXP,CHAR_WIND_EXP,
        CHAR_EARTH_RESIST,CHAR_WATER_RESIST,CHAR_FIRE_RESIST,CHAR_WIND_RESIST
#endif
    };
    static const int derived[]={CHAR_WORKMAXHP,CHAR_WORKMAXMP,CHAR_WORKATTACKPOWER,CHAR_WORKDEFENCEPOWER,CHAR_WORKQUICK};
    /* Indices can be reused, and an item can change without leaving its slot.
     * Hash actual configuration, including pet equipment and skill metadata.
     * HP/MP and other transient combat state are deliberately excluded. */
    hash=hash_number(hash,CHAR_getCharMakeSequenceNumber(c));
    for(i=0;i<(int)(sizeof(fields)/sizeof(fields[0]));i++)hash=hash_number(hash,CHAR_getInt(c,fields[i]));
    for(i=0;i<(int)(sizeof(derived)/sizeof(derived[0]));i++)hash=hash_number(hash,CHAR_getWorkInt(c,derived[i]));
    for(i=0;i<CHAR_MAXITEMHAVE;i++) {
        index=CHAR_getItemIndex(c,i);hash=hash_number(hash,index);
        if(!ITEM_CHECKINDEX(index))continue;
        item=ITEM_getItemPointer(index);
        for(j=0;j<ITEM_DATAINTNUM;j++)hash=hash_number(hash,item->data[j]);
        for(j=0;j<ITEM_DATACHARNUM;j++)hash=hash_text(hash,item->string[j].string,sizeof(item->string[j].string));
    }
    for(i=0;i<CHAR_SKILLMAXHAVE;i++) {
        skill=CHAR_getCharHaveSkill(c,i);hash=hash_number(hash,skill?skill->use:0);
        if(!skill || !skill->use)continue;
        for(j=0;j<SKILL_DATAINTNUM;j++)hash=hash_number(hash,skill->skill.data[j]);
        for(j=0;j<SKILL_DATACHARNUM;j++)hash=hash_text(hash,skill->skill.string[j].string,sizeof(skill->skill.string[j].string));
    }
    return hash;
}
static double actor_power(int c)
{ return Ladder_EquivalentPoints(CHAR_getWorkInt(c,CHAR_WORKMAXHP),CHAR_getWorkInt(c,CHAR_WORKATTACKPOWER),CHAR_getWorkInt(c,CHAR_WORKDEFENCEPOWER),CHAR_getWorkInt(c,CHAR_WORKQUICK)); }
static int profile(int c,LadderProfile *f)
{
    const char *id,*name;int i,j,pet;double base,equivalent;
    if(!valid_player(c))return 0;
    id=StoneAge_CharacterIdentityGetLoadedByIndex(c);if(!id || !StoneAge_CharacterIdentityValid(id))return 0;
    memset(f,0,sizeof(*f));snprintf(f->id,sizeof(f->id),"%s",id);f->character=c;
    name=CHAR_getChar(c,CHAR_NAME);
    for(i=0;name && name[i] && i<64;i++)snprintf(f->name_hex+i*2,sizeof(f->name_hex)-i*2,"%02x",(unsigned char)name[i]);
    f->online=online(c);f->idle=1;f->busy_reason="";
    if(CHAR_getWorkInt(c,CHAR_WORKBATTLEMODE)!=BATTLE_CHARMODE_NONE){f->idle=0;f->busy_reason="battle";}
    else if(CHAR_getWorkInt(c,CHAR_WORKTRADEMODE)!=CHAR_TRADE_FREE){f->idle=0;f->busy_reason="trade";}
    else if(CHAR_getWorkInt(c,CHAR_WORKPARTYMODE)!=CHAR_PARTY_NONE){f->idle=0;f->busy_reason="party";}
    else if(native_players[c].window_open){f->idle=0;f->busy_reason="dialog";}
    else if(CHAR_getInt(c,CHAR_HP)<=0 || CHAR_getFlg(c,CHAR_ISDIE)){f->idle=0;f->busy_reason="dead";}
    f->active_pet=CHAR_getInt(c,CHAR_DEFAULTPET);f->ride_pet=CHAR_getInt(c,CHAR_RIDEPET);
    f->loadout_hash=hash_actor(1469598103934665603ULL,c);
    f->loadout_hash=hash_number(f->loadout_hash,f->active_pet);f->loadout_hash=hash_number(f->loadout_hash,f->ride_pet);
    base=((double)CHAR_getInt(c,CHAR_VITAL)+CHAR_getInt(c,CHAR_STR)+CHAR_getInt(c,CHAR_TOUGH)+CHAR_getInt(c,CHAR_DEX))/100.0+CHAR_getInt(c,CHAR_SKILLUPPOINT);
    equivalent=actor_power(c)+CHAR_getInt(c,CHAR_SKILLUPPOINT);f->character_power=base>equivalent?base:equivalent;
    for(i=0;i<5;i++) {
        pet=CHAR_getCharPet(c,i);f->loadout_hash=hash_number(f->loadout_hash,pet);
        if(!CHAR_CHECKINDEX(pet))continue;
        if(CHAR_getInt(pet,CHAR_WHICHTYPE)!=CHAR_TYPEPET || CHAR_getWorkInt(pet,CHAR_WORKPLAYERINDEX)!=c)return 0;
        f->pet_mask|=1<<i;f->pet_power[i]=actor_power(pet);
        f->loadout_hash=hash_actor(f->loadout_hash,pet);
        for(j=0;j<CHAR_MAXPETSKILLHAVE;j++)f->loadout_hash=hash_number(f->loadout_hash,CHAR_getPetSkill(pet,j));
    }
    return 1;
}
static int contact(int c,int slot)
{
    ADDRESSBOOK_entry *entry;int i;LadderProfile f;
    if(slot<0 || slot>=ADDRESSBOOK_MAX)return -1;
    entry=CHAR_getAddressbookEntry(c,slot);if(!entry || !entry->use)return -1;
    if(!StoneAge_CharacterIdentityValid(entry->persistent_character_id))return LADDER_CONTACT_IDENTITY_REQUIRED;
    for(i=0;i<CHAR_getPlayerMaxNum();i++)if(valid_player(i) && online(i) &&
        !strcmp(entry->cdkey,CHAR_getChar(i,CHAR_CDKEY)) && !strcmp(entry->charname,CHAR_getChar(i,CHAR_NAME)) && profile(i,&f))
        return strcmp(entry->persistent_character_id,f.id)?LADDER_CONTACT_IDENTITY_CHANGED:i;
    return -1;
}
static int contact_entry(int c,int slot,LadderContact *out)
{
    ADDRESSBOOK_entry *entry;size_t i;
    if(!valid_player(c) || slot<0 || slot>=ADDRESSBOOK_MAX)return 0;
    entry=CHAR_getAddressbookEntry(c,slot);if(!entry || !entry->use)return 0;
    memset(out,0,sizeof(*out));
    snprintf(out->id,sizeof(out->id),"%s",StoneAge_LadderContactIdentity(entry));
    for(i=0;i<sizeof(entry->charname) && entry->charname[i] && i<64;i++)
        snprintf(out->name_hex+i*2,sizeof(out->name_hex)-i*2,"%02x",(unsigned char)entry->charname[i]);
    out->online=contact(c,slot)>=0;return 1;
}
void StoneAge_LadderBindContact(int c,void *value)
{
    ADDRESSBOOK_entry *entry=value;const char *id;
    if(!entry)return;
    entry->persistent_character_id[0]=0;
    if(!valid_player(c))return;
    id=StoneAge_CharacterIdentityGetLoadedByIndex(c);
    if(id && StoneAge_CharacterIdentityValid(id))
        snprintf(entry->persistent_character_id,sizeof(entry->persistent_character_id),"%s",id);
}
const char *StoneAge_LadderContactIdentity(const void *value)
{
    const ADDRESSBOOK_entry *entry=value;
    return entry && StoneAge_CharacterIdentityValid(entry->persistent_character_id)?entry->persistent_character_id:"";
}
void StoneAge_LadderParseContact(void *value,const char *record)
{
    ADDRESSBOOK_entry *entry=value;char id[128];
    if(!entry)return;
    entry->persistent_character_id[0]=0;id[0]=0;
    if(record && getStringFromIndexWithDelim((char*)record,"|",7,id,sizeof(id)) && StoneAge_CharacterIdentityValid(id))
        snprintf(entry->persistent_character_id,sizeof(entry->persistent_character_id),"%s",id);
}
void StoneAge_LadderRefreshContacts(int left,int right)
{
    int sides[2],i,slot,c,target;ADDRESSBOOK_entry *entry;
    if(!valid_player(left) || !valid_player(right))return;
    sides[0]=left;sides[1]=right;
    for(i=0;i<2;i++) {
        c=sides[i];target=sides[1-i];
        slot=ADDRESSBOOK_getIndexInAddressbook(c,CHAR_getChar(target,CHAR_CDKEY),CHAR_getChar(target,CHAR_NAME));
        if(slot<0)continue;
        entry=CHAR_getAddressbookEntry(c,slot);
        if(entry && entry->use)StoneAge_LadderBindContact(target,entry);
    }
}
static void send_wire(int c,const char *wire)
{ if(valid_player(c) && online(c))lssproto_S_send(CHAR_getWorkInt(c,CHAR_WORKFD),(char*)wire); }
static NativeMatch *native_battle(int battle)
{
    int i;if(battle<0)return NULL;
    for(i=0;i<LADDER_MATCH_MAX;i++)if(native_matches[i].used && native_matches[i].battle==battle)return &native_matches[i];
    return NULL;
}
static NativeMatch *native_character(int c)
{
    int i,j;for(i=0;i<LADDER_MATCH_MAX;i++)if(native_matches[i].used)
        for(j=0;j<native_matches[i].count;j++)if(native_matches[i].characters[j]==c)return &native_matches[i];
    return NULL;
}
static int owner(int c)
{
    if(!CHAR_CHECKINDEX(c))return -1;
    if(CHAR_getInt(c,CHAR_WHICHTYPE)==CHAR_TYPEPLAYER)return c;
    if(CHAR_getInt(c,CHAR_WHICHTYPE)==CHAR_TYPEPET)return CHAR_getWorkInt(c,CHAR_WORKPLAYERINDEX);
    return -1;
}
static SavedActor *saved_actor(int c)
{
    int i,j;SavedActor *s;
    if(!CHAR_CHECKINDEX(c))return NULL;
    for(i=0;i<LADDER_MATCH_MAX;i++)if(native_matches[i].used)
        for(j=0;j<native_matches[i].actor_count;j++) {
            s=&native_matches[i].actors[j];
            if(s->character==c && s->before && s->before->CharMakeSequenceNumber==CHAR_getCharMakeSequenceNumber(c))return s;
        }
    return NULL;
}
static SavedActor *saved_pointer(void *character)
{
    int i,j;SavedActor *s;
    if(!character)return NULL;
    for(i=0;i<LADDER_MATCH_MAX;i++)if(native_matches[i].used)
        for(j=0;j<native_matches[i].actor_count;j++) {
            s=&native_matches[i].actors[j];
            if(CHAR_getCharPointer(s->character)==character && saved_actor(s->character)==s)return s;
        }
    return NULL;
}
char *StoneAge_LadderSavedCharacter(void *character)
{ SavedActor *s=saved_pointer(character);return s?s->save_data:NULL; }
char *StoneAge_LadderSavedOptions(void *character)
{ SavedActor *s=saved_pointer(character);return s?s->save_options:NULL; }
char *StoneAge_LadderSavedPet(int c)
{ SavedActor *s=saved_actor(c);return s?s->save_pet:NULL; }
int StoneAge_LadderAppendPetItems(int c,char *data,size_t capacity)
{
    int slot,item,n;size_t used=strlen(data);char *value;
    if(used>=capacity)return 0;
    for(slot=0;slot<CHAR_MAXITEMHAVE;slot++) {
        item=CHAR_getItemIndex(c,slot);if(!ITEM_CHECKINDEX(item))continue;
        /* Mode 1 uses commas within an item and already escapes its strings,
         * keeping the enclosing pet's pipe-separated fields unambiguous. */
        value=ITEM_makeStringFromItemIndex(item,1);if(!value || !*value)return 0;
        n=snprintf(data+used,capacity-used,"pitem%d:%s|",slot,value);
        if(n<0 || (size_t)n>=capacity-used)return 0;used+=(size_t)n;
    }
    return used+32<capacity;
}
int StoneAge_LadderParsePetItem(void *character,const char *key,char *value)
{
    Char *ch=(Char*)character;ITEM_Item item;char *end;long slot;int index;
    if(strncmp(key,"pitem",5))return 0;
    if(key[5]<'0' || key[5]>'9')return -1;
    slot=strtol(key+5,&end,10);
    if(*end || slot<0 || slot>=CHAR_MAXITEMHAVE || ch->indexOfExistItems[slot]!=-1)return -1;
    if(!ITEM_makeExistItemsFromStringToArg(value,&item,1))return -1;
    index=ITEM_initExistItemsOne(&item);if(index<0)return -1;
    ch->indexOfExistItems[slot]=index;return 1;
}
int StoneAge_LadderRetainsItem(int item)
{
    int i,j,k;
    for(i=0;i<LADDER_MATCH_MAX;i++)if(native_matches[i].used)
        for(j=0;j<native_matches[i].actor_count;j++)
            for(k=0;k<native_matches[i].actors[j].item_count;k++)
                if(native_matches[i].actors[j].items[k].index==item)return 1;
    return 0;
}
static int save_actor(NativeMatch *m,int c)
{
    SavedActor *s;Char *ch;int i,item,pet,count=0;char *data;
    if(!CHAR_CHECKINDEX(c) || m->actor_count>=60 || saved_actor(c))return 0;
    s=&m->actors[m->actor_count++];s->character=c;ch=CHAR_getCharPointer(c);
    s->before=malloc(sizeof(Char));if(!s->before)return 0;memcpy(s->before,ch,sizeof(Char));
    for(i=0;i<CHAR_MAXITEMHAVE;i++)if(ITEM_CHECKINDEX(ch->indexOfExistItems[i]))count++;
    if(count) {s->items=calloc(count,sizeof(SavedItem));if(!s->items)return 0;}
    for(i=0;i<CHAR_MAXITEMHAVE;i++) {
        item=ch->indexOfExistItems[i];if(!ITEM_CHECKINDEX(item))continue;
        /* Duplicate ownership would make restoring either actor ambiguous. */
        if(StoneAge_LadderRetainsItem(item))return 0;
        s->items[s->item_count].index=item;
        memcpy(&s->items[s->item_count++].value,ITEM_getItemPointer(item),sizeof(ITEM_Item));
    }
    if(CHAR_getInt(c,CHAR_WHICHTYPE)==CHAR_TYPEPLAYER) {
        /* The legacy character serializer skips pets whose serializer fails.
         * Validate every carried/banked pet, including nonparticipants, so a
         * checkpoint can never overwrite an archive with a missing pet. Its
         * parser also has a 4096-byte per-line limit, including the key. */
        for(i=0;i<CHAR_MAXPETHAVE+CHAR_MAXPOOLPETHAVE;i++) {
            pet=i<CHAR_MAXPETHAVE?ch->unionTable.indexOfPet[i]:ch->indexOfPoolPet[i-CHAR_MAXPETHAVE];
            if(!CHAR_CHECKINDEX(pet))continue;
            data=CHAR_makePetStringFromPetIndex(pet);
            if(!data || !*data || strlen(data)+32>=4096)return 0;
        }
        data=CHAR_makeStringFromCharData(ch);if(!data || !*data)return 0;
        s->save_data=strdup(data);if(!s->save_data)return 0;
        data=CHAR_makeOptionString(ch);s->save_options=strdup(data?data:"");if(!s->save_options)return 0;
    } else {
        data=CHAR_makePetStringFromPetIndex(c);if(!data || !*data)return 0;
        s->save_pet=strdup(data);if(!s->save_pet)return 0;
    }
    return 1;
}
static void restore_actors(NativeMatch *m)
{
    int i,j,c,fd,object;Char *ch;SavedActor *s;
    restoring=1;
    /* Restore all item objects before returning them to their original slots.
     * Native consumption still removes them from the active inventory; only
     * allocator release is deferred until the match no longer owns them. */
    for(i=0;i<m->actor_count;i++) {
        s=&m->actors[i];
        for(j=0;j<s->item_count;j++)if(ITEM_CHECKINDEX(s->items[j].index))
            memcpy(ITEM_getItemPointer(s->items[j].index),&s->items[j].value,sizeof(ITEM_Item));
    }
    for(i=0;i<m->actor_count;i++) {
        s=&m->actors[i];c=s->character;if(!s->before || saved_actor(c)!=s)continue;
        ch=CHAR_getCharPointer(c);fd=ch->workint[CHAR_WORKFD];object=ch->workint[CHAR_WORKOBJINDEX];
        memcpy(ch->data,s->before->data,sizeof(ch->data));
        memcpy(ch->string,s->before->string,sizeof(ch->string));
        memcpy(ch->flg,s->before->flg,sizeof(ch->flg));
        memcpy(ch->haveSkill,s->before->haveSkill,sizeof(ch->haveSkill));
        memcpy(ch->indexOfExistItems,s->before->indexOfExistItems,sizeof(ch->indexOfExistItems));
        ch->unionTable=s->before->unionTable;
        memcpy(ch->workint,s->before->workint,sizeof(ch->workint));
        memcpy(ch->workchar,s->before->workchar,sizeof(ch->workchar));
        /* Authenticated reconnect and the world object outlive battle state. */
        ch->workint[CHAR_WORKFD]=fd;ch->workint[CHAR_WORKOBJINDEX]=object;
    }
    restoring=0;
}
static void release_actors(NativeMatch *m)
{
    int i;for(i=0;i<m->actor_count;i++) {
        SavedActor *s=&m->actors[i];
        free(s->before);free(s->items);free(s->save_data);free(s->save_options);free(s->save_pet);
        memset(s,0,sizeof(*s));
    }
    m->actor_count=0;
}
static void send_status(int c)
{
    int i,flags=0;char category[8],*observation;
    if(!online(c))return;
    CHAR_sendStatusString(c,"C");CHAR_sendStatusString(c,"P");CHAR_sendStatusString(c,"I");CHAR_sendStatusString(c,"S");CHAR_sendStatusString(c,"D");
    for(i=0;i<5;i++) {snprintf(category,sizeof(category),"K%d",i);CHAR_sendStatusString(c,category);snprintf(category,sizeof(category),"W%d",i);CHAR_sendStatusString(c,category);}
    lssproto_KS_send(CHAR_getWorkInt(c,CHAR_WORKFD),CHAR_getInt(c,CHAR_DEFAULTPET),TRUE);
    /* A retained-seat login bypasses CHAR_login. Restore the current flags
     * without applying a fresh login's resets to the existing character. */
    if(CHAR_getFlg(c,CHAR_ISPARTY))flags|=CHAR_FS_PARTY;
    if(CHAR_getFlg(c,CHAR_ISDUEL))flags|=CHAR_FS_DUEL;
    if(CHAR_getFlg(c,CHAR_ISPARTYCHAT))flags|=CHAR_FS_PARTYCHAT;
    if(CHAR_getFlg(c,CHAR_ISTRADECARD))flags|=CHAR_FS_TRADECARD;
    if(CHAR_getFlg(c,CHAR_ISTRADE))flags|=CHAR_FS_TRADE;
    lssproto_FS_send(CHAR_getWorkInt(c,CHAR_WORKFD),flags);
    observation=StoneAge_AIObservationMake(c);if(observation)lssproto_S_send(CHAR_getWorkInt(c,CHAR_WORKFD),observation);
}
static NativeMatch *native_id(const char *id)
{
    int i;for(i=0;i<LADDER_MATCH_MAX;i++)if(native_matches[i].used && !strcmp(native_matches[i].id,id))return &native_matches[i];
    return NULL;
}
static void cancel_prepare(const char *id)
{
    NativeMatch *m=native_id(id);
    if(!m || m->battle>=0)return;
    /* No combat has run, so do not overwrite unrelated idle work fields.
     * Late save replies are discarded by their unique request tokens. */
    release_actors(m);m->used=0;
}
static size_t escaped_length(const char *value,const char *escaped)
{
    const unsigned char *p=(const unsigned char*)value;size_t n=0;
    for(;*p;p++) {
        if(*p>=0x80 && p[1]){n+=2;p++;}
        else n+=strchr(escaped,*p)?2:1;
    }
    return n;
}
static int checkpoint_fits(SavedActor *s)
{
    const char *name=CHAR_getChar(s->character,CHAR_NAME),*account=CHAR_getChar(s->character,CHAR_CDKEY);
    size_t wire=128+escaped_length(account,"\\ \n\r")+escaped_length(name,"\\ \n\r")+
        escaped_length(s->save_options,"\\ \n\r")+escaped_length(s->save_data,"\\ \n\r");
    size_t stored=8+escaped_length(name,"\\\n,|")+escaped_length(s->save_options,"\\\n,|")+
        escaped_length(s->save_data,"\\\n,|");
    /* Both legacy encoders can silently truncate. Reject before sending any
     * participant's save, rather than persisting a truncated live archive. */
    return wire<saacproto.workbufsize && wire<SAAC_ARCHIVE_MAX && stored<SAAC_ARCHIVE_MAX;
}
static int prepare(const char *id,const int *characters,const int *masks,int mode)
{
    int i,j,c,slot,failed=0,pending=0;NativeMatch *m=native_id(id);LadderProfile f;
    SavedActor *s;
    if(m) {
        if(m->count!=mode*2)return -1;
        for(i=0;i<m->count;i++)if(m->characters[i]!=characters[i] || m->masks[i]!=masks[i])return -1;
        for(i=0;i<m->actor_count;i++) {
            s=&m->actors[i];
            if(saved_actor(s->character)!=s || s->save_confirmed<0)return -1;
            if(s->save_data && !s->save_confirmed)pending=1;
        }
        return !pending;
    }
    for(slot=0;slot<LADDER_MATCH_MAX;slot++)if(!native_matches[slot].used)break;
    if(slot==LADDER_MATCH_MAX)return -1;
    for(i=0;i<mode*2;i++)if(!profile(characters[i],&f) || !f.online || !f.idle)return -1;
    m=&native_matches[slot];memset(m,0,sizeof(*m));m->used=1;m->battle=-1;m->count=mode*2;m->last_turn=-1;m->forced_winner=-1;
    snprintf(m->id,sizeof(m->id),"%s",id);
    for(i=0;i<mode*2;i++){m->characters[i]=characters[i];m->masks[i]=masks[i];}
    for(i=0;i<mode*2;i++) {
        c=characters[i];if(!save_actor(m,c)){failed=1;break;}
        for(j=0;j<5;j++)if(masks[i]&(1<<j))if(!save_actor(m,CHAR_getCharPet(c,j))){failed=1;break;}
        if(failed)break;
    }
    if(failed){cancel_prepare(id);return -1;}
    for(i=0;i<m->actor_count;i++)if(m->actors[i].save_data && !checkpoint_fits(&m->actors[i])) {
        cancel_prepare(id);return -1;
    }
    /* Save the actual pre-match character archive (including all pets and
     * inventory), without unlocking the account or touching its connection.
     * Every participant's immutable snapshot exists before the first send. */
    for(i=0;i<m->actor_count;i++) {
        s=&m->actors[i];if(!s->save_data)continue;
        if(next_save_request==INT_MIN){cancel_prepare(id);return -1;}
        c=s->character;s->save_request=--next_save_request;
#ifdef _NEWSAVE
        saacproto_ACCharSave_send(acfd,CHAR_getChar(c,CHAR_CDKEY),CHAR_getChar(c,CHAR_NAME),s->save_options,s->save_data,FALSE,s->save_request,CHAR_getInt(c,CHAR_SAVEINDEXNUMBER));
#else
        saacproto_ACCharSave_send(acfd,CHAR_getChar(c,CHAR_CDKEY),CHAR_getChar(c,CHAR_NAME),s->save_options,s->save_data,FALSE,s->save_request);
#endif
    }
    return prepare(id,characters,masks,mode);
}
static int start(const char *id,const int *characters,const int *masks,int mode)
{
    int i,j,c,b,pet,side,failed=0;NativeMatch *m=native_id(id);LadderProfile f;
    if(!m || m->battle>=0 || prepare(id,characters,masks,mode)!=1)return -1;
    for(i=0;i<mode*2;i++)if(!profile(characters[i],&f) || !f.online || !f.idle)return -1;
    b=BATTLE_CreateBattle();if(b<0)return -1;m->battle=b;
    BattleArray[b].type=BATTLE_TYPE_P_vs_P;BattleArray[b].field_no=0;BattleArray[b].leaderindex=characters[0];
    BattleArray[b].dpbattle=0;BattleArray[b].winside=0;
    for(side=0;side<2;side++){BattleArray[b].Side[side].type=BATTLE_S_TYPE_PLAYER;BattleArray[b].Side[side].flg=0;}
    restoring=1;
    for(i=0;i<m->actor_count;i++) {c=m->actors[i].character;CHAR_setInt(c,CHAR_HP,CHAR_getWorkInt(c,CHAR_WORKMAXHP));CHAR_setInt(c,CHAR_MP,CHAR_getWorkInt(c,CHAR_WORKMAXMP));CHAR_setFlg(c,CHAR_ISDIE,0);}
    entering=1;
    for(i=0;i<mode*2;i++) {
        c=characters[i];side=i/mode;
        if(BATTLE_NewEntry(c,b,side) || BATTLE_PetDefaultEntry(c,b,side)){failed=1;break;}
        j=CHAR_getInt(c,CHAR_DEFAULTPET);
        if(j>=0) {
            pet=CHAR_getCharPet(c,j);
            if(!CHAR_CHECKINDEX(pet) || CHAR_getWorkInt(pet,CHAR_WORKBATTLEINDEX)!=b){failed=1;break;}
        }
        BATTLE_ClearGetExp(c);
    }
    entering=restoring=0;
    if(failed) {restore_actors(m);BATTLE_DeleteBattle(b);release_actors(m);m->used=0;return -1;}
    for(i=0;i<mode*2;i++) {
        c=characters[i];send_status(c);lssproto_EN_send(CHAR_getWorkInt(c,CHAR_WORKFD),BATTLE_TYPE_P_vs_P,0);CHAR_sendBattleEffect(c,ON);
        for(j=0;j<5;j++){pet=CHAR_getCharPet(c,j);if(CHAR_CHECKINDEX(pet) && status_sources)memset(status_sources[pet],-1,sizeof(status_sources[pet]));}
        if(status_sources)memset(status_sources[c],-1,sizeof(status_sources[c]));
    }
    return b;
}
static void abandon(int c)
{
    NativeMatch *m=native_character(c);int s,i,v;
    if(!m)return;
    for(s=0;s<2;s++)for(i=0;i<BATTLE_ENTRY_MAX;i++) {
        v=BattleArray[m->battle].Side[s].Entry[i].charaindex;
        if(CHAR_CHECKINDEX(v) && owner(v)==c) {
            BattleArray[m->battle].Side[s].Entry[i].charaindex=-1;
            CHAR_setWorkInt(v,CHAR_WORKBATTLEMODE,BATTLE_CHARMODE_FINAL);
        }
    }
}
static void force_finish(int b,int winner,const char *reason)
{
    NativeMatch *m=native_battle(b);if(!m)return;
    m->forced_winner=winner;m->forced_reason=reason;
    BattleArray[b].winside=winner==0?-1:winner==1?1:0;
    BATTLE_FinishSet(b);
}
static void offline_save(int c)
{
    Char *ch;char *data;int request;
    if(!valid_player(c) || online(c) || native_players[c].pending_save || now_ms()<native_players[c].retry_at)return;
    ch=CHAR_getCharPointer(c);data=CHAR_makeStringFromCharData(ch);if(!data || !*data)return;
    /* Tokens are not native character indices: delayed/duplicate replies can
     * never delete a newer occupant of a reused slot. Do not wrap tokens. */
    if(next_save_request==INT_MIN)return;
    request=--next_save_request;native_players[c].save_request=request;
    native_players[c].pending_save=1;native_players[c].save_at=now_ms();
#ifdef _NEWSAVE
    saacproto_ACCharSave_send(acfd,CHAR_getChar(c,CHAR_CDKEY),CHAR_getChar(c,CHAR_NAME),CHAR_makeOptionString(ch),data,TRUE,request,CHAR_getInt(c,CHAR_SAVEINDEXNUMBER));
#else
    saacproto_ACCharSave_send(acfd,CHAR_getChar(c,CHAR_CDKEY),CHAR_getChar(c,CHAR_NAME),CHAR_makeOptionString(ch),data,TRUE,request);
#endif
}
static void initialize(void)
{
    LadderHooks h;const char *path=getenv("STONEAGE_LADDER_DB");int i;
    if(initialized)return;initialized=1;
    memset(&h,0,sizeof(h));h.now=now_ms;h.profile=profile;h.contact=contact;h.contact_entry=contact_entry;h.send=send_wire;h.start=start;h.abandon=abandon;h.finish=force_finish;
    h.prepare=prepare;h.cancel_prepare=cancel_prepare;
    enabled=Ladder_Init(path,&h);
    if(!enabled){fprintf(stderr,"Ladder unavailable: configure a writable STONEAGE_LADDER_DB\n");return;}
    chmod(path,0600);
    status_sources=malloc(sizeof(*status_sources)*CHAR_getCharNum());
    if(status_sources)for(i=0;i<CHAR_getCharNum();i++)memset(status_sources[i],-1,sizeof(status_sources[i]));
    else {enabled=0;Ladder_Shutdown();fprintf(stderr,"Ladder unavailable: status attribution allocation failed\n");}
}
void StoneAge_LadderTick(void)
{
    int c;initialize();if(!enabled)return;
    Ladder_Tick();
    for(c=0;c<CHAR_getPlayerMaxNum() && c<LADDER_PLAYER_MAX;c++)if(native_players[c].retained && !native_character(c) && valid_player(c)) {
        if(native_players[c].pending_save && now_ms()-native_players[c].save_at>10000)native_players[c].pending_save=0;
        offline_save(c);
    }
}
void StoneAge_BattleRulesChanged(void)
{
    int c,fd;
    if(!getenv("STONEAGE_BATTLE_RULESET_ID"))return;
    unsetenv("STONEAGE_BATTLE_RULESET_ID");
    /* Clear cached client metadata too: a commander already in battle must
     * not continue inferring against an obsolete startup digest. */
    for(c=0;c<CHAR_getPlayerMaxNum();c++)if(valid_player(c)) {
        fd=CHAR_getWorkInt(c,CHAR_WORKFD);
        if(fd>=0 && CONNECT_isCLI(fd) && CONNECT_isLOGIN(fd))
            lssproto_S_send(fd,"BTRULES||");
    }
}

int StoneAge_LadderRequest(int fd,const char *category)
{
	/* Additive capability query: do not change the legacy BTIME shape. */
	if(category && !strcmp(category,"BTRULES")) {
		const char *rules=getenv("STONEAGE_BATTLE_RULESET_ID");
		char reply[128];
#if defined(__aarch64__)
		const char *platform="linux-arm64";
#elif defined(__x86_64__)
		const char *platform="linux-amd64";
#else
		const char *platform="linux-other";
#endif
		if(!CONNECT_isCLI(fd) || !CONNECT_isLOGIN(fd) || !valid_player(CONNECT_getCharaindex(fd)))return 1;
		if(!rules || strlen(rules)!=64 || strspn(rules,"0123456789abcdef")!=64)rules="";
		snprintf(reply,sizeof(reply),"BTRULES|%s|%s",rules,platform);
		lssproto_S_send(fd,reply);
		return 1;
	}
    /* Optional observer-only clock. Never initialize/extend a command timer:
     * that remains exclusively owned by StoneAge_LadderCommandWait. */
    if(category && !strcmp(category,"BTIME")) {
        int c;NativeMatch *m;char reply[256];LadderTime deadline=0;
        if(!CONNECT_isCLI(fd) || !CONNECT_isLOGIN(fd))return 1;
        c=CONNECT_getCharaindex(fd);
        if(!valid_player(c))return 1;
        m=native_character(c);
        if(m && !BATTLE_CHECKINDEX(m->battle))m=NULL;
        if(m && m->last_turn==BattleArray[m->battle].turn)deadline=m->deadline;
        snprintf(reply,sizeof(reply),"BTIME|%s|%d|%lld|%lld|stoneage-native-ladder-v1",
                 m?m->id:"",m?BattleArray[m->battle].turn:-1,
                 (long long)deadline,(long long)now_ms());
        lssproto_S_send(fd,reply);
        return 1;
    }
    if(!category || strncmp(category,"LADDER|",7))return 0;
    initialize();
    if(CONNECT_isCLI(fd) && CONNECT_isLOGIN(fd))Ladder_Request(CONNECT_getCharaindex(fd),category);
    return 1;
}
int StoneAge_LadderIsBattle(int b){return native_battle(b)!=NULL || StoneAge_BattleEnvironmentIsBattle(b);}
int StoneAge_LadderEntryAllowed(int c,int b)
{
    int p=owner(c),slot;
    if(entering)return 1;
    if(native_battle(b)) {
        if(p==c || !valid_player(p) || Ladder_CharacterBattle(p)!=b)return 0;
        for(slot=0;slot<5;slot++)if(CHAR_getCharPet(p,slot)==c)return Ladder_PetAllowed(p,slot);
        return 0;
    }
    return !Ladder_Reserved(p);
}
int StoneAge_LadderPetAllowed(int c,int slot){return Ladder_PetAllowed(c,slot);}
int StoneAge_LadderReserved(int c){int p=owner(c);return enabled && valid_player(p) && Ladder_Reserved(p);}
int StoneAge_LadderInteractionAllowed(int c,int target)
{
    int source=owner(c),destination=owner(target);
    if(!enabled)return 1;
    if(Ladder_Reserved(source) || Ladder_Reserved(destination)) {
        if(valid_player(source))Ladder_Rejected(source);
        return 0;
    }
    return 1;
}
int StoneAge_LadderGuard(int fd,const char *op)
{
    int c,i,member;
    if(!enabled || !CONNECT_isLOGIN(fd))return 0;
    c=CONNECT_getCharaindex(fd);if(!valid_player(c))return 0;
    /* EO from a death watchdog must never detach a ladder seat. The engine
     * finishes the whole match and the separate ladder ack closes its result. */
    if(!strcmp(op,"EO") && Ladder_Reserved(c))return 1;
    /* M only reads a map rectangle. Echo is transport keepalive: blocking it
     * while queued/in battle makes clients time out and reconnect, even when
     * other observations keep flowing. Neither mutates reserved resources. */
    if(!strcmp(op,"B") || !strcmp(op,"EO") || !strcmp(op,"S") || !strcmp(op,"AB") || !strcmp(op,"M") || !strcmp(op,"Echo"))return 0;
    if(Ladder_Reserved(c)){Ladder_Rejected(c);return 1;}
    /* An ordinary party leader cannot move/rescue/enter battle with a member
     * reserved by a different ladder room. */
    if(!strcmp(op,"W") || !strcmp(op,"EV") || !strcmp(op,"DU") || !strcmp(op,"JB")) {
        for(i=1;i<CHAR_PARTYMAX;i++){member=CHAR_getWorkInt(c,CHAR_WORKPARTYINDEX1+i);if(valid_player(member) && Ladder_Reserved(member)){Ladder_Rejected(member);return 1;}}
    }
    return 0;
}
void StoneAge_LadderWindow(int c,int open)
{if(valid_player(c))native_players[c].window_open=open;}
void StoneAge_LadderWindowSent(int c,int type,int sequence,int object,const char *data)
{
    /* The native login notification is inert in both clients. It must neither
     * block preparation nor dismiss a real NPC dialog already in progress. */
    if(type==28 && sequence<0 && object<0 &&
       (!data || data[strspn(data," \t\r\n\v\f")]=='\0'))return;
    StoneAge_LadderWindow(c,1);
}
void StoneAge_LadderAuthenticated(int fd,int accepted)
{
    if(fd<0 || fd>=AUTH_SLOTS || !CONNECT_checkfd(fd))return;
    authenticated[fd].fdid=accepted?CONNECT_getFdid(fd):-1;
    if(accepted)CONNECT_getCdkey(fd,authenticated[fd].account,sizeof(authenticated[fd].account));
    else authenticated[fd].account[0]=0;
}
int StoneAge_LadderDisconnect(int fd)
{
    int c;
    /* Hard close marks the socket unused before invoking CHAR_logout. The
     * character still points to that fd until we detach it here. */
    if(!enabled || fd<0 || fd>=ConnectLen)return 0;
    c=CONNECT_getCharaindex(fd);if(!valid_player(c))return 0;
    if(CHAR_getWorkInt(c,CHAR_WORKFD)!=fd)return 0;
    Ladder_Disconnected(c);
    if(!native_character(c))return 0;
    native_players[c].retained=1;CHAR_setWorkInt(c,CHAR_WORKFD,-1);
    CONNECT_setCharaindex(fd,-1);CONNECT_setState(fd,NOTLOGIN);return 1;
}
void StoneAge_LadderCharacterLoaded(int c)
{ if(enabled && valid_player(c)){memset(&native_players[c],0,sizeof(native_players[c]));Ladder_Reconnected(c);} }
void StoneAge_LadderCharacterDeleted(int c)
{
    if(!enabled || !valid_player(c))return;
    Ladder_Detached(c);memset(&native_players[c],0,sizeof(native_players[c]));
}
int StoneAge_LadderResume(int fd,const char *name)
{
    int c,b,i,s,actor,accepted;char account[CDKEYLEN],turn[64];NativeMatch *m;
    if(!enabled)return 0;
    if(fd<0 || fd>=AUTH_SLOTS || !CONNECT_checkfd(fd))return 1;
    CONNECT_getCdkey(fd,account,sizeof(account));
    if(authenticated[fd].fdid!=CONNECT_getFdid(fd) || !authenticated[fd].account[0] || strcmp(account,authenticated[fd].account)) {
        lssproto_CharLogin_send(fd,FAILED,"Client login required");return 1;
    }
    for(c=0;c<CHAR_getPlayerMaxNum() && c<LADDER_PLAYER_MAX;c++)if(valid_player(c) && native_players[c].retained && !strcmp(account,CHAR_getChar(c,CHAR_CDKEY))) {
        if(strcmp(name,CHAR_getChar(c,CHAR_NAME)) || native_players[c].pending_save) {
            lssproto_CharLogin_send(fd,FAILED,"Ladder character is still reserved");return 1;
        }
        CONNECT_setCharname(fd,(char*)name);CONNECT_setCharaindex(fd,c);CONNECT_setState(fd,LOGIN);CHAR_setWorkInt(c,CHAR_WORKFD,fd);
        native_players[c].retained=0;lssproto_CharLogin_send(fd,SUCCESSFUL,"");send_status(c);Ladder_Reconnected(c);
        m=native_character(c);b=Ladder_CharacterBattle(c);
        if(m && b>=0) {
            lssproto_EN_send(fd,BATTLE_TYPE_P_vs_P,BattleArray[b].field_no);
            resync_target=c;BATTLE_CharSendAll(b);resync_target=-1;
            accepted=0;
            for(s=0;s<2;s++)for(i=0;i<BATTLE_ENTRY_MAX;i++) {
                actor=BattleArray[b].Side[s].Entry[i].charaindex;
                if(CHAR_CHECKINDEX(actor) && CHAR_getWorkInt(actor,CHAR_WORKBATTLEMODE)==BATTLE_CHARMODE_C_OK)accepted|=1<<(s*BATTLE_ENTRY_MAX+i);
            }
            snprintf(turn,sizeof(turn),"BA|%X|%X|",accepted,BattleArray[b].turn);
            BATTLE_CommandSend(c,turn);
        }
        return 1;
    }
    return 0;
}
int StoneAge_LadderOfflineSaveReply(int request,const char *result)
{
    int c,i,j;SavedActor *s;
    if(request>=-OFFLINE_SAVE_BASE)return 0;
    for(i=0;i<LADDER_MATCH_MAX;i++)if(native_matches[i].used && native_matches[i].battle<0)
        for(j=0;j<native_matches[i].actor_count;j++) {
            s=&native_matches[i].actors[j];
            if(s->save_request==request) {
                if(!s->save_confirmed && saved_actor(s->character)==s)
                    s->save_confirmed=!strcmp(result,SUCCESSFUL)?1:-1;
                return 1;
            }
        }
    for(c=0;c<LADDER_PLAYER_MAX;c++)if(native_players[c].save_request==request && native_players[c].pending_save)break;
    if(c==LADDER_PLAYER_MAX || !native_players[c].retained || !valid_player(c))return 1;
    native_players[c].pending_save=0;
    if(!strcmp(result,SUCCESSFUL)) {
        Ladder_Detached(c);
        ADDRESSBOOK_notifyLoginLogout(c,0);CHAR_CharaDeleteHavePet(c);CHAR_CharaDelete(c);
        memset(&native_players[c],0,sizeof(native_players[c]));
    } else native_players[c].retry_at=now_ms()+5000;
    return 1;
}
int StoneAge_LadderFinish(int b)
{
    NativeMatch *m=native_battle(b);int i,c,winner;
    if(!m)return 0;
    winner=m->forced_reason?m->forced_winner:BattleArray[b].winside==-1?0:BattleArray[b].winside==1?1:-1;
    Ladder_End(b,winner,BattleArray[b].turn,m->forced_reason?m->forced_reason:"defeat",1);
    restore_actors(m);
    for(i=0;i<m->count;i++) {
        c=m->characters[i];CHAR_sendBattleEffect(c,OFF);
        if(online(c)){BATTLE_CommandSend(c,"BU");send_status(c);}
    }
    BATTLE_DeleteBattle(b);release_actors(m);m->used=0;return 1;
}
/* A flying knockout removes an engine entry, not the player's membership in
 * the match. Keep those players observing the same public battle until the
 * authoritative arena settlement. Training uses this exact observer path. */
static int observation_members(int b,int *members)
{
    NativeMatch *m=native_battle(b);
    if(m){memcpy(members,m->characters,sizeof(int)*m->count);return m->count;}
    return StoneAge_BattleEnvironmentMembers(b,members,10);
}
void StoneAge_LadderRemovedObservation(int b,const char *roster,const char *vitals)
{
    int members[10],count=observation_members(b,members),i,c,side,slot,actor,accepted=0;
    char bp[64],ba[64];
    if(!count || !BATTLE_CHECKINDEX(b))return;
    for(side=0;side<2;side++)for(slot=0;slot<BATTLE_ENTRY_MAX;slot++) {
        actor=BattleArray[b].Side[side].Entry[slot].charaindex;
        if(CHAR_CHECKINDEX(actor) && CHAR_getWorkInt(actor,CHAR_WORKBATTLEMODE)==BATTLE_CHARMODE_C_OK)
            accepted|=1<<(side*BATTLE_ENTRY_MAX+slot);
    }
    snprintf(ba,sizeof(ba),"BA|%X|%X|",accepted,BattleArray[b].turn);
    for(i=0;i<count;i++) {
        c=members[i];
        if(!valid_player(c) || BATTLE_Index2No(b,c)>=0 || !StoneAge_LadderSendRecipient(c))continue;
        snprintf(bp,sizeof(bp),"BP|%X|%X|%X",i/(count/2)*10+i%(count/2),
            BP_FLG_PLAYER_MENU_OFF|BP_FLG_PET_MENU_OFF,CHAR_getInt(c,CHAR_MP));
        BATTLE_CommandSend(c,bp);
        BATTLE_CommandSend(c,(char*)roster);
        BATTLE_CommandSend(c,(char*)vitals);
        BATTLE_CommandSend(c,ba);
    }
}
void StoneAge_LadderRemovedMovie(int b,const char *movie,const int *delivered,int sent)
{
    int members[10],count=observation_members(b,members),i,j,c;
    if(!count || !BATTLE_CHECKINDEX(b))return;
    for(i=0;i<count;i++) {
        c=members[i];
        if(!valid_player(c) || BATTLE_Index2No(b,c)>=0)continue;
        /* Newly knocked-out entries are in this round's original EntryList
         * and already received the movie. Never duplicate their effects. */
        for(j=0;j<sent && delivered[j]!=c;j++);
        if(j==sent)BATTLE_CommandSend(c,(char*)movie);
    }
}
int StoneAge_LadderKnockout(int b,int c)
{
    int side,i,actor;
    /* Training must observe the same dead/removed actors as the arena. Normal
     * PvP's post-overkill revival to 1 HP is not an arena combat rule. */
    if(!StoneAge_LadderIsBattle(b))return 0;
    for(side=0;side<2;side++)for(i=0;i<BATTLE_ENTRY_MAX;i++) {
        actor=BattleArray[b].Side[side].Entry[i].charaindex;
        if(!CHAR_CHECKINDEX(actor))continue;
        if(actor==c || (valid_player(c) && owner(actor)==c)) {
            BattleArray[b].Side[side].Entry[i].charaindex=-1;
            CHAR_setWorkInt(actor,CHAR_WORKBATTLEMODE,BATTLE_CHARMODE_FINAL);
        }
    }
    if(CHAR_CHECKINDEX(c) && CHAR_getInt(c,CHAR_WHICHTYPE)==CHAR_TYPEPET) {
        int p=owner(c),slot=CHAR_getInt(p,CHAR_DEFAULTPET);
        if(slot>=0 && CHAR_getCharPet(p,slot)==c)CHAR_setInt(p,CHAR_DEFAULTPET,-1);
    }
    return 1;
}
int StoneAge_LadderCommandWait(int b,int side)
{
    NativeMatch *m=native_battle(b);int i,c,waiting=0;
    if(!m)return -1;
    if(m->last_turn!=BattleArray[b].turn){m->last_turn=BattleArray[b].turn;m->deadline=now_ms()+TURN_TIMEOUT_MS;}
    for(i=0;i<BATTLE_ENTRY_MAX;i++) {
        c=BattleArray[b].Side[side].Entry[i].charaindex;
        if(!CHAR_CHECKINDEX(c) || CHAR_getFlg(c,CHAR_ISDIE))continue;
        if(CHAR_getWorkInt(c,CHAR_WORKBATTLEMODE)==BATTLE_CHARMODE_C_WAIT) {
            if(BATTLE_IsCharge(c) || !BATTLE_CanMoveCheck(c)) {
                CHAR_setWorkInt(c,CHAR_WORKBATTLEMODE,BATTLE_CHARMODE_C_OK);continue;
            }
            if(now_ms()<m->deadline){waiting=1;continue;}
            if(CHAR_getWorkInt(c,CHAR_WORKBATTLECOM1)<=BATTLE_COM_NONE) {
                CHAR_setWorkInt(c,CHAR_WORKBATTLECOM1,CHAR_getInt(c,CHAR_WHICHTYPE)==CHAR_TYPEPLAYER?BATTLE_COM_GUARD:BATTLE_COM_WAIT);
                CHAR_setWorkInt(c,CHAR_WORKBATTLECOM2,-1);CHAR_setWorkInt(c,CHAR_WORKBATTLECOM3,-1);
            }
            CHAR_setWorkInt(c,CHAR_WORKBATTLEMODE,BATTLE_CHARMODE_C_OK);
        }
    }
    return !waiting;
}
int StoneAge_LadderSendRecipient(int c){return resync_target<0 || resync_target==c;}
int StoneAge_LadderEscape(int b,int bid)
{
    int c;
    if(!native_battle(b))return 0;
    c=BATTLE_No2Index(b,bid);
    if(valid_player(c))Ladder_Abandon(c);
    return 1;
}
int StoneAge_LadderCommandAllowed(int c,const char *command)
{
    NativeMatch *m=native_character(c);int actor=c,slot,pet;
    if(!m && (!CHAR_CHECKINDEX(c) || !StoneAge_BattleEnvironmentIsBattle(CHAR_getWorkInt(c,CHAR_WORKBATTLEINDEX))))return 1;
    if((m && Ladder_CharacterBattle(c)<0) || !command || !*command)return 0;
    /* Parameterized legacy handlers read command+2; J/I even match only
     * their first byte. Reject truncated packets before entering any parser. */
    if(strchr("HTSWJI",command[0]) && (command[1]!='|' || !command[2]))return 0;
    if(command[0]=='W')actor=CHAR_getCharPet(c,CHAR_getInt(c,CHAR_DEFAULTPET));
    if(command[0]=='S') {
        if(!strcmp(command+2,"-1"))slot=-1;
        else {
            slot=command[2]-'0';
            if(slot<0 || slot>=CHAR_MAXPETHAVE || command[3])return 0;
        }
        if(!Ladder_PetAllowed(c,slot))return 0;
        /* The legacy handler silently converts invalid standby/riding slots
         * to recall. Reject before dispatch in arena and training alike. */
        if(slot>=0) {
            pet=CHAR_getCharPet(c,slot);
            if(slot==CHAR_getInt(c,CHAR_DEFAULTPET) || slot==CHAR_getInt(c,CHAR_RIDEPET) ||
               !(CHAR_getWorkInt(c,CHAR_WORKSTANDBYPET)&(1<<slot)) ||
               CHAR_getWorkInt(c,CHAR_WORK_PET0_STAT+slot)!=PET_STAT_SELECT ||
               !CHAR_CHECKINDEX(pet) || CHAR_getInt(pet,CHAR_HP)<=0)return 0;
        } else if(CHAR_getInt(c,CHAR_DEFAULTPET)<0)return 0;
    }
    /* A command belongs to a single currently open actor menu. Reconnects
     * and duplicate packets cannot replace a previously accepted action. */
    if(!CHAR_CHECKINDEX(actor) || CHAR_getWorkInt(actor,CHAR_WORKBATTLEMODE)!=BATTLE_CHARMODE_C_WAIT)return 0;
    return 1;
}
int StoneAge_LadderResumeFlags(int c,int flags)
{
    int p;
    if(resync_target!=c)return flags;
    if(CHAR_getWorkInt(c,CHAR_WORKBATTLEMODE)!=BATTLE_CHARMODE_C_WAIT)flags|=BP_FLG_PLAYER_MENU_OFF;
    p=CHAR_getCharPet(c,CHAR_getInt(c,CHAR_DEFAULTPET));
    if(CHAR_CHECKINDEX(p) && CHAR_getWorkInt(p,CHAR_WORKBATTLEMODE)!=BATTLE_CHARMODE_C_WAIT)flags|=BP_FLG_PET_MENU_OFF;
    return flags;
}
void StoneAge_LadderExecuting(int b,int bid){current_source=native_battle(b)?BATTLE_No2Index(b,bid):-1;}
void StoneAge_LadderExecutionEnd(void){current_source=-1;}
int StoneAge_LadderSetHP(int source,int target,int hp)
{
    int previous=current_source,value;current_source=source;value=CHAR_setInt(target,CHAR_HP,hp);current_source=previous;return value;
}
void StoneAge_LadderStatusChanged(int c,int field,int before,int after)
{
    int i;
    if(!status_sources || !CHAR_CHECKINDEX(c) || restoring)return;
    for(i=1;i<BATTLE_ST_END;i++)if(field==StatusTbl[i]) {
        if(after>0 && (before<=0 || current_source>=0))status_sources[c][i]=current_source;
        if(after<=0)status_sources[c][i]=-1;
        return;
    }
}
void StoneAge_LadderPeriodicSource(int c,int field)
{
    int i;current_source=-1;
    if(!status_sources || !CHAR_CHECKINDEX(c))return;
    if(field<0){current_source=c;return;}
    for(i=1;i<BATTLE_ST_END;i++)if(field==StatusTbl[i]){current_source=status_sources[c][i];return;}
}
void StoneAge_LadderHPChanged(int c,int before,int after)
{
    NativeMatch *m;LadderStats delta;int target_owner,source_owner,source,ti=-1,si=-1,ai=-1,i,amount,pet,ride=0;
    if(restoring || before==after || !CHAR_CHECKINDEX(c))return;
    target_owner=owner(c);m=native_character(target_owner);if(!m || !Ladder_Battle(m->battle))return;
    source=current_source;
    source_owner=owner(source);
    for(i=0;i<m->count;i++){if(m->characters[i]==target_owner)ti=i;if(m->characters[i]==source_owner)si=i;}
    for(i=0;i<m->actor_count;i++)if(m->actors[i].character==c)ai=i;
    if(ti<0 || ai<0)return;
    before=before<0?0:before;after=after<0?0:after;
    pet=c!=target_owner;
    if(pet && CHAR_getCharPet(target_owner,CHAR_getInt(target_owner,CHAR_RIDEPET))==c)ride=1;
    if(after<before) {
        amount=before-after;memset(&delta,0,sizeof(delta));delta.damage_taken=amount;
        if(pet)delta.pet_damage_taken=amount;if(ride)delta.ride_damage_taken=amount;
        if(before>0 && after==0 && !pet)delta.deaths=1;
        Ladder_Stats(m->battle,target_owner,&delta);
        if(si>=0 && si/(m->count/2)!=ti/(m->count/2)) {
            memset(&delta,0,sizeof(delta));delta.damage=amount;if(source!=source_owner)delta.pet_damage=amount;
            if(before>0 && after==0){if(pet)delta.pet_kills=1;else delta.player_kills=1;}
            Ladder_Stats(m->battle,source_owner,&delta);m->contributors[ai]|=1<<si;
        }
        if(before>0 && after==0) {
            for(i=0;i<m->count;i++)if(i!=si && (m->contributors[ai]&(1<<i))) {memset(&delta,0,sizeof(delta));delta.assists=1;Ladder_Stats(m->battle,m->characters[i],&delta);}
            m->contributors[ai]=0;
        }
    } else if(si>=0 && si/(m->count/2)==ti/(m->count/2)) {
        int maxhp=CHAR_getWorkInt(c,CHAR_WORKMAXHP),effective=after>maxhp?maxhp:after;
        memset(&delta,0,sizeof(delta));delta.healing=effective>before?effective-before:0;
        if(before==0 && after>0){delta.revives=1;m->contributors[ai]=0;}
        Ladder_Stats(m->battle,source_owner,&delta);
    }
}

static int admin_battle_turn(int battle)
{ return native_battle(battle) && BATTLE_CHECKINDEX(battle)?BattleArray[battle].turn:-1; }
int StoneAge_LadderAdminSnapshot(char *out,size_t capacity,int offset)
{
    initialize();
    return enabled && Ladder_AdminSnapshot(out,capacity,offset,admin_battle_turn);
}
