/* Bounded field-resource work. No game commands or URL rewriting live here. */
(function(root){
  'use strict';
  class MapLayers {
    constructor(hooks){this.h=hooks;this.cells=new Map();this.chunks=new Map();this.bytes=0;this.current=null;this.timer=0;this.serial=0;this.imageIDs=new WeakMap();}
    imageID(image){if(!this.imageIDs.has(image))this.imageIDs.set(image,++this.serial);return this.imageIDs.get(image);}
    ensure(map,key,palette,manifestReady){
      key=`${key}|palette:${palette}|manifest:${manifestReady}`;
      if(this.current?.key===key)return this.current;
      const cache={key,incremental:true,map,manifestReady,ready:false,rasterComplete:false,rasterScheduled:false,rasterFailed:false,preparing:true,dirty:true,
        parts:[],pendingParts:new Map(),required:new Set(),pending:new Set(),failed:new Set(),missing:new Set(),unavailable:new Set(),retryCount:new Map(),
        objectWindow:{x1:map.x1,y1:map.y1,width:map.width,height:map.height,objects:map.objects},
        records:[],waiting:new Map(),queue:[],groundChunks:[],stats:{builtCells:0,reusedCells:0,builtChunks:0,reusedChunks:0,maxTaskMs:0}};
      this.current=cache;
      // Prepare the visible/forward area first; order of preparation does not
      // change the native diagonal order used to rasterize or draw PARTS.
      const focus=this.h.focus?.()||[map.x1+map.width/2,map.y1+map.height/2];
      for(let y=0;y<map.height;y++)for(let x=0;x<map.width;x++)cache.queue.push({x:x+map.x1,y:y+map.y1,index:y*map.width+x,palette});
      cache.queue.sort((a,b)=>Math.max(Math.abs(a.x-focus[0]),Math.abs(a.y-focus[1]))-Math.max(Math.abs(b.x-focus[0]),Math.abs(b.y-focus[1])));
      this.schedule(cache);return cache;
    }
    schedule(cache){
      if(this.timer)return;
      this.timer=this.h.enqueue(()=>{this.timer=0;this.pump(this.current);});
    }
    prepare(cache,cell){
      const map=cache.map,values=[map.tiles[cell.index]||0,map.objects?.[cell.index]||0];
      for(let layer=0;layer<2;layer++){
        const value=Number(values[layer]);if(value<=99)continue;
        const key=`${map.floor}:${cell.palette}:${cell.x}:${cell.y}:${layer}:${value}`;
        const previous=this.cells.get(key);
        if(previous&&this.h.ready(previous.image)&&this.h.validImage(previous)){
          this.cells.delete(key);this.cells.set(key,previous);
          cache.records.push(previous);cache.required.add(previous.info.file);cache.stats.reusedCells++;continue;
        }
        const info=this.h.resolve(value);if(!info?.file)continue;
        cache.required.add(info.file);
        const image=this.h.image(info.file),anchor=this.h.pixel(cell.x,cell.y);if(!anchor)continue;
        const record={key,x:cell.x,y:cell.y,value,layer,info,image,anchor};
        if(!this.h.ready(image)){
          const first=!cache.pending.has(info.file);
          cache.pending.add(info.file);
          if(!cache.waiting.has(info.file))cache.waiting.set(info.file,[]);
          cache.waiting.get(info.file).push(record);
          if(first&&image?._assetFailed)this.assetReady(cache,info.file,true);
          continue;
        }
        this.remember(cache,record);
      }
    }
    remember(cache,record){
      this.cells.delete(record.key);this.cells.set(record.key,record);
      while(this.cells.size>12000)this.cells.delete(this.cells.keys().next().value);
      cache.records.push(record);cache.stats.builtCells++;
    }
    assetReady(cache,file,failed){
      if(this.current!==cache||!cache.pending.has(file))return false;
      if(failed){
        const attempts=cache.retryCount.get(file)||0;
        if(attempts<2){cache.retryCount.set(file,attempts+1);this.h.retry(file,attempts+1,cache);}
        else {cache.pending.delete(file);cache.failed.add(file);this.h.changed(cache);}
        return true;
      }
      // Resolve ready cells in the same bounded queue, not in image.onload.
      cache.queue.push({file});this.schedule(cache);return true;
    }
    prepareChunk(cache,cells){
      const first=cells[0],id=`${cache.map.floor}:${first.y-first.x}:${Math.floor(first.x/8)}`;
      const signature=cells.map(c=>`${c.key}:${this.imageID(c.image)}`).join('|');
      const old=this.chunks.get(id);
      if(old?.signature===signature){this.chunks.delete(id);this.chunks.set(id,old);cache.stats.reusedChunks++;return old;}
      let left=Infinity,top=Infinity,right=-Infinity,bottom=-Infinity;
      for(const c of cells){const x=c.anchor[0]+Number(c.info.xoffset??c.info.x??0),y=c.anchor[1]+Number(c.info.yoffset??c.info.y??0);left=Math.min(left,x);top=Math.min(top,y);right=Math.max(right,x+c.image.naturalWidth);bottom=Math.max(bottom,y+c.image.naturalHeight);}
      const canvas=this.h.canvas(Math.max(1,Math.ceil(right-left)),Math.max(1,Math.ceil(bottom-top))),ctx=canvas.getContext('2d');
      if(!ctx)throw new Error('地图画布不可用');ctx.imageSmoothingEnabled=false;
      for(const c of cells)this.h.draw(ctx,c.image,c.info,[c.anchor[0]-left,c.anchor[1]-top]);
      const chunk={signature,canvas,minX:left,minY:top,bytes:canvas.width*canvas.height*4};
      if(old)this.bytes-=old.bytes;this.chunks.delete(id);this.chunks.set(id,chunk);this.bytes+=chunk.bytes;
      while(this.chunks.size>512||this.bytes>64*1024*1024){const key=this.chunks.keys().next().value;this.bytes-=this.chunks.get(key).bytes;this.chunks.delete(key);}
      cache.stats.builtChunks++;return chunk;
    }
    pump(cache){
      if(!cache||cache!==this.current||cache.ready||cache.rasterFailed)return;
      const started=this.h.now();let count=0;
      try{
        while(cache.queue.length){
          const cell=cache.queue.shift();
          if(cell.file){
            const waiting=cache.waiting.get(cell.file)||[],image=this.h.image(cell.file);
            if(this.h.ready(image)){for(const row of waiting)cache.queue.push({record:row,image});cache.waiting.delete(cell.file);cache.pending.delete(cell.file);}
          }else if(cell.record){cell.record.image=cell.image;this.remember(cache,cell.record);}
          else this.prepare(cache,cell);
          if(++count>=48||this.h.now()-started>=4)break;
        }
        if(!cache.queue.length){
          cache.preparing=false;
          if(!cache.pending.size&&!cache.failed.size&&cache.manifestReady){
            if(!cache.groups){
              const ground=cache.records.filter(c=>c.layer===0).sort((a,b)=>(a.y-a.x)-(b.y-b.x)||a.x-b.x);
              cache.parts=cache.records.filter(c=>c.layer===1).sort((a,b)=>(b.y-b.x)-(a.y-a.x)||b.x-a.x);
              cache.groups=[];let group=null,key='';
              for(const cell of ground){const next=`${cell.y-cell.x}:${Math.floor(cell.x/8)}`;if(key!==next){key=next;group=[];cache.groups.push(group);}group.push(cell);}
              cache.rasterScheduled=true;cache.groupIndex=0;
            }
            while(cache.groupIndex<cache.groups.length&&this.h.now()-started<4){cache.groundChunks.push(this.prepareChunk(cache,cache.groups[cache.groupIndex++]));}
            if(cache.groupIndex===cache.groups.length){cache.groups=null;cache.rasterScheduled=false;cache.rasterComplete=true;cache.ready=true;cache.dirty=false;this.h.changed(cache);}
          }
        }
      }catch(error){cache.rasterFailed=true;cache.rasterScheduled=false;cache.queue.length=0;cache.failed.add('raster');this.h.error?.(error);this.h.changed(cache);}
      cache.stats.maxTaskMs=Math.max(cache.stats.maxTaskMs,this.h.now()-started);
      if(cache.queue.length||cache.rasterScheduled)this.schedule(cache);
    }
  }
  root.StoneAgeWorldResources={MapLayers};
  if(typeof module!=='undefined')module.exports=root.StoneAgeWorldResources;
})(typeof window!=='undefined'?window:globalThis);
