/* Authoritative single-threaded ladder coordinator. The native adapter owns
 * live characters and combat. SQLite owns ratings and completed settlements. */
#include "stoneage_ladder_core.h"
#include <sqlite3.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <stdarg.h>
#include <math.h>
#include <limits.h>
#include <errno.h>
#include <fcntl.h>
#include <unistd.h>
#include <sys/stat.h>

#define RECEIPTS 32
#define REVISION_MAX 9007199254740991ULL
#define REQUEST_MAX 256
#define INVITE_MS 60000
#define RECONNECT_MS 60000
#define COUNTDOWN_MS 10000
#define COOLDOWN_MS 300000
#define MAX_MATCH_MS 1800000
#define OFFLINE_ROOM_MS 300000

typedef struct {
    char request[REQUEST_MAX], code[48], boot[33];
    unsigned long long revision;
} Receipt;
typedef struct {
    int used, room, match, mask, ready, ratings[5], acknowledged, abandoned;
    LadderProfile profile;
	char strategy[33];
    unsigned long long revision, frozen_hash;
    LadderTime offline_at, offline_used, cooldown_until, detached_at;
} Player;
typedef struct {
    int used, mode, members[5], count, match;
    char id[65];
    LadderTime queued_at;
} Room;
typedef struct {
    int used, from, to, room;
    char id[65];
    LadderTime expires;
} Invitation;
typedef struct {
    int used, mode, rooms[2], members[10], battle, phase, winner, turns, rated;
    int journal_terminal, cancel_failed;
    char id[65], reason[48];
    LadderTime start_at, started_at, ended_at, queued_at[2];
    double powers[10], ratings[2];
    int before[10], delta;
    LadderProfile profiles[10];
    LadderStats stats[10];
    char result[LADDER_WIRE_MAX/2];
} Match;
typedef struct { char *data; size_t size, length; int failed; } JSON;

static Player players[LADDER_PLAYER_MAX];
static Room rooms[LADDER_ROOM_MAX];
static Invitation invitations[LADDER_INVITE_MAX];
static Match matches[LADDER_MATCH_MAX];
static LadderHooks hooks;
static sqlite3 *db;
static unsigned long long revision, serial;
static unsigned long long published_revision;
static char boot[33];
typedef struct PendingNotice {
    struct PendingNotice *next;
    int character;
    char wire[];
} PendingNotice;
static PendingNotice *notice_head,*notice_tail;
static int mutation_active,mutation_failed;
static int revision_exhausted;
/* Requests only change room/player/invitation state and (for ACK) their
 * current match's release flags. Native combat runs separately in Tick. */
typedef struct {
    Player players[LADDER_PLAYER_MAX];
    Room rooms[LADDER_ROOM_MAX];
    Invitation invitations[LADDER_INVITE_MAX];
    Match match;
    int match_index;
    unsigned long long revision,serial;
} MutationBackup;
static void result_json(Match *v);
static int journal_match(Match *v,int terminal);
static int publish_pending(const char *id);
static int recover_pending(void);

static void append(JSON *j, const char *format, ...)
{
    int n;
    va_list ap;
    if(j->failed) return;
    va_start(ap,format); n=vsnprintf(j->data+j->length,j->size-j->length,format,ap); va_end(ap);
    if(n<0 || (size_t)n>=j->size-j->length) { j->failed=1; return; }
    j->length+=(size_t)n;
}
static int token(const char *s, size_t max)
{
    size_t n=0;
    for(;s[n];n++) if(n>=max || !((s[n]>='a'&&s[n]<='z') ||
        (s[n]>='A'&&s[n]<='Z') || (s[n]>='0'&&s[n]<='9') || s[n]=='_' || s[n]=='-')) return 0;
    return n>0;
}
static int number(const char *s, int low, int high)
{
    char *end;const char *at;
    long n;
    if(!*s || (*s=='0' && s[1])) return -1;
    for(at=s;*at;at++)if(*at<'0' || *at>'9')return -1;
    errno=0;
    n=strtol(s,&end,10);
    return errno || *end || n<low || n>high ? -1 : (int)n;
}
static void identifier(char *out, const char *prefix)
{ snprintf(out,65,"%s_%s_%llu",prefix,boot,++serial); }
static int execsql(const char *sql)
{ return sqlite3_exec(db,sql,NULL,NULL,NULL)==SQLITE_OK; }

static unsigned long long next_revision(void)
{
    if(revision>=REVISION_MAX) {
        revision_exhausted=1;
        if(mutation_active)mutation_failed=1;
        return 0;
    }
    return ++revision;
}

static int store_revision(unsigned long long value)
{
    sqlite3_stmt *s=NULL;int ok=0;
    if(value>REVISION_MAX)return 0; /* JSON's exact integer range. */
    if(sqlite3_prepare_v2(db,"UPDATE ladder_revision SET high=MAX(high,?) WHERE singleton=1",-1,&s,NULL)==SQLITE_OK) {
        sqlite3_bind_int64(s,1,(sqlite3_int64)value);
        ok=sqlite3_step(s)==SQLITE_DONE && sqlite3_changes(db)==1;
    }
    sqlite3_finalize(s);return ok;
}
/* No observed version can be reused after restart or clock rollback. Reads
 * of an already published snapshot need no additional database write. */
static void publish_wire(int character,const char *wire,unsigned long long value)
{
    if(revision_exhausted)return;
    if(mutation_active) {
        size_t size=strlen(wire)+1;PendingNotice *notice;
        if(mutation_failed)return;
        notice=malloc(sizeof(*notice)+size);
        if(!notice){mutation_failed=1;return;}
        notice->next=NULL;notice->character=character;memcpy(notice->wire,wire,size);
        if(notice_tail)notice_tail->next=notice;else notice_head=notice;
        notice_tail=notice;return;
    }
    if(value>published_revision) {
        if(!store_revision(value))return; /* Caller times out; no false receipt. */
        published_revision=value;
    }
    hooks.send(character,wire);
}
static void finish_notices(int publish)
{
    PendingNotice *notice;
    while((notice=notice_head)!=NULL) {
        notice_head=notice->next;
        if(publish)hooks.send(notice->character,notice->wire);
        free(notice);
    }
    notice_tail=NULL;
}
/* -1 is a storage failure, 0 a miss, 1 an exact ID lookup. Never turn a
 * failed lookup into permission to execute an uncertain request again. */
static int receipt_read(const char *player_id,const char *id,Receipt *out)
{
    sqlite3_stmt *s=NULL;int result=-1,step;const char *request,*code,*epoch;
    if(sqlite3_prepare_v2(db,"SELECT request,code,revision,boot FROM ladder_receipt WHERE player=? AND request_id=?",-1,&s,NULL)!=SQLITE_OK)goto done;
    sqlite3_bind_text(s,1,player_id,-1,SQLITE_TRANSIENT);sqlite3_bind_text(s,2,id,-1,SQLITE_TRANSIENT);
    step=sqlite3_step(s);if(step==SQLITE_DONE){result=0;goto done;}if(step!=SQLITE_ROW)goto done;
    request=(const char*)sqlite3_column_text(s,0);code=(const char*)sqlite3_column_text(s,1);epoch=(const char*)sqlite3_column_text(s,3);
    if(!request || strlen(request)>=sizeof(out->request) || !code || !token(code,sizeof(out->code)-1) ||
       !epoch || !token(epoch,32) || sqlite3_column_int64(s,2)<=0)goto done;
    strcpy(out->request,request);strcpy(out->code,code);strcpy(out->boot,epoch);
    out->revision=(unsigned long long)sqlite3_column_int64(s,2);result=1;
done:
    sqlite3_finalize(s);return result;
}
static int receipt_write(int p,const char *id,const char *wire,const char *code)
{
    sqlite3_stmt *s=NULL;int ok=0;
    if(sqlite3_prepare_v2(db,"INSERT INTO ladder_receipt(player,request_id,request,code,revision,boot) VALUES(?,?,?,?,?,?)",-1,&s,NULL)!=SQLITE_OK)goto done;
    sqlite3_bind_text(s,1,players[p].profile.id,-1,SQLITE_TRANSIENT);sqlite3_bind_text(s,2,id,-1,SQLITE_TRANSIENT);
    sqlite3_bind_text(s,3,wire,-1,SQLITE_TRANSIENT);sqlite3_bind_text(s,4,code,-1,SQLITE_TRANSIENT);
    sqlite3_bind_int64(s,5,(sqlite3_int64)players[p].revision);sqlite3_bind_text(s,6,boot,-1,SQLITE_TRANSIENT);
    if(sqlite3_step(s)!=SQLITE_DONE || sqlite3_changes(db)!=1)goto done;
    sqlite3_finalize(s);s=NULL;
    if(sqlite3_prepare_v2(db,"DELETE FROM ladder_receipt WHERE player=?1 AND rowid NOT IN (SELECT rowid FROM ladder_receipt WHERE player=?1 ORDER BY rowid DESC LIMIT ?2)",-1,&s,NULL)!=SQLITE_OK)goto done;
    sqlite3_bind_text(s,1,players[p].profile.id,-1,SQLITE_TRANSIENT);sqlite3_bind_int(s,2,RECEIPTS);
    ok=sqlite3_step(s)==SQLITE_DONE;
done:
    sqlite3_finalize(s);return ok;
}

