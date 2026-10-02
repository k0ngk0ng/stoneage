/* Test-only composition: use the real arena fixture and the exact static
 * training reset/step implementation, with no new production test API. */
#define main ladder_fixture_main
#include "ladder-native-smoke.c"
#undef main
#define initialize dataset_initialize
#define sink dataset_sink
#include "../stoneage_battle_dataset.c"
#undef initialize
#undef sink

static unsigned long parity_rand_calls;
int __real_rand(void);
void __real_srand(unsigned int seed);
int __wrap_rand(void)
{
    parity_rand_calls++;
    if(getenv("STONEAGE_PARITY_RNG_TRACE") && parity_rand_calls<32)
        fprintf(stderr,"RNG|%lu|%p\n",parity_rand_calls,__builtin_return_address(0));
    return __real_rand();
}
void __wrap_srand(unsigned int seed){parity_rand_calls=0;__real_srand(seed);}
/* The production transport shares libc rand() with combat. This test compiles
 * only autil.c against a separate stream, so network presence cannot shift the
 * combat draws being compared. No battle RNG call is replaced or skipped. */
int parity_transport_rand(void)
{
    static unsigned int state=17;
    state=state*1664525U+1013904223U;
    return (int)(state&0x7fffffffU);
}

static void parity_snapshot(int mode)
{
    int i,j,c,b=env_battle,ended=BattleArray[b].mode==BATTLE_MODE_FINISH;
    printf("PARITY|{\"turn\":%d,\"ended\":%s,\"winner\":%d,\"rng_calls\":%lu,\"actors\":[",
        BattleArray[b].turn,ended?"true":"false",ended?(BattleArray[b].winside==-1?0:BattleArray[b].winside==1?1:-1):-1,parity_rand_calls);
    for(i=0;i<env_count*(1+env_pets);i++) {
        c=i<env_count?env_char[i]:env_pet[i-env_count];
        printf("%s[%d,%d,%d,%d,%d,%d,%d,%d,%d,%d]",i?",":"",
            BATTLE_Index2No(b,c),CHAR_getInt(c,CHAR_HP),CHAR_getInt(c,CHAR_MP),
            CHAR_getWorkInt(c,CHAR_WORKMAXHP),CHAR_getWorkInt(c,CHAR_WORKMAXMP),
            CHAR_getWorkInt(c,CHAR_WORKATTACKPOWER),CHAR_getWorkInt(c,CHAR_WORKDEFENCEPOWER),
            CHAR_getWorkInt(c,CHAR_WORKQUICK),CHAR_getFlg(c,CHAR_ISDIE),CHAR_getInt(c,CHAR_DEFAULTPET));
    }
    printf("],\"reserves\":[");
    for(i=0;i<env_count;i++) {
        printf("%s[",i?",":"");
        for(j=1;j<3;j++) {
            c=CHAR_getCharPet(env_char[i],j);
            if(j>1)putchar(',');
            if(!CHAR_CHECKINDEX(c))printf("null");
            else printf("[%d,%d,%d,%d,%d]",BATTLE_Index2No(b,c),CHAR_getInt(c,CHAR_HP),CHAR_getWorkInt(c,CHAR_WORKATTACKPOWER),CHAR_getWorkInt(c,CHAR_WORKQUICK),CHAR_getFlg(c,CHAR_ISDIE));
        }
        putchar(']');
    }
    printf("],\"inventory\":[");
    for(i=0;i<env_count;i++) {
        printf("%s[",i?",":"");
        for(j=0;j<CHAR_MAXITEMHAVE;j++) {
            c=CHAR_getItemIndex(env_char[i],j);
            printf("%s%d",j?",":"",ITEM_CHECKINDEX(c)?ITEM_getInt(c,ITEM_ID):-1);
        }
        putchar(']');
    }
    printf("],\"packets\":[");
    for(i=0;i<env_count;i++) {
        printf("%s[",i?",":"");
        for(j=0;j<env_packets[i];j++) {
            if(j)putchar(',');environment_hex(stdout,env_packet[i][j]);
        }
        putchar(']');
    }
    puts("]}");
    /* Keep original structured client packets for shared Go projection checks. */
    printf("FRAME|");assert(environment_response(stdout,mode,200));
    fflush(stdout);
}

