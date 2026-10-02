/* Test-only executable linked to the actual GMSV character and battle engine.
 * No listener or SAAC connection is opened. Only time and network/persistence
 * boundaries of the adapter are intercepted; combat runs in BATTLE_Loop. */
#include "version.h"
#include <assert.h>
#include <sys/time.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <math.h>
#include <sqlite3.h>
#include "char.h"
#include "char_base.h"
#include "battle.h"
#include "battle_command.h"
#include "battle_event.h"
#include "buf.h"
#include "configfile.h"
#include "function.h"
#include "handletime.h"
#include "item.h"
#include "net.h"
#include "object.h"
#include "readmap.h"
#include <time.h>
#include "autil.h"
#include "pet_skill.h"
#include "trade.h"
#include "petmail.h"
#include "lssproto_serv.h"
#include "saacproto_cli.h"
#include "stoneage_battle_log.h"
#include "stoneage_character_identity.h"
#include "stoneage_ladder.h"

static long long test_clock=1000000;
static char last[32][24000];
static char capabilities[32][256];
static int packets[64],request_serial,saved_request,save_count,login_ok;
static int social_flags[64];
static int snapshot_allocations_before_failure=-1;
static const char *expected_saved_data;
static int checkpoint_auto_ack=1,checkpoint_count;
static int checkpoint_requests[32];
static char *checkpoint_data[32];
static void *smoke_malloc(size_t size)
{
    if(snapshot_allocations_before_failure==0)return NULL;
    if(snapshot_allocations_before_failure>0)snapshot_allocations_before_failure--;
    return malloc(size);
}
static int smoke_time(struct timeval *t,void *zone)
{ (void)zone;t->tv_sec=test_clock/1000;t->tv_usec=test_clock%1000*1000;return 0; }
static void smoke_send(int fd,char *wire)
{
    int c=CONNECT_getCharaindex(fd);
    if(c>=0 && c<32 && !strncmp(wire,"LADDER|",7))snprintf(last[c],sizeof(last[c]),"%s",wire);
    if(c>=0 && c<32 && !strncmp(wire,"BTRULES|",8))snprintf(capabilities[c],sizeof(capabilities[c]),"%s",wire);
}
static void smoke_login(int fd,char *result,char *data)
{ (void)fd;(void)data;login_ok=!strcmp(result,SUCCESSFUL); }
static void smoke_social(int fd,int flags)
{ assert(fd>=0 && fd<64);social_flags[fd]=flags; }
/* World/map presentation is outside this offline combat fixture. */
static int smoke_status(int c,char *category,char *file,int line)
{ (void)file;(void)line;(void)c;(void)category;return TRUE; }
static void smoke_effect(int c,int on){(void)c;(void)on;}
static char *smoke_options(Char *ch){(void)ch;return "offline test";}
static void smoke_delete(int c)
{ StoneAge_LadderCharacterDeleted(c);CHAR_endCharOneArray(c); }
static void smoke_save(int fd,char *account,char *name,char *options,char *data,int unlock,int request
#ifdef _NEWSAVE
    ,int slot
#endif
)
{
    (void)fd;(void)account;(void)name;(void)options;(void)unlock;
    if(!unlock) {
        int c;
        for(c=0;c<32;c++)if(CHAR_CHECKINDEX(c) && !strcmp(CHAR_getChar(c,CHAR_CDKEY),account))break;
        assert(c<32 && data && *data);checkpoint_count++;
        free(checkpoint_data[c]);checkpoint_data[c]=strdup(data);assert(checkpoint_data[c]);
        checkpoint_requests[c]=request;
        if(checkpoint_auto_ack)assert(StoneAge_LadderOfflineSaveReply(request,SUCCESSFUL));
        return;
    }
    assert(data && *data);saved_request=request;save_count++;
    if(expected_saved_data)assert(!strcmp(data,expected_saved_data));
}

#define gettimeofday smoke_time
#define lssproto_S_send smoke_send
#define lssproto_CharLogin_send smoke_login
#define lssproto_FS_send smoke_social
#define saacproto_ACCharSave_send smoke_save
#define _CHAR_sendStatusString smoke_status
#define CHAR_sendBattleEffect smoke_effect
#define CHAR_makeOptionString smoke_options
#define CHAR_CharaDelete smoke_delete
#define malloc smoke_malloc
#include "../stoneage_ladder.c"
#undef malloc
#undef gettimeofday
#undef lssproto_S_send
#undef lssproto_CharLogin_send
#undef saacproto_ACCharSave_send
#undef _CHAR_sendStatusString
#undef CHAR_sendBattleEffect
#undef CHAR_makeOptionString
#undef CHAR_CharaDelete