double Ladder_EquivalentPoints(double hp, double attack, double defense, double speed)
{ return (3.0*hp+10.0*(attack+defense+speed))/14.0; }
double Ladder_PowerGap(double a, double b)
{ return a+b>0 ? 2.0*fabs(a-b)/(a+b) : 0; }
int Ladder_RatingDelta(double a, double b, double score)
{
    double change=32.0*(score-1.0/(1.0+pow(10.0,(b-a)/400.0)));
    return (int)(change>=0 ? floor(change+0.5) : ceil(change-0.5));
}
static int rating(const char *id, int mode)
{
    sqlite3_stmt *s=NULL;
    int value=1000;
    if(sqlite3_prepare_v2(db,"SELECT rating FROM ladder_rating WHERE player=? AND mode=?",-1,&s,NULL)==SQLITE_OK) {
        sqlite3_bind_text(s,1,id,-1,SQLITE_TRANSIENT);sqlite3_bind_int(s,2,mode);
        if(sqlite3_step(s)==SQLITE_ROW) value=sqlite3_column_int(s,0);
    }
    sqlite3_finalize(s);return value;
}
static LadderTime cooldown(const char *id)
{
    sqlite3_stmt *s=NULL;LadderTime value=0;
    if(sqlite3_prepare_v2(db,"SELECT until_ms FROM ladder_cooldown WHERE player=?",-1,&s,NULL)==SQLITE_OK) {
        sqlite3_bind_text(s,1,id,-1,SQLITE_TRANSIENT);
        if(sqlite3_step(s)==SQLITE_ROW)value=sqlite3_column_int64(s,0);
    }
    sqlite3_finalize(s);return value;
}
static int save_cooldown(const char *id,LadderTime until)
{
    sqlite3_stmt *s=NULL;int ok=0;
    if(sqlite3_prepare_v2(db,"INSERT INTO ladder_cooldown(player,until_ms) VALUES(?,?) ON CONFLICT(player) DO UPDATE SET until_ms=MAX(until_ms,excluded.until_ms)",-1,&s,NULL)==SQLITE_OK) {
        sqlite3_bind_text(s,1,id,-1,SQLITE_TRANSIENT);sqlite3_bind_int64(s,2,until);
        ok=sqlite3_step(s)==SQLITE_DONE;
    }
    sqlite3_finalize(s);return ok;
}
static int find(int character)
{
    int i;
    if(character<0)return -1;
    for(i=0;i<LADDER_PLAYER_MAX;i++) if(players[i].used && players[i].profile.character==character) return i;
    return -1;
}
static int strategy_preference(const char *id,char *strategy,const char *write)
{
    sqlite3_stmt *s=NULL;int ok=0;
    const char *sql=write?"INSERT INTO ladder_preference(player,strategy) VALUES(?,?) ON CONFLICT(player) DO UPDATE SET strategy=excluded.strategy":"SELECT strategy FROM ladder_preference WHERE player=?";
    if(sqlite3_prepare_v2(db,sql,-1,&s,NULL)==SQLITE_OK) {
        sqlite3_bind_text(s,1,id,-1,SQLITE_TRANSIENT);
        if(write) {sqlite3_bind_text(s,2,write,-1,SQLITE_TRANSIENT);ok=sqlite3_step(s)==SQLITE_DONE;}
        else if(sqlite3_step(s)==SQLITE_ROW) {
            const char *value=(const char*)sqlite3_column_text(s,0);
            if(value && token(value,32)){snprintf(strategy,33,"%s",value);ok=1;}
        }
    }
    sqlite3_finalize(s);return ok;
}
static int refresh(int p)
{
    LadderProfile f;
    if(p<0 || !players[p].used || !hooks.profile(players[p].profile.character,&f) ||
       strcmp(f.id,players[p].profile.id)) return 0;
    players[p].profile=f;return 1;
}
static int player(int character)
{
    LadderProfile f;
    int i,empty=-1;
    if(!hooks.profile(character,&f) || !token(f.id,64)) return -1;
    /* The native engine reuses array slots. A new persistent identity never
     * inherits reservations, ratings or receipt history from that slot. */
    i=find(character);
    if(i>=0 && strcmp(players[i].profile.id,f.id)) {
        /* Slot retirement can touch the native battle engine. Do not perform
         * it as a side effect of a transactional invitation lookup. */
        if(mutation_active)return -1;
        Ladder_Detached(character);
    }
    for(i=0;i<LADDER_PLAYER_MAX;i++) {
        if(players[i].used && !strcmp(players[i].profile.id,f.id)) {
            players[i].profile=f;players[i].detached_at=0;return i;
        }
        if(!players[i].used && empty<0) empty=i;
    }
    if(empty<0 || revision_exhausted) return -1;
    if(!next_revision())return -1;
    i=empty;memset(&players[i],0,sizeof(Player));players[i].used=1;
    players[i].room=players[i].match=-1;players[i].profile=f;
    strcpy(players[i].strategy,"manual");
    strategy_preference(f.id,players[i].strategy,NULL);
    players[i].cooldown_until=cooldown(f.id);
    players[i].mask=(f.active_pet>=0 ? 1<<f.active_pet : 0) | (f.ride_pet>=0 ? 1<<f.ride_pet : 0);
    players[i].revision=revision;
    for(empty=0;empty<5;empty++) players[i].ratings[empty]=rating(f.id,empty+1);
    return i;
}
static double power(int p)
{
    int i;double n=players[p].profile.character_power;
    for(i=0;i<5;i++) if(players[p].mask&(1<<i)) n+=players[p].profile.pet_power[i];
    return n;
}
static const char *phase(int p)
{
    int m=players[p].match,r=players[p].room;
    if(m>=0) {
        if(matches[m].phase==1) return "countdown";
        if(matches[m].phase==2) return "battle";
        if(matches[m].phase==3 || matches[m].phase==5) return "settling";
        if(matches[m].phase==4 && !players[p].acknowledged) return "result";
    }
    return r<0 ? "idle" : rooms[r].queued_at ? "queued" : "lobby";
}
static void player_json(JSON *j,int p,int mode,const LadderProfile *saved,double saved_power,int saved_rating)
{
    Player *v=&players[p];
    const LadderProfile *f=saved ? saved : &v->profile;
    LadderTime remaining=RECONNECT_MS-v->offline_used;
    if(v->offline_at) remaining-=hooks.now()-v->offline_at;
    if(remaining<0) remaining=0;
    append(j,"{\"id\":\"%s\",\"strategy\":\"%s\",\"name_hex\":\"%s\",\"online\":%s,\"ready\":%s,"
        "\"power\":%.6f,\"rating\":%d,\"pet_mask\":%d,\"available_pet_mask\":%d,"
        "\"active_pet\":%d,\"ride_pet\":%d,\"idle\":%s,\"busy_reason\":\"%s\","
        "\"reconnect_remaining_ms\":%lld,\"abandoned\":%s}",f->id,v->strategy,f->name_hex,
        v->profile.online ? "true":"false",v->ready ? "true":"false",saved?saved_power:power(p),
        saved?saved_rating:v->ratings[mode-1],v->mask,f->pet_mask,f->active_pet,f->ride_pet,
        v->profile.idle ? "true":"false",v->profile.busy_reason?v->profile.busy_reason:"",remaining,
        v->abandoned ? "true":"false");
}
static void room_json(JSON *j,int r)
{
    int i;Room *v=&rooms[r];
    append(j,"{\"id\":\"%s\",\"leader_id\":\"%s\",\"mode\":%d,\"phase\":\"%s\","
        "\"queued_at_ms\":%lld,\"members\":[",v->id,players[v->members[0]].profile.id,v->mode,
        phase(v->members[0]),v->queued_at);
    for(i=0;i<v->count;i++) { if(i)append(j,",");player_json(j,v->members[i],v->mode,NULL,0,0); }
    append(j,"]}");
}
static void match_json(JSON *j,int m,int p)
{
    Match *v=&matches[m];int i,side=0,s;double totals[2]={0,0};
    for(i=0;i<v->mode*2;i++) { totals[i/v->mode]+=v->powers[i];if(v->members[i]==p)side=i/v->mode; }
    append(j,"{\"id\":\"%s\",\"mode\":%d,\"phase\":\"%s\",\"side\":%d,"
        "\"start_at_ms\":%lld,\"started_at_ms\":%lld,\"power_gap\":%.8f,\"teams\":[",
        v->id,v->mode,phase(p),side,v->start_at,v->started_at,Ladder_PowerGap(totals[0],totals[1]));
    for(s=0;s<2;s++) {
        if(s)append(j,",");
        append(j,"{\"power\":%.6f,\"rating\":%.6f,\"members\":[",totals[s],v->ratings[s]);
        for(i=0;i<v->mode;i++) { int k=s*v->mode+i;if(i)append(j,",");
            player_json(j,v->members[k],v->mode,&v->profiles[k],v->powers[k],v->before[k]); }
        append(j,"]}");
    }
    append(j,"]}");
}
static int stored_result(int p,const char *id,char *out,size_t size)
{
    sqlite3_stmt *s=NULL;int ok=0;
    const char *sql=*id ?
        "SELECT m.result FROM ladder_match m JOIN ladder_participant p ON p.match_id=m.id WHERE p.player=? AND m.id=?" :
        "SELECT m.result FROM ladder_match m JOIN ladder_participant p ON p.match_id=m.id WHERE p.player=? AND p.ack=0 ORDER BY m.ended_at DESC LIMIT 1";
    if(sqlite3_prepare_v2(db,sql,-1,&s,NULL)==SQLITE_OK) {
        sqlite3_bind_text(s,1,players[p].profile.id,-1,SQLITE_TRANSIENT);
        if(*id)sqlite3_bind_text(s,2,id,-1,SQLITE_TRANSIENT);
        if(sqlite3_step(s)==SQLITE_ROW && (size_t)sqlite3_column_bytes(s,0)<size) {
            strcpy(out,(const char*)sqlite3_column_text(s,0));ok=1;
        }
    }
    sqlite3_finalize(s);return ok;
}
static void request_hex(char *out,const char *request)
{
    static const char digits[]="0123456789abcdef";size_t i=0;
    if(request)for(;request[i] && i<REQUEST_MAX-1;i++) {
        unsigned char c=(unsigned char)request[i];out[i*2]=digits[c>>4];out[i*2+1]=digits[c&15];
    }
    out[i*2]=0;
}
static void send_reply(int p,const char *request,const char *code,int replay,unsigned long long applied,const char *event,const char *result_id,const char *original,const char *receipt_boot)
{
    char wire[LADDER_WIRE_MAX],stored[LADDER_WIRE_MAX/2],hex[REQUEST_MAX*2];JSON j={wire,sizeof(wire),0,0};
    int i,first=1,r=players[p].room,m=players[p].match,mode=r<0?1:rooms[r].mode;
    const char *result=NULL,*current_phase=phase(p);
    if(!players[p].profile.online) return;
    if(result_id && *result_id) { if(stored_result(p,result_id,stored,sizeof(stored)))result=stored; }
    else if(m>=0 && matches[m].phase==4 && !players[p].acknowledged) result=matches[m].result;
    else if(m<0 && stored_result(p,"",stored,sizeof(stored))) { result=stored;current_phase="result"; }
    request_hex(hex,original);
    append(&j,"LADDER|{\"version\":1,\"request_id\":\"%s\",\"request_hex\":\"%s\",\"ok\":%s,\"code\":\"%s\","
        "\"replay\":%s,\"applied_revision\":%llu,\"revision\":%llu,\"sequence\":%llu,"
        "\"server_time_ms\":%lld,\"server_boot\":\"%s\",\"receipt_boot\":\"%s\",\"event\":\"%s\",\"snapshot\":{\"phase\":\"%s\",\"self\":",
        request,hex,!strcmp(code,"ok")?"true":"false",code,replay?"true":"false",applied,
        players[p].revision,players[p].revision,hooks.now(),boot,receipt_boot?receipt_boot:"",event,current_phase);
    player_json(&j,p,mode,NULL,0,0);
    append(&j,",\"ratings\":[%d,%d,%d,%d,%d],\"cooldown_until_ms\":%lld,\"room\":",
        players[p].ratings[0],players[p].ratings[1],players[p].ratings[2],players[p].ratings[3],players[p].ratings[4],players[p].cooldown_until);
    if(r<0)append(&j,"null");else room_json(&j,r);
    append(&j,",\"invitations\":[");
    for(i=0;i<LADDER_INVITE_MAX;i++) if(invitations[i].used && invitations[i].to==p) {
        Invitation *v=&invitations[i];if(!first)append(&j,",");
        first=0;
        append(&j,"{\"id\":\"%s\",\"room_id\":\"%s\",\"mode\":%d,\"expires_at_ms\":%lld,\"from\":",
            v->id,rooms[v->room].id,rooms[v->room].mode,v->expires);
        player_json(&j,v->from,rooms[v->room].mode,NULL,0,0);append(&j,"}");
    }
    append(&j,"],\"match\":");if(m<0)append(&j,"null");else match_json(&j,m,p);
    append(&j,",\"result\":%s}}",result?result:"null");
    if(!j.failed)publish_wire(players[p].profile.character,wire,players[p].revision);
}
static void changed(int p,const char *event)
{
    unsigned long long value=next_revision();
    if(!value)return;
    players[p].revision=value;send_reply(p,"","ok",0,0,event,NULL,NULL,NULL);
}
/* A directory read must not carry the potentially large match/result and
 * invitation snapshots. Its receipt never replaces the gameplay projection. */