static int parity_target(int side,int pets_first)
{
    int k,offset,c;
    for(k=0;k<10;k++) {
        offset=pets_first?(k+5)%10:k;
        c=BATTLE_No2Index(env_battle,(1-side)*10+offset);
        if(CHAR_CHECKINDEX(c) && !CHAR_getFlg(c,CHAR_ISDIE) && CHAR_getInt(c,CHAR_HP)>0)
            return (1-side)*10+offset;
    }
    return -1;
}

/* Exercise real effect functions with prepared status, without adding these
 * private fixture controls to the public observation or training adapter. */
static void recipient_probe(void)
{
    const char *names[]={"ordinary","guardian","reflection","guardian_reflection","absorb","vanish","counter_reflection"};
    int kind,attempt,i,c,b,changed,expected,damage,flags,target,guardian,counter_luck;
    int before[20],after[20];
    char movie[8192],*hit;
    assert(environment_enter(1,42));b=env_battle;
    for(kind=0;kind<7;kind++) {
        for(attempt=0;attempt<100;attempt++) {
            for(i=0;i<20;i++) {
                c=BATTLE_No2Index(b,i);
                before[i]=after[i]=-1;
                if(!CHAR_CHECKINDEX(c))continue;
                CHAR_setInt(c,CHAR_HP,10000);CHAR_setWorkInt(c,CHAR_WORKMAXHP,10000);
                CHAR_setFlg(c,CHAR_ISDIE,FALSE);
                CHAR_setWorkInt(c,CHAR_WORKBATTLECOM1,BATTLE_COM_ATTACK);
                CHAR_setWorkInt(c,CHAR_WORKDAMAGEREFLEC,0);
                CHAR_setWorkInt(c,CHAR_WORKDAMAGEABSROB,0);
                CHAR_setWorkInt(c,CHAR_WORKDAMAGEVANISH,0);
                CHAR_setWorkInt(c,CHAR_WORKBATTLEFLG,0);
                BattleArray[b].Side[i/10].Entry[i%10].guardian=-1;
                before[i]=10000;
            }
            if(kind==1 || kind==3) {
                BattleArray[b].Side[1].Entry[0].guardian=15;
                CHAR_setWorkInt(BATTLE_No2Index(b,15),CHAR_WORKBATTLEFLG,CHAR_BATTLEFLG_GUARDIAN);
            }
            c=BATTLE_No2Index(b,kind==3?15:10);
            if(kind==2 || kind==3)CHAR_setWorkInt(c,CHAR_WORKDAMAGEREFLEC,1);
            if(kind==4) {
                CHAR_setInt(c,CHAR_HP,9000);before[10]=9000;
                CHAR_setWorkInt(c,CHAR_WORKDAMAGEABSROB,1);
            }
            if(kind==5)CHAR_setWorkInt(c,CHAR_WORKDAMAGEVANISH,1);
            srand(500+attempt);
            movie[0]=0;pszBattleTop=movie;pszBattleLast=movie+sizeof(movie);
            szBadStatusString[0]=0;
            gBattleDamageModyfy=1.0;gBattleDuckModyfy=0;gBattleStausChange=-1;
            gDamageDiv=1.0;gWeponType=BATTLE_GetWepon(BATTLE_No2Index(b,0));
            BATTLESTR_ADD("BH|a0|");
            BATTLE_Attack(b,0,10);
            if(kind==6) {
                CHAR_setWorkInt(BATTLE_No2Index(b,0),CHAR_WORKDAMAGEREFLEC,1);
                /* This probes reflection attribution, not counter frequency.
                 * Native libc seed sequences differ; give this synthetic
                 * counterattacker sufficient luck to exercise the real gate. */
                c=BATTLE_No2Index(b,10);
                counter_luck=CHAR_getWorkInt(c,CHAR_WORKFIXLUCK);
                CHAR_setWorkInt(c,CHAR_WORKFIXLUCK,100);
                BATTLE_Counter(b,10,0);
                CHAR_setWorkInt(c,CHAR_WORKFIXLUCK,counter_luck);
                if(!strstr(movie,"counter"))continue;
            }
            BATTLESTR_ADD("FF|");
            hit=strstr(movie,"rA|");assert(hit);
            assert(sscanf(hit,"r%X|f%X|d%X|",&target,&flags,&damage)==3);
            if(flags & BCF_DODGE)continue;
            if(kind!=5 && damage<=0)continue;
            expected=kind==1?15:(kind==2 || kind==3)?0:10;
            changed=0;
            for(i=0;i<20;i++) {
                c=BATTLE_No2Index(b,i);if(!CHAR_CHECKINDEX(c))continue;
                after[i]=CHAR_getInt(c,CHAR_HP);
                if(after[i]!=before[i]) { assert(i==expected);changed++; }
            }
            assert(changed==(kind==5?0:1));
            guardian=kind==1 || kind==3;
            assert(!!(flags & BCF_GUARDIAN)==guardian);
            if(guardian)assert(strstr(movie,"gF|"));
            if(kind==2 || kind==3)assert(flags & 1024);
            if(kind==4)assert((flags & 2048) && after[10]>before[10]);
            if(kind==5)assert(flags & 4096);
            if(kind==6) {
                hit=strstr(movie,"r0|");assert(hit);
                assert(sscanf(hit,"r%X|f%X|counter%X|",&target,&flags,&damage)==3);
                if((flags & BCF_DODGE) || damage<=0)continue;
                assert(flags & 1024);
            }
            printf("RECIPIENT|{\"case\":\"%s\",\"packet\":",names[kind]);environment_hex(stdout,movie);
            printf(",\"before\":[");for(i=0;i<20;i++)printf("%s%d",i?",":"",before[i]);
            printf("],\"after\":[");for(i=0;i<20;i++)printf("%s%d",i?",":"",after[i]);
            puts("]}");break;
        }
        if(attempt==100)fprintf(stderr,"recipient scenario exhausted: %s\n",names[kind]);
        assert(attempt<100);
    }
    environment_dispose();
}

