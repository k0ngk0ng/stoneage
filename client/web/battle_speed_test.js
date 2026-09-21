const fs=require('node:fs'), vm=require('node:vm'), assert=require('node:assert/strict');
const source=fs.readFileSync(__dirname+'/runtimeassets/index.html','utf8');
function fn(name){const start=source.indexOf('  function '+name+'(');assert(start>=0,name);const lineEnd=source.indexOf('\n',start);if(source.slice(start,lineEnd).endsWith('}'))return source.slice(start,lineEnd);return source.slice(start,source.indexOf('\n  }',lineEnd)+4);}
const context={app:{systemSettings:{battleAnimationSpeed:1}}, BATTLE_ANIMATION_SPEED_OPTIONS:JSON.parse(source.match(/BATTLE_ANIMATION_SPEED_OPTIONS=Object\.freeze\((\[[^\]]+\])\)/)[1]),BATTLE_PROC_TICK_MS:1000/60,performance:{now:()=>10000},Date:{now:()=>1000},Map,battleSide:()=>0,battleSlotDirection:()=>5};
vm.createContext(context);
for(const name of ['battleAnimationSpeedValue','battleAnimationSpeed','battlePlaybackSpeed','battleMotionRenderPriority','battleNativeTickProgress','battleNativeSpeedProgress','battleNativeAlternatingOffset','battleAnimationElapsedWithHolds','battleMotionValue'])vm.runInContext(fn(name),context);
const base={actor:0,target:0,startedAt:1000,playbackStartAt:1000,duration:1000,direction:5,toX:100,toY:50,approachDuration:100,attackDuration:300,postAttackHold:100,returnDuration:200,deadStartOffset:600,deadDuration:400,knockbackDuration:100,decelDuration:100,pauseDuration:100,deathStartOffset:300};
let samples=0;
for(const kind of ['attack','counter-attack','hit','death','death-direct','cast','charge','appear','fade','escape','escape-fail','guard','dodge','bd-damage','catch']){
 for(const elapsed of [50,150,350,550,750,950]){
  const render=speed=>context.battleMotionValue({motions:[{...base,kind,playbackSpeed:speed,until:1000+1000/speed}]},0,1000+elapsed/speed);
  for(const speed of [2,5,10]){assert.deepEqual(JSON.parse(JSON.stringify(render(speed))),JSON.parse(JSON.stringify(render(1))),kind+' '+elapsed+' at '+speed+'x');samples++;}
 }
}
const death={...base,kind:'death',playbackSpeed:2,until:1500};
const state={motions:[death],deathStartedAt:new Map([[0,1300]])};
const middle=context.battleMotionValue(state,0,1350);
assert.equal(middle.action,2);
assert.equal(middle.animationStartedAt,9900,'DEAD at 2x must play its intermediate frame, not skip to final');

// Keep real wire countdowns separate from the client movie clock.
let now=1000,nextTimer=0,played=[];
const timers=new Map();
context.Date.now=()=>now;
context.Set=Set;
context.BATTLE_ANIMATION_SPEED_KEY='stoneage:web:battle-animation-speed';
context.BATTLE_TURN_COUNTDOWN_MS=30000;
context.BATTLE_BP_PLAYER_MENU_NON=1;
context.battleEntryPending=()=>false;
context.clearBattleChoiceTimer=()=>{};
context.renderBattleTaskbar=context.renderBattleWorld=context.renderBattle=()=>{};
context.battleSlotPoint=()=>[320,240];
context.playSoundEffect=sound=>played.push(sound);
context.window={setTimeout(callback,delay){const id=++nextTimer;timers.set(id,{callback,at:now+delay});return id;},clearTimeout(id){timers.delete(id);}};
function advance(at){now=at;for(const [id,timer] of [...timers])if(timer.at<=now){timers.delete(id);timer.callback();}}
for(const name of ['battleAnimationScale','battleAnimationOffset','battleAnimationStorage','loadBattleAnimationSpeed','saveBattleAnimationSpeed','setBattleAnimationSpeed','battlePushMotion','battlePushProjectile','battleProjectileValue','battlePushEffect','battleScheduleDamage','armBattlePlayerChoiceTimer','battleChoiceDeadline','battleChoiceRemaining'])vm.runInContext(fn(name),context);
for(const invalid of [undefined,null,0,-1,11,2.5,'bad',Infinity])assert.equal(context.battleAnimationSpeedValue(invalid),1);
assert.equal(context.loadBattleAnimationSpeed(null),1);
const storage=new Map();
context.localStorage={getItem:key=>storage.get(key),setItem:(key,value)=>storage.set(key,value)};
assert.equal(context.loadBattleAnimationSpeed(),1);
for(const speed of [1,1.5,2,3,4,5,6,7,8,9,10]){context.setBattleAnimationSpeed(speed);assert.equal(context.loadBattleAnimationSpeed(),speed);}
assert.equal(context.loadBattleAnimationSpeed({getItem(){throw Error('blocked');}}),1);
assert.equal(context.saveBattleAnimationSpeed(3,{setItem(){throw Error('quota');}}),3);

