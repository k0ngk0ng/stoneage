'use strict';
const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const html=fs.readFileSync(__dirname+'/runtimeassets/index.html','utf8');
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
 for(const name of ['fastBattleEnabled','stopFastBattlePresentation','beginFastBattle','finishFastBattle','fastBattleSideDefeated','receiveFastBattleStatus','syncFastBattlePresentation','receiveFastBattlePacket','parseBattleResult','openBattleResult','receiveBattleStatus','receiveBattlePacket','renderBattle','renderBattleWorld'])vm.runInContext(fn(name),c);
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

function roster(rows,width=13){
 return '0|'+rows.map(([id,hp,flags])=>[id.toString(16),'actor','','1965a','8',hp.toString(16),'4d',flags.toString(16),...(width===13?['1','ride','7','49','81']:[])].join('|')).join('|')+'|';
}
test('defeat in one batch exits without ever observing active control, RS or BU',()=>{
 for(const width of [8,13])for(const envelope of ['BC','B'])for(const rosterFirst of [false,true]){
  const f=fixture();f.control.automationState.in_battle=false;f.c.beginFastBattle(f.app.battleState);
  f.c.deferBattleResourcePacket=()=>assert.fail('defeat must not load graphics');
  const terminal=roster([[0,0,6],[5,36,0],[15,0,2],[16,99,0]],width);
  const receive=()=>envelope==='BC'?f.c.receiveBattleStatus(terminal):f.c.receiveBattlePacket('BC|'+terminal);
  if(rosterFirst){receive();assert.equal(f.app.battle,true);}
  f.c.receiveBattlePacket('BP|0|2|64');if(!rosterFirst)receive();
  assert.equal(f.app.battleState.fastSawActive,false);
  assert.equal(f.app.phase,'world');assert.equal(f.app.battle,false);
  assert.equal(f.chats.filter(x=>x==='战斗结束。').length,1);
  receive();f.c.receiveBattlePacket('BU');f.c.syncFastBattlePresentation();
  assert.equal(f.chats.filter(x=>x==='战斗结束。').length,1,'duplicate terminal packets settle once');
 }
});
test('stale idle control and a surviving teammate do not end the new battle',()=>{
 const f=fixture();f.control.automationState.in_battle=false;f.c.beginFastBattle(f.app.battleState);
 f.c.receiveBattlePacket('BP|A|2|64');
 f.c.receiveBattleStatus(roster([[10,0,6],[11,20,4],[15,36,0],[0,99,4]]));
 f.c.syncFastBattlePresentation();assert.equal(f.app.battle,true,'another player on my side is alive');
 f.c.receiveBattleStatus(roster([[10,0,6],[11,0,6],[15,36,0],[0,99,4]]));
 assert.equal(f.app.battle,false,'side one defeat ignores living pet and enemy');
 // A fresh EN creates a fresh state; the previous finished flag cannot hide it.
 f.app.battle=true;f.app.phase='battle';f.app.battleState={fieldNo:1,type:1};f.c.beginFastBattle(f.app.battleState);
 f.c.receiveBattlePacket('BP|0|0|64');f.c.receiveBattleStatus(roster([[0,50,4],[10,99,4]]));
 f.c.syncFastBattlePresentation();assert.equal(f.app.battle,true);
});
test('empty, truncated, malformed and missing-owner rosters cannot prove defeat',()=>{
 const f=fixture();f.control.automationState.in_battle=false;f.c.beginFastBattle(f.app.battleState);f.c.receiveBattlePacket('BP|0|0|64');
 for(const data of ['', '0|',roster([[0,0,6]]).slice(0,-4),roster([[0,0,6]]).replace('|0|4d|','|invalid|4d|'),roster([[5,0,2],[10,99,4]]),roster([[0,0,6],[0,0,6]])]){
  f.c.receiveBattleStatus(data);assert.equal(f.app.battle,true,data);
 }
});

test('enabling fast mode after terminal roster was received also settles defeat',()=>{
 const f=fixture();f.control.automationState.in_battle=false;
 f.app.battleState.lastTurnPacket='BP|0|2|64';
 f.app.battleState.lastRosterPacket=roster([[0,0,6],[5,36,0],[10,99,4]]);
 f.c.beginFastBattle(f.app.battleState);assert.equal(f.app.battle,false);assert.equal(f.app.phase,'world');
});