static void capacity_probe(void)
{
    int cases[][4]={{0,0,0,0},{1,0,0,1},{1,0,1,0},{1,1,1,1},{0,1,6,1},{7,0,6,1}};
    int i,c=env_char[0],p=env_pet[0],accepted;
    char command[32];
    assert(environment_enter(1,42));
    for(i=0;i<6;i++) {
        CHAR_setInt(p,CHAR_SLOT,cases[i][0]);
        CHAR_setInt(p,CHAR_TRANSMIGRATION,cases[i][1]);
        CHAR_setWorkInt(c,CHAR_WORKBATTLEMODE,BATTLE_CHARMODE_C_OK);
        CHAR_setWorkInt(p,CHAR_WORKBATTLEMODE,BATTLE_CHARMODE_C_WAIT);
        CHAR_setWorkInt(p,CHAR_WORKBATTLECOM1,BATTLE_COM_WAIT);
        snprintf(command,sizeof(command),"W|%X|%X",cases[i][2],cases[i][2]==1?5:10);
        StoneAge_BattleDatasetCommand(c,command);
        assert(CHAR_getWorkInt(p,CHAR_WORKBATTLEMODE)==BATTLE_CHARMODE_C_OK);
        accepted=CHAR_getWorkInt(p,CHAR_WORKBATTLECOM1)!=BATTLE_COM_WAIT;
        assert(accepted==cases[i][3]);
        printf("CAPACITY|{\"slots\":%d,\"transmigration\":%d,\"index\":%d,\"accepted\":%s,\"status\":",
            cases[i][0],cases[i][1],cases[i][2],accepted?"true":"false");
        environment_hex(stdout,CHAR_makeStatusString(c,"K0"));
        puts("}");
    }
    environment_dispose();
}