static void send_contacts(int p,const char *request,const char *original)
{
    char wire[LADDER_WIRE_MAX],hex[REQUEST_MAX*2];JSON j={wire,sizeof(wire),0,0};
    LadderContact entry;int i,first=1;
    request_hex(hex,original);
    append(&j,"LADDER|{\"version\":1,\"request_id\":\"%s\",\"request_hex\":\"%s\","
        "\"ok\":true,\"code\":\"ok\",\"revision\":%llu,\"sequence\":0,\"server_time_ms\":%lld,"
        "\"event\":\"contacts_lookup\",\"snapshot\":{\"self\":{\"id\":\"%s\"}},\"contacts\":[",
        request,hex,players[p].revision,hooks.now(),players[p].profile.id);
    for(i=0;i<80;i++) {
        memset(&entry,0,sizeof(entry));
        if(!hooks.contact_entry(players[p].profile.character,i,&entry))continue;
        if(!first)append(&j,",");
        first=0;
        append(&j,"{\"slot\":%d,\"id\":\"%s\",\"name_hex\":\"%s\",\"online\":%s}",
            i,entry.id,entry.name_hex,entry.online?"true":"false");
    }
    append(&j,"]}");if(!j.failed)publish_wire(players[p].profile.character,wire,players[p].revision);
}
static void room_changed(int r,const char *event)
{ int i;for(i=0;i<rooms[r].count;i++)changed(rooms[r].members[i],event); }
static void reset_ready(int r)
{ int i;for(i=0;i<rooms[r].count;i++)players[rooms[r].members[i]].ready=0; }
static void expire_invites(int r)
{
    int i;
    for(i=0;i<LADDER_INVITE_MAX;i++) if(invitations[i].used && invitations[i].room==r) {
        invitations[i].used=0;changed(invitations[i].to,"invitation_expired");
    }
}
static int locked(int r)
{ return rooms[r].queued_at || rooms[r].match>=0; }
static const char *eligible(int p,int require_ready)
{
    Player *v=&players[p];
    if(!refresh(p) || !v->profile.online) return "member_offline";
    if(!v->profile.idle) return "member_busy";
    if(require_ready && !v->ready) return "member_not_ready";
    if(v->cooldown_until>hooks.now()) return "cooldown";
    if((v->mask & v->profile.pet_mask)!=v->mask ||
       (v->profile.active_pet>=0 && !(v->mask&(1<<v->profile.active_pet))) ||
       (v->profile.ride_pet>=0 && !(v->mask&(1<<v->profile.ride_pet)))) return "invalid_loadout";
    if(!isfinite(power(p)) || power(p)<=0) return "invalid_power";
    return "ok";
}
static int remove_member(int r,int p)
{
    int i,k=-1;
    for(i=0;i<rooms[r].count;i++)if(rooms[r].members[i]==p)k=i;
    if(k<0)return 0;
    for(i=k+1;i<rooms[r].count;i++)rooms[r].members[i-1]=rooms[r].members[i];
    rooms[r].count--;players[p].room=-1;players[p].ready=0;
    expire_invites(r);reset_ready(r);
    if(!rooms[r].count)rooms[r].used=0;
    changed(p,"left");room_changed(r,"room_changed");return 1;
}
static void release_result_sides(int m)
{
    int i;
    /* A side may continue independently; unacknowledged offline results stay
     * in SQLite even when their transient room membership expires. */
    for(i=0;i<2;i++) {
        int r=matches[m].rooms[i],k,side_ack=1;
        if(rooms[r].match!=m)continue;
        for(k=0;k<rooms[r].count;k++)if(!players[rooms[r].members[k]].acknowledged)side_ack=0;
        if(side_ack) {
            for(k=0;k<rooms[r].count;k++)players[rooms[r].members[k]].match=-1;
            rooms[r].match=-1;room_changed(r,"room_changed");
        }
    }
    if(rooms[matches[m].rooms[0]].match!=m && rooms[matches[m].rooms[1]].match!=m)matches[m].used=0;
}
static const char *acknowledge(int p)
{
    sqlite3_stmt *s=NULL;int ok=0,m=players[p].match;
    if(m>=0 && matches[m].phase!=4) return "match_active";
    if(sqlite3_prepare_v2(db,"UPDATE ladder_participant SET ack=1 WHERE player=? AND ack=0",-1,&s,NULL)==SQLITE_OK) {
        sqlite3_bind_text(s,1,players[p].profile.id,-1,SQLITE_TRANSIENT);ok=sqlite3_step(s)==SQLITE_DONE;
    }
    sqlite3_finalize(s);if(!ok)return "storage_unavailable";
    players[p].acknowledged=1;players[p].ready=0;
    if(m>=0)release_result_sides(m);
    changed(p,"acknowledged");return "ok";
}

