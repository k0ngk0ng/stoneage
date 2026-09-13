'use strict';
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const html=fs.readFileSync(__dirname+'/index.html','utf8');
function section(start,end){const a=html.indexOf(start),b=html.indexOf(end,a+start.length);assert(a>=0&&b>a);return html.slice(a,b);}
const classes=new Set();
const rect={left:10,top:20,width:320,height:240};
let mobile=true, paints=0;
const app={phase:'world',battle:false,cursor:{},taskbarVisible:false};
const ctx={app,window:{matchMedia:()=>({matches:mobile})},navigator:{maxTouchPoints:1},
  $:id=>id==='world-tools'?{classList:{toggle:(name,on)=>on?classes.add(name):classes.delete(name)}}:{getBoundingClientRect:()=>rect},
  worldScreen:{getBoundingClientRect:()=>rect},renderWorldOverlay(){paints++;},
  worldPointerIsUiTarget:()=>false,isWorldUiTarget:()=>false,nearestTileAt:(x,y)=>[x,y]};
vm.createContext(ctx);
vm.runInContext(section('  function mobileWorldTaskbarPinned(', '  function faceTowardTile('),ctx);
ctx.setWorldTaskbarVisible(false);
assert.equal(app.taskbarVisible,true,'mobile entry pins menu');
ctx.updateWorldTaskbarPointer(50,50,null);
assert.equal(app.taskbarVisible,true,'moving away does not hide mobile menu');
ctx.setWorldTaskbarVisible(false);
assert(classes.has('taskbar-visible'),'pointerleave cannot hide mobile menu');
app.battle=true;ctx.setWorldTaskbarVisible(false);assert.equal(app.taskbarVisible,false);
app.battle=false;mobile=false;ctx.setWorldTaskbarVisible(false);assert.equal(app.taskbarVisible,false);
ctx.updateWorldTaskbarPointer(100,255,null);assert.equal(app.taskbarVisible,true);
ctx.updateWorldTaskbarPointer(100,50,null);assert.equal(app.taskbarVisible,false);
const listeners=[];
ctx.document={addEventListener:(type,fn,capture)=>listeners.push({type,fn,capture})};
vm.runInContext(html.match(/document.addEventListener\("pointerdown",event=>\{\s*if\(event.isPrimary!==false\)updateWorldPointer\(event\);\s*\},true\);/)[0],ctx);
assert.equal(listeners.length,1);assert.equal(listeners[0].capture,true);
listeners[0].fn({isPrimary:true,isTrusted:true,pointerType:'touch',clientX:110,clientY:80});
assert.equal(app.cursor.x,200);assert.equal(app.cursor.y,120);assert.equal(app.cursorTouchLike,true);assert.equal(paints,1);
listeners[0].fn({isPrimary:false,isTrusted:true,pointerType:'touch',clientX:220,clientY:100});
assert.equal(app.cursor.x,200,'second finger must not move cursor');
assert.match(html,/#app,#app \* \{[^}]*-webkit-user-select:none;[^}]*user-select:none;/);
assert.match(html,/#app input,#app textarea,[^\n]+user-select:text;/);
assert.match(section('  function applyScreenVisibility(', '  function ',),/else setWorldTaskbarVisible\(app.taskbarVisible\)/);
(async()=>{
 for(const failure of ['none','throw','reject']){
  let closes=0,cancelled=0;const events={};
  const state={phase:'world',polling:true,transport:{close(options){closes++;assert.equal(options.keepalive,true);if(failure==='throw')throw Error('closed');if(failure==='reject')return Promise.reject(Error('closed'));return Promise.resolve();},logoutAndClose(){assert.fail('must not send record-point logout');}}};
  const env={app:state,cancelConnectionRetry(){cancelled++;},window:{addEventListener:(name,fn)=>events[name]=fn}};
  vm.createContext(env);vm.runInContext(section('  let pageUnloadLogoutSent=false;','  function returnToLogin('),env);
  let prevented=0;events.beforeunload({preventDefault(){prevented++;}});
  assert.equal(prevented,1);assert.equal(closes,0,'cancelled unload retains connection');
  events.pagehide();events.pagehide();await Promise.resolve();
  assert.equal(closes,1);assert.equal(state.transport,null);assert.equal(state.polling,false);assert.equal(cancelled,2);
 }
 console.log('mobile tap, pinned menu, selection and in-place browser-close regressions passed');
})().catch(error=>{console.error(error);process.exitCode=1;});
