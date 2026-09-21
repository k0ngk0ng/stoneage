'use strict';
const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const html=fs.readFileSync(__dirname+'/index.html','utf8');
function fn(name){const a=html.indexOf('  function '+name+'('),line=html.indexOf('\n',a);assert(a>=0);return html.slice(a,html.slice(a,line).endsWith('}')?line:html.indexOf('\n  }',line)+4);}
function fixture(){
 const chats=[],calls=[];
 const app={battle:true,phase:'battle',character:'玩家',petSlots:[{name:'乌力'}],systemSettings:{fastBattle:true},battleState:{fieldNo:1,type:1}};
 const control={mode:'leveling',automationActive:true,automationState:{in_battle:true}};
 const c={app,Date,worldScreen:{id:'world'},window:{clearTimeout(){},StoneAgeAutomation:{currentControl:()=>control}},
  addChat:(...args)=>chats.push(args[1]),setWorldState(){},petDisplayName:p=>p?.name||'',
  decimal:(s,f=0)=>Number.isFinite(parseInt(s,10))?parseInt(s,10):f,battleBase62:s=>{const alphabet='0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ';let n=0;for(const ch of String(s))n=n*62+alphabet.indexOf(ch);return n;}};
 for(const name of ['clearBattleChoiceTimer','battleCancelPendingUi','battleCancelPendingDamage','battleClearCommandPending','cancelBattleMenuMotionFrame','cancelSceneTransition','applyScreenVisibility','restoreMapMusic','closeBattlePopup','clearPendingBattlePackets','renderWorld','scheduleWorldAnimation'])c[name]=(...args)=>calls.push([name,...args]);
 vm.createContext(c);
 for(const name of ['fastBattleEnabled','stopFastBattlePresentation','beginFastBattle','finishFastBattle','syncFastBattlePresentation','receiveFastBattlePacket','parseBattleResult','openBattleResult','receiveBattleStatus','receiveBattlePacket','renderBattle','renderBattleWorld'])vm.runInContext(fn(name),c);
 return {c,app,control,chats,calls};
}
test('fast battle is opt-in and only active under a running battle owner',()=>{
 const {c,app,control}=fixture();assert.equal(c.fastBattleEnabled(),true);
 for(const mode of ['manual','paused','quest','agent']){control.mode=mode;assert.equal(c.fastBattleEnabled(),false);}
 control.mode='battle';control.automationActive=false;assert.equal(c.fastBattleEnabled(),false);
 control.automationActive=true;app.systemSettings.fastBattle=false;assert.equal(c.fastBattleEnabled(),false);
});
test('quiet packets skip rendering and preloading, retain current roster and turn, and settle once',()=>{
 const {c,app,chats,calls}=fixture();
 c.deferBattleResourcePacket=()=>assert.fail('quiet mode must not request animation assets');
 c.beginFastBattle(app.battleState);
 c.receiveBattleStatus('0|0|player|');c.receiveBattlePacket('BP|0|0|A');c.receiveBattlePacket('BA|0|1');
 assert.equal(app.battleState.lastRosterPacket,'0|0|player|');assert.equal(app.battleState.lastTurnPacket,'BP|0|0|A');
 c.receiveBattlePacket('BP|BH|r0|aA|FF');assert.equal(app.battleState.fastAwaitingTurn,true);
 c.receiveBattlePacket('BP|0|0|A');assert.equal(app.battleState.fastAwaitingTurn,false);
 c.renderBattle();c.renderBattleWorld(); // DOM/asset access is deliberately absent in fixture.
 c.openBattleResult('RS','-2|1|A,0|0|2,,,,石头|');
 assert.equal(app.phase,'world');assert.equal(app.battle,false);
 assert(chats.some(line=>line.includes('玩家 经验 +36（升级）')&&line.includes('乌力 经验 +2')&&line.includes('石头')));
 const before=chats.length;c.openBattleResult('RS','-2|1|A,0|0|2,,,,石头|');c.receiveBattlePacket('BU');
 assert.equal(chats.length,before);assert(calls.some(call=>call[0]==='applyScreenVisibility'&&call[1].id==='world'));
});
test('BU before rewards preserves text rewards without reopening combat',()=>{
 const {c,app,chats}=fixture();c.beginFastBattle(app.battleState);c.receiveBattlePacket('BU');
 c.openBattleResult('RD','A|2');assert.equal(app.phase,'world');assert(chats.some(line=>line.includes('人物 经验 +36')&&line.includes('宠物 经验 +2')));
});
test('defeat without RS follows authoritative end; takeover rebuilds normal controls',()=>{
 const {c,app,control}=fixture();c.beginFastBattle(app.battleState);
 control.automationState.in_battle=false;c.syncFastBattlePresentation();assert.equal(app.battle,false);
 const f=fixture();f.c.beginFastBattle(f.app.battleState);f.c.receiveBattlePacket('BC|roster');f.c.receiveBattlePacket('BP|0|0|A');f.c.receiveBattlePacket('BA|0|2');
 const replay=[];f.c.enterBattle=(field,type)=>{replay.push(['enter',field,type]);f.app.battleState={};};
 f.c.receiveBattleStatus=value=>replay.push(['BC',value]);f.c.receiveBattlePacket=value=>replay.push(['B',value]);
 f.control.mode='manual';f.control.automationActive=false;f.c.syncFastBattlePresentation();
 assert.deepEqual(replay,[['enter',1,1],['BC','BC|roster'],['B','BP|0|0|A'],['B','BA|0|2']]);
});
test('switching off during a movie waits for a fresh turn instead of reusing old BP',()=>{
 const f=fixture();f.c.beginFastBattle(f.app.battleState);f.c.receiveBattlePacket('BP|0|0|A');f.c.receiveBattlePacket('BH|r0|FF');
 const replay=[];f.c.enterBattle=()=>{f.app.battleState={};};f.c.receiveBattlePacket=p=>replay.push(p);f.app.systemSettings.fastBattle=false;
 f.c.syncFastBattlePresentation();assert.deepEqual(replay,[]);
});