static int contact_argument(const char *arg,const char **identity)
{
    char slot[3];const char *sep=strchr(arg,':');size_t length;
    if(!sep || (length=(size_t)(sep-arg))<1 || length>=sizeof(slot) || !token(sep+1,64))return -1;
    memcpy(slot,arg,length);slot[length]=0;if(identity)*identity=sep+1;
    return number(slot,0,79);
}
static int valid_operation(const char *op,const char *arg)
{
    if(!strcmp(op,"strategy"))return token(arg,32);
    if(!strcmp(op,"status") || !strcmp(op,"contacts") || !strcmp(op,"ready") || !strcmp(op,"unready") ||
       !strcmp(op,"queue") || !strcmp(op,"cancel") || !strcmp(op,"leave") || !strcmp(op,"ack"))return !*arg;
    if(!strcmp(op,"create") || !strcmp(op,"mode"))return number(arg,1,5)>=0;
    if(!strcmp(op,"invite"))return contact_argument(arg,NULL)>=0;
    if(!strcmp(op,"loadout"))return number(arg,0,31)>=0;
    if(!strcmp(op,"accept") || !strcmp(op,"decline") || !strcmp(op,"kick") || !strcmp(op,"leader"))return token(arg,64);
    if(!strcmp(op,"result"))return !*arg || token(arg,64);
    return 0;
}

static void unavailable_reply(int character,const char *request,const char *code,const char *original)
{
    char wire[1024],hex[REQUEST_MAX*2];request_hex(hex,original);
    snprintf(wire,sizeof(wire),"LADDER|{\"version\":1,\"request_id\":\"%s\",\"request_hex\":\"%s\",\"ok\":false,\"code\":\"%s\",\"snapshot\":{\"phase\":\"unavailable\"}}",request,hex,code);
    if(hooks.send)hooks.send(character,wire);
}
static const char *mutate(int p,const char *op,const char *arg)
{
    Player *v=&players[p];int r=v->room,i,n,target=-1,slot=-1,inbound=0;const char *code;
    char outstanding[LADDER_WIRE_MAX/2];
    if(!strcmp(op,"strategy")) {
        if(!token(arg,32))return "invalid_strategy";
        if(!strategy_preference(v->profile.id,NULL,arg))return "database_unavailable";
        snprintf(v->strategy,sizeof(v->strategy),"%s",arg);changed(p,"strategy_changed");return "ok";
    }
    if(!strcmp(op,"ack"))return acknowledge(p);
    if(v->match>=0 || stored_result(p,"",outstanding,sizeof(outstanding)))return "result_or_match_pending";
    if(!strcmp(op,"create")) {
        n=number(arg,1,5);if(n<0)return "invalid_mode";
        if(r>=0)return "already_in_room";
        for(i=0;i<LADDER_ROOM_MAX;i++)if(!rooms[i].used){slot=i;break;}
        if(slot<0)return "capacity";
        memset(&rooms[slot],0,sizeof(Room));rooms[slot].used=1;rooms[slot].mode=n;rooms[slot].match=-1;
        identifier(rooms[slot].id,"room");rooms[slot].members[0]=p;rooms[slot].count=1;v->room=slot;
        changed(p,"room_created");return "ok";
    }
    if(!strcmp(op,"accept") || !strcmp(op,"decline")) {
        for(i=0;i<LADDER_INVITE_MAX;i++)if(invitations[i].used && !strcmp(invitations[i].id,arg)){slot=i;break;}
        if(slot<0 || invitations[slot].to!=p || invitations[slot].expires<=hooks.now())return "invitation_expired";
        if(!strcmp(op,"decline")){invitations[slot].used=0;changed(p,"invitation_declined");return "ok";}
        if(r>=0)return "already_in_room";
        r=invitations[slot].room;if(!rooms[r].used || locked(r))return "room_locked";
        if(rooms[r].count>=5)return "room_full";
        invitations[slot].used=0;rooms[r].members[rooms[r].count++]=p;v->room=r;
        for(i=0;i<LADDER_INVITE_MAX;i++)if(invitations[i].to==p)invitations[i].used=0;
        reset_ready(r);room_changed(r,"member_joined");return "ok";
    }
    if(r<0)return "not_in_room";
    if(!strcmp(op,"cancel")) {
        if(rooms[r].members[0]!=p)return "leader_required";
        if(!rooms[r].queued_at)return "not_queued";
        rooms[r].queued_at=0;reset_ready(r);room_changed(r,"queue_cancelled");return "ok";
    }
    if(locked(r))return "room_locked";
    if(!strcmp(op,"leave")){remove_member(r,p);return "ok";}
    if(!strcmp(op,"loadout")) {
        n=number(arg,0,31);if(n<0)return "invalid_loadout";
        refresh(p);if((n&v->profile.pet_mask)!=n)return "invalid_loadout";
        v->mask=n;reset_ready(r);room_changed(r,"loadout_changed");return "ok";
    }
    if(!strcmp(op,"ready")) {
        code=eligible(p,0);if(strcmp(code,"ok"))return code;
        v->ready=1;room_changed(r,"member_ready");return "ok";
    }
    if(!strcmp(op,"unready")){v->ready=0;room_changed(r,"member_unready");return "ok";}
    if(!strcmp(op,"invite")) {
        const char *expected_identity=NULL;
        n=contact_argument(arg,&expected_identity);if(n<0)return "invalid_contact";
        if(rooms[r].count>=5)return "room_full";
        target=hooks.contact(v->profile.character,n);
        if(target==LADDER_CONTACT_IDENTITY_REQUIRED)return "contact_identity_required";
        if(target==LADDER_CONTACT_IDENTITY_CHANGED)return "contact_identity_changed";
        if(target<0)return "contact_unavailable";
        target=player(target);if(target<0)return "identity_unavailable";
        if(strcmp(players[target].profile.id,expected_identity))return "contact_slot_changed";
        if(players[target].room>=0 || players[target].match>=0)return "already_in_room";
        for(i=0;i<LADDER_INVITE_MAX;i++) {
            if(invitations[i].used && invitations[i].room==r && invitations[i].to==target)return "invitation_pending";
            if(invitations[i].used && invitations[i].to==target)inbound++;
            if(!invitations[i].used && slot<0)slot=i;
        }
        if(slot<0 || inbound>=16)return "capacity";
        invitations[slot].used=1;invitations[slot].room=r;invitations[slot].from=p;invitations[slot].to=target;
        invitations[slot].expires=hooks.now()+INVITE_MS;identifier(invitations[slot].id,"invite");
        changed(target,"invitation_received");changed(p,"invitation_sent");return "ok";
    }
    if(rooms[r].members[0]!=p)return "leader_required";
    if(!strcmp(op,"mode")) {
        n=number(arg,1,5);if(n<0)return "invalid_mode";
        rooms[r].mode=n;reset_ready(r);expire_invites(r);room_changed(r,"mode_changed");return "ok";
    }
    if(!strcmp(op,"kick") || !strcmp(op,"leader")) {
        for(i=1;i<rooms[r].count;i++)if(!strcmp(players[rooms[r].members[i]].profile.id,arg))target=i;
        if(target<0)return "member_not_found";
        if(!strcmp(op,"kick")){remove_member(r,rooms[r].members[target]);return "ok";}
        rooms[r].members[0]=rooms[r].members[target];rooms[r].members[target]=p;
        reset_ready(r);room_changed(r,"leader_changed");return "ok";
    }
    if(!strcmp(op,"queue")) {
        if(rooms[r].count!=rooms[r].mode)return "team_size_mismatch";
        for(i=0;i<rooms[r].count;i++) {
            target=rooms[r].members[i];code=eligible(target,1);if(strcmp(code,"ok"))return code;
        }
        for(i=0;i<rooms[r].count;i++) {
            target=rooms[r].members[i];players[target].frozen_hash=players[target].profile.loadout_hash;
        }
        rooms[r].queued_at=hooks.now();expire_invites(r);room_changed(r,"queued");return "ok";
    }
    return "invalid_operation";
}

