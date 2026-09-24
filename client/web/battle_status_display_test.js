'use strict';
const assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const html=fs.readFileSync(__dirname+'/runtimeassets/index.html','utf8');
function fn(name){const a=html.indexOf('  function '+name+'('),line=html.indexOf('\n',a);assert(a>=0);return html.slice(a,html.slice(a,line).endsWith('}')?line:html.indexOf('\n  }',line)+4);}
const app={battle:true,pc:{maxMp:100},systemSettings:{},battleState:{myNo:0,myMp:30,participants:[{battleId:0,player:true},{battleId:10,player:true,name:'敌人',level:10,hp:80,maxHp:120},{battleId:15,player:false,name:'乌力',hp:90,maxHp:100}]}};
const c={app,renderBattleWorld(){},queueBattleControl(state,packet){state.pendingBattleControls.push(packet);},battleControlQueue:s=>s.pendingBattleControls||[],battleSide:id=>id<10?0:1};vm.createContext(c);
for(const name of ['battleNumber','applyBattleVitals','battleVitalValues','battleInspectionText','applyQueuedBattleControl','flushQueuedBattleControls','receiveFastBattlePacket'])vm.runInContext(fn(name),c);
const s=app.battleState;
c.applyBattleVitals(s,'BVS|0|1E|64|A|28|C8|');
assert.equal(s.participants[1].mp,40);assert.equal(s.participants[1].maxMp,200);
assert.equal(c.battleInspectionText(s.participants[1],s),'敌人 Lv.10　体力 80/120　气力 40/200');
assert.equal(c.battleInspectionText(s.participants[2],s),'乌力 Lv.0　体力 90/100');
assert.equal(c.battleVitalValues({battleId:10,player:true},s),null,'missing remote MP must not use local player max');
for(const payload of ['BVS|A|oops|64|','BVS|A|28|','BVS|A|28|64|A|10|64|','BVS|FF|28|64|']){c.applyBattleVitals(s,payload);assert.equal(s.participants[1].maxMp,200,'malformed packet must be atomic');}
// BVS follows the same deferred turn boundary as BC, never updates the movie early.
s.pendingBattleControls=[{kind:'BVS',payload:'BVS|A|32|C8|',receivedAt:2},{kind:'BC',payload:'new roster',receivedAt:1}];
c.receiveBattleStatus=()=>{s.participants=[{battleId:10,player:true}];};
c.flushQueuedBattleControls(s);assert.equal(s.participants[0].mp,50);
c.applyBattleVitals(s,'BVS|');assert.equal(c.battleVitalValues(s.participants[0],s),null,'empty snapshot clears obsolete vitals');
s.fastBattle=true;s.fastAwaitingTurn=false;c.receiveFastBattlePacket('BVS|A|32|C8|');assert.equal(s.fastAwaitingTurn,false,'display data must not start fast-battle movie');
c.receiveFastBattlePacket('BV|0|1|');assert.equal(s.fastAwaitingTurn,true,'legacy attribute-change movie must remain a movie');
s.fastAwaitingTurn=true;c.receiveFastBattlePacket('BVS|A|32|C8|');assert.equal(s.fastAwaitingTurn,true,'display data must not finish a movie');
// Persist both switches independently across a reload.
vm.runInContext(html.match(/  const SYSTEM_SETTINGS_KEY=.*\n/)[0]+html.match(/  const SYSTEM_SETTINGS_FIELDS=.*\n/)[0],c);
for(const name of ['saveSystemSettings','loadSystemSettings'])vm.runInContext(fn(name),c);
let saved='';const storage={setItem(k,v){saved=v;},getItem(){return saved;}};
app.systemSettings={battleHpVisible:false,battleMpVisible:true};c.saveSystemSettings(storage);app.systemSettings={};c.loadSystemSettings(storage);
assert.equal(app.systemSettings.battleHpVisible,false);assert.equal(app.systemSettings.battleMpVisible,true);
console.log('Battle vitals: remote values, absent MP, malformed snapshots, turn ordering, fast battle and persistence passed.');
