'use strict';
const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const html=fs.readFileSync(__dirname+'/runtimeassets/index.html','utf8');
function fn(name){const a=html.indexOf('  function '+name+'('),line=html.indexOf('\n',a);assert(a>=0,name);return html.slice(a,html.slice(a,line).endsWith('}')?line:html.indexOf('\n  }',line)+4);}
function fixture(){
  let now=1000,nextTimer=0;
  const timers=new Map(),sent=[],presented=[],calls=[],node={textContent:'',classList:{add(){}}};
  const state={ladderID:'m1',ladderAwaitingCommands:true,myNo:10,myNoKnown:true,bpReceived:true,bpFlags:0,serverTurnNo:3,
    participants:[{battleId:10,hp:10,flags:4}],entryPending:false,commandPending:{},animations:[],motions:[],projectiles:[],effects:[],pendingBattleControls:[],
    turnKey:3,movieGeneration:0,bpMovieGeneration:0,deathStartedAt:new Map(),lastPlayerActionKind:'attack'};
  const app={battle:true,phase:'battle',battleState:state,battleCommands:[],transport:{id:'current'},connectionToken:1,pendingBattlePackets:[]};
  const c={app,Map,Set,Promise,Date:{now:()=>now},$:()=>node,worldScreen:{},assetState:{spritesReady:true},
    BATTLE_BP_PLAYER_MENU_NON:2,BATTLE_BP_PET_MENU_NON:8,BATTLE_BC_DEATH:2,
    window:{setTimeout(callback,delay){const id=++nextTimer;timers.set(id,{callback,at:now+delay});return id;},clearTimeout:id=>timers.delete(id),
      StoneAgeLadder:{receive:e=>calls.push(['receive',e]),presentResult:id=>presented.push(id)}},
    battleNumber:s=>parseInt(s,16)||0,battleCommandKind:s=>s.startsWith('W')?'pet':'player',battleHelpCommand:()=>false,
    battleChoiceExpired:()=>false,battleLocalDeath:()=>false,battleServerSideDefeated:()=>true,
    battleMovieEffects:()=>[],battlePushMotion:(motions,motion)=>motions.push(motion),
    send:(...args)=>{sent.push(args);return Promise.resolve();},battleClearCommandPending:(s,kind)=>{if(kind)delete s.commandPending[kind];else s.commandPending={};}};
  for(const name of ['clearBattleChoiceTimer','clearBattlePlayerChoiceTimer','clearBattlePetChoiceTimer','renderBattle','renderBattleWorld','renderBattleTargets',
    'battleCancelPendingUi','battleCancelPendingDamage','battleOpenCommandCountdown','armDefaultBattleAttack','submitBattleUnavailableDefaults',
    'continueBattlePetAfterPlayer','noteBattleInitialization','armBattleEntry','battleNoteCommandPacket','scheduleBattleTransientCleanup','resetBattleMenuMotion',
    'restoreMapMusic','show','setWorldState','closeBattlePopup','addEvent','reportError','playSoundEffect','renderWorld'])c[name]=(...args)=>calls.push([name,...args]);
  vm.createContext(c);
  for(const name of ['battleEntryPending','clearBattleEntryTimer','releaseBattleEntry','battleCommandAllowed','battleActionAllowed','applyBattleLadderCommands',
    'battleApplyAnimationState','applyBattleTurnState','battleControlQueue','queueBattleControl','applyQueuedBattleControl','flushQueuedBattleControls',
    'battleMotionBlocksCommand','battleMovieHoldUntil','battleMoviePending','battleFinishMovieIfReady','armQueuedBattleTurn','queueBattleTurnState',
    'battleTerminalHoldUntil','receiveBattlePacket','receiveBattleLadderEnvelope','queueBattleLadderResult','finishBattleLadderResult',
    'battleResetCommandLocks','sendBattleEndOnce','sendBattlePetDefault','finishBattleWorldExit','queueBattleWorldExit','finishLocalBattleDeath','scheduleBattleDeathExit',
    'openBattleResult','battlePlayerTimeoutDefaults','deferBattleResourcePacket','battleResourceMoviePacket','clearPendingBattlePackets',
    'queuePendingBattlePacket','flushPendingBattlePackets','finishFastBattle','syncFastBattlePresentation'])vm.runInContext(fn(name),c);
  c.pendingBattlePacketTTL=15000;
  return {c,app,state,timers,sent,presented,calls,advance(ms){now+=ms;for(const [id,timer] of [...timers])if(timer.at<=now){timers.delete(id);timer.callback();}}};
}
const accepted=(player=true,pet=true,turn=3)=>({match_id:'m1',commands:{turn,my_no:10,player_submitted:player,pet_submitted:pet}});
const result=(id='m1',revision=20)=>({revision,snapshot:{phase:'result',result:{id}}});

