'use strict';
const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const html=fs.readFileSync(__dirname+'/runtimeassets/index.html','utf8');
function section(a,b){return html.slice(html.indexOf(a),html.indexOf(b,html.indexOf(a)));}
function fixture(hidden=false){
 let now=0,id=0,paint=0,finalized=0;const raf=new Map(),timers=new Map();
 const app={phase:'world'},c={app,document:{visibilityState:hidden?'hidden':'visible'},performance:{now:()=>now},
  window:{requestAnimationFrame:fn=>{raf.set(++id,fn);return id;},cancelAnimationFrame:key=>raf.delete(key),setTimeout:(fn,ms)=>{timers.set(++id,{fn,ms});return id;},clearTimeout:key=>timers.delete(key)},
  currentWorldWalkAnimation:()=>app.walkAnimation,fieldActorAnimationActive:()=>false,fieldActorVisualChanged:()=>true,walkProgress:()=>now/96,
  finalizePendingMove:()=>{finalized++;app.walkAnimation=null;c.requestWorldRender(true);},finalizePartyFollowMove:()=>assert.fail('wrong owner'),
  renderWorld:()=>{paint++;c.scheduleWorldAnimation();}};
 vm.createContext(c);vm.runInContext(section('  function requestWorldRender(', '  /* handletime.cpp'),c);
 return {app,c,raf,timers,get paint(){return paint;},get finalized(){return finalized;},frame(at){now=at;const callbacks=[...raf.values()];raf.clear();callbacks.forEach(fn=>fn(at));},timeout(at){now=at;const fn=timers.values().next().value.fn;fn();}};
}
test('bursts of resource/network paints coalesce into one display frame',()=>{
 const f=fixture();for(let i=0;i<100;i++)f.c.requestWorldRender(i%2===0);
 assert.equal(f.paint,0);assert.equal(f.raf.size,1);assert.equal(f.timers.size,1);
 f.frame(16);assert.equal(f.paint,1);assert.equal(f.raf.size,0);assert.equal(f.timers.size,0);
});
test('walking follows RAF without an extra delay after every paint',()=>{
 const f=fixture();f.app.walkAnimation={};f.c.scheduleWorldAnimation();
 for(const at of [16,32,48,64,80]){f.frame(at);assert.equal(f.raf.size,1);assert.equal(f.timers.size,1);}
 assert.equal(f.paint,5);f.frame(96);assert.equal(f.finalized,1);assert.equal(f.paint,6);assert.equal(f.raf.size,0);
});
test('hidden and suspended visible pages advance once; stale callbacks cannot duplicate a step',()=>{
 for(const hidden of [false,true]){
  const f=fixture(hidden);f.app.walkAnimation={};f.c.scheduleWorldAnimation();
  const stale=[...f.raf.values()][0];assert.equal(f.raf.size,hidden?0:1);
  f.timeout(150);assert.equal(f.finalized,1);assert.equal(f.paint,1);stale?.();assert.equal(f.finalized,1);assert.equal(f.paint,1);
 }
});
test('battle or logout cancels a queued field paint',()=>{
 const f=fixture();f.c.requestWorldRender(true);f.app.battle=true;f.frame(16);assert.equal(f.paint,0);assert.equal(f.raf.size,0);assert.equal(f.timers.size,0);
});
test('map persistence coalesces windows and yields while the character is walking',()=>{
 const stored=new Map([['old-window','keep']]),timers=[];
 const c={app:{phase:'world',walkAnimation:{},mapPaletteNo:0},window:{setTimeout:fn=>{timers.push(fn);return timers.length;}},localStorage:{setItem:(k,v)=>stored.set(k,v)},MAP_DATA_CACHE_MAX_BYTES:100000,mapDataCacheKey:p=>`${p.floor}:${p.x1}`};
 vm.createContext(c);vm.runInContext(section('  const pendingMapDataWrites=', '  function readCachedMapData('),c);
 for(let x=0;x<20;x++)c.rememberMapData({floor:100,x1:x,width:1,height:1,tiles:[101]});
 assert.equal(timers.length,1);assert.equal(stored.size,1);timers.shift()();assert.equal(stored.size,1);
 c.app.walkAnimation=null;timers.shift()();assert.equal(stored.size,2);assert(stored.has('100:19'));assert.equal(stored.get('old-window'),'keep');
});