static const char *mutate_receipted(int p,const char *id,const char *wire,const char *op,const char *arg,Receipt *receipt,int *recorded)
{
    MutationBackup *backup;const char *code="storage_unavailable";int committed=0;
    *recorded=0;
    backup=malloc(sizeof(*backup));if(!backup)return code;
    if(!execsql("BEGIN IMMEDIATE")){free(backup);return code;}
    memcpy(backup->players,players,sizeof(players));memcpy(backup->rooms,rooms,sizeof(rooms));
    memcpy(backup->invitations,invitations,sizeof(invitations));
    backup->match_index=players[p].match;
    if(backup->match_index>=0)backup->match=matches[backup->match_index];
    backup->revision=revision;backup->serial=serial;
    mutation_active=1;mutation_failed=0;
    code=mutate(p,op,arg);
    /* Even a refused attempt consumes its version. After receipt eviction,
     * that original revision cannot become executable when eligibility changes. */
    if(players[p].revision==backup->players[p].revision)changed(p,"request_processed");
    if(!mutation_failed && !sqlite3_get_autocommit(db) && receipt_write(p,id,wire,code) &&
       store_revision(revision))committed=execsql("COMMIT");
    mutation_active=0;
    if(!committed) {
        execsql("ROLLBACK");
        memcpy(players,backup->players,sizeof(players));memcpy(rooms,backup->rooms,sizeof(rooms));
        memcpy(invitations,backup->invitations,sizeof(invitations));
        if(backup->match_index>=0)matches[backup->match_index]=backup->match;
        revision=backup->revision;serial=backup->serial;code="storage_unavailable";
    } else {
        published_revision=revision;
        strcpy(receipt->request,wire);strcpy(receipt->code,code);strcpy(receipt->boot,boot);
        receipt->revision=players[p].revision;*recorded=1;
    }
    free(backup);finish_notices(committed);return code;
}

int Ladder_Init(const char *database,const LadderHooks *h)
{
    FILE *random;unsigned char bytes[16];int i,fd;struct stat st;sqlite3_stmt *s=NULL;sqlite3_int64 high;
    Ladder_Shutdown();
    if(h)hooks=*h;
    if(!database || !*database || !h || !h->now || !h->profile || !h->contact || !h->send || !h->start || !h->abandon || !h->finish)return 0;
    hooks=*h;
    random=fopen("/dev/urandom","rb");if(!random)return 0;
    i=(int)fread(bytes,1,sizeof(bytes),random);fclose(random);if(i!=sizeof(bytes))return 0;
    for(i=0;i<16;i++)snprintf(boot+i*2,sizeof(boot)-i*2,"%02x",bytes[i]);
    if(hooks.now()<0 || (unsigned long long)hooks.now()>9007199254740991ULL/1024)return 0;
    revision=(unsigned long long)hooks.now()*1024;serial=0;published_revision=0;
    /* SQLite WAL files inherit the database permissions. Set them before
     * SQLite can create a journal, and reject symlinks/non-regular files. */
    fd=open(database,O_RDWR|O_CREAT|O_NOFOLLOW,0600);
    if(fd<0)return 0;
    i=fstat(fd,&st)==0 && S_ISREG(st.st_mode) && fchmod(fd,0600)==0;
    close(fd);if(!i)return 0;
    if(sqlite3_open(database,&db)!=SQLITE_OK){Ladder_Shutdown();return 0;}
    sqlite3_busy_timeout(db,100);
    if(!execsql("PRAGMA journal_mode=WAL;PRAGMA synchronous=FULL;PRAGMA foreign_keys=ON;"
        "CREATE TABLE IF NOT EXISTS ladder_rating(player TEXT NOT NULL,mode INTEGER NOT NULL CHECK(mode BETWEEN 1 AND 5),rating INTEGER NOT NULL,PRIMARY KEY(player,mode));"
        "CREATE TABLE IF NOT EXISTS ladder_match(id TEXT PRIMARY KEY,ended_at INTEGER NOT NULL,result TEXT NOT NULL);"
        "CREATE TABLE IF NOT EXISTS ladder_participant(match_id TEXT NOT NULL REFERENCES ladder_match(id),player TEXT NOT NULL,ack INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(match_id,player));"
        "CREATE INDEX IF NOT EXISTS ladder_pending ON ladder_participant(player,ack);")) { Ladder_Shutdown();return 0; }
    if(!execsql("CREATE TABLE IF NOT EXISTS ladder_preference(player TEXT PRIMARY KEY,strategy TEXT NOT NULL);")){Ladder_Shutdown();return 0;}
    if(!execsql(
        "CREATE TABLE IF NOT EXISTS ladder_cooldown(player TEXT PRIMARY KEY,until_ms INTEGER NOT NULL);"
        "CREATE TABLE IF NOT EXISTS ladder_pending_match(id TEXT PRIMARY KEY,ended_at INTEGER NOT NULL,result TEXT NOT NULL,terminal INTEGER NOT NULL CHECK(terminal IN(0,1)));"
        "CREATE TABLE IF NOT EXISTS ladder_pending_member(match_id TEXT NOT NULL REFERENCES ladder_pending_match(id) ON DELETE CASCADE,player TEXT NOT NULL UNIQUE,mode INTEGER NOT NULL CHECK(mode BETWEEN 1 AND 5),rating_before INTEGER NOT NULL,rating_after INTEGER NOT NULL,PRIMARY KEY(match_id,player));"
    ) || !execsql(
        "CREATE TABLE IF NOT EXISTS ladder_revision(singleton INTEGER PRIMARY KEY CHECK(singleton=1),high INTEGER NOT NULL CHECK(high>=0 AND high<=9007199254740991));"
        "INSERT OR IGNORE INTO ladder_revision(singleton,high) VALUES(1,0);"
        "CREATE TABLE IF NOT EXISTS ladder_receipt(player TEXT NOT NULL,request_id TEXT NOT NULL,request TEXT NOT NULL,code TEXT NOT NULL,revision INTEGER NOT NULL,boot TEXT NOT NULL,PRIMARY KEY(player,request_id));"
    )){Ladder_Shutdown();return 0;}
    if(sqlite3_prepare_v2(db,"SELECT MAX(high,COALESCE((SELECT MAX(revision) FROM ladder_receipt),0)) FROM ladder_revision WHERE singleton=1",-1,&s,NULL)!=SQLITE_OK || sqlite3_step(s)!=SQLITE_ROW) {
        sqlite3_finalize(s);Ladder_Shutdown();return 0;
    }
    high=sqlite3_column_int64(s,0);sqlite3_finalize(s);
    if(high<0 || high>=9007199254740991LL){Ladder_Shutdown();return 0;}
    published_revision=(unsigned long long)high;
    if(revision<published_revision)revision=published_revision;
    if(!recover_pending()){Ladder_Shutdown();return 0;}
    return 1;
}
void Ladder_Shutdown(void)
{
    mutation_active=mutation_failed=revision_exhausted=0;finish_notices(0);published_revision=0;
    if(db)sqlite3_close(db);
    db=NULL;
    memset(players,0,sizeof(players));memset(rooms,0,sizeof(rooms));memset(matches,0,sizeof(matches));memset(invitations,0,sizeof(invitations));
}
void Ladder_Request(int character,const char *wire)
{
    char copy[REQUEST_MAX],original[REQUEST_MAX],*parts[6],*at;int i,p,read,replay=0,recorded=0;
    unsigned long long expected;char *end;const char *code="ok";
    Receipt saved,*receipt=NULL;
    if(!wire || strncmp(wire,"LADDER|",7))return;
    if(strlen(wire)>=sizeof(copy)){unavailable_reply(character,"","invalid_request",NULL);return;}
    strcpy(copy,wire);strcpy(original,wire);parts[0]=copy;at=copy;
    for(i=1;i<6;i++){at=strchr(at,'|');if(!at)break;*at++=0;parts[i]=at;}
    if(i!=6 || strchr(parts[5],'|') || strcmp(parts[1],"1") || !token(parts[2],64)) {
        unavailable_reply(character,"","invalid_request",original);return;
    }
    if(!db){unavailable_reply(character,parts[2],"unavailable",original);return;}
    if(revision_exhausted){unavailable_reply(character,parts[2],"version_exhausted",original);return;}
    p=player(character);if(p<0){unavailable_reply(character,parts[2],revision_exhausted?"version_exhausted":"identity_unavailable",original);return;}
    errno=0;
    expected=strtoull(parts[3],&end,10);
    if(errno || !*parts[3] || *end || *parts[3]<'0' || *parts[3]>'9' ||
       (*parts[3]=='0' && parts[3][1]) || !valid_operation(parts[4],parts[5])) {
        send_reply(p,parts[2],"invalid_request",0,0,"reply",NULL,original,NULL);return;
    }
    if(!strcmp(parts[4],"contacts")) {
        if(hooks.contact_entry)send_contacts(p,parts[2],original);
        else send_reply(p,parts[2],"contacts_unavailable",0,0,"reply",NULL,original,NULL);
        return;
    }
    read=!strcmp(parts[4],"status") || !strcmp(parts[4],"result");
    if(!read) {
        i=receipt_read(players[p].profile.id,parts[2],&saved);
        if(i<0)return; /* Unknown lookup outcome: preserve the client's original retry. */
        if(i>0)receipt=&saved;
        if(receipt) {
            replay=1;code=strcmp(receipt->request,original)?"request_conflict":receipt->code;
        } else if(!expected || expected!=players[p].revision) code="stale_revision";
        else {
            code=mutate_receipted(p,parts[2],original,parts[4],parts[5],&saved,&recorded);
            if(recorded)receipt=&saved;
        }
    } else if(!strcmp(parts[4],"result") && *parts[5]) {
        char found[LADDER_WIRE_MAX/2];if(!stored_result(p,parts[5],found,sizeof(found)))code="result_not_found";
    }
    if(revision_exhausted){unavailable_reply(character,parts[2],"version_exhausted",original);return;}
    send_reply(p,parts[2],code,replay,receipt?receipt->revision:0,
        !strcmp(parts[4],"result") && *parts[5]?"result_lookup":"reply",!strcmp(parts[4],"result")?parts[5]:NULL,original,receipt?receipt->boot:NULL);
}