test('restored player and pet submissions survive entrance release and forced defaults',()=>{
  const f=fixture(),s=f.state;s.entryPending=true;
  f.c.battleApplyAnimationState(s,0x8400,3,accepted());
  f.c.releaseBattleEntry(s);
  assert.equal(s.entryPending,false);assert.equal(s.commandLocked,true);assert.equal(s.petCommandLocked,true);
  assert.equal(f.c.battleCommandAllowed('H|0'),false);assert.equal(f.c.battleActionAllowed({kind:'pet'}),false);
  f.c.sendBattlePetDefault(s,{force:true});assert.equal(f.sent.length,0);
  // A duplicate BP and a delayed empty BA cannot retract accepted actions.
  f.c.applyBattleTurnState(s,{myNo:10,bpFlags:0,myMp:100});
  f.c.battleApplyAnimationState(s,0,3,accepted(false,false));
  assert.equal(s.ladderPlayerSubmitted,true);assert.equal(s.ladderPetSubmitted,true);
});

test('unsubmitted pet is available after player recovery; local in-flight writes stay locked',()=>{
  const f=fixture(),s=f.state;
  s.commandPending.player='H|0';s.commandPending.pet='W|0|0';
  f.c.battleApplyAnimationState(s,0,3,accepted(false,false));
  assert.equal(s.commandLocked,true);assert.equal(s.petCommandLocked,true);
  delete s.commandPending.pet;
  f.c.battleApplyAnimationState(s,0x400,3,accepted(true,false));
  assert.equal(s.commandPending.player,undefined);assert.equal(s.commandLocked,true);assert.equal(s.petCommandLocked,false);
  assert.equal(s.petAfterPlayerReady,true);assert.equal(f.c.battleActionAllowed({kind:'pet'}),true);
  assert(f.calls.some(([name])=>name==='continueBattlePetAfterPlayer'));
});

test('new turn waits for its own BA and delayed animation controls retain the matching projection',()=>{
  const f=fixture(),s=f.state;
  f.c.battleApplyAnimationState(s,0x8400,3,accepted());
  s.movieGeneration=1;s.movieActive=true;s.motionQueueAt=2000;
  f.c.receiveBattlePacket('BP|A|0|64');
  const view=accepted(false,false,4);f.c.receiveBattlePacket('BA|0|4',view);
  assert.equal(s.ladderPlayerSubmitted,true,'future BA must wait behind the old movie');
  assert.equal(s.pendingBattleControls[1].ladderView,view);
  f.advance(1100);
  assert.equal(s.serverTurnNo,4);assert.equal(s.ladderAwaitingCommands,false);
  assert.equal(s.ladderPlayerSubmitted,false);assert.equal(s.ladderPetSubmitted,false);
  assert.equal(f.c.battleCommandAllowed('H|0'),true);
  s.movieGeneration=2;f.c.applyBattleTurnState(s,{myNo:10,bpFlags:0,myMp:100});
  assert.equal(f.c.battleCommandAllowed('H|0'),false,'BP alone cannot expose a reconnect menu');
});

test('local defeat, BU, ordinary rewards and timeout cannot own ladder settlement',()=>{
  const f=fixture(),s=f.state;s.ladderAwaitingCommands=false;s.commandLocked=false;
  f.c.scheduleBattleDeathExit(s,true);f.c.finishLocalBattleDeath(s);
  f.c.queueBattleWorldExit(s);f.c.finishBattleWorldExit(s);f.c.openBattleResult('RD','0|0');
  f.c.battlePlayerTimeoutDefaults(s);f.c.receiveBattlePacket('BU');
  assert.equal(f.c.sendBattleEndOnce(s,'test'),false);
  assert.equal(f.app.battle,true);assert.equal(f.app.phase,'battle');assert.equal(f.sent.length,0);
  assert.equal(s.result,undefined);assert.equal(f.timers.size,0);
});

test('authoritative result waits for the full final death tail and ignores old encounter timers',()=>{
  const f=fixture(),s=f.state;s.motions=[{kind:'death',until:3000}];
  f.c.receiveBattleLadderEnvelope(result());
  assert.equal(s.ladderSettling,true);assert.equal(f.app.battle,true);
  assert.equal(f.c.battleCommandAllowed('H|0'),false);
  f.advance(1800);assert.equal(f.presented.length,0);
  assert.equal(f.calls.some(([name])=>name==='renderWorld'),false,'do not restart the field under the final movie');
  s.motions[0].until=4000;f.advance(400);assert.equal(f.presented.length,0,'late terminal motion extends the hold');
  f.advance(1000);assert.equal(f.app.battle,false);assert.equal(f.app.phase,'world');assert.equal(s.ladderResultReady,true);
  assert.deepEqual(f.presented,['m1']);assert.equal(f.sent.length,0,'result never sends EO');
  assert(f.calls.some(([name,force])=>name==='renderWorld'&&force===true),'resume map loading/painting after direct battle login');
  f.c.finishBattleLadderResult(s);assert.deepEqual(f.presented,['m1']);
  const g=fixture();g.c.receiveBattleLadderEnvelope(result());g.app.battleState={ladderID:'m2'};g.advance(1000);
  assert.equal(g.app.battle,true);assert.deepEqual(g.presented,[]);
});

