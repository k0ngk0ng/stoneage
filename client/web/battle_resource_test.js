'use strict';
const assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const html=fs.readFileSync(__dirname+'/runtimeassets/index.html','utf8');
const read=name=>{const data=JSON.parse(fs.readFileSync(__dirname+'/assets/original/'+name,'utf8'));return data.sprites||data;};
const full=read('sprites.json'),bootstrap=read('field-bootstrap-sprites.json');
const spriteSource=html.slice(html.indexOf('  function assetManifestKeys'),html.indexOf('  function reportError'));
function fn(name){const a=html.indexOf('  function '+name+'('),line=html.indexOf('\n',a);assert(a>=0);return html.slice(a,html.slice(a,line).endsWith('}')?line:html.indexOf('\n  }',line)+4);}
function rendered(table,graphic,action,cold){
 const row=full[graphic].actions.find(a=>a.direction===5&&a.action===action),images=new Map();
 for(const a of table[graphic].actions)for(const f of a.frames)images.set(f.file,{complete:true,naturalWidth:64,naturalHeight:64,src:f.file});
 if(cold){images.get(row.frames[0].file).complete=false;images.get(row.frames[0].file).naturalWidth=0;}
 const c={app:{},assetState:{sprites:{[graphic]:table[graphic]},spritesReady:table===full},performance:{now:()=>1000},Protocol:{base62:()=>NaN},LEGACY_PROC_TICK_MS:8,LEGACY_FIELD_ANIMATION_TICK_MS:1000/60,loadAsset:file=>images.get(file)};
 vm.createContext(c);vm.runInContext(spriteSource,c);
 return c.actorFrame({battle:true,graphic:Number(graphic),direction:5,action,animationStartedAt:1000,animationLoop:false});
}
for(const graphic of ['100296','100288'])for(const action of [0,2]){
 assert.equal(rendered(bootstrap,graphic,action,false),null,'incomplete table must not invent a stand pose/direction');
 assert.equal(rendered(full,graphic,action,true),null,'undecoded first frame must not wrap to a future frame');
 const hot=rendered(full,graphic,action,false);
 assert.equal(hot.animation.action,action);assert.equal(hot.frame.file,full[graphic].actions.find(a=>a.direction===5&&a.action===action).frames[0].file);
}
const tick=async()=>{for(let i=0;i<12;i++)await Promise.resolve();};
(async()=>{
 let now=1000,next=0,requests=0,resolveManifest;
 const timers=new Map(),delivered=[];
 const bitmap={complete:true,naturalWidth:64};
 const c={Date:{now:()=>now},Set,Map,Promise,app:{battle:true,phase:'battle'},assetState:{spritesReady:false},$:()=>null,actorGraphicValue:a=>a.graphic,clearBattleChoiceTimer:()=>{},spriteEntryForActor:()=>({sprite:{actions:[{action:0,frames:[{file:'same.png'},{file:'same.png'}]},{action:2,frames:[{file:'same.png'}]}]}}),loadAsset:()=>{requests++;return bitmap;},loadSpriteManifest:()=>new Promise(resolve=>{resolveManifest=()=>{c.assetState.spritesReady=true;resolve(true);};}),window:{setTimeout(callback,delay){const id=++next;timers.set(id,{callback,at:now+delay});return id;},clearTimeout:id=>timers.delete(id)}};
 vm.createContext(c);for(const name of ['battleResourceMoviePacket','battleResourceImages','waitBattleResourceImage','deferBattleResourcePacket'])vm.runInContext(fn(name),c);
 c.receiveBattlePacket=data=>{assert.equal(c.deferBattleResourcePacket('B',data),false);delivered.push(['B',data]);};
 c.receiveBattleStatus=data=>{assert.equal(c.deferBattleResourcePacket('BC',data),false);delivered.push(['BC',data]);};
 c.openBattleResult=(kind,data)=>{assert.equal(c.deferBattleResourcePacket(kind,data),false);delivered.push([kind,data]);};
 const state={participants:[{graphic:100296}],movieHoldUntil:0};c.app.battleState=state;
 assert.equal(c.deferBattleResourcePacket('B','BP|0|1|A'),false,'initial turn state must not start an asset wait');
 assert.equal(c.deferBattleResourcePacket('B','BH|a0|rA|f0|d1|FF|'),true);
 for(const [kind,data] of [['BC','roster'],['B','BP|0|1|A'],['B','BA|0|1'],['RS','-2|0|1,,,,,||'],['B','BU|']])assert.equal(c.deferBattleResourcePacket(kind,data),true);
 assert.equal(delivered.length,0);assert.equal(state.commandLocked,true);
 resolveManifest();await tick();
 assert.deepEqual(delivered.map(p=>p[0]+':'+p[1]),['B:BH|a0|rA|f0|d1|FF|','BC:roster','B:BP|0|1|A','B:BA|0|1','RS:-2|0|1,,,,,||','B:BU|']);
 assert.equal(requests,1,'duplicate references share a single cached Image lookup');
 assert.equal(c.deferBattleResourcePacket('B','BH|next'),false);assert.equal(requests,1,'later movies reuse the prepared resource set');
 // A completed cache hit has no asynchronous gate, manifest fetch or new image copy.
 c.app.battleState={participants:[{graphic:100296}]};assert.equal(c.deferBattleResourcePacket('B','BH|cached'),false);
 assert.equal(requests,2);assert.equal(c.app.battleState.resourcePacketQueue,undefined);
 // A timed-out load must release the exact queue once, and late completion is inert.
 c.assetState.spritesReady=false;c.app.battleState={participants:[{graphic:100288}]};
 assert.equal(c.deferBattleResourcePacket('B','BH|timeout'),true);
 now+=10000;for(const [id,t] of [...timers])if(t.at<=now){timers.delete(id);t.callback();}
 await tick();assert.equal(delivered.at(-1)[1],'BH|timeout');const count=delivered.length;
 resolveManifest();await tick();assert.equal(delivered.length,count);
 // Completing an old encounter must not replay packets into a replacement encounter.
 c.assetState.spritesReady=false;c.app.battleState={participants:[{graphic:100288}]};
 c.deferBattleResourcePacket('B','BH|old');c.app.battleState={participants:[]};resolveManifest();await tick();assert.equal(delivered.length,count);
 console.log('battle resources: actual SPR cold/hot frames, ordered packet gate, cache reuse, timeout and encounter isolation passed');
})().catch(error=>{console.error(error);process.exitCode=1;});