static int compare_double(const void *a,const void *b)
{ double x=*(const double*)a,y=*(const double*)b;return x<y?-1:x>y?1:0; }
static int compatible(int a,int b,double *gap)
{
    double pa[5],pb[5],ta=0,tb=0,ra=0,rb=0,limit;
    int i,rlimit,n=rooms[a].mode;
    LadderTime age=hooks.now()-(rooms[a].queued_at>rooms[b].queued_at?rooms[a].queued_at:rooms[b].queued_at);
    if(n!=rooms[b].mode)return 0;
    for(i=0;i<n;i++){
        pa[i]=power(rooms[a].members[i]);pb[i]=power(rooms[b].members[i]);ta+=pa[i];tb+=pb[i];
        ra+=players[rooms[a].members[i]].ratings[n-1];rb+=players[rooms[b].members[i]].ratings[n-1];
    }
    limit=age>=90000?.10:age>=30000?.08:.05;rlimit=age>=90000?300:age>=30000?200:100;
    *gap=Ladder_PowerGap(ta,tb);
    if(*gap>limit || fabs(ra-rb)/n>rlimit)return 0;
    qsort(pa,n,sizeof(double),compare_double);qsort(pb,n,sizeof(double),compare_double);
    for(i=0;i<n;i++)if(Ladder_PowerGap(pa[i],pb[i])>.15)return 0;
    return 1;
}
static int reserve_match(int a,int b)
{
    int m,i,s,p;
    for(m=0;m<LADDER_MATCH_MAX;m++)if(!matches[m].used)break;
    if(m==LADDER_MATCH_MAX)return 0;
    memset(&matches[m],0,sizeof(Match));matches[m].used=1;matches[m].battle=-1;matches[m].phase=1;
    matches[m].mode=rooms[a].mode;matches[m].rooms[0]=a;matches[m].rooms[1]=b;
    matches[m].start_at=hooks.now()+COUNTDOWN_MS;identifier(matches[m].id,"match");
    for(s=0;s<2;s++){
        int r=s?b:a;matches[m].queued_at[s]=rooms[r].queued_at;
        for(i=0;i<rooms[r].count;i++) {
            int k=s*matches[m].mode+i;p=rooms[r].members[i];
            matches[m].members[k]=p;matches[m].profiles[k]=players[p].profile;matches[m].powers[k]=power(p);
            matches[m].before[k]=players[p].ratings[matches[m].mode-1];matches[m].ratings[s]+=matches[m].before[k];
        }
        matches[m].ratings[s]/=matches[m].mode;
    }
    /* Commit an unrated interruption receipt before announcing a match or
     * entering the native engine. A crash must not silently lose its roster. */
    matches[m].winner=-1;strcpy(matches[m].reason,"server_restart");
    matches[m].started_at=matches[m].ended_at=hooks.now();result_json(&matches[m]);
    if(!journal_match(&matches[m],0)){matches[m].used=0;return 0;}
    matches[m].started_at=matches[m].ended_at=0;matches[m].result[0]=0;
    for(s=0;s<2;s++) {
        int r=s?b:a;rooms[r].match=m;rooms[r].queued_at=0;
        for(i=0;i<rooms[r].count;i++) {
            p=rooms[r].members[i];players[p].match=m;players[p].acknowledged=0;
            players[p].offline_at=players[p].offline_used=0;players[p].abandoned=0;
        }
    }
    room_changed(a,"match_found");room_changed(b,"match_found");return 1;
}
static void cancel_match(int m,int failed)
{
    sqlite3_stmt *q=NULL;int s,i,ok=0;
    LadderTime until=hooks.now()+COOLDOWN_MS;
    matches[m].phase=5;matches[m].cancel_failed=failed;
    /* Do not release a roster while its durable reservation still exists. */
    if(!execsql("BEGIN IMMEDIATE"))return;
    if(failed>=0 && !save_cooldown(players[failed].profile.id,until))goto done;
    if(sqlite3_prepare_v2(db,"DELETE FROM ladder_pending_match WHERE id=?",-1,&q,NULL)!=SQLITE_OK)goto done;
    sqlite3_bind_text(q,1,matches[m].id,-1,SQLITE_TRANSIENT);
    if(sqlite3_step(q)!=SQLITE_DONE)goto done;
    ok=execsql("COMMIT");
done:
    sqlite3_finalize(q);if(!ok){execsql("ROLLBACK");return;}
    if(hooks.cancel_prepare)hooks.cancel_prepare(matches[m].id);
    for(s=0;s<2;s++) {
        int r=matches[m].rooms[s],healthy=failed>=0;
        for(i=0;i<rooms[r].count;i++) {
            int p=rooms[r].members[i];
            if(p==failed || strcmp(eligible(p,1),"ok") || players[p].frozen_hash!=players[p].profile.loadout_hash)healthy=0;
        }
        rooms[r].match=-1;rooms[r].queued_at=healthy?matches[m].queued_at[s]:0;
        for(i=0;i<rooms[r].count;i++) {
            int p=rooms[r].members[i];players[p].match=-1;if(!healthy)players[p].ready=0;
            if(p==failed)players[p].cooldown_until=until;
        }
        room_changed(r,healthy?"opponent_cancelled_requeued":"match_cancelled");
    }
    matches[m].used=0;
}
static void result_json(Match *v)
{
    JSON j={v->result,sizeof(v->result),0,0};int i,incomplete=v->phase==1 || v->phase==2;
    append(&j,"{\"id\":\"%s\",\"mode\":%d,\"winner_side\":%d,\"rated\":%s,\"reason\":\"%s\","
        "\"turns\":%d,\"duration_ms\":%lld,\"statistics_incomplete\":%s,\"power_version\":\"budget-v1\",\"rating_version\":\"team-elo32-v1\",\"members\":[",
        v->id,v->mode,v->winner,v->rated?"true":"false",v->reason,v->turns,v->ended_at-v->started_at,
        incomplete?"true":"false");
    for(i=0;i<v->mode*2;i++) {
        int side=i/v->mode,delta=side?-v->delta:v->delta;
        LadderStats *st=&v->stats[i];
        if(i)append(&j,",");
        /* An interruption checkpoint knows the roster, not the players'
         * eventual online/abandoned state. In particular it must not copy
         * an abandonment flag left over from their previous match. */
        if(incomplete)append(&j,"{\"id\":\"%s\",\"name_hex\":\"%s\",\"power\":%.6f,\"rating\":%d}",
            v->profiles[i].id,v->profiles[i].name_hex,v->powers[i],v->before[i]);
        else player_json(&j,v->members[i],v->mode,&v->profiles[i],v->powers[i],v->before[i]);
        /* Extend the player object into the flat ResultPlayer schema. */
        if(!j.failed && j.length)j.length--;
        append(&j,",\"side\":%d,\"rating_before\":%d,\"rating_delta\":%d,\"rating_after\":%d,\"statistics\":{"
            "\"player_kills\":%d,\"pet_kills\":%d,\"deaths\":%d,\"assists\":%d,\"revives\":%d,"
            "\"damage\":%lld,\"damage_taken\":%lld,\"pet_damage\":%lld,\"pet_damage_taken\":%lld,"
            "\"ride_damage_taken\":%lld,\"healing\":%lld}}",side,v->before[i],delta,v->before[i]+delta,
            st->player_kills,st->pet_kills,st->deaths,st->assists,st->revives,st->damage,st->damage_taken,
            st->pet_damage,st->pet_damage_taken,st->ride_damage_taken,st->healing);
    }
    append(&j,"]}");if(j.failed)v->result[0]=0;
}
static int journal_match(Match *v,int terminal)
{
    sqlite3_stmt *s=NULL;int i,ok=0;
    if(!v->result[0] || !execsql("BEGIN IMMEDIATE"))return 0;
    if(sqlite3_prepare_v2(db,terminal?
        "UPDATE ladder_pending_match SET ended_at=?2,result=?3,terminal=1 WHERE id=?1 AND terminal=0":
        "INSERT INTO ladder_pending_match(id,ended_at,result,terminal) VALUES(?1,?2,?3,0)",-1,&s,NULL)!=SQLITE_OK)goto done;
    sqlite3_bind_text(s,1,v->id,-1,SQLITE_TRANSIENT);sqlite3_bind_int64(s,2,v->ended_at);sqlite3_bind_text(s,3,v->result,-1,SQLITE_TRANSIENT);
    if(sqlite3_step(s)!=SQLITE_DONE || sqlite3_changes(db)!=1)goto done;
    sqlite3_finalize(s);s=NULL;
    for(i=0;i<v->mode*2;i++) {
        if(sqlite3_prepare_v2(db,terminal?
            "UPDATE ladder_pending_member SET rating_after=?5 WHERE match_id=?1 AND player=?2 AND mode=?3 AND rating_before=?4":
            "INSERT INTO ladder_pending_member(match_id,player,mode,rating_before,rating_after) VALUES(?1,?2,?3,?4,?5)",-1,&s,NULL)!=SQLITE_OK)goto done;
        sqlite3_bind_text(s,1,v->id,-1,SQLITE_TRANSIENT);sqlite3_bind_text(s,2,v->profiles[i].id,-1,SQLITE_TRANSIENT);
        sqlite3_bind_int(s,3,v->mode);sqlite3_bind_int(s,4,v->before[i]);
        sqlite3_bind_int(s,5,v->before[i]+(i<v->mode?v->delta:-v->delta));
        if(sqlite3_step(s)!=SQLITE_DONE || sqlite3_changes(db)!=1)goto done;
        sqlite3_finalize(s);s=NULL;
    }
    ok=execsql("COMMIT");
done:
    sqlite3_finalize(s);if(!ok){execsql("ROLLBACK");return 0;}
    return 1;
}
static int publish_pending(const char *id)
{
    const char *sql[]={
        "INSERT INTO ladder_match(id,ended_at,result) SELECT id,ended_at,result FROM ladder_pending_match WHERE id=?",
        "INSERT INTO ladder_participant(match_id,player) SELECT match_id,player FROM ladder_pending_member WHERE match_id=?",
        "INSERT INTO ladder_rating(player,mode,rating) SELECT player,mode,rating_after FROM ladder_pending_member WHERE match_id=? ON CONFLICT(player,mode) DO UPDATE SET rating=excluded.rating",
        "DELETE FROM ladder_pending_match WHERE id=?"
    };
    sqlite3_stmt *s=NULL;int i,ok=0;
    if(!execsql("BEGIN IMMEDIATE"))return 0;
    for(i=0;i<4;i++) {
        if(sqlite3_prepare_v2(db,sql[i],-1,&s,NULL)!=SQLITE_OK)goto done;
        sqlite3_bind_text(s,1,id,-1,SQLITE_TRANSIENT);
        if(sqlite3_step(s)!=SQLITE_DONE || !sqlite3_changes(db))goto done;
        sqlite3_finalize(s);s=NULL;
    }
    ok=execsql("COMMIT");
done:
    sqlite3_finalize(s);if(!ok)execsql("ROLLBACK");return ok;
}
static int recover_pending(void)
{
    sqlite3_stmt *s=NULL;char id[65];int step;
    for(;;) {
        if(sqlite3_prepare_v2(db,"SELECT id FROM ladder_pending_match ORDER BY ended_at,id LIMIT 1",-1,&s,NULL)!=SQLITE_OK)return 0;
        step=sqlite3_step(s);
        if(step==SQLITE_ROW)snprintf(id,sizeof(id),"%s",sqlite3_column_text(s,0));
        sqlite3_finalize(s);s=NULL;
        if(step==SQLITE_DONE)return 1;
        if(step!=SQLITE_ROW || !publish_pending(id))return 0;
    }
}
static int settle(int m)
{
    Match *v=&matches[m];int i;
    /* Persist the actual terminal outcome separately from applying ratings.
     * A rating transaction failure/restart must retain that exact result. */
    if(!v->journal_terminal) {
        result_json(v);if(!journal_match(v,1))return 0;v->journal_terminal=1;
    }
    if(!publish_pending(v->id))return 0;
    v->phase=4;
    for(i=0;i<v->mode*2;i++){
        int p=v->members[i];players[p].ratings[v->mode-1]=v->before[i]+(i<v->mode?v->delta:-v->delta);
        players[p].ready=0;changed(p,"match_result");
    }
    return 1;
}

