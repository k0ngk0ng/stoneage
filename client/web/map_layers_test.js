'use strict';
const test=require('node:test'),assert=require('node:assert/strict');
const {MapLayers}=require('./world-resources.js');
function fixture(){
 const tasks=[],images=new Map(),draws=[];let clock=0,canvasCount=0;
 const h={now:()=>clock+=0.02,enqueue:fn=>{tasks.push(fn);return tasks.length;},resolve:value=>({file:`${value}.png`,xoffset:-32,yoffset:-24}),pixel:(x,y)=>[(x+y)*32,(y-x)*24],
 image:file=>{if(!images.has(file))images.set(file,{complete:true,naturalWidth:64,naturalHeight:48,file});return images.get(file);},ready:image=>image?.complete&&image.naturalWidth>0,validImage:r=>images.get(r.info.file)===r.image,
 canvas:(width,height)=>{canvasCount++;return {width,height,getContext:()=>({})};},draw:(_,image,info,anchor)=>draws.push({file:image.file,anchor}),changed(){},retry(){}};
 const layers=new MapLayers(h);
 return {layers,images,tasks,draws,get canvases(){return canvasCount;},flush(){let count=0;while(tasks.length){assert(count++<5000,'bounded task completion');tasks.shift()();}}};
}
function map(x1=0,y1=0,w=37,h=37){return {floor:100,x1,y1,width:w,height:h,tiles:Array(w*h).fill(101),objects:Array(w*h).fill(102)};}
test('sliding window reuses overlapping cells and raster chunks; complete buffers stay immutable',()=>{
 const f=fixture(),first=f.layers.ensure(map(),'a',0,true);f.flush();assert(first.ready);const canvasList=first.groundChunks.map(c=>c.canvas);
 const second=f.layers.ensure(map(4,0),'b',0,true);assert.equal(second.ready,false);f.flush();assert(second.ready);
 assert.equal(second.stats.reusedCells,33*37*2);assert.equal(second.stats.builtCells,4*37*2);
 assert(second.stats.reusedChunks>50);assert.deepEqual(first.groundChunks.map(c=>c.canvas),canvasList);
 assert(second.groundChunks.some(c=>canvasList.includes(c.canvas)));
});
test('diagonal chunk composition preserves the original native ground and PARTS order',()=>{
 const f=fixture(),m=map(3,4,5,4);m.tiles=m.tiles.map((_,i)=>101+i);m.objects=m.objects.map((_,i)=>201+i);
 const c=f.layers.ensure(m,'ordering',0,true);f.flush();
 const native=[];let y=m.height-1,x=0;
 while(y>=0){let row=y,col=x;while(row>=0&&col>=0){native.push({value:m.tiles[row*m.width+col],x:col+m.x1,y:row+m.y1});row--;col--;}if(x<m.width-1)x++;else y--;}
 assert.deepEqual(f.draws.map(d=>+d.file.split('.')[0]),native.map(c=>c.value).reverse());
 assert.deepEqual(c.parts.map(c=>[c.x,c.y]),native.map(c=>[c.x,c.y]));
});
test('cold images publish no incomplete ground and are prepared in bounded tasks',()=>{
 const f=fixture();f.images.set('101.png',{complete:false,naturalWidth:0,naturalHeight:48,file:'101.png'});
 const c=f.layers.ensure(map(),'cold',0,true);f.flush();assert.equal(c.ready,false);assert.equal(c.pending.size,1);
 Object.assign(f.images.get('101.png'),{complete:true,naturalWidth:64});f.layers.assetReady(c,'101.png',false);assert.equal(c.ready,false);
 f.flush();assert(c.ready);assert(c.stats.maxTaskMs<6);assert.equal(c.records.length,37*37*2);
});
test('changed tiles and palette cannot reuse stale chunks; superseded work is abandoned',()=>{
 const f=fixture(),m=map(),first=f.layers.ensure(m,'first',0,true);f.flush();
 const changed=map();changed.tiles[100]=103;const second=f.layers.ensure(changed,'changed',0,true);f.flush();
 assert.equal(second.stats.builtCells,1);assert(second.stats.builtChunks>0);
 const pending=f.layers.ensure(map(8,8),'abandoned',0,true),last=f.layers.ensure(map(10,10),'last',1,true);f.flush();
 assert.equal(pending.ready,false);assert(last.ready);assert.equal(last.stats.reusedCells,0);
});
