"use strict";
importScripts(new URL("map-pack.js",self.STONEAGE_RUNTIME_ROOT||self.location.href).href);
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
  if(importing!==null)throw new Error("已有地图包正在导入");
  importing=id;
  try{
    const pack=await StoneAgeMapPack.read(args.file,args.revision);
    const cache=await caches.open(`stoneage-static-v1-${args.revision}`);
    let bytes=0,completed=0,reused=0;
    postMessage({id,progress:{bytes,total:pack.header.bytes,completed,count:pack.header.entries.length,reused}});
    for(const entry of pack.header.entries){
      if(cancelled.has(id))return {cancelled:true,completed,reused};
      const url=StoneAgeMapPack.entryURL(entry.path,args.roots),existing=await cache.match(url);
      // Validate existing bytes too: normal network responses do not carry a
      // digest. A completed entry doubles as the durable resume checkpoint.
      if(existing?.ok&&await StoneAgeMapPack.sha256(await existing.arrayBuffer())===entry.sha256){reused++;}
      else{
        const data=await args.file.slice(pack.start+entry.offset,pack.start+entry.offset+entry.size).arrayBuffer();
        if(await StoneAgeMapPack.sha256(data)!==entry.sha256)throw new Error(`地图包校验失败：${entry.path}`);
        if(cancelled.has(id))return {cancelled:true,completed,reused};
        await cache.put(url,new Response(data,{headers:{"Content-Type":entry.path.endsWith(".png")?"image/png":"application/octet-stream","Content-Length":String(entry.size)}}));
      }
      bytes+=entry.size;completed++;
      postMessage({id,progress:{bytes,total:pack.header.bytes,completed,count:pack.header.entries.length,reused}});
    }
    return {completed,reused,bytes};
  }finally{cancelled.delete(id);importing=null;}
}
self.onmessage=async event=>{
  const {id,type,args}=event.data||{};
  if(type==="cancel"){if(importing===id)cancelled.add(id);return;}
  try{
    if(type==="inspect"){
      const {header}=await StoneAgeMapPack.read(args.file,args.revision);
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