static void expire_detached(LadderTime now)
{
    int p,i,j,referenced;
    for(p=0;p<LADDER_PLAYER_MAX;p++)if(players[p].used && players[p].detached_at &&
        now-players[p].detached_at>=OFFLINE_ROOM_MS) {
        int m=players[p].match,r=players[p].room;
        if(m>=0 && matches[m].phase==4) {
            players[p].acknowledged=1;release_result_sides(m);
        }
        if(players[p].match>=0)continue;
        if(r>=0)remove_member(r,p);
        if(players[p].cooldown_until>now)continue;
        /* Completed matches can still reference a player after their side
         * has continued. Never recycle the record under the other side. */
        referenced=0;
        for(i=0;i<LADDER_MATCH_MAX && !referenced;i++)if(matches[i].used)
            for(j=0;j<matches[i].mode*2;j++)if(matches[i].members[j]==p)referenced=1;
        for(i=0;i<LADDER_INVITE_MAX && !referenced;i++)if(invitations[i].used &&
            (invitations[i].from==p || invitations[i].to==p))referenced=1;
        if(!referenced)memset(&players[p],0,sizeof(players[p]));
    }
}

void Ladder_Tick(void)
{
    int i,j,k,p,oldest,best,visited[LADDER_ROOM_MAX]={0};double gap,bestgap;
    LadderTime now;
    if(!db)return;
    now=hooks.now();
    for(i=0;i<LADDER_INVITE_MAX;i++)if(invitations[i].used && invitations[i].expires<=now){
        invitations[i].used=0;changed(invitations[i].to,"invitation_expired");
    }
    for(i=0;i<LADDER_ROOM_MAX;i++)if(rooms[i].used && rooms[i].queued_at){
        for(j=0;j<rooms[i].count;j++){
            p=rooms[i].members[j];
            if(strcmp(eligible(p,1),"ok") || players[p].frozen_hash!=players[p].profile.loadout_hash) {
                rooms[i].queued_at=0;reset_ready(i);room_changed(i,"queue_invalidated");break;
            }
        }
    }
    /* Oldest room chooses its closest eligible opponent. Never widen a
     * newly queued team's tolerance just because the other team waited. */
    for(k=0;k<LADDER_ROOM_MAX;k++) {
        oldest=-1;
        for(i=0;i<LADDER_ROOM_MAX;i++)if(rooms[i].used && rooms[i].queued_at && !visited[i] &&
            (oldest<0 || rooms[i].queued_at<rooms[oldest].queued_at))oldest=i;
        if(oldest<0)break;
        visited[oldest]=1;best=-1;bestgap=2;
        for(i=0;i<LADDER_ROOM_MAX;i++)if(i!=oldest && rooms[i].used && rooms[i].queued_at && compatible(oldest,i,&gap) && gap<bestgap){best=i;bestgap=gap;}
        if(best>=0 && !reserve_match(oldest,best))break;
    }
    for(i=0;i<LADDER_MATCH_MAX;i++)if(matches[i].used){
        Match *v=&matches[i];
        if(v->phase==1) {
            int chars[10],masks[10],failed=-1,prepared;
            for(j=0;j<v->mode*2;j++) {
                p=v->members[j];chars[j]=players[p].profile.character;masks[j]=players[p].mask;
                if(strcmp(eligible(p,1),"ok") || players[p].frozen_hash!=players[p].profile.loadout_hash){failed=p;break;}
            }
            if(failed>=0){cancel_match(i,failed);continue;}
            prepared=hooks.prepare?hooks.prepare(v->id,chars,masks,v->mode):1;
            if(prepared<0 || (!prepared && now>=v->start_at)){cancel_match(i,-1);continue;}
            if(now>=v->start_at) {
                v->battle=hooks.start(v->id,chars,masks,v->mode);
                if(v->battle<0){cancel_match(i,-1);continue;}
                v->phase=2;v->started_at=now;
                for(j=0;j<v->mode*2;j++)changed(v->members[j],"battle_started");
            }
        } else if(v->phase==2) {
            int living[2]={0,0};
            for(j=0;j<v->mode*2;j++) {
                p=v->members[j];
                if(!players[p].abandoned && players[p].offline_used+(players[p].offline_at?now-players[p].offline_at:0)>=RECONNECT_MS) {
                    Ladder_Abandon(players[p].profile.character);
                }
                if(!players[p].abandoned)living[j/v->mode]++;
            }
            if(!living[0] || !living[1])hooks.finish(v->battle,living[0]?0:living[1]?1:-1,"abandonment");
            else if(now-v->started_at>=MAX_MATCH_MS)hooks.finish(v->battle,-1,"time_limit");
        } else if(v->phase==3)settle(i);
        else if(v->phase==5)cancel_match(i,v->cancel_failed);
    }
    expire_detached(now);
}
void Ladder_Disconnected(int character)
{
    int p=find(character),r;
    if(p<0)return;
    players[p].profile.online=0;r=players[p].room;
    if(players[p].match>=0 && matches[players[p].match].phase==2 && !players[p].offline_at)players[p].offline_at=hooks.now();
    if(r>=0 && rooms[r].queued_at){rooms[r].queued_at=0;reset_ready(r);}
    if(r>=0)room_changed(r,"member_disconnected");
}
void Ladder_Reconnected(int character)
{
    int p=player(character);if(p<0)return;
    if(players[p].offline_at){players[p].offline_used+=hooks.now()-players[p].offline_at;players[p].offline_at=0;}
    players[p].profile.online=1;
    if(players[p].offline_used>=RECONNECT_MS)Ladder_Abandon(character);
    if(players[p].room>=0)room_changed(players[p].room,"member_reconnected");else changed(p,"reconnected");
}
int Ladder_Abandon(int character)
{
    int p=find(character),m,r;
    if(p<0 || (m=players[p].match)<0 || matches[m].phase!=2)return 0;
    if(players[p].abandoned)return 1;
    if(!save_cooldown(players[p].profile.id,hooks.now()+COOLDOWN_MS))return 0;
    players[p].abandoned=1;players[p].cooldown_until=hooks.now()+COOLDOWN_MS;
    hooks.abandon(character);r=players[p].room;
    if(r>=0)room_changed(r,"member_abandoned");else changed(p,"abandoned");
    return 1;
}
void Ladder_Detached(int character)
{
    int p=find(character),m;
    if(p<0)return;
    Ladder_Disconnected(character);
    m=players[p].match;
    if(m>=0 && matches[m].phase==1)cancel_match(m,p);
    else if(m>=0 && matches[m].phase==2)Ladder_Abandon(character);
    players[p].profile.character=-1;players[p].detached_at=hooks.now();
}
void Ladder_Rejected(int character)
{
    int p=find(character);
    if(p>=0)send_reply(p,"","ladder_reserved",0,0,"operation_rejected",NULL,NULL,NULL);
}
int Ladder_Reserved(int character)
{
    int p=find(character),r;
    if(p<0)return 0;
    r=players[p].room;
    return players[p].match>=0 || (r>=0 && rooms[r].queued_at);
}
int Ladder_PetAllowed(int character,int slot)
{
    int p=find(character);
    return p<0 || !Ladder_Reserved(character) || slot<0 || (slot<5 && (players[p].mask&(1<<slot)));
}
int Ladder_Battle(int battle)
{
    int i;for(i=0;i<LADDER_MATCH_MAX;i++)if(matches[i].used && matches[i].battle==battle && matches[i].phase==2)return 1;
    return 0;
}
int Ladder_CharacterBattle(int character)
{
    int p=find(character);return p<0 || players[p].match<0 || matches[players[p].match].phase!=2 || players[p].abandoned ||
        players[p].offline_used+(players[p].offline_at?hooks.now()-players[p].offline_at:0)>=RECONNECT_MS ? -1 : matches[players[p].match].battle;
}
void Ladder_End(int battle,int winner_side,int turns,const char *reason,int rated)
{
    int m;
    for(m=0;m<LADDER_MATCH_MAX;m++)if(matches[m].used && matches[m].battle==battle && matches[m].phase==2) {
        Match *v=&matches[m];v->winner=winner_side;v->turns=turns;v->ended_at=hooks.now();v->rated=rated;
        snprintf(v->reason,sizeof(v->reason),"%s",reason);v->phase=3;
        v->delta=rated?Ladder_RatingDelta(v->ratings[0],v->ratings[1],winner_side<0?.5:winner_side==0?1:0):0;
        settle(m);return;
    }
}
void Ladder_Stats(int battle,int character,const LadderStats *delta)
{
    int m,i;
    for(m=0;m<LADDER_MATCH_MAX;m++)if(matches[m].used && matches[m].phase==2 && matches[m].battle==battle) {
        for(i=0;i<matches[m].mode*2;i++)if(matches[m].profiles[i].character==character) {
            LadderStats *s=&matches[m].stats[i];
            s->player_kills+=delta->player_kills;s->pet_kills+=delta->pet_kills;s->deaths+=delta->deaths;s->assists+=delta->assists;s->revives+=delta->revives;
            s->damage+=delta->damage;s->damage_taken+=delta->damage_taken;s->pet_damage+=delta->pet_damage;s->pet_damage_taken+=delta->pet_damage_taken;
            s->ride_damage_taken+=delta->ride_damage_taken;s->healing+=delta->healing;return;
        }
    }
}