test('wrong match and stale result cannot terminate the active encounter',()=>{
  const f=fixture();f.state.ladderRevision=30;
  f.c.receiveBattleLadderEnvelope(result('m1',20));
  f.c.receiveBattleLadderEnvelope(result('m2',31));
  assert.equal(f.state.ladderSettling,undefined);assert.equal(f.timers.size,0);
});

test('direct battle login with only a map header requests tiles after settlement; resident tiles are reused',()=>{
  const f=fixture(),requests=[];Object.assign(f.app,{floor:1006,position:[12,25],map:{floor:1006,tiles:[]}});
  f.c.clearInitialMapChecksum=()=>requests.push('clear-checksum');
  f.c.legacyMapWindowBounds=(floor,x,y)=>[floor,x-10,y-10,x+10,y+10];
  f.c.requestMapWindow=(...bounds)=>requests.push(bounds);
  f.c.receiveBattleLadderEnvelope(result());assert.equal(requests.length,0);
  f.advance(100);
  assert.deepEqual(requests,['clear-checksum',[1006,2,15,22,35]]);
  assert.equal(f.app.phase,'world');assert.equal(f.sent.length,0,'map recovery does not send EO');
  const g=fixture();Object.assign(g.app,{floor:1006,position:[12,25],map:{floor:1006,tiles:[1]}});
  g.c.requestMapWindow=()=>assert.fail('resident tiles must not be requested again');
  g.c.receiveBattleLadderEnvelope(result());g.advance(100);
  assert.equal(g.app.phase,'world');
});

test('cold resources preserve native order, BA metadata and final ladder result',async()=>{
  const f=fixture(),s=f.state;let loaded;
  f.c.assetState.spritesReady=false;
  f.c.actorGraphicValue=()=>100001;f.c.battleResourceImages=()=>[];
  f.c.loadSpriteManifest=()=>new Promise(resolve=>{loaded=resolve;});
  f.c.waitBattleResourceImage=()=>Promise.resolve();
  f.c.battleMovieEffects=()=>{s.motions=[{kind:'death',until:5000}];return [];};
  f.c.receiveBattlePacket('BH|r0|aA|FF');
  const view=accepted();f.c.receiveBattlePacket('BA|8400|3',view);
  f.c.receiveBattleLadderEnvelope(result());
  assert.equal(s.resourcePacketQueue.length,3);assert.equal(s.resourcePacketQueue[1].ladderView,view);
  assert.equal(s.ladderResult,undefined);assert.equal(f.calls.some(([name])=>name==='receive'),false);
  loaded(true);for(let i=0;i<16;i++)await Promise.resolve();
  assert.equal(s.resourcePacketQueue,null);assert.equal(s.ladderResult.id,'m1');assert.equal(f.app.battle,true);
  f.advance(3000);assert.equal(f.presented.length,0);
  f.advance(1500);assert.deepEqual(f.presented,['m1']);
});

test('map-transition queue keeps projection identity and rejects retired transports',()=>{
  const f=fixture(),delivered=[];f.app.phase='world';f.app.battle=false;
  f.c.mapTransitionState={active:true,pressed:false,pendingPackets:[]};
  f.c.Protocol={decodeMessage:packet=>packet};f.c.textOr=x=>x;
  for(const name of ['addWire','addEvent','renderStatus','flushPendingBattlePackets'])f.c[name]=()=>{};
  f.c.enterBattle=(field,type,id)=>{delivered.push(['EN',id]);f.app.battle=true;f.app.phase='battle';};
  f.c.receiveBattlePacket=(text,view)=>delivered.push(['B',text,view]);
  for(const name of ['deferMapTransitionPacket','flushMapTransitionPackets','handlePacket'])vm.runInContext(fn(name),f.c);
  f.c.handlePacket({function:'EN',textValues:['2','218']},f.app.transport,1,{match_id:'m1'});
  const view=accepted();f.c.handlePacket({function:'B',textValues:['BA|8400|3']},f.app.transport,1,view);
  assert.equal(delivered.length,0);
  f.c.mapTransitionState.pressed=true;f.c.flushMapTransitionPackets();
  assert.equal(delivered[0][1],'m1');assert.equal(delivered[1][2],view);
  f.c.handlePacket({function:'B',textValues:['BA|0|4']},{id:'old'},1,accepted(false,false,4));
  assert.equal(delivered.length,2);
});

test('fast ladder battle waits for the authority even when its ordinary exit path fires',()=>{
  const f=fixture(),s=f.state;s.fastBattle=true;s.fastSawActive=true;
  f.c.fastBattleEnabled=()=>false;
  f.c.window.StoneAgeAutomation={currentControl:()=>({automationState:{in_battle:false}})};
  f.c.finishFastBattle(s);f.c.syncFastBattlePresentation();assert.equal(f.app.battle,true);
  f.c.receiveBattleLadderEnvelope(result());f.advance(100);
  assert.equal(f.app.battle,false);assert.deepEqual(f.presented,['m1']);
});
