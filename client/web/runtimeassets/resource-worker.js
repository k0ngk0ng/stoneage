"use strict";
importScripts(...["map-pack.js","resource-pack.js"].map(name=>new URL(name,self.STONEAGE_RUNTIME_ROOT||self.location.href).href));
async function readPack(file,revision){
  const magic=new TextDecoder().decode(await file.slice(0,8).arrayBuffer());
  if(magic==="SAMAP001"){const pack=await StoneAgeMapPack.read(file,revision);pack.data=entry=>file.slice(pack.start+entry.offset,pack.start+entry.offset+entry.size).arrayBuffer();return pack;}
  return StoneAgeResourcePack.read(file,revision);
}
let importing=null;
const cancelled=new Set();
const indexes=new Map();
function* indexPieces(value,path=[]){
  if(path.length){
    const size=JSON.stringify(value).length;
    if(size<=65536){yield {path,value,size};return;}
  }
  if(value===null||typeof value!=="object"){yield {path,value,size:16};return;}
  yield {path,value:Array.isArray(value)?[]:{},size:16};
  for(const key of Object.keys(value)){
    if(key==="__proto__"||key==="constructor"||key==="prototype")throw new Error("资源清单键无效");
    yield* indexPieces(value[key],path.concat(key));
  }
}
function decodeDAT(buffer,floor,source){
  const view=new DataView(buffer);if(view.byteLength<8)throw new Error("地图数据不完整");
  const width=view.getInt32(0,true),height=view.getInt32(4,true),count=width*height;
  if(width<=0||height<=0||count>4000000)throw new Error("地图尺寸无效");
  const layers=Math.floor((view.byteLength-8)/(count*2));if(layers<1)throw new Error("地图图层不完整");
  const read=layer=>{const data=new Uint16Array(count);if(layer<layers)for(let i=0;i<count;i++)data[i]=view.getUint16(8+(layer*count+i)*2,true);return data;};
  const tile=read(0),parts=read(1),event=read(2);
  if(layers<3)for(let i=0;i<count;i++)if(tile[i]||parts[i])event[i]=0x4000;
  return {floor,width,height,tile,parts,event,source};
}
async function importPack(id,args){
  if(importing!==null)throw new Error("已有资源包正在导入");
  importing=id;
  try{
    const pack=await readPack(args.file,args.revision);
    const cache=await caches.open(`stoneage-static-v1-${args.revision}`);
    const oldCaches=await Promise.all((await caches.keys()).filter(name=>name.startsWith("stoneage-static-v1-")&&name!==`stoneage-static-v1-${args.revision}`).map(name=>caches.open(name)));
    let bytes=0,completed=0,reused=0,lastProgress=0;
    postMessage({id,progress:{bytes,total:pack.header.bytes,completed,count:pack.header.entries.length,reused}});
    for(const entry of pack.header.entries){
      if(cancelled.has(id))return {cancelled:true,completed,reused};
      const url=StoneAgeResourcePack.entryURL(entry.path,args.roots);
      let data=null,hitCurrent=false;
      for(const candidate of [cache,...oldCaches]){
        const existing=await candidate.match(url);
        if(existing?.ok){
          const buffer=await existing.arrayBuffer();
          if(buffer.byteLength===entry.size&&await StoneAgeMapPack.sha256(buffer)===entry.sha256){data=buffer;hitCurrent=candidate===cache;reused++;break;}
        }
      }
      if(!data){
        data=await pack.data(entry);
        if(await StoneAgeMapPack.sha256(data)!==entry.sha256)throw new Error(`资源包校验失败：${entry.path}`);
      }
      if(cancelled.has(id))return {cancelled:true,completed,reused};
      if(!hitCurrent)await cache.put(url,new Response(data,{headers:{"Content-Type":StoneAgeResourcePack.contentType(entry.path),"Content-Length":String(entry.size),"X-Stoneage-Resource-Revision":args.revision}}));
      bytes+=entry.size;completed++;
      if(Date.now()-lastProgress>=100||completed===pack.header.entries.length){
        postMessage({id,progress:{bytes,total:pack.header.bytes,completed,count:pack.header.entries.length,reused}});lastProgress=Date.now();
      }
    }
    return {completed,reused,bytes};
  }finally{cancelled.delete(id);importing=null;}
}
self.onmessage=async event=>{
  const {id,type,args}=event.data||{};
  if(type==="cancel"){if(importing===id)cancelled.add(id);return;}
  try{
    if(type==="inspect"){
      const {header}=await readPack(args.file,args.revision);
      postMessage({id,result:{bytes:header.bytes,floors:header.floors?.length||0,count:header.entries.length}});
    }else if(type==="dat"){
      const data=decodeDAT(args.buffer,args.floor,args.source);
      postMessage({id,result:data},[data.tile.buffer,data.parts.buffer,data.event.buffer]);
    }else if(type==="palette"){
      const {bitmap,base,target}=args,canvas=new OffscreenCanvas(bitmap.width,bitmap.height),context=canvas.getContext("2d",{willReadFrequently:true});
      try{
        context.drawImage(bitmap,0,0);
        const pixels=context.getImageData(0,0,canvas.width,canvas.height),lookup=new Map();
        for(let i=0;i<256;i++)lookup.set((base[i][0]<<16)|(base[i][1]<<8)|base[i][2],target[i]);
        for(let i=0;i<pixels.data.length;i+=4){if(!pixels.data[i+3])continue;const c=lookup.get((pixels.data[i]<<16)|(pixels.data[i+1]<<8)|pixels.data[i+2]);if(c){pixels.data[i]=c[0];pixels.data[i+1]=c[1];pixels.data[i+2]=c[2];}}
        context.putImageData(pixels,0,0);
        const result=canvas.transferToImageBitmap();postMessage({id,result},[result]);
      }finally{bitmap.close();}
    }else if(type==="index-open"){
      const payload=JSON.parse(new TextDecoder().decode(args.buffer));
      indexes.set(id,indexPieces(payload));postMessage({id,result:{token:id}});
    }else if(type==="index-next"){
      const iterator=indexes.get(args.token);if(!iterator)throw new Error("资源清单会话过期");
      const pieces=[];let bytes=0,done=false;
      while(bytes<65536&&pieces.length<128){const next=iterator.next();if(next.done){done=true;indexes.delete(args.token);break;}pieces.push(next.value);bytes+=next.value.size;}
      postMessage({id,result:{pieces,done}});
    }else if(type==="index-close"){
      indexes.delete(args.token);postMessage({id,result:true});
    }else if(type==="import")postMessage({id,result:await importPack(id,args)});
    else throw new Error("未知资源任务");
  }catch(error){postMessage({id,error:error?.message||String(error)});}
};