/* Read-only admin view, taken on the same main thread as matchmaking. A
 * bounded page keeps the private bridge response below its protocol limit. */
static void admin_member(JSON *j,int p,const LadderProfile *saved)
{
    Player *v=&players[p];const LadderProfile *f=saved?saved:&v->profile;
    append(j,"{\"id\":\"%s\",\"name_hex\":\"%s\",\"strategy\":\"%s\",\"online\":%s}",
           f->id,f->name_hex,v->strategy,v->profile.online?"true":"false");
}
int Ladder_AdminSnapshot(char *out,size_t capacity,int offset,int (*battle_turn)(int))
{
    JSON j={out,capacity,0,0};int i,k,s,queued=0,active=0,seen=0,written=0;
    LadderTime now;
    if(!db || !hooks.now || offset<0 || offset>LADDER_ROOM_MAX)return 0;
    now=hooks.now();
    for(i=0;i<LADDER_ROOM_MAX;i++)if(rooms[i].used && rooms[i].queued_at)queued++;
    for(i=0;i<LADDER_MATCH_MAX;i++)if(matches[i].used && matches[i].phase!=4)active++;
    append(&j,"{\"schema_version\":1,\"at_ms\":%lld,\"offset\":%d,\"page_size\":8,\"queued_total\":%d,\"active_total\":%d,\"queues\":[",now,offset,queued,active);
    for(i=0;i<LADDER_ROOM_MAX;i++) {
        Room *r=&rooms[i];
        if(!r->used || !r->queued_at)continue;
        if(seen++<offset || written>=8)continue;
        if(written++)append(&j,",");
        append(&j,"{\"id\":\"%s\",\"mode\":%d,\"wait_ms\":%lld,\"members\":[",r->id,r->mode,now>r->queued_at?now-r->queued_at:0);
        for(k=0;k<r->count;k++){if(k)append(&j,",");admin_member(&j,r->members[k],NULL);}
        append(&j,"]}");
    }
    append(&j,"],\"matches\":[");seen=written=0;
    for(i=0;i<LADDER_MATCH_MAX;i++) {
        Match *m=&matches[i];int turn=-1;
        if(!m->used || m->phase==4)continue;
        if(seen++<offset || written>=8)continue;
        if(written++)append(&j,",");
        if(m->phase==2 && battle_turn)turn=battle_turn(m->battle);
        append(&j,"{\"id\":\"%s\",\"mode\":%d,\"phase\":\"%s\",\"turn\":%d,\"elapsed_ms\":%lld,\"countdown_ms\":%lld,\"teams\":[",m->id,m->mode,m->phase==1?"countdown":m->phase==2?"battle":"settling",turn,m->started_at && now>m->started_at?now-m->started_at:0,m->phase==1 && m->start_at>now?m->start_at-now:0);
        for(s=0;s<2;s++) {
            if(s)append(&j,",");
            append(&j,"{\"members\":[");
            for(k=0;k<m->mode;k++){int n=s*m->mode+k;if(k)append(&j,",");admin_member(&j,m->members[n],&m->profiles[n]);}
            append(&j,"]}");
        }
        append(&j,"]}");
    }
    append(&j,"]}");return !j.failed;
}