static void switch_probe(void)
{
    int points[4]={30,30,30,30},c=env_char[0],old=env_pet[0],reserve,i,mp;
    char *commands[4]={"S|1","W|0|A","G","W|FF|FF"};
    reserve=synthetic_character(points,35,CHAR_TYPEPET,c);assert(reserve>=0);
    CHAR_setChar(reserve,CHAR_NAME,"reserve-pet");
    /* Distinct skills prove W continues to refer to the outgoing pet:
       slot zero means attack there, guard on the incoming reserve. */
    CHAR_setPetSkill(reserve,0,2);
    CHAR_setCharPet(c,1,reserve);
    CHAR_setWorkInt(c,CHAR_WORKSTANDBYPET,3);
    CHAR_setWorkInt(c,CHAR_WORK_PET0_STAT,PET_STAT_SELECT);
    CHAR_setWorkInt(c,CHAR_WORK_PET0_STAT+1,PET_STAT_SELECT);
    assert(environment_enter(1,42));
    mp=CHAR_getInt(c,CHAR_MP);
    /* The shared guard must reject invalid slots before the legacy handler
       could reinterpret them as recall. No command or readiness may change. */
    for(i=0;i<3;i++) {
        CHAR_setWorkInt(c,CHAR_WORKSTANDBYPET,i?3:1);
        CHAR_setInt(c,CHAR_RIDEPET,i==1?1:-1);
        CHAR_setWorkInt(c,CHAR_WORK_PET0_STAT+1,i==2?PET_STAT_NONE:PET_STAT_SELECT);
        CHAR_setWorkInt(c,CHAR_WORKBATTLEMODE,BATTLE_CHARMODE_C_WAIT);
        CHAR_setWorkInt(c,CHAR_WORKBATTLECOM1,BATTLE_COM_WAIT);
        StoneAge_BattleDatasetCommand(c,"S|1");
        assert(CHAR_getWorkInt(c,CHAR_WORKBATTLECOM1)==BATTLE_COM_WAIT);
        assert(CHAR_getWorkInt(c,CHAR_WORKBATTLEMODE)==BATTLE_CHARMODE_C_WAIT);
    }
    CHAR_setInt(c,CHAR_RIDEPET,-1);CHAR_setWorkInt(c,CHAR_WORKSTANDBYPET,3);
    CHAR_setWorkInt(c,CHAR_WORK_PET0_STAT+1,PET_STAT_SELECT);
    CHAR_setWorkInt(c,CHAR_WORKBATTLEMODE,BATTLE_CHARMODE_C_WAIT);
    printf("SWITCHFRAME|");assert(environment_response(stdout,1,20));
    StoneAge_BattleDatasetCommand(c,"S|1");
    assert(CHAR_getWorkInt(c,CHAR_WORKBATTLECOM1)==BATTLE_COM_PETOUT);
    assert(CHAR_getInt(c,CHAR_DEFAULTPET)==0 && BATTLE_No2Index(env_battle,5)==old);
    assert(CHAR_getWorkInt(old,CHAR_WORKBATTLEMODE)==BATTLE_CHARMODE_C_WAIT);
    assert(BATTLE_Index2No(env_battle,reserve)<0);
    StoneAge_BattleDatasetCommand(c,"W|0|A");
    assert(CHAR_getWorkInt(old,CHAR_WORKBATTLECOM1)==BATTLE_COM_ATTACK);
    assert(CHAR_getInt(c,CHAR_MP)==mp); /* current BATTLE_MpDown is disabled */
    /* Re-submit through the same joint adapter after restoring only command
       readiness; no combat stats, RNG, effects or HP are replaced. */
    CHAR_setWorkInt(c,CHAR_WORKBATTLEMODE,BATTLE_CHARMODE_C_WAIT);
    CHAR_setWorkInt(old,CHAR_WORKBATTLEMODE,BATTLE_CHARMODE_C_WAIT);
    assert(environment_step(commands));
    assert(CHAR_getInt(c,CHAR_DEFAULTPET)==1 && BATTLE_No2Index(env_battle,5)==reserve);
    assert(CHAR_getInt(c,CHAR_MP)==mp);
    printf("SWITCHFRAME|");assert(environment_response(stdout,1,20));
    commands[0]="S|-1";commands[1]="W|0|5";
    assert(environment_step(commands));
    assert(CHAR_getInt(c,CHAR_DEFAULTPET)==-1 && BATTLE_No2Index(env_battle,5)<0);
    printf("SWITCHFRAME|");assert(environment_response(stdout,1,20));
    commands[0]="S|0";commands[1]="-";
    assert(environment_step(commands));
    assert(CHAR_getInt(c,CHAR_DEFAULTPET)==0 && BATTLE_No2Index(env_battle,5)==old);
    assert(CHAR_getInt(c,CHAR_MP)==mp);
    printf("SWITCHFRAME|");assert(environment_response(stdout,1,20));
    puts("SWITCH|{\"invalid_mask_rejected\":true,\"riding_slot_rejected\":true,\"outgoing_skill\":\"attack\",\"incoming_skill\":\"guard\",\"switch_recall_resummon\":true,\"mp_consumed\":0}");
    environment_dispose();
}