static int sink(int fd,char *wire,int n)
{ (void)wire;if(fd>=0 && fd<64)packets[fd]++;return n; }
static void tick(long long advance)
{ test_clock+=advance;NowTime.tv_sec=test_clock/1000;NowTime.tv_usec=0;StoneAge_LadderTick(); }
static void connect_player(int c,int fd)
{
    CONNECT_setUse(fd,TRUE);CONNECT_setCtype(fd,CLI);CONNECT_setState(fd,LOGIN);
    CONNECT_setCdkey(fd,CHAR_getChar(c,CHAR_CDKEY));CONNECT_setCharaindex(fd,c);
    CONNECT_setCharname(fd,CHAR_getChar(c,CHAR_NAME));CHAR_setWorkInt(c,CHAR_WORKFD,fd);
}
static void resume_player(int c,int fd)
{
    CONNECT_setUse(fd,TRUE);CONNECT_setCtype(fd,CLI);CONNECT_setState(fd,NOTLOGIN);
    CONNECT_setCdkey(fd,CHAR_getChar(c,CHAR_CDKEY));CONNECT_setCharaindex(fd,-1);
    StoneAge_LadderAuthenticated(fd,1);
    assert(StoneAge_LadderResume(fd,CHAR_getChar(c,CHAR_NAME))==1);
    assert(login_ok && CONNECT_getCharaindex(fd)==c && online(c));
}
static int fixture(int sequence,int pet_owner)
{
    Char ch;int c,i;
    assert(CHAR_getDefaultChar(&ch,100000));
    ch.data[CHAR_WHICHTYPE]=pet_owner<0?CHAR_TYPEPLAYER:CHAR_TYPEPET;
    ch.data[CHAR_BASEIMAGENUMBER]=ch.data[CHAR_BASEBASEIMAGENUMBER]=100000;
    ch.data[CHAR_LV]=35;ch.data[CHAR_TRANSMIGRATION]=0;
    ch.data[CHAR_DEFAULTPET]=ch.data[CHAR_RIDEPET]=-1;
    ch.data[CHAR_VITAL]=ch.data[CHAR_STR]=ch.data[CHAR_TOUGH]=ch.data[CHAR_DEX]=3000;
    ch.data[CHAR_SKILLUPPOINT]=0;ch.data[CHAR_LUCK]=0;ch.data[CHAR_CHARM]=100;
    if(pet_owner>=0)ch.data[CHAR_SLOT]=7;
    ch.data[CHAR_EARTHAT]=100;ch.data[CHAR_WATERAT]=ch.data[CHAR_FIREAT]=ch.data[CHAR_WINDAT]=0;
    ch.workint[CHAR_WORKOBJINDEX]=-1;ch.workint[CHAR_WORKBATTLEINDEX]=-1;
    ch.workint[CHAR_WORKBATTLEMODE]=BATTLE_CHARMODE_NONE;ch.workint[CHAR_WORKTRADEMODE]=CHAR_TRADE_FREE;
    ch.workint[CHAR_WORKPLAYERINDEX]=pet_owner;
    for(i=0;i<CHAR_MAXPETHAVE;i++)ch.unionTable.indexOfPet[i]=-1;
#ifdef _PETSKILL_BECOMEPIG
    ch.data[CHAR_BECOMEPIG]=-1;
#endif
    snprintf(ch.string[CHAR_NAME].string,64,"LadderTest%d",sequence);
    snprintf(ch.string[CHAR_CDKEY].string,64,"laddertest%d",sequence);
    c=CHAR_initCharOneArray(&ch);assert(c>=0);
    CHAR_complianceParameter(c);
    CHAR_setInt(c,CHAR_HP,CHAR_getWorkInt(c,CHAR_WORKMAXHP)-7);
    CHAR_setInt(c,CHAR_MP,CHAR_getWorkInt(c,CHAR_WORKMAXMP));
    if(pet_owner<0) {
        assert(c<32);assert(StoneAge_CharacterIdentityPrepareSave(CHAR_getCharPointer(c)));
        StoneAge_CharacterIdentityMarkLoaded(CHAR_getCharPointer(c));connect_player(c,c+3);
        StoneAge_LadderCharacterLoaded(c);
    }
    return c;
}
static unsigned long long revision_of(int c)
{
    unsigned long long revision=0;const char *key=strstr(last[c],"\"revision\":");
    assert(key && sscanf(key,"\"revision\":%llu",&revision)==1);return revision;
}
static void request(int c,const char *op,const char *arg)
{
    char wire[256];int fd=CHAR_getWorkInt(c,CHAR_WORKFD);unsigned long long revision=0;
    if(strcmp(op,"status")) {
        snprintf(wire,sizeof(wire),"LADDER|1|observe_%d|0|status|",++request_serial);
        assert(StoneAge_LadderRequest(fd,wire));revision=revision_of(c);
    }
    snprintf(wire,sizeof(wire),"LADDER|1|request_%d|%llu|%s|%s",++request_serial,revision,op,arg);
    assert(StoneAge_LadderRequest(fd,wire));
    if(!strstr(last[c],"\"code\":\"ok\"")){fprintf(stderr,"request %s failed: %s\n",op,last[c]);abort();}
}
static void team_loadout(int *characters,int n,int mask)
{
    int i;char arg[65];
    snprintf(arg,sizeof(arg),"%d",n);request(characters[0],"create",arg);
    for(i=1;i<n;i++) {
        ADDRESSBOOK_entry *book=CHAR_getAddressbookEntry(characters[0],i-1);
        const char *start;size_t len;
        assert(book);memset(book,0,sizeof(*book));book->use=1;
        snprintf(book->cdkey,sizeof(book->cdkey),"%s",CHAR_getChar(characters[i],CHAR_CDKEY));
        snprintf(book->charname,sizeof(book->charname),"%s",CHAR_getChar(characters[i],CHAR_NAME));
        StoneAge_LadderBindContact(characters[i],book);
        snprintf(arg,sizeof(arg),"%d:%s",i-1,book->persistent_character_id);request(characters[0],"invite",arg);
        start=strstr(last[characters[i]],"invite_");assert(start);
        len=strcspn(start,"\"");assert(len<sizeof(arg));memcpy(arg,start,len);arg[len]=0;
        request(characters[i],"accept",arg);
    }
    snprintf(arg,sizeof(arg),"%d",mask);
    for(i=0;i<n;i++)if(CHAR_getInt(characters[i],CHAR_DEFAULTPET)>=0)request(characters[i],"loadout",arg);
    for(i=0;i<n;i++)request(characters[i],"ready","");
    request(characters[0],"queue","");
    for(i=0;i<n;i++)assert(!StoneAge_LadderGuard(CHAR_getWorkInt(characters[i],CHAR_WORKFD),"Echo"));
}
static void team(int *characters,int n){team_loadout(characters,n,1);}
static int start_match(int *c,int n,int pets)
{
    int i,b,pet;
    for(i=0;i<n*2;i++) {
        c[i]=fixture(request_serial+i,-1);
        if(pets) {
            pet=fixture(100+i,c[i]);CHAR_getCharPointer(c[i])->unionTable.indexOfPet[0]=pet;
            CHAR_setInt(c[i],CHAR_DEFAULTPET,0);CHAR_setPetSkill(pet,0,1);
        }
    }
    team(c,n);team(c+n,n);tick(0);
    assert(strstr(last[c[0]],"\"phase\":\"countdown\""));tick(10000);
    b=Ladder_CharacterBattle(c[0]);assert(b>=0 && native_battle(b)->count==n*2);
    for(i=0;i<n*2;i++) {
        assert(Ladder_CharacterBattle(c[i])==b);
        assert(CHAR_getInt(c[i],CHAR_HP)==CHAR_getWorkInt(c[i],CHAR_WORKMAXHP));
        if(pets)assert(CHAR_getWorkInt(CHAR_getCharPet(c[i],0),CHAR_WORKBATTLEINDEX)==b);
    }
    BATTLE_Loop();return b;
}
static void reconnect_test(void)
{
    int c[2],b,fd,accepted,opponent_packets;
    b=start_match(c,1,0);
    fd=CHAR_getWorkInt(c[0],CHAR_WORKFD);
    assert(!StoneAge_LadderGuard(fd,"M"));
    assert(!StoneAge_LadderGuard(fd,"Echo"));
    {
        const char *rules="0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef";
        LadderTime deadline=native_battle(b)->deadline;
        setenv("STONEAGE_BATTLE_RULESET_ID",rules,1);
        assert(StoneAge_LadderRequest(fd,"BTRULES"));
        assert(strstr(capabilities[c[0]],rules) && !strncmp(capabilities[c[0]],"BTRULES|",8));
        assert(native_battle(b)->deadline==deadline);
        StoneAge_BattleRulesChanged();
        assert(!getenv("STONEAGE_BATTLE_RULESET_ID"));
        assert(!strcmp(capabilities[c[0]],"BTRULES||") && !strcmp(capabilities[c[1]],"BTRULES||"));
        assert(native_battle(b)->deadline==deadline);
    }
    assert(StoneAge_LadderGuard(fd,"W"));
    assert(StoneAge_LadderGuard(fd,"PI"));
    assert(StoneAge_LadderGuard(fd,"EO"));
    lssproto_EO_recv(fd,0);
    assert(CHAR_getWorkInt(c[0],CHAR_WORKBATTLEINDEX)==b);
    assert(StoneAge_LadderCommandWait(b,0)==0);
    /* Public battle packets can be truncated. The ladder guard must reject
     * them before its own parser or the legacy dispatch reads command+2. */
    {
        static const char *invalid[]={"S","J","I","W","H","T","S|","S|1junk","S|999999999999999999999999","S|-2","S|5","S|A","S|FF","S|01","S|+1","S|-1"};
        int i,mode=CHAR_getWorkInt(c[0],CHAR_WORKBATTLEMODE),mp=CHAR_getInt(c[0],CHAR_MP);
        for(i=0;i<(int)(sizeof(invalid)/sizeof(invalid[0]));i++) {
            char *packet=strdup(invalid[i]);assert(packet);
            assert(!StoneAge_LadderCommandAllowed(c[0],packet));
            BattleCommandDispach(fd,packet);free(packet);
            assert(CHAR_getWorkInt(c[0],CHAR_WORKBATTLEMODE)==mode);
            assert(CHAR_getInt(c[0],CHAR_MP)==mp);
        }
        /* This fixture has no active pet: even a syntactically valid recall
         * must not consume the player's open command menu. */
        assert(!StoneAge_LadderCommandAllowed(c[0],"S|-1"));
        assert(!StoneAge_LadderCommandAllowed(c[0],"S|0")); /* Not registered. */
    }
    BattleCommandDispach(CHAR_getWorkInt(c[0],CHAR_WORKFD),"G");
    accepted=CHAR_getWorkInt(c[0],CHAR_WORKBATTLECOM1);assert(accepted==BATTLE_COM_GUARD);
    CHAR_setFlg(c[0],CHAR_ISTRADECARD,1);
    CHAR_setFlg(c[0],CHAR_ISPARTYCHAT,1);
    fd=CHAR_getWorkInt(c[0],CHAR_WORKFD);assert(StoneAge_LadderDisconnect(fd));
    assert(CHAR_CHECKINDEX(c[0]) && CHAR_getWorkInt(c[0],CHAR_WORKBATTLEINDEX)==b);
    tick(40000);opponent_packets=packets[CHAR_getWorkInt(c[1],CHAR_WORKFD)];
    resume_player(c[0],40);
    assert(social_flags[40]==(CHAR_FS_TRADECARD|CHAR_FS_PARTYCHAT));
    assert(packets[CHAR_getWorkInt(c[1],CHAR_WORKFD)]==opponent_packets);
    assert(Ladder_CharacterBattle(c[0])==b && CHAR_getWorkInt(c[0],CHAR_WORKBATTLECOM1)==accepted);
    assert(!StoneAge_LadderCommandAllowed(c[0],"H|A"));
    /* The native empty command is zero, not -1. Its menu remains open. */
    resync_target=c[1];assert(!(StoneAge_LadderResumeFlags(c[1],0)&BP_FLG_PLAYER_MENU_OFF));resync_target=-1;
    assert(StoneAge_LadderCommandWait(b,1)==1);
    assert(CHAR_getWorkInt(c[1],CHAR_WORKBATTLECOM1)==BATTLE_COM_GUARD);
    /* Exercise the hard-close ordering, where use is false before logout. */
    CONNECT_setUse(40,FALSE);assert(CHAR_logout(40,TRUE));
    assert(native_players[c[0]].retained && CHAR_CHECKINDEX(c[0]));
    test_clock+=20000;resume_player(c[0],41); /* No timeout tick first. */
    assert(Ladder_CharacterBattle(c[0])==-1);tick(0);BATTLE_Loop();
    assert(!native_battle(b));request(c[0],"status","");
    assert(strstr(last[c[0]],"\"reason\":\"abandonment\""));
    assert(strstr(last[c[0]],"\"rating_delta\":-16"));
    assert(StoneAge_LadderGuard(CHAR_getWorkInt(c[0],CHAR_WORKFD),"EO"));
    assert(!StoneAge_LadderGuard(CHAR_getWorkInt(c[0],CHAR_WORKFD),"M"));
    assert(!StoneAge_LadderGuard(CHAR_getWorkInt(c[0],CHAR_WORKFD),"Echo"));
    assert(StoneAge_LadderGuard(CHAR_getWorkInt(c[0],CHAR_WORKFD),"W"));
    assert(CHAR_getInt(c[0],CHAR_HP)==CHAR_getWorkInt(c[0],CHAR_WORKMAXHP)-7);
    request(c[0],"ack","");request(c[1],"ack","");
    assert(!StoneAge_LadderGuard(CHAR_getWorkInt(c[0],CHAR_WORKFD),"EO"));
    assert(!StoneAge_LadderGuard(CHAR_getWorkInt(c[0],CHAR_WORKFD),"W"));
    fprintf(stderr,"\nnative reconnect: retained slot, accepted action, timeout guard and cumulative deadline passed\n");
}
static void pet_switch_test(void)
{
    int c[2],b,p,turn,loops,phase,i;
    for(i=0;i<2;i++) {
        c[i]=fixture(960+i,-1);p=fixture(970+i,c[i]);CHAR_setCharPet(c[i],0,p);
        CHAR_setInt(c[i],CHAR_DEFAULTPET,0);CHAR_setPetSkill(p,0,1);
        /* Establish both native pre-battle selections through the real world
         * handlers; assigning DEFAULTPET alone does not mark a standby pet. */
        lssproto_SPET_recv(CHAR_getWorkInt(c[i],CHAR_WORKFD),1);
        lssproto_PETST_recv(CHAR_getWorkInt(c[i],CHAR_WORKFD),0,PET_STAT_SELECT);
        assert(CHAR_getWorkInt(c[i],CHAR_WORKSTANDBYPET)==1);
        assert(CHAR_getWorkInt(c[i],CHAR_WORK_PET0_STAT)==PET_STAT_SELECT);
    }
    team(c,1);team(c+1,1);tick(0);tick(10000);
    b=Ladder_CharacterBattle(c[0]);assert(b>=0);BATTLE_Loop();p=CHAR_getCharPet(c[0],0);
    for(phase=0;phase<2;phase++) {
        char *command=phase?"S|0":"S|-1";
        fprintf(stderr,"switch gate: phase=%d active=%d mask=%d ride=%d hp=%d mode=%d registered=%d\n",phase,
            CHAR_getInt(c[0],CHAR_DEFAULTPET),CHAR_getWorkInt(c[0],CHAR_WORKSTANDBYPET),CHAR_getInt(c[0],CHAR_RIDEPET),
            CHAR_getInt(p,CHAR_HP),CHAR_getWorkInt(c[0],CHAR_WORKBATTLEMODE),Ladder_PetAllowed(c[0],0));
        assert(StoneAge_LadderCommandAllowed(c[0],command));
        assert(!StoneAge_LadderCommandAllowed(c[0],phase?"S|-1":"S|0"));
        assert(!StoneAge_LadderCommandAllowed(c[0],"S|1"));
        turn=BattleArray[b].turn;
        lssproto_B_recv(CHAR_getWorkInt(c[0],CHAR_WORKFD),command);
        /* The old pet remains active until settlement of the recall turn. */
        assert(CHAR_getInt(c[0],CHAR_DEFAULTPET)==(phase?-1:0));
        if(!phase)lssproto_B_recv(CHAR_getWorkInt(c[0],CHAR_WORKFD),"W|FF|FF");
        lssproto_B_recv(CHAR_getWorkInt(c[1],CHAR_WORKFD),"G");
        lssproto_B_recv(CHAR_getWorkInt(c[1],CHAR_WORKFD),"W|FF|FF");
        for(loops=0;loops<8 && BattleArray[b].turn==turn;loops++)BATTLE_Loop();
        assert(BattleArray[b].turn==turn+1);
        assert(CHAR_getInt(c[0],CHAR_DEFAULTPET)==(phase?0:-1));
        assert(phase?BATTLE_No2Index(b,5)==p:BATTLE_No2Index(b,5)<0);
    }
    force_finish(b,0,"defeat");BATTLE_Loop();assert(!native_battle(b));
    assert(CHAR_getInt(c[0],CHAR_DEFAULTPET)==0);
    for(i=0;i<2;i++){request(c[i],"ack","");request(c[i],"leave","");smoke_delete(c[i]);}
    fprintf(stderr,"native pet switching: actual arena B entry, recall/resummon turns, old pet command and selected-pet restoration passed\n");
}
static void combat_test(void)
{
    int c[10],b,round,i,side,target,pet;char command[32];
    b=start_match(c,5,1);
    for(round=0;round<500 && native_battle(b);round++) {
        for(i=0;i<10;i++) {
            if(CHAR_getFlg(c[i],CHAR_ISDIE))continue;
            side=i/5;target=-1;
            for(pet=0;pet<5;pet++)if(!CHAR_getFlg(c[(1-side)*5+pet],CHAR_ISDIE)){target=(1-side)*10+pet;break;}
            if(target<0)continue;
            snprintf(command,sizeof(command),"H|%X",target);BattleCommandDispach(CHAR_getWorkInt(c[i],CHAR_WORKFD),command);
            snprintf(command,sizeof(command),"W|0|%X",target);BattleCommandDispach(CHAR_getWorkInt(c[i],CHAR_WORKFD),command);
        }
        test_clock+=1000;NowTime.tv_sec=test_clock/1000;BATTLE_Loop();
    }
    if(native_battle(b)) {
        fprintf(stderr,"combat stuck: battle_mode=%d turn=%d\n",BattleArray[b].mode,BattleArray[b].turn);
        for(i=0;i<10;i++)fprintf(stderr,"player %d hp=%d mode=%d command=%d pet_mode=%d\n",c[i],CHAR_getInt(c[i],CHAR_HP),CHAR_getWorkInt(c[i],CHAR_WORKBATTLEMODE),CHAR_getWorkInt(c[i],CHAR_WORKBATTLECOM1),CHAR_getWorkInt(CHAR_getCharPet(c[i],0),CHAR_WORKBATTLEMODE));
    }
    assert(!native_battle(b));request(c[0],"status","");
    assert(strstr(last[c[0]],"\"phase\":\"result\""));
    assert(strstr(last[c[0]],"\"reason\":\"defeat\""));
    for(i=0;i<10;i++) {
        assert(CHAR_getWorkInt(c[i],CHAR_WORKBATTLEMODE)==BATTLE_CHARMODE_NONE);
        assert(CHAR_getInt(c[i],CHAR_HP)==CHAR_getWorkInt(c[i],CHAR_WORKMAXHP)-7);
        pet=CHAR_getCharPet(c[i],0);assert(CHAR_CHECKINDEX(pet));
        assert(CHAR_getInt(pet,CHAR_HP)==CHAR_getWorkInt(pet,CHAR_WORKMAXHP)-7);
    }
    printf("\n%s\n",last[c[0]]);
    fflush(stdout);
    fprintf(stderr,"native combat: actual 5v5 players and pets, %d engine ticks, settlement and state restoration passed\n",round);
}
static void offline_save_test(void)
{
    int c[2],b,first_request,second_request,previous_count,reused=-1,attempt;
    b=start_match(c,1,1);
    assert(StoneAge_LadderDisconnect(CHAR_getWorkInt(c[0],CHAR_WORKFD)));
    force_finish(b,1,"defeat");BATTLE_Loop();tick(0);
    assert(save_count>0 && native_players[c[0]].pending_save);
    first_request=saved_request;previous_count=save_count;
    assert(StoneAge_LadderOfflineSaveReply(first_request,FAILED));
    tick(4999);assert(save_count==previous_count && CHAR_CHECKINDEX(c[0]));
    tick(1);second_request=saved_request;assert(second_request!=first_request);
    assert(StoneAge_LadderOfflineSaveReply(first_request,SUCCESSFUL));
    assert(CHAR_CHECKINDEX(c[0]) && native_players[c[0]].pending_save);
    assert(StoneAge_LadderOfflineSaveReply(second_request,SUCCESSFUL));
    assert(!CHAR_CHECKINDEX(c[0]));
    for(attempt=0;attempt<32 && reused!=c[0];attempt++)reused=fixture(900+attempt,-1);
    assert(reused==c[0]);
    assert(StoneAge_LadderOfflineSaveReply(second_request,SUCCESSFUL));
    assert(CHAR_CHECKINDEX(reused) && !Ladder_Reserved(reused));
    fprintf(stderr,"\nnative offline save: retry delay and stale reply fencing across character-slot reuse passed\n");
}
static void statistics_test(void)
{
    int c[2],b,hp,damage=30,pet_damage=0,reflection=0;
    b=start_match(c,1,0);hp=CHAR_getInt(c[0],CHAR_HP);
    CHAR_setWorkInt(c[1],CHAR_WORKDAMAGEREFLEC,1);
    BATTLE_DamageSub(c[0],c[1],&damage,&pet_damage,&reflection);
    assert(CHAR_getInt(c[0],CHAR_HP)==hp-30);
    StoneAge_LadderExecuting(b,10);CHAR_setWorkInt(c[0],CHAR_WORKPOISON,3);StoneAge_LadderExecutionEnd();
    CHAR_setInt(c[0],CHAR_HP,hp-35); /* Unattributed HP change while poisoned. */
    StoneAge_LadderPeriodicSource(c[0],CHAR_WORKPOISON);
    CHAR_setInt(c[0],CHAR_HP,hp-42);StoneAge_LadderExecutionEnd();
    StoneAge_LadderSetHP(c[0],c[0],hp-33); /* Nine effective healing. */
    StoneAge_LadderSetHP(c[1],c[0],0);
    StoneAge_LadderSetHP(c[0],c[0],10);
    force_finish(b,1,"defeat");BATTLE_Loop();request(c[0],"status","");
    printf("\nLADDER_STATS|%s\n",last[c[0]]+7);fflush(stdout);
    fprintf(stderr,"native statistics: reflection, scoped poison, healing and revive source checks passed\n");
}
static void multiplayer_statistics_test(void)
{
    int c[4],b,i,pet[4],hp,damage=10,pet_damage=0,reflection=0;
    b=start_match(c,2,1);
    for(i=0;i<4;i++)pet[i]=CHAR_getCharPet(c[i],0);
    hp=CHAR_getInt(c[2],CHAR_HP);
    BATTLE_DamageSub(c[0],c[2],&damage,&pet_damage,&reflection);
    assert(CHAR_getInt(c[2],CHAR_HP)==hp-10);
    StoneAge_LadderSetHP(pet[0],c[2],hp-15);
    StoneAge_LadderSetHP(c[1],c[2],0);
    /* A revived target starts a fresh contribution history. */
    StoneAge_LadderSetHP(c[2],c[2],20);
    StoneAge_LadderSetHP(c[1],c[2],0);
    StoneAge_LadderSetHP(pet[0],pet[2],CHAR_getInt(pet[2],CHAR_HP)-7);
    StoneAge_LadderSetHP(c[1],pet[2],0);
    force_finish(b,0,"defeat");BATTLE_Loop();request(c[0],"status","");
    printf("\nLADDER_TEAM_STATS|%s\n",last[c[0]]+7);fflush(stdout);
    for(i=0;i<4;i++)request(c[i],"ack","");
    for(i=0;i<4;i++){request(c[i],"leave","");smoke_delete(c[i]);}
    fprintf(stderr,"native team statistics: pet ownership, distinct contributors and assist reset after revival passed\n");
}
static void ride_statistics_test(void)
{
    int c[2],pet[2],i,b,hp,pethp,damage=60,petdamage=0,reflection=0;
    for(i=0;i<2;i++) {
        c[i]=fixture(910+i,-1);pet[i]=fixture(912+i,c[i]);
        CHAR_getCharPointer(c[i])->unionTable.indexOfPet[0]=pet[i];
        CHAR_setInt(c[i],CHAR_RIDEPET,0);
        request(c[i],"create","1");request(c[i],"loadout","1");
        request(c[i],"ready","");request(c[i],"queue","");
    }
    tick(0);tick(10000);b=Ladder_CharacterBattle(c[0]);assert(b>=0);BATTLE_Loop();
    hp=CHAR_getInt(c[1],CHAR_HP);pethp=CHAR_getInt(pet[1],CHAR_HP);
    BATTLE_DamageSub(c[0],c[1],&damage,&petdamage,&reflection);
    assert(CHAR_getInt(c[1],CHAR_HP)<hp && CHAR_getInt(pet[1],CHAR_HP)<pethp);
    assert(CHAR_getInt(c[1],CHAR_HP)>0 && CHAR_getInt(pet[1],CHAR_HP)>0);
    damage=10000;petdamage=0;reflection=0;
    BATTLE_DamageSub(c[0],c[1],&damage,&petdamage,&reflection);
    assert(CHAR_getInt(c[1],CHAR_HP)==0 && CHAR_getInt(pet[1],CHAR_HP)==0);
    assert(CHAR_getInt(c[1],CHAR_RIDEPET)==-1);
    force_finish(b,0,"defeat");BATTLE_Loop();request(c[0],"status","");
    printf("\nLADDER_RIDE_STATS|%s\n",last[c[0]]+7);fflush(stdout);
    printf("\nLADDER_RIDE_EXPECTED|%d|%d\n",hp,pethp);fflush(stdout);
    assert(CHAR_getInt(c[1],CHAR_RIDEPET)==0 && CHAR_getInt(pet[1],CHAR_HP)==pethp-7);
    for(i=0;i<2;i++){request(c[i],"ack","");request(c[i],"leave","");smoke_delete(c[i]);}
    fprintf(stderr,"native ride statistics: actual split damage, overkill clamping, rider/pet deaths and ride restoration passed\n");
}
static void external_warp_test(void)
{
    int stage,c[2],objects[2],i,b,expires,started;Object object;
    assert(getenv("STONEAGE_LADDER_SMOKE_MAPS"));
    assert(MAP_initReadMap(getMaptilefile(),getenv("STONEAGE_LADDER_SMOKE_MAPS")));
    for(stage=0;stage<3;stage++) {
        for(i=0;i<2;i++) {
            c[i]=fixture(920+i,-1);CHAR_setInt(c[i],CHAR_FLOOR,7025);
            CHAR_setInt(c[i],CHAR_X,1+i);CHAR_setInt(c[i],CHAR_Y,1);
            memset(&object,0,sizeof(object));object.type=OBJTYPE_CHARA;object.index=c[i];
            object.floor=7025;object.x=1+i;object.y=1;
            objects[i]=initObjectOne(&object);assert(objects[i]>=0);
            CHAR_setWorkInt(c[i],CHAR_WORKOBJINDEX,objects[i]);
            CHAR_setWorkInt(c[i],CHAR_WORKPETFOLLOW,-1);
            CHAR_setWorkInt(c[i],CHAR_WORKTICKETTIME,(int)time(NULL)+1000);
        }
        expires=(int)time(NULL)-10;started=expires-60;
        CHAR_setWorkInt(c[0],CHAR_WORKTICKETTIME,expires);
        CHAR_setWorkInt(c[0],CHAR_WORKTICKETTIMESTART,started);
        team(c,1);if(stage){team(c+1,1);tick(0);}
        if(stage==2){tick(10000);BATTLE_Loop();}
        assert(StoneAge_LadderReserved(c[0]));
        assert(!CHAR_warpToSpecificPoint(c[0],7001,41,6));
        check_TimeTicket();
        assert(CHAR_getInt(c[0],CHAR_FLOOR)==7025 && CHAR_getInt(c[0],CHAR_X)==1);
        assert(CHAR_getWorkInt(c[0],CHAR_WORKTICKETTIME)==expires);
        assert(CHAR_getWorkInt(c[0],CHAR_WORKTICKETTIMESTART)==started);
        if(stage==2) {
            b=Ladder_CharacterBattle(c[0]);force_finish(b,0,"defeat");BATTLE_Loop();
            for(i=0;i<2;i++)request(c[i],"ack","");
        } else if(stage==1) {
            /* Countdown is locked against player cancellation. Invalidate
             * preparation through the tested configuration-change path. */
            CHAR_setInt(c[0],CHAR_VITAL,CHAR_getInt(c[0],CHAR_VITAL)+1);tick(0);
            request(c[1],"cancel","");tick(0);
        } else {request(c[0],"cancel","");tick(0);}
        assert(!StoneAge_LadderReserved(c[0]));
        check_TimeTicket();
        assert(CHAR_getInt(c[0],CHAR_FLOOR)==7001 && CHAR_getInt(c[0],CHAR_X)==41 && CHAR_getInt(c[0],CHAR_Y)==6);
        assert(CHAR_getWorkInt(c[0],CHAR_WORKTICKETTIME)==0);
        assert(CHAR_getWorkInt(c[0],CHAR_WORKTICKETTIMESTART)==0);
        assert(CHAR_warpToSpecificPoint(c[0],7025,1,1));
        for(i=0;i<2;i++){endObjectOne(objects[i]);CHAR_setWorkInt(c[i],CHAR_WORKOBJINDEX,-1);smoke_delete(c[i]);}
    }
    fprintf(stderr,"\nnative external warp: queued/countdown/battle block direct warps and preserve expired tickets; release resumes actual ticket warp\n");
}
static void loadout_test(void)
{
    int c,pet,index,other;LadderProfile before,after;ITEM_Item item;char wire[256];
    c=fixture(500,-1);pet=fixture(501,c);
    CHAR_getCharPointer(c)->unionTable.indexOfPet[0]=pet;
    memset(&item,0,sizeof(item));item.data[ITEM_ID]=123;item.data[ITEM_MODIFYATTACK]=20;
    index=ITEM_initExistItemsOne(&item);assert(ITEM_CHECKINDEX(index));
    CHAR_setItemIndex(c,CHAR_EQUIPPLACENUM,index);
    assert(profile(c,&before));
    CHAR_setInt(c,CHAR_HP,CHAR_getInt(c,CHAR_HP)-1);
    assert(profile(c,&after) && after.loadout_hash==before.loadout_hash);
    ITEM_setInt(index,ITEM_MODIFYATTACK,21);
    assert(profile(c,&after) && after.loadout_hash!=before.loadout_hash);before=after;
    ITEM_setChar(index,ITEM_NAME,"Changed item");
    assert(profile(c,&after) && after.loadout_hash!=before.loadout_hash);before=after;
    /* A reused pet array index with identical stats is still a new actor. */
    CHAR_getCharPointer(pet)->CharMakeSequenceNumber++;
    assert(profile(c,&after) && after.loadout_hash!=before.loadout_hash);before=after;
    CHAR_setItemIndex(c,CHAR_EQUIPPLACENUM,-1);CHAR_setItemIndex(pet,CHAR_EQUIPPLACENUM,index);
    assert(profile(c,&before));ITEM_setInt(index,ITEM_MAGICID,99);
    assert(profile(c,&after) && after.loadout_hash!=before.loadout_hash);
    CHAR_setItemIndex(pet,CHAR_EQUIPPLACENUM,-1);CHAR_setItemIndex(c,CHAR_EQUIPPLACENUM,index);
    /* The main-thread coordinator must cancel a queue for an in-place item
     * edit before allocating a battle, not just notice the hash in isolation. */
    team(&c,1);ITEM_setInt(index,ITEM_MODIFYATTACK,22);tick(0);request(c,"status","");
    assert(strstr(last[c],"\"phase\":\"lobby\""));
    other=CHAR_getWorkInt(c,CHAR_WORKPARTYMODE);
    CHAR_setWorkInt(c,CHAR_WORKPARTYMODE,CHAR_PARTY_LEADER);
    assert(profile(c,&after) && !after.idle && !strcmp(after.busy_reason,"party"));
    snprintf(wire,sizeof(wire),"LADDER|1|party_busy|%llu|ready|",revision_of(c));
    StoneAge_LadderRequest(CHAR_getWorkInt(c,CHAR_WORKFD),wire);
    assert(strstr(last[c],"\"code\":\"member_busy\""));
    CHAR_setWorkInt(c,CHAR_WORKPARTYMODE,other);
    CHAR_setItemIndex(c,CHAR_EQUIPPLACENUM,-1);ITEM_endExistItemsOne(index);
    CHAR_getCharPointer(c)->unionTable.indexOfPet[0]=-1;CHAR_endCharOneArray(pet);smoke_delete(c);
    fprintf(stderr,"native loadout: same-slot item changes, pet identity/equipment, queue invalidation and ordinary-party exclusion passed\n");
}
static double displayed_power(int c)
{
    double value;const char *key;
    request(c,"status","");key=strstr(last[c],"\"power\":");
    assert(key && sscanf(key,"\"power\":%lf",&value)==1);return value;
}
static void power_configuration_test(void)
{
    int c,pet[3],i,item,attack;LadderProfile bare,points,equipped,roster;
    double original,registered;ITEM_Item equipment;
    c=fixture(800,-1);assert(profile(c,&bare));original=displayed_power(c);
    CHAR_setInt(c,CHAR_SKILLUPPOINT,17);
    assert(profile(c,&points));
    assert(fabs(points.character_power-bare.character_power-17.0)<0.000001);
    assert(fabs(displayed_power(c)-original-17.0)<0.00001);
    /* Exercise native compliance using an actual equipped item rather than
     * injecting a precomputed power into the coordinator callback. */
    memset(&equipment,0,sizeof(equipment));equipment.data[ITEM_ID]=123;
    equipment.data[ITEM_MODIFYATTACK]=140;
    item=ITEM_initExistItemsOne(&equipment);assert(ITEM_CHECKINDEX(item));
    attack=CHAR_getWorkInt(c,CHAR_WORKATTACKPOWER);
    CHAR_setItemIndex(c,CHAR_ARM,item);CHAR_complianceParameter(c);
    assert(CHAR_getWorkInt(c,CHAR_WORKATTACKPOWER)==attack+140);
    assert(profile(c,&equipped));
    assert(equipped.character_power>points.character_power);
    assert(displayed_power(c)>original+17.0);
    for(i=0;i<3;i++) {
        pet[i]=fixture(801+i,c);CHAR_getCharPointer(c)->unionTable.indexOfPet[i]=pet[i];
    }
    CHAR_setInt(c,CHAR_DEFAULTPET,0);CHAR_setInt(c,CHAR_RIDEPET,1);CHAR_complianceParameter(c);
    assert(profile(c,&roster));assert(roster.active_pet==0 && roster.ride_pet==1 && roster.pet_mask==7);
    request(c,"create","1");request(c,"loadout","3");
    registered=displayed_power(c);
    assert(fabs(registered-roster.character_power-roster.pet_power[0]-roster.pet_power[1])<0.00001);
    request(c,"loadout","7");
    assert(fabs(displayed_power(c)-registered-roster.pet_power[2])<0.00001);
    CHAR_setItemIndex(c,CHAR_ARM,-1);ITEM_endExistItemsOne(item);
    CHAR_setInt(c,CHAR_DEFAULTPET,-1);CHAR_setInt(c,CHAR_RIDEPET,-1);
    for(i=0;i<3;i++){CHAR_getCharPointer(c)->unionTable.indexOfPet[i]=-1;CHAR_endCharOneArray(pet[i]);}
    smoke_delete(c);
    fprintf(stderr,"\nnative power: unallocated points, real equipped attack, active/ride/reserve pet registration and public snapshot passed\n");
}
static void countdown_configuration_test(void)
{
    int kind,c[2],pet[2],i,item=-1;ITEM_Item equipment;
    for(kind=0;kind<3;kind++) {
        for(i=0;i<2;i++) {
            c[i]=fixture(810+kind*2+i,-1);pet[i]=fixture(820+kind*2+i,c[i]);
            CHAR_getCharPointer(c[i])->unionTable.indexOfPet[0]=pet[i];CHAR_setInt(c[i],CHAR_DEFAULTPET,0);
        }
        team(c,1);team(c+1,1);tick(0);
        assert(strstr(last[c[0]],"\"phase\":\"countdown\""));
        if(kind==0) {
            memset(&equipment,0,sizeof(equipment));equipment.data[ITEM_ID]=123;equipment.data[ITEM_MODIFYATTACK]=140;
            item=ITEM_initExistItemsOne(&equipment);assert(ITEM_CHECKINDEX(item));
            CHAR_setItemIndex(c[0],CHAR_ARM,item);CHAR_complianceParameter(c[0]);
        } else if(kind==1)CHAR_setInt(c[0],CHAR_RIDEPET,0);
        else CHAR_setPetSkill(pet[0],0,2);
        tick(10000);
        request(c[0],"status","");assert(strstr(last[c[0]],"\"phase\":\"lobby\""));
        assert(strstr(last[c[0]],"\"ready\":false"));
        request(c[1],"status","");assert(strstr(last[c[1]],"\"phase\":\"queued\""));
        for(i=0;i<2;i++) {
            assert(Ladder_CharacterBattle(c[i])<0 && native_character(c[i])==NULL);
            assert(CHAR_getWorkInt(c[i],CHAR_WORKBATTLEMODE)==BATTLE_CHARMODE_NONE);
        }
        if(item>=0){CHAR_setItemIndex(c[0],CHAR_ARM,-1);ITEM_endExistItemsOne(item);item=-1;}
        for(i=0;i<2;i++) {
            CHAR_setInt(c[i],CHAR_DEFAULTPET,-1);CHAR_setInt(c[i],CHAR_RIDEPET,-1);
            CHAR_getCharPointer(c[i])->unionTable.indexOfPet[0]=-1;CHAR_endCharOneArray(pet[i]);smoke_delete(c[i]);
        }
    }
    fprintf(stderr,"\nnative configuration freeze: equipment, riding and pet skills changed during countdown cancel entry and requeue healthy opponent\n");
}
static void external_interaction_test(void)
{
    int c[2],outsider,i,stage,fd;char message[128];
    c[0]=fixture(840,-1);c[1]=fixture(841,-1);outsider=fixture(842,-1);
    team(c,1);
    for(stage=0;stage<2;stage++) {
        if(stage){team(c+1,1);tick(0);}
        request(c[0],"status","");
        assert(strstr(last[c[0]],stage?"\"phase\":\"countdown\"":"\"phase\":\"queued\""));
        assert(!StoneAge_LadderInteractionAllowed(outsider,c[0]));
        last[c[0]][0]=0;
        assert(!CHAR_JoinParty(c[0]));
        assert(strstr(last[c[0]],"\"code\":\"ladder_reserved\""));
        last[c[0]][0]=0;
        assert(!TRADE_Search(CHAR_getWorkInt(c[0],CHAR_WORKFD),c[0],"D|"));
        assert(strstr(last[c[0]],"\"code\":\"ladder_reserved\""));
        CHAR_JoinParty_Main(outsider,c[0]);
        CHAR_JoinParty_Main(c[0],outsider);
        for(i=0;i<2;i++)assert(CHAR_getWorkInt(i?outsider:c[0],CHAR_WORKPARTYMODE)==CHAR_PARTY_NONE);
        fd=CHAR_getWorkInt(outsider,CHAR_WORKFD);
        snprintf(message,sizeof(message),"W|%d|target",CHAR_getWorkInt(c[0],CHAR_WORKFD));
        last[outsider][0]=0;CHAR_Trade(fd,outsider,message);
        assert(strstr(last[outsider],"\"code\":\"ladder_reserved\""));
        snprintf(message,sizeof(message),"T|%d|target",CHAR_getWorkInt(c[0],CHAR_WORKFD));
        last[outsider][0]=0;CHAR_Trade(fd,outsider,message);
        assert(strstr(last[outsider],"\"code\":\"ladder_reserved\""));
        for(i=0;i<2;i++)assert(CHAR_getWorkInt(i?outsider:c[0],CHAR_WORKTRADEMODE)==CHAR_TRADE_FREE);
        tick(0);request(c[0],"status","");
        assert(strstr(last[c[0]],stage?"\"phase\":\"countdown\"":"\"phase\":\"queued\""));
        assert(strstr(last[c[0]],"\"cooldown_until_ms\":0"));
    }
    for(i=0;i<2;i++)smoke_delete(c[i]);
    c[0]=fixture(843,-1);
    for(i=0;i<CHAR_PARTYMAX;i++) {
        CHAR_setWorkInt(c[0],CHAR_WORKPARTYINDEX1+i,-1);
        CHAR_setWorkInt(outsider,CHAR_WORKPARTYINDEX1+i,-1);
    }
    assert(StoneAge_LadderInteractionAllowed(outsider,c[0]));
    CHAR_JoinParty_Main(outsider,c[0]);
    assert(CHAR_getWorkInt(c[0],CHAR_WORKPARTYMODE)==CHAR_PARTY_LEADER);
    assert(CHAR_getWorkInt(outsider,CHAR_WORKPARTYMODE)==CHAR_PARTY_CLIENT);
    assert(CHAR_getPartyIndex(outsider,0)==c[0]);
    CHAR_DischargePartyNoMsg(c[0]);
    assert(CHAR_getWorkInt(outsider,CHAR_WORKPARTYMODE)==CHAR_PARTY_NONE);
    smoke_delete(c[0]);smoke_delete(outsider);
    fprintf(stderr,"\nnative external interactions: ordinary party and trade requests cannot mutate queued/countdown players or cancel their match\n");
}
static void resistance_configuration_test(void)
{
    const int base[]={CHAR_POISON,CHAR_PARALYSIS,CHAR_SLEEP,CHAR_STONE,CHAR_DRUNK,CHAR_CONFUSION
#ifdef _ATTACK_MAGIC
        ,CHAR_EARTH_EXP,CHAR_WATER_EXP,CHAR_FIRE_EXP,CHAR_WIND_EXP,
        CHAR_EARTH_RESIST,CHAR_WATER_RESIST,CHAR_FIRE_RESIST,CHAR_WIND_RESIST
#endif
    };
    const int derived[]={CHAR_WORKMODPOISON,CHAR_WORKMODPARALYSIS,CHAR_WORKMODSLEEP,CHAR_WORKMODSTONE,CHAR_WORKMODDRUNK,CHAR_WORKMODCONFUSION
#ifdef _ATTACK_MAGIC
        ,-1,-1,-1,-1,-1,-1,-1,-1
#endif
    };
    int field,actor,c[2],pets[2],i,target;
    for(actor=0;actor<2;actor++)for(field=0;field<(int)(sizeof(base)/sizeof(base[0]));field++) {
        for(i=0;i<2;i++) {
            c[i]=fixture(880+i,-1);pets[i]=fixture(882+i,c[i]);
            CHAR_getCharPointer(c[i])->unionTable.indexOfPet[0]=pets[i];
        }
        team(c,1);team(c+1,1);tick(0);
        request(c[0],"status","");assert(strstr(last[c[0]],"\"phase\":\"countdown\""));
        target=actor?pets[0]:c[0];
        CHAR_setInt(target,base[field],CHAR_getInt(target,base[field])+17);
        CHAR_complianceParameter(target);
        if(derived[field]>=0)assert(CHAR_getWorkInt(target,derived[field])==CHAR_getInt(target,base[field]));
        tick(10000);request(c[0],"status","");
        assert(strstr(last[c[0]],"\"phase\":\"lobby\""));
        assert(strstr(last[c[0]],"\"ready\":false"));
        request(c[1],"status","");assert(strstr(last[c[1]],"\"phase\":\"queued\""));
        for(i=0;i<2;i++) {
            assert(Ladder_CharacterBattle(c[i])<0 && native_character(c[i])==NULL);
            CHAR_getCharPointer(c[i])->unionTable.indexOfPet[0]=-1;
            CHAR_endCharOneArray(pets[i]);smoke_delete(c[i]);
        }
    }
    fprintf(stderr,"\nnative resistance freeze: %lu character/pet status-resistance and elemental-magic configuration changes cancel countdown\n",2UL*sizeof(base)/sizeof(base[0]));
}
static void external_petmail_test(void)
{
    int side,stage,c[2],outsider,pet,message,item,sender,recipient,i;
    assert(PETMAIL_initOffmsgBuffer(16));
    for(side=0;side<2;side++) {
        c[0]=fixture(860+side*4,-1);c[1]=fixture(861+side*4,-1);outsider=fixture(862+side*4,-1);
        sender=side?c[0]:outsider;recipient=side?outsider:c[0];
        pet=fixture(863+side*4,sender);
        CHAR_setChar(pet,CHAR_OWNERCDKEY,CHAR_getChar(sender,CHAR_CDKEY));
        CHAR_setChar(pet,CHAR_OWNERCHARANAME,CHAR_getChar(sender,CHAR_NAME));
        message=PETMAIL_addOffmsg(sender,CHAR_getChar(recipient,CHAR_CDKEY),CHAR_getChar(recipient,CHAR_NAME),"fixture",0);
        assert(message>=0);CHAR_setInt(pet,CHAR_PETMAILBUFINDEX,message);
        item=ITEM_makeItemAndRegist(1234);assert(ITEM_CHECKINDEX(item));
        CHAR_setItemIndex(pet,CHAR_STARTITEMARRAY,item);
        team(c,1);
        for(stage=0;stage<2;stage++) {
            if(stage){team(c+1,1);tick(0);}
            CHAR_setInt(pet,CHAR_MAILMODE,CHAR_PETMAIL_RETURNWAIT);
            CHAR_setInt(pet,CHAR_PETMAILIDLETIME,1);
            PETMAIL_Loopfunc(pet);
            assert(CHAR_getInt(pet,CHAR_MAILMODE)==CHAR_PETMAIL_RETURNWAIT);
            assert(CHAR_getInt(pet,CHAR_PETMAILIDLETIME)==NowTime.tv_sec);
            assert(CHAR_getItemIndex(pet,CHAR_STARTITEMARRAY)==item && ITEM_CHECKINDEX(item));
            assert(PETMAIL_getOffmsg(message)->use);
            request(c[0],"status","");
            assert(strstr(last[c[0]],stage?"\"phase\":\"countdown\"":"\"phase\":\"queued\""));
            assert(strstr(last[c[0]],"\"cooldown_until_ms\":0"));
        }
        for(i=0;i<2;i++)smoke_delete(c[i]);
        CHAR_setInt(pet,CHAR_PETMAILIDLETIME,1);PETMAIL_Loopfunc(pet);
        assert(CHAR_getInt(pet,CHAR_MAILMODE)==CHAR_PETMAIL_IDLE3);
        assert(CHAR_getItemIndex(pet,CHAR_STARTITEMARRAY)==item);
        PETMAIL_deleteOffmsg(message);CHAR_endCharOneArray(pet);smoke_delete(outsider);
    }
    fprintf(stderr,"\nnative pet mail: reserved sender or recipient pauses timers and retains item/message; ordinary progression resumes after release\n");
}
static void pet_archive_compatibility_test(void)
{
    int c=fixture(850,-1),pet=fixture(851,c),item,copy,was_enabled=enabled;
    char *old_archive,*new_archive,*input;Char restored;
    old_archive=strdup(CHAR_makePetStringFromPetIndex(pet));assert(old_archive && *old_archive);
    assert(!strstr(old_archive,"pitem"));
    item=ITEM_makeItemAndRegist(1234);assert(ITEM_CHECKINDEX(item));
    CHAR_setItemIndex(pet,CHAR_EQUIPPLACENUM,item);
    ITEM_setChar(item,ITEM_NAME,"compat|gear,with:escapes\\");
    /* Archive support must not depend on the coordinator being available. */
    enabled=0;
    new_archive=strdup(CHAR_makePetStringFromPetIndex(pet));assert(new_archive && strstr(new_archive,"pitem"));
    input=strdup(new_archive);assert(input);
    assert(CHAR_makePetFromStringToArg(input,&restored,0)==TRUE);free(input);
    copy=restored.indexOfExistItems[CHAR_EQUIPPLACENUM];
    assert(ITEM_CHECKINDEX(copy) && ITEM_getInt(copy,ITEM_ID)==1234);
    assert(!strcmp(ITEM_getChar(copy,ITEM_NAME),ITEM_getChar(item,ITEM_NAME)));
    CHAR_endCharData(&restored);
    input=strdup(old_archive);assert(input);
    assert(CHAR_makePetFromStringToArg(input,&restored,0)==TRUE);free(input);
    assert(!ITEM_CHECKINDEX(restored.indexOfExistItems[CHAR_EQUIPPLACENUM]));
    assert(restored.data[CHAR_HP]==CHAR_getInt(pet,CHAR_HP));
    CHAR_endCharData(&restored);enabled=was_enabled;
    free(old_archive);free(new_archive);CHAR_endCharOneArray(pet);smoke_delete(c);
    fprintf(stderr,"\nnative pet archive compatibility: old itemless archives load; extended items survive serialization and reload with ladder unavailable\n");
}
static int database_count(sqlite3 *connection,const char *query)
{
    sqlite3_stmt *statement=NULL;int result;
    assert(sqlite3_prepare_v2(connection,query,-1,&statement,NULL)==SQLITE_OK);
    assert(sqlite3_step(statement)==SQLITE_ROW);result=sqlite3_column_int(statement,0);
    sqlite3_finalize(statement);return result;
}
static void resources_test(void)
{
    int c[2],pet,b,food,equipment,petitem,extra,round,fd,i;
    char *persisted,*petpersisted,*recovery_data;char command[40];Char before,petbefore,recovered;
    ITEM_Item foodbefore,equipmentbefore,petitembefore;
    sqlite3 *fault_database=NULL;int matches_before,ratings_before;
    c[0]=fixture(600,-1);c[1]=fixture(601,-1);
    pet=fixture(602,c[0]);CHAR_getCharPointer(c[0])->unionTable.indexOfPet[0]=pet;
    /* Keep this pet registered as a reserve so the sides have equal active
     * fighters; give the other side an equal registered reserve for matching. */
    b=fixture(603,c[1]);CHAR_getCharPointer(c[1])->unionTable.indexOfPet[0]=b;
    food=ITEM_makeItemAndRegist(1234);assert(ITEM_CHECKINDEX(food));
    equipment=ITEM_makeItemAndRegist(1234);assert(ITEM_CHECKINDEX(equipment));
    petitem=ITEM_makeItemAndRegist(1234);assert(ITEM_CHECKINDEX(petitem));
    CHAR_setItemIndex(c[0],CHAR_EQUIPPLACENUM,food);
    CHAR_setItemIndex(c[0],CHAR_ARM,equipment);
    CHAR_setItemIndex(pet,CHAR_EQUIPPLACENUM,petitem);
    ITEM_setChar(petitem,ITEM_NAME,"Pet|gear,with:slashes\\");
#ifdef _TAKE_ITEMDAMAGE
    ITEM_setInt(equipment,ITEM_DAMAGECRUSHE,1);ITEM_setInt(equipment,ITEM_MAXDAMAGECRUSHE,100);
#endif
    for(i=0;i<2;i++) {
        request(c[i],"create","1");request(c[i],"loadout","1");
        request(c[i],"ready","");request(c[i],"queue","");
    }
    tick(0);
    before=*CHAR_getCharPointer(c[0]);petbefore=*CHAR_getCharPointer(pet);
    foodbefore=*ITEM_getItemPointer(food);equipmentbefore=*ITEM_getItemPointer(equipment);petitembefore=*ITEM_getItemPointer(petitem);
    persisted=strdup(CHAR_makeStringFromCharData(CHAR_getCharPointer(c[0])));assert(persisted && *persisted);
    petpersisted=strdup(CHAR_makePetStringFromPetIndex(pet));assert(petpersisted && *petpersisted);
    tick(10000);b=Ladder_CharacterBattle(c[0]);assert(b>=0);BATTLE_Loop();
    CHAR_setInt(c[0],CHAR_HP,40);
    snprintf(command,sizeof(command),"I|%X|0",CHAR_EQUIPPLACENUM);
    BattleCommandDispach(CHAR_getWorkInt(c[0],CHAR_WORKFD),command);
    assert(CHAR_getWorkInt(c[0],CHAR_WORKBATTLECOM1)==BATTLE_COM_ITEM);
    for(round=0;round<40 && CHAR_getItemIndex(c[0],CHAR_EQUIPPLACENUM)==food;round++) {
        tick(1000);BATTLE_Loop();
    }
    assert(CHAR_getItemIndex(c[0],CHAR_EQUIPPLACENUM)==-1);
    assert(CHAR_getInt(c[0],CHAR_HP)>40); /* Real native item healing executed. */
    /* Reload the confirmed pre-match payload through the real archive parser
     * while the live battle has already consumed its food. A process restart
     * reads this archive from SAAC, without needing the in-memory snapshot. */
    assert(checkpoint_data[c[0]] && !strcmp(checkpoint_data[c[0]],persisted));
    if(getenv("STONEAGE_LADDER_SMOKE_ARCHIVE")) {
        FILE *archive=fopen(getenv("STONEAGE_LADDER_SMOKE_ARCHIVE"),"wb");size_t length=strlen(persisted);
        assert(archive && fwrite(persisted,1,length,archive)==length);assert(!fclose(archive));
    }
    recovery_data=strdup(checkpoint_data[c[0]]);assert(recovery_data);
    assert(CHAR_makeCharFromStringToArg(recovery_data,&recovered));free(recovery_data);
    assert(recovered.data[CHAR_HP]==before.data[CHAR_HP]);
    assert(!strcmp(recovered.string[CHAR_PERSISTENTID].string,before.string[CHAR_PERSISTENTID].string));
    assert(ITEM_CHECKINDEX(recovered.indexOfExistItems[CHAR_EQUIPPLACENUM]));
    assert(ITEM_getInt(recovered.indexOfExistItems[CHAR_EQUIPPLACENUM],ITEM_ID)==1234);
    assert(CHAR_CHECKINDEX(recovered.unionTable.indexOfPet[0]));
    assert(CHAR_getInt(recovered.unionTable.indexOfPet[0],CHAR_HP)==petbefore.data[CHAR_HP]);
    i=CHAR_getItemIndex(recovered.unionTable.indexOfPet[0],CHAR_EQUIPPLACENUM);
    assert(ITEM_CHECKINDEX(i) && ITEM_getInt(i,ITEM_ID)==1234);
    assert(!strcmp(ITEM_getChar(i,ITEM_NAME),ITEM_getChar(petitem,ITEM_NAME)));
#ifdef _TAKE_ITEMDAMAGE
    assert(ITEM_getInt(recovered.indexOfExistItems[CHAR_ARM],ITEM_DAMAGECRUSHE)==1);
    assert(ITEM_getInt(recovered.indexOfExistItems[CHAR_ARM],ITEM_MAXDAMAGECRUSHE)==100);
#endif
    for(i=0;i<CHAR_MAXPETHAVE;i++)if(CHAR_CHECKINDEX(recovered.unionTable.indexOfPet[i]))CHAR_endCharOneArray(recovered.unionTable.indexOfPet[i]);
    CHAR_endCharData(&recovered);
    assert(ITEM_CHECKINDEX(food) && StoneAge_LadderRetainsItem(food));
    extra=ITEM_makeItemAndRegist(1234);assert(ITEM_CHECKINDEX(extra) && extra!=food);
#ifdef _TAKE_ITEMDAMAGE
    BATTLE_ItemCrush(c[0],CHAR_ARM,300,0);
    assert(CHAR_getItemIndex(c[0],CHAR_ARM)==-1 && ITEM_CHECKINDEX(equipment));
#else
    CHAR_DelItem(c[0],CHAR_ARM);assert(ITEM_CHECKINDEX(equipment));
#endif
    CHAR_setInt(pet,CHAR_HP,1);
    CHAR_setWorkInt(c[0],CHAR_WORKDAMAGEREFLEC,3);CHAR_setWorkInt(pet,CHAR_WORKPOISON,5);
    ITEM_setInt(petitem,ITEM_MODIFYATTACK,777);
    /* Every regular save entry uses the cached pre-match serializer, including
     * all pets. It must neither persist nor undo the live battle's changes. */
    assert(!strcmp(CHAR_makeStringFromCharData(CHAR_getCharPointer(c[0])),persisted));
    assert(!strcmp(CHAR_makeStringFromCharIndex(c[0]),persisted));
    assert(!strcmp(CHAR_makePetStringFromPetIndex(pet),petpersisted));
    assert(CHAR_getItemIndex(c[0],CHAR_EQUIPPLACENUM)==-1 && CHAR_getInt(pet,CHAR_HP)==1);
    fd=CHAR_getWorkInt(c[0],CHAR_WORKFD);assert(StoneAge_LadderDisconnect(fd));
    assert(!strcmp(CHAR_makeStringFromCharData(CHAR_getCharPointer(c[0])),persisted));
    resume_player(c[0],45);
    assert(CHAR_getItemIndex(c[0],CHAR_EQUIPPLACENUM)==-1 && CHAR_getInt(pet,CHAR_HP)==1);
    /* Knockout does not send this participant through ordinary PK rewards or
     * detach its character. Its resources remain held until the whole finish. */
    CHAR_setInt(c[0],CHAR_HP,0);CHAR_setFlg(c[0],CHAR_ISDIE,1);
    BATTLE_GetProfit(b,0,0);BATTLE_UltimateExtra(b,c[1],c[0]);
    assert(Ladder_CharacterBattle(c[0])==b);
    assert(CHAR_getWorkInt(c[0],CHAR_WORKBATTLEMODE)==BATTLE_CHARMODE_FINAL);
    assert(CHAR_getItemIndex(c[0],CHAR_EQUIPPLACENUM)==-1);
    assert(sqlite3_open(getenv("STONEAGE_LADDER_DB"),&fault_database)==SQLITE_OK);
    matches_before=database_count(fault_database,"SELECT count(*) FROM ladder_match");
    ratings_before=database_count(fault_database,"SELECT sum(rating) FROM ladder_rating");
    /* Side zero loses first; reject only the winner's subsequent row so this
     * exercises rollback after the first player's rating write has run. */
    assert(sqlite3_exec(fault_database,"CREATE TRIGGER native_reject_rating BEFORE INSERT ON ladder_rating WHEN NEW.rating>1000 BEGIN SELECT RAISE(ABORT,'fixture rating failure'); END",NULL,NULL,NULL)==SQLITE_OK);
    force_finish(b,1,"defeat");BATTLE_Loop();assert(!native_battle(b));
    request(c[0],"status","");assert(strstr(last[c[0]],"\"phase\":\"settling\""));
    assert(StoneAge_LadderGuard(CHAR_getWorkInt(c[0],CHAR_WORKFD),"ID"));
    assert(StoneAge_LadderReserved(c[0]));
    assert(database_count(fault_database,"SELECT count(*) FROM ladder_match")==matches_before);
    assert(database_count(fault_database,"SELECT sum(rating) FROM ladder_rating")==ratings_before);
    tick(1000);
    request(c[0],"status","");assert(strstr(last[c[0]],"\"phase\":\"settling\""));
    assert(CHAR_getWorkInt(c[0],CHAR_WORKFD)==45 && online(c[0]));
    assert(!memcmp(CHAR_getCharPointer(c[0])->data,before.data,sizeof(before.data)));
    assert(!memcmp(CHAR_getCharPointer(c[0])->flg,before.flg,sizeof(before.flg)));
    assert(CHAR_getWorkInt(c[0],CHAR_WORKDAMAGEREFLEC)==before.workint[CHAR_WORKDAMAGEREFLEC]);
    assert(CHAR_getWorkInt(pet,CHAR_WORKPOISON)==petbefore.workint[CHAR_WORKPOISON]);
    assert(CHAR_getInt(pet,CHAR_HP)==petbefore.data[CHAR_HP]);
    assert(CHAR_getItemIndex(c[0],CHAR_EQUIPPLACENUM)==food && CHAR_getItemIndex(c[0],CHAR_ARM)==equipment);
    assert(!memcmp(ITEM_getItemPointer(food),&foodbefore,sizeof(foodbefore)));
    assert(!memcmp(ITEM_getItemPointer(equipment),&equipmentbefore,sizeof(equipmentbefore)));
    assert(!memcmp(ITEM_getItemPointer(petitem),&petitembefore,sizeof(petitembefore)));
    assert(!StoneAge_LadderRetainsItem(food));
    assert(!strcmp(CHAR_makeStringFromCharData(CHAR_getCharPointer(c[0])),persisted));
    assert(sqlite3_exec(fault_database,"DROP TRIGGER native_reject_rating",NULL,NULL,NULL)==SQLITE_OK);
    tick(0);request(c[0],"status","");assert(strstr(last[c[0]],"\"phase\":\"result\""));
    assert(database_count(fault_database,"SELECT count(*) FROM ladder_match")==matches_before+1);
    tick(0);Ladder_End(b,0,999,"duplicate",1);
    assert(database_count(fault_database,"SELECT count(*) FROM ladder_match")==matches_before+1);
    request(c[0],"status","");
    assert(strstr(last[c[0]],"\"winner_side\":1") && strstr(last[c[0]],"\"reason\":\"defeat\""));
    sqlite3_close(fault_database);
    fprintf(stderr,"\nnative settlement storage failure: partial rating transaction rolls back, resources restore while reserved, retry publishes original result exactly once\n");
    /* A second match uses the same restored item, then completes offline.
     * The data handed to SAAC must contain the original full inventory. */
    for(i=0;i<2;i++){request(c[i],"ack","");request(c[i],"ready","");request(c[i],"queue","");}
    tick(0);tick(10000);b=Ladder_CharacterBattle(c[0]);assert(b>=0);BATTLE_Loop();
    CHAR_setInt(c[0],CHAR_HP,40);BattleCommandDispach(CHAR_getWorkInt(c[0],CHAR_WORKFD),command);
    for(round=0;round<40 && CHAR_getItemIndex(c[0],CHAR_EQUIPPLACENUM)==food;round++){tick(1000);BATTLE_Loop();}
    assert(CHAR_getItemIndex(c[0],CHAR_EQUIPPLACENUM)==-1);
    assert(StoneAge_LadderDisconnect(CHAR_getWorkInt(c[0],CHAR_WORKFD)));
    expected_saved_data=persisted;
    force_finish(b,1,"defeat");BATTLE_Loop();tick(0);
    assert(native_players[c[0]].pending_save && CHAR_getItemIndex(c[0],CHAR_EQUIPPLACENUM)==food);
    expected_saved_data=NULL;
    assert(!StoneAge_LadderRetainsItem(food));
    CHAR_DelItem(c[0],CHAR_EQUIPPLACENUM);assert(!ITEM_CHECKINDEX(food));
    CHAR_DelItem(c[0],CHAR_ARM);CHAR_setItemIndex(pet,CHAR_EQUIPPLACENUM,-1);ITEM_endExistItemsOne(petitem);ITEM_endExistItemsOne(extra);
    assert(StoneAge_LadderOfflineSaveReply(saved_request,SUCCESSFUL));
    free(persisted);free(petpersisted);
    fprintf(stderr,"\nnative resources: real item consumption, equipment destruction, pre-match saves, reconnect, restoration and offline save passed\n");
}
static void snapshot_failure_test(void)
{
    int c[2],masks[2]={0,0},item,saves,pet,i;size_t buffer_size;Char before;char argument[200];
    c[0]=fixture(610,-1);c[1]=fixture(611,-1);
    item=ITEM_makeItemAndRegist(1234);assert(ITEM_CHECKINDEX(item));
    CHAR_setItemIndex(c[0],CHAR_EQUIPPLACENUM,item);before=*CHAR_getCharPointer(c[0]);
    snapshot_allocations_before_failure=1; /* First actor saved, second fails. */
    assert(prepare("failed_snapshot",c,masks,1)==-1);
    snapshot_allocations_before_failure=-1;
    assert(!native_character(c[0]) && !native_character(c[1]));
    assert(!StoneAge_LadderRetainsItem(item) && CHAR_getItemIndex(c[0],CHAR_EQUIPPLACENUM)==item);
    assert(!memcmp(CHAR_getCharPointer(c[0])->data,before.data,sizeof(before.data)));
    assert(CHAR_getWorkInt(c[0],CHAR_WORKBATTLEMODE)==BATTLE_CHARMODE_NONE);
    saves=checkpoint_count;buffer_size=saacproto.workbufsize;saacproto.workbufsize=128;
    assert(prepare("archive_too_large",c,masks,1)==-1);saacproto.workbufsize=buffer_size;
    assert(checkpoint_count==saves && !native_character(c[0]) && !native_character(c[1]));
    /* An unregistered pet still belongs in the owner's checkpoint. A large
     * pet archive must cancel preparation, never save a character minus it. */
    pet=fixture(612,c[0]);CHAR_getCharPointer(c[0])->unionTable.indexOfPet[0]=pet;
    memset(argument,'x',sizeof(argument)-1);argument[sizeof(argument)-1]=0;
    for(i=0;i<CHAR_MAXITEMHAVE;i++) {
        int gear=ITEM_makeItemAndRegist(1234);assert(ITEM_CHECKINDEX(gear));
        ITEM_setChar(gear,ITEM_ARGUMENT,argument);ITEM_setChar(gear,ITEM_EFFECTSTRING,argument);
        ITEM_setChar(gear,ITEM_NAME,argument);CHAR_setItemIndex(pet,i,gear);
    }
    assert(!*CHAR_makePetStringFromPetIndex(pet));
    assert(!*CHAR_makeStringFromCharData(CHAR_getCharPointer(c[0])));
    assert(prepare("unregistered_pet_too_large",c,masks,1)==-1);
    assert(checkpoint_count==saves && !native_character(c[0]) && !native_character(c[1]));
    CHAR_getCharPointer(c[0])->unionTable.indexOfPet[0]=-1;CHAR_endCharOneArray(pet);
    CHAR_DelItem(c[0],CHAR_EQUIPPLACENUM);assert(!ITEM_CHECKINDEX(item));
    smoke_delete(c[0]);smoke_delete(c[1]);
    fprintf(stderr,"\nnative snapshot failure: allocation and oversized pet archives cancel without saving or entering battle\n");
}
static void checkpoint_test(void)
{
    int c[2],old_request[2],i,b,hp;
    c[0]=fixture(710,-1);c[1]=fixture(711,-1);hp=CHAR_getInt(c[0],CHAR_HP);
    checkpoint_auto_ack=0;team(c,1);team(c+1,1);tick(0);
    for(i=0;i<2;i++) {
        assert(native_character(c[i]) && native_character(c[i])->battle==-1);
        old_request[i]=checkpoint_requests[c[i]];
        assert(old_request[i]<-OFFLINE_SAVE_BASE);
    }
    assert(StoneAge_LadderOfflineSaveReply(old_request[0],SUCCESSFUL));tick(9000);
    assert(Ladder_CharacterBattle(c[0])<0 && CHAR_getInt(c[0],CHAR_HP)==hp);
    assert(StoneAge_LadderOfflineSaveReply(old_request[1],FAILED));tick(0);
    assert(!native_character(c[0]) && !native_character(c[1]));
    assert(CHAR_getWorkInt(c[0],CHAR_WORKBATTLEMODE)==BATTLE_CHARMODE_NONE);
    for(i=0;i<2;i++){request(c[i],"ready","");request(c[i],"queue","");}
    tick(0);
    for(i=0;i<2;i++) {
        assert(checkpoint_requests[c[i]]!=old_request[i]);
        assert(StoneAge_LadderOfflineSaveReply(old_request[i],SUCCESSFUL));
        assert(!saved_actor(c[i])->save_confirmed);
        assert(StoneAge_LadderOfflineSaveReply(checkpoint_requests[c[i]],SUCCESSFUL));
    }
    tick(10000);b=Ladder_CharacterBattle(c[0]);assert(b>=0 && Ladder_CharacterBattle(c[1])==b);
    force_finish(b,0,"checkpoint_test");BATTLE_Loop();assert(!native_battle(b));
    for(i=0;i<2;i++){request(c[i],"ack","");smoke_delete(c[i]);}
    checkpoint_auto_ack=1;
    fprintf(stderr,"\nnative checkpoint: all saves confirmed before entry, save failure cancels, stale acknowledgements cannot start a new match\n");
}
static void contact_identity_test(void)
{
    int left=fixture(730,-1),right=fixture(731,-1);ADDRESSBOOK_entry *book,loaded;
    LadderContact card;
    char archived[512],id[37],key[256],*separator;
    book=CHAR_getAddressbookEntry(left,0);assert(book);memset(book,0,sizeof(*book));book->use=1;
    snprintf(book->cdkey,sizeof(book->cdkey),"%s",CHAR_getChar(right,CHAR_CDKEY));
    snprintf(book->charname,sizeof(book->charname),"%s",CHAR_getChar(right,CHAR_NAME));
    assert(contact(left,0)==LADDER_CONTACT_IDENTITY_REQUIRED);
    StoneAge_LadderBindContact(right,book);assert(contact(left,0)==right);
    assert(contact_entry(left,0,&card) && card.online);
    assert(!strcmp(card.id,StoneAge_CharacterIdentityGetLoadedByIndex(right)));
    snprintf(id,sizeof(id),"%s",book->persistent_character_id);
    snprintf(archived,sizeof(archived),"%s",ADDRESSBOOK_makeAddressbookString(book));
    memset(&loaded,0,sizeof(loaded));ADDRESSBOOK_makeAddressbookEntry(archived,&loaded);
    assert(!strcmp(loaded.persistent_character_id,id));
    snprintf(key,sizeof(key),"%s_%s",book->cdkey,book->charname);
    saacproto_DBGetEntryString_recv(-1,FAILED,"",DB_ADDRESSBOOK,key,0,0);
    assert(book->use && !strcmp(book->persistent_character_id,id));
    saacproto_DBGetEntryString_recv(-1,FAILED,"",DB_ADDRESSBOOK,key,0,1);
    assert(!book->use); /* Confirmed deletion still removes the card. */
    *book=loaded;
    /* A recreated character reuses both the account/name and native slot.
     * The persisted card must keep pointing to the former incarnation. */
    smoke_delete(right);right=fixture(731,-1);
    assert(strcmp(id,StoneAge_CharacterIdentityGetLoadedByIndex(right)));
    *book=loaded;assert(contact(left,0)==LADDER_CONTACT_IDENTITY_CHANGED);
    assert(contact_entry(left,0,&card) && !card.online && !strcmp(card.id,id));
    /* Exercise the real exchange caller, including its terminal return. */
    ADDRESSBOOK_addAddressBook(left,right);assert(contact(left,0)==right);
    /* Old archives and malformed identifiers cannot inherit prior memory or
     * silently bind themselves to whoever currently has the old name. */
    separator=strrchr(archived,'|');assert(separator);*separator=0;
    ADDRESSBOOK_makeAddressbookEntry(archived,&loaded);assert(!*loaded.persistent_character_id);
    strcat(archived,"|pc1_invalid");ADDRESSBOOK_makeAddressbookEntry(archived,&loaded);
    assert(!*loaded.persistent_character_id);*book=loaded;
    assert(contact(left,0)==LADDER_CONTACT_IDENTITY_REQUIRED);
    smoke_delete(left);smoke_delete(right);
    fprintf(stderr,"\nnative contacts: archived stable identity rejects same-name recreated characters; old cards require explicit exchange\n");
}
static void window_test(void)
{
    int c=fixture(720,-1);LadderProfile f;
    StoneAge_LadderWindowSent(c,28,-1,-1," \t\r\n");
    assert(profile(c,&f) && f.idle);
    StoneAge_LadderWindowSent(c,0,12,42,"NPC dialog");
    assert(profile(c,&f) && !f.idle && !strcmp(f.busy_reason,"dialog"));
    StoneAge_LadderWindowSent(c,28,-1,-1,"");
    assert(profile(c,&f) && !f.idle);
    StoneAge_LadderWindow(c,0);
    StoneAge_LadderWindowSent(c,28,-1,-1,"real notice");
    assert(profile(c,&f) && !f.idle);
    StoneAge_LadderWindow(c,0);
    StoneAge_LadderWindowSent(c,28,-1,42,"");
    assert(profile(c,&f) && !f.idle);
    StoneAge_LadderWindow(c,0);
    assert(profile(c,&f) && f.idle);
    smoke_delete(c);
    fprintf(stderr,"\nnative dialog: inert login notice allows preparation and preserves actual NPC dialogs\n");
}
int main(int argc,char **argv)
{
    assert(argc==2);StoneAge_BattleLogOffline();setNewTime();assert(util_Init());defaultConfig(argv[0]);
    assert(readconfigfile(argv[1]) && configmem(getMemoryunit(),getMemoryunitnum()) && memInit());
    assert(initConnect(64) && initObjectArray(64) && CHAR_initCharArray(32,64,8));
    assert(ITEM_readItemConfFile(getItemfile()) && ITEM_initExistItemsArray(128));
    assert(initFunctionTable() && PETSKILL_initPetskill(getPetskillfile()));
    assert(lssproto_InitServer(sink,65536)>=0 && saacproto_InitClient(sink,65536,0)>=0 && BATTLE_initBattleArray(8));
    /* Execute the actual reload hooks, not just the invalidation callback. */
    setenv("STONEAGE_BATTLE_RULESET_ID","test-startup-digest",1);
    assert(readconfigfile(argv[1]));assert(!getenv("STONEAGE_BATTLE_RULESET_ID"));
    setenv("STONEAGE_BATTLE_RULESET_ID","test-startup-digest",1);
    assert(PETSKILL_initPetskill(getPetskillfile()));assert(!getenv("STONEAGE_BATTLE_RULESET_ID"));
    tick(0);assert(enabled);contact_identity_test();window_test();external_warp_test();multiplayer_statistics_test();ride_statistics_test();loadout_test();power_configuration_test();countdown_configuration_test();resistance_configuration_test();external_interaction_test();external_petmail_test();pet_archive_compatibility_test();snapshot_failure_test();checkpoint_test();pet_switch_test();srand(42);reconnect_test();combat_test();statistics_test();resources_test();offline_save_test();
    StoneAge_BattleLogShutdown();Ladder_Shutdown();return 0;
}
