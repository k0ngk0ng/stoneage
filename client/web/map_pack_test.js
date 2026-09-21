"use strict";
const assert=require("node:assert/strict"),test=require("node:test"),fs=require("node:fs"),vm=require("node:vm"),{webcrypto}=require("node:crypto");
const pack=require("./runtimeassets/map-pack.js");
function harness(){
  const stores=new Map(),requests=new Map();let seq=0;
  const scope={location:{href:"https://cdn.example/game/web/test/resource-worker.js"},crypto:webcrypto,Blob,Response,TextDecoder,Uint16Array,DataView,URL,Map,Set,ArrayBuffer,console,
    caches:{async open(name){if(!stores.has(name))stores.set(name,new Map());const rows=stores.get(name);return {async match(key){return rows.get(key)?.clone();},async put(key,value){rows.set(key,value.clone());}};}},
    fetch(){throw new Error("import must not download resources");},
    postMessage(message){const task=requests.get(message.id);if(message.progress){task.progress?.(message.progress,message.id);return;}requests.delete(message.id);message.error?task.reject(new Error(message.error)):task.resolve(message.result);},
  };
  scope.self=scope;scope.importScripts=()=>vm.runInContext(fs.readFileSync(__dirname+"/runtimeassets/map-pack.js","utf8"),context);
  const context=vm.createContext(scope);vm.runInContext(fs.readFileSync(__dirname+"/runtimeassets/resource-worker.js","utf8"),context);
  return {stores,cancel:id=>scope.onmessage({data:{id,type:"cancel"}}),run(type,args,progress){const id=++seq;return new Promise((resolve,reject)=>{requests.set(id,{resolve,reject,progress});scope.onmessage({data:{id,type,args}});});}};
}
async function fixture(){
  const buffers=[new Uint8Array([1,2,3]),new Uint8Array([4,5,6,7])];let offset=0;
  const entries=[];
  for(let i=0;i<buffers.length;i++){const size=buffers[i].length;entries.push({path:`assets/bitmaps/bitmap_${i}.png`,size,offset,sha256:await pack.sha256(buffers[i])});offset+=size;}
  const header={format:1,revision:"test-0001",bytes:offset,floors:[1000],entries};
  const json=new TextEncoder().encode(JSON.stringify(header)),prefix=new Uint8Array(12);prefix.set(new TextEncoder().encode("SAMAP001"));new DataView(prefix.buffer).setUint32(8,json.length,true);
  return {header,file:new Blob([prefix,json,...buffers])};
}
test("pack rejects version drift, truncation, duplicate paths and traversal before writes",async()=>{
  const {file,header}=await fixture();assert.equal((await pack.read(file,"test-0001")).header.entries.length,2);
  await assert.rejects(pack.read(file,"test-0002"),/版本/);
  await assert.rejects(pack.read(file.slice(0,-1),"test-0001"),/不完整/);
  for(const path of ["assets/bitmaps/../x.png","https://evil.example/x.png","assets/bitmaps/bitmap_1.png"]){const changed=structuredClone(header);changed.entries[0].path=path;assert.throws(()=>pack.validate(changed,7,"test-0001"),/路径/);}
});
test("cancel/resume survives worker replacement and verifies existing bytes without network",async()=>{
  const h=harness(),{file}=await fixture(),args={file,revision:"test-0001",roots:{assets:"https://cdn.example/game/assets/",maps:"https://cdn.example/game/maps/"}};
  const first=await h.run("import",args,(progress,id)=>{if(progress.completed===1)h.cancel(id);});assert.equal(first.cancelled,true);assert.equal(first.completed,1);
  const resumed=harness();for(const [key,value] of h.stores)resumed.stores.set(key,value);
  const result=await resumed.run("import",args);assert.equal(result.completed,2);assert.equal(result.reused,1);
  const rows=resumed.stores.get("stoneage-static-v1-test-0001");assert(rows.has("https://cdn.example/game/assets/bitmaps/bitmap_0.png"));
  rows.set("https://cdn.example/game/assets/bitmaps/bitmap_0.png",new Response("bad"));
  assert.equal((await resumed.run("import",args)).reused,1);
  const bytes=new Uint8Array(await file.arrayBuffer());bytes[bytes.length-1]^=1;
  await assert.rejects(harness().run("import",{...args,file:new Blob([bytes])}),/校验失败/);
});
test("DAT worker preserves native layers and legacy exploration flags",async()=>{
  const h=harness(),buffer=new ArrayBuffer(12),view=new DataView(buffer);view.setInt32(0,2,true);view.setInt32(4,1,true);view.setUint16(8,26001,true);
  const result=await h.run("dat",{buffer,floor:1000,source:"1000.MAP"});assert.deepEqual([...result.tile],[26001,0]);assert.deepEqual([...result.event],[0x4000,0]);
  await assert.rejects(h.run("dat",{buffer:new ArrayBuffer(7)}),/完整/);
});
test("large indexes return bounded batches with exact nested content",async()=>{
  const h=harness(),input={format:1,sprites:{}};
  for(let i=0;i<80;i++)input.sprites[i]={frames:Array.from({length:1000},(_,n)=>({image:`bitmap_${n}.png`,x:n,y:-n}))};
  const {token}=await h.run("index-open",{buffer:new TextEncoder().encode(JSON.stringify(input)).buffer});let result={},batches=0;
  for(;;){const batch=await h.run("index-next",{token});batches++;assert(JSON.stringify(batch).length<150000);
    for(const {path,value} of batch.pieces){if(!path.length){result=value;continue;}let parent=result;for(const key of path.slice(0,-1))parent=parent[key];parent[path.at(-1)]=value;}
    if(batch.done)break;
  }
  assert(batches>20);assert.equal(JSON.stringify(result),JSON.stringify(input));
});
test("image downloads stay bounded and duplicate completion cannot release twice",()=>{
  const scope={URL,location:{href:"https://game.example/"},document:{currentScript:{src:"https://cdn.example/game/web/test/resource-client.js"}},window:{}};vm.createContext(scope);vm.runInContext(fs.readFileSync(__dirname+"/runtimeassets/resource-client.js","utf8"),scope);
  const releases=[];let active=0,peak=0,started=0;
  for(let i=0;i<40;i++)scope.window.StoneAgeResources.enqueueImage(done=>{active++;started++;peak=Math.max(peak,active);releases.push(()=>{active--;done();done();});});
  assert.equal(started,6);while(releases.length)releases.shift()();
  assert.equal(started,40);assert.equal(active,0);assert.equal(peak,6);
});