// Run the production movie queue closure, including its real sound scheduling.
const movieStart=source.indexOf('  function battleMovieEffects(');
const queueStart=source.indexOf('    const queueMotion=',movieStart);
const queueEnd=source.indexOf('    const timedMotion=',queueStart);
assert(queueEnd>queueStart);
vm.runInContext('function queueForTest(state,motion){'+source.slice(queueStart,queueEnd)+'return queueMotion(motion);}',context);
for(const speed of [1,1.5,2,3,4,5,6,7,8,9,10]){
 now=1000;timers.clear();played=[];context.setBattleAnimationSpeed(speed);
 const state={motions:[],projectiles:[],effects:[],motionQueueAt:1300};context.app.battle=true;context.app.battleState=state;
 const timing=context.queueForTest(state,{kind:'attack',actor:0,duration:1200,attackStartOffset:150,attackDuration:600,contactOffset:600,soundEvents:[{sound:9,offset:600}]});
 assert.equal(timing.startAt,1300,'queued start is already a wall-clock deadline');
 assert.equal(timing.contactAt,1300+600/speed);
 assert.equal(state.motionQueueAt,1300+1200/speed);
 assert.equal(state.motions[0].until,state.motionQueueAt);
 assert.equal(state.pendingDamageUntil,timing.contactAt,'sound callback shares the attack contact clock');
 let hp=10;context.battleScheduleDamage(state,timing.contactAt,()=>{hp-=3;});
 advance(timing.contactAt-1);assert.equal(hp,10);assert.deepEqual(played,[]);
 advance(timing.contactAt);assert.equal(hp,7);assert.deepEqual(played,[9]);
 advance(timing.contactAt+1);assert.equal(hp,7);assert.deepEqual(played,[9],'no repeated callbacks');
 now=1000;
 context.battlePushProjectile(state,{actor:0,from:[0,0],to:[100,0],startAt:1300,duration:1200});
 const projectile=state.projectiles[0];assert.equal(projectile.until,1300+1200/speed);
 assert.equal(context.battleProjectileValue(projectile,1300+600/speed).x,50);
 context.battlePushEffect(state.effects,0,'3','damage',{startsAt:1300,duration:1200});
 assert.equal(state.effects[0].until,1300+1200/speed);
 const captured=state.motions[0],deadline=captured.until;
 context.setBattleAnimationSpeed(speed===3?1:3);
 assert.equal(context.battlePlaybackSpeed(captured),speed,'already queued motions keep their speed');
 assert.equal(captured.until,deadline);
 assert.equal(context.battlePlaybackSpeed(projectile),speed);
 context.setBattleAnimationSpeed(speed);timers.clear();
 context.armBattlePlayerChoiceTimer(state);
 assert.equal(state.choiceDeadline,31000,'player choice remains 30 seconds at every speed');
 assert.equal(context.battleChoiceRemaining(state,11000),20000);
 assert.equal(timers.get(state.choiceTimer).at,31000);
}
// The transient DEAD movie owns its intermediate frames; only expired corpses pin the end.
const finalCorpse=context.battleMotionValue({motions:[],deathStartedAt:new Map([[0,1300]])},0,1600);
assert.equal(finalCorpse.action,2);assert.equal(finalCorpse.animationLoop,false);
assert(finalCorpse.animationStartedAt<0);
console.log('battle speed: settings, '+samples+' phase samples, queue/callbacks, projectiles, effects, DEAD and real countdown passed');