int main(int argc,char **argv)
{
    int mode,pets,seed,arena,i,j,c,b,turn,target,loops,stride,healing,items,slot;
    int saved_items[10][15];
    int points[10][4],pet_points[10][4],reserves,reserve_points[10][2][4],reserve_masks[10][2],active,skills,pet_mask;
    char names[32],commands[20][32],*pointers[20];
    assert(argc==8 || argc==9 || argc==10);
    reserves=argc>=9?atoi(argv[8]):0;assert(reserves>=0 && reserves<=2);
    pet_mask=argc==10?atoi(argv[9]):127;assert(environment_skill_mask(pet_mask));
    healing=atoi(argv[6]);
    items=atoi(argv[7]);
    mode=atoi(argv[3]);pets=atoi(argv[4]);seed=atoi(argv[5]);arena=!strcmp(argv[2],"arena");
    assert((arena || !strcmp(argv[2],"offline") || !strcmp(argv[2],"capacity") || !strcmp(argv[2],"switch") || !strcmp(argv[2],"recipient")) && mode>=1 && mode<=5 && pets>=0 && pets<=1);
    StoneAge_BattleLogOffline();setNewTime();assert(util_Init());defaultConfig(argv[0]);
    assert(readconfigfile(argv[1]) && configmem(getMemoryunit(),getMemoryunitnum()) && memInit());
    assert(initConnect(64) && initObjectArray(64) && CHAR_initCharArray(32,64,8));
    assert(ITEM_readItemConfFile(getItemfile()) && ITEM_initExistItemsArray(256));
    assert(initFunctionTable() && PETSKILL_initPetskill(getPetskillfile()));
    assert(MAGIC_initMagic(getMagicfile()));
    assert(environment_skill_contract());
    {
        int skill=PETSKILL_getPetskillArray(60);
        char saved[128];
        snprintf(saved,sizeof(saved),"%s",PETSKILL_getChar(skill,PETSKILL_OPTION));
        PETSKILL_setChar(skill,PETSKILL_OPTION,"changed semantics");
        assert(!environment_skill_contract());
        PETSKILL_setChar(skill,PETSKILL_OPTION,saved);
        assert(environment_skill_contract());
    }
    assert(lssproto_InitServer(sink,65536)>=0 && saacproto_InitClient(sink,65536,0)>=0 && BATTLE_initBattleArray(8));
    tick(0);assert(enabled);
    for(i=0;i<mode*2;i++)for(j=0;j<4;j++) {
        /* Equal budgets, matching team powers; varied seats and seeds. */
        points[i][j]=j==(i%mode+seed)%4?60:20;
        pet_points[i][j]=j==(i%mode+seed+1)%4?60:20;
        if(seed==7) {
            /* Exercise overkill/flying and removal of an owner's active pet. */
            points[i][j]=j==1?117:1;
            pet_points[i][j]=j==3?117:1;
        }
    }
    env_active=1;
    assert(environment_characters(mode,35,pets,points,pet_points,healing,items));
    if(pets)for(i=0;i<mode*2;i++)assert(environment_pet_skills(env_pet[i],pet_mask));
    for(i=0;i<mode*2;i++)for(j=0;j<reserves;j++) {
        int k;
        for(k=0;k<4;k++)reserve_points[i][j][k]=k==(j+i%mode)%4?60:20;
        reserve_masks[i][j]=3|(1<<(j?6:3));
    }
    assert(environment_reserves(reserves,35,reserve_points,reserve_masks));
    if(!strcmp(argv[2],"recipient")) {
        assert(mode==1 && pets==1 && !healing && !items && !reserves);
        recipient_probe();return 0;
    }
    if(!strcmp(argv[2],"capacity")) {
        assert(mode==1 && pets==1 && !healing);
        capacity_probe();return 0;
    }
    if(!strcmp(argv[2],"switch")) {
        assert(mode==1 && pets==1 && !healing && !items);
        switch_probe();return 0;
    }
    for(i=0;i<env_count;i++) {
        c=env_char[i];snprintf(names,sizeof(names),"Parity%d",i);CHAR_setChar(c,CHAR_NAME,names);
        for(j=0;j<items;j++)saved_items[i][j]=CHAR_getItemIndex(c,5+j);
        snprintf(names,sizeof(names),"parity%d",i);CHAR_setChar(c,CHAR_CDKEY,names);
        if(arena) {
            CHAR_setWorkInt(c,CHAR_WORKTRADEMODE,CHAR_TRADE_FREE);
            assert(StoneAge_CharacterIdentityPrepareSave(CHAR_getCharPointer(c)));
            StoneAge_CharacterIdentityMarkLoaded(CHAR_getCharPointer(c));connect_player(c,c+3);
            StoneAge_LadderCharacterLoaded(c);
            CHAR_setInt(c,CHAR_HP,CHAR_getWorkInt(c,CHAR_WORKMAXHP)-7);
            CHAR_setInt(c,CHAR_MP,CHAR_getWorkInt(c,CHAR_WORKMAXMP)>3?CHAR_getWorkInt(c,CHAR_WORKMAXMP)-3:0);
            if(pets)CHAR_setInt(env_pet[i],CHAR_HP,CHAR_getWorkInt(env_pet[i],CHAR_WORKMAXHP)-7);
        }
    }
    if(arena) {
        team_loadout(env_char,mode,(1<<(reserves+1))-1);team_loadout(env_char+mode,mode,(1<<(reserves+1))-1);tick(0);
        assert(strstr(last[env_char[0]],"\"phase\":\"countdown\""));
        srand(seed);tick(10000);
        env_battle=Ladder_CharacterBattle(env_char[0]);assert(env_battle>=0);
        for(i=0;i<env_count;i++)assert(BATTLE_Index2No(env_battle,env_char[i])==i/mode*10+i%mode);
        BATTLE_Loop();
    } else assert(environment_enter(mode,seed));
    parity_snapshot(mode);
    stride=1+pets;b=env_battle;
    while(BattleArray[b].mode!=BATTLE_MODE_FINISH && BattleArray[b].turn<200) {
        turn=BattleArray[b].turn;
        for(i=0;i<env_count;i++) {
            target=parity_target(i/mode,turn%4==2);
            slot=-1;
            for(j=5;j<20;j++)if(ITEM_CHECKINDEX(CHAR_getItemIndex(env_char[i],j))){slot=j;break;}
            if(target<0 || CHAR_getFlg(env_char[i],CHAR_ISDIE))strcpy(commands[i*stride],"N");
            else if(reserves && turn<reserves+1)
                snprintf(commands[i*stride],32,"S|%d",(turn+1)%(reserves+1));
            else if(slot>=0 && turn%4==1)
                snprintf(commands[i*stride],32,"I|%X|%X",slot,i/mode*10+i%mode);
            else if(healing && turn%4==1 && CHAR_getInt(env_char[i],CHAR_MP)>=(healing==10?8:20))
                snprintf(commands[i*stride],32,"J|1|%X",healing==10?i/mode*10+i%mode:20+i/mode);
            else if(turn%4==0 && i%3==0)strcpy(commands[i*stride],"G");
            else snprintf(commands[i*stride],32,"H|%X",target);
            if(pets) {
                active=CHAR_getCharPet(env_char[i],CHAR_getInt(env_char[i],CHAR_DEFAULTPET));
                skills=0;
                if(CHAR_CHECKINDEX(active))while(skills<CHAR_MAXPETSKILLHAVE && CHAR_getPetSkill(active,skills)>=0)skills++;
                assert(!CHAR_CHECKINDEX(active) || skills>0);
                if(target<0 || !CHAR_CHECKINDEX(active) || CHAR_getFlg(active,CHAR_ISDIE))strcpy(commands[i*stride+1],"-");
                else if(turn%4==1 && i%2==0)snprintf(commands[i*stride+1],32,"W|1|%X",i/mode*10+i%mode+5);
                else snprintf(commands[i*stride+1],32,"W|%X|%X",(turn+i)%skills,(turn+i)%skills==1?i/mode*10+i%mode+5:target);
            }
        }
        for(i=0;i<env_count*stride;i++)pointers[i]=commands[i];
        if(arena) {
            environment_clear_packets();
            for(i=0;i<env_count;i++) {
                lssproto_B_recv(CHAR_getWorkInt(env_char[i],CHAR_WORKFD),commands[i*stride]);
                if(pets && strcmp(commands[i*stride+1],"-"))lssproto_B_recv(CHAR_getWorkInt(env_char[i],CHAR_WORKFD),commands[i*stride+1]);
            }
            for(loops=0;loops<8 && BattleArray[b].turn==turn && BattleArray[b].mode!=BATTLE_MODE_FINISH;loops++)BATTLE_Loop();
            assert(BattleArray[b].turn!=turn || BattleArray[b].mode==BATTLE_MODE_FINISH);
        } else assert(environment_step(pointers));
        parity_snapshot(mode);
    }
    assert(BattleArray[b].mode==BATTLE_MODE_FINISH);
    if(arena) {
        BATTLE_Loop();assert(!native_battle(b));request(env_char[0],"status","");
        assert(strstr(last[env_char[0]],"\"reason\":\"defeat\""));
        for(i=0;i<env_count;i++) {
            c=env_char[i];
            assert(CHAR_getInt(c,CHAR_HP)==CHAR_getWorkInt(c,CHAR_WORKMAXHP)-7);
            assert(CHAR_getInt(c,CHAR_MP)==(CHAR_getWorkInt(c,CHAR_WORKMAXMP)>3?CHAR_getWorkInt(c,CHAR_WORKMAXMP)-3:0));
            if(pets)assert(CHAR_getInt(env_pet[i],CHAR_HP)==CHAR_getWorkInt(env_pet[i],CHAR_WORKMAXHP)-7);
            for(j=0;j<items;j++) {
                assert(CHAR_getItemIndex(c,5+j)==saved_items[i][j]);
                assert(ITEM_CHECKINDEX(saved_items[i][j]) && ITEM_getInt(saved_items[i][j],ITEM_ID)==1234);
            }
        }
        env_battle=-1;
    }
    environment_dispose();StoneAge_BattleLogShutdown();Ladder_Shutdown();
    return 0;
}
