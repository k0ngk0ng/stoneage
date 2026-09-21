"use strict";
const assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const html=fs.readFileSync(__dirname+'/runtimeassets/index.html','utf8');
const between=(a,b)=>{const start=html.indexOf(a),end=html.indexOf(b,start);assert(start>=0&&end>start);return html.slice(start,end);};
const digits='0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ';
function encode62(n){let s='';do{s=digits[n%62]+s;n=Math.floor(n/62);}while(n);return s;}
const packets=[],messages=[],timers=new Map();let timerID=0;
const app={phase:'world',battle:false,position:[1,1],transport:{},pc:{ridePet:-1,learnRide:200},petSlots:[{name:'骑宠',ai:100,level:1,hp:100}],selectedPet:-1,petMailNo:-1,status:{standbyPetMask:0}};
const context=vm.createContext({app,Number,String,Object,Math,Boolean,Protocol:{base62:s=>[...s].reduce((a,c)=>a*62+digits.indexOf(c),0)},stateNumber:Number,unescapeCharacterOption:s=>s,window:{setTimeout:fn=>{timers.set(++timerID,fn);return timerID;},clearTimeout:id=>timers.delete(id)},mapMovementBlocked:()=>false,renderPets(){},renderBattle(){},setStatus(){},addChat(){},setWorldState:(...v)=>messages.push(v),reportError:e=>{throw e},fieldSend:async(...v)=>{packets.push(JSON.parse(JSON.stringify(v)));return true},send:async(...v)=>{packets.push(JSON.parse(JSON.stringify(v)));return true}});
vm.runInContext(between('  const PC_FIELDS=','  const PET_FIELDS='),context);
vm.runInContext(between('  function petDisplayName(','  function petAttributeBitmap('),context);
vm.runInContext(between('  function applyMaskedFields(','  /* CHAR_makeStatusString'),context);
const start=html.indexOf('      case "P": {',html.indexOf('  function receiveSystemState(')),end=html.indexOf('      case "M":',start);
vm.runInContext('function receiveP(data){const parts=data.split("|"),kind=parts[0];switch(kind[0]){'+html.slice(start,end)+'}}',context);
(async()=>{
 // Follow MENU.CPP's actual state-button path, not a separate mount shortcut.
 context.activatePetMenuState(0);assert.equal(context.petMenuState(0).key,'standby');
 context.activatePetMenuState(0);assert.deepEqual(packets.at(-1),['KS',[0]]);assert.equal(app.petActionRequest.slot,0);
 const ksCount=packets.length;context.activatePetMenuState(0);assert.equal(packets.length,ksCount,'pending KS clicks are deduplicated');
 context.receivePetBattleStatus(0,1);assert.equal(app.selectedPet,0);assert.equal(app.petActionRequest,null);
 context.activatePetMenuState(0);assert.deepEqual(packets.at(-1),['KS',[-1]]);assert.equal(app.petActionRequest.slot,-1);
 context.receivePetBattleStatus(-1,1);assert.equal(app.selectedPet,-1);
 assert.equal(context.petMenuState(0).key,'mail');context.activatePetMenuState(0);await Promise.resolve();
 assert.deepEqual(packets.at(-1),['FM',['R|P|0']]);assert.equal(app.pc.ridePet,-1,'request must not optimistically mount');
 const count=packets.length;context.activatePetMenuState(0);assert.equal(packets.length,count,'pending clicks are deduplicated');
 context.receiveP('P'+encode62(1<<27)+'|0');assert.equal(context.petMenuState(0).key,'ride');assert.equal(context.petMenuState(0).bitmap,234536);assert.equal(app.rideRequest,null);
 context.activatePetMenuState(0);await Promise.resolve();assert.deepEqual(packets.at(-1),['FM',['R|P|-1']]);assert.equal(app.pc.ridePet,0,'dismount waits for the authoritative P reply');
 context.receiveP('P'+encode62(1<<27)+'|-1');assert.equal(context.petMenuState(0).key,'rest');assert.equal(timers.size,0);
 // A rejected KS releases the latch and keeps the native intermediate state
 // usable for a subsequent click.
 app.status.standbyPetMask=0;app.petMailNo=-1;app.selectedPet=-1;
 context.activatePetMenuState(0);context.activatePetMenuState(0);
 const rejectCount=packets.length;assert.deepEqual(packets.at(-1),['KS',[0]]);
 context.receivePetBattleStatus(0,0);assert.equal(app.petActionRequest,null);assert.equal(context.petMenuState(0).key,'standby');
 context.activatePetMenuState(0);assert.equal(packets.length,rejectCount+1);assert.deepEqual(packets.at(-1),['KS',[0]]);
 // A lost KS callback also releases the latch after the bounded timeout.
 const timeout=timers.get(app.petActionRequest.timer);assert.equal(typeof timeout,'function');timeout();assert.equal(app.petActionRequest,null);timers.clear();
 const timeoutCount=packets.length;context.activatePetMenuState(0);assert.equal(packets.length,timeoutCount+1);assert.deepEqual(packets.at(-1),['KS',[0]]);
 context.receivePetBattleStatus(0,1);assert.equal(app.selectedPet,0);context.receivePetBattleStatus(-1,1);assert.equal(app.selectedPet,-1);
 // Native masks include transmigration before NAME; combined updates must
 // neither turn a name into a ride slot nor consume the wrong token.
 const mask=(1<<24)|(1<<25)|(1<<26)|(1<<27)|(1<<28)|(1<<29);
 context.receiveP('P'+encode62(mask)+'|2|RideHero|称号|0|120|100000');
 assert.equal(app.pc.transmigration,2);assert.equal(app.pc.name,'RideHero');assert.equal(app.pc.freeName,'称号');assert.equal(app.pc.ridePet,0);assert.equal(app.pc.learnRide,120);assert.equal(app.pc.image,100000);
 context.receiveP('P'+encode62(1<<27)+'|-1');
 for(const setup of [()=>app.battle=true,()=>app.trade={active:true},()=>app.petSlots[0].ai=99,()=>app.selectedPet=0,()=>app.pc.learnRide=0]){
  app.battle=false;app.trade=null;app.petSlots[0].ai=100;app.selectedPet=-1;app.pc.learnRide=200;setup();
  const before=packets.length;assert.equal(await context.requestPetRide(0),false);assert.equal(packets.length,before);
 }
 app.pc.learnRide=200;app.selectedPet=-1;app.petSlots[0].ai=100;app.battle=false;app.trade=null;
 await context.requestPetRide(0);for(const fn of [...timers.values()])fn();timers.clear();assert.equal(app.rideRequest,null);assert.equal(app.pc.ridePet,-1,'rejection/timeout must not mount');
 await context.requestPetRide(0);assert.equal(timers.size,1);context.clearRideRequest();assert.equal(timers.size,0);assert.equal(app.rideRequest,null);
 // P1 after reconnect carries the authoritative existing mount as well.
 const full=Array(28).fill('0');full[24]='0';full[25]='200';full[26]='100000';
 context.receiveP('P1|'+full.join('|')+'|RideHero|');assert.equal(context.petMenuState(0).key,'ride');
 console.log('Native pet-state riding/dismount, server masks, reconnect state, rejection and duplicate-click tests passed.');
})().catch(e=>{console.error(e);process.exitCode=1});
