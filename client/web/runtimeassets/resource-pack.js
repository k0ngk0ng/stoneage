/* Standard ZIP (STORE) resource packs produced by build-resource-pack.py.
   Read one entry at a time; ZIP64 entry counts support the full sprite tree. */
(function(root){
  "use strict";
  const MANIFEST="stoneage-resources.json",MAX_FILE=256*1024*1024,MAX_HEADER=128*1024*1024;
  const fail=message=>{throw new Error(message);};
  const safePath=path=>typeof path==="string"&&/^(assets|maps|audio)\/(?:[A-Za-z0-9_-][A-Za-z0-9_.-]*\/)*[A-Za-z0-9_-][A-Za-z0-9_.-]*$/.test(path)&&!path.split("/").some(p=>p==="."||p==="..");
  async function slice(file,start,length){
    if(!Number.isSafeInteger(start)||!Number.isSafeInteger(length)||start<0||length<0||start+length>file.size)fail("资源包不完整");
    const data=await file.slice(start,start+length).arrayBuffer();if(data.byteLength!==length)fail("资源包不完整");return data;
  }
  async function local(file,offset,name,size){
    const data=await slice(file,offset,30),v=new DataView(data);
    if(v.getUint32(0,true)!==0x04034b50||v.getUint16(6,true)&~0x800||v.getUint16(8,true)!==0)fail("请使用百科提供的游戏资源 ZIP 包");
    const n=v.getUint16(26,true),extra=v.getUint16(28,true),bytes=v.getUint32(22,true);
    if(extra!==0||bytes>MAX_FILE||v.getUint32(18,true)!==bytes||(size!==undefined&&bytes!==size)||new TextDecoder().decode(await slice(file,offset+30,n))!==name)fail("资源包文件信息无效");
    return {start:offset+30+n,size:bytes};
  }
  async function directory(file){
    // Our ZIP has no archive comment. Locate the bounded final directory,
    // accepting ZIP64 counts while rejecting split/encrypted archives.
    const end=new DataView(await slice(file,file.size-22,22));
    if(end.getUint32(0,true)!==0x06054b50||end.getUint16(4,true)||end.getUint16(6,true)||end.getUint16(20,true))fail("资源 ZIP 目录无效");
    let count=end.getUint16(10,true),size=end.getUint32(12,true),offset=end.getUint32(16,true),tail=file.size-22;
    if(count===65535||size===0xffffffff||offset===0xffffffff){
      const locator=new DataView(await slice(file,tail-20,20));
      if(locator.getUint32(0,true)!==0x07064b50||locator.getUint32(4,true)!==0||locator.getUint32(16,true)!==1)fail("资源 ZIP64 目录无效");
      const position=Number(locator.getBigUint64(8,true));
      const z=new DataView(await slice(file,position,56));
      if(z.getUint32(0,true)!==0x06064b50||Number(z.getBigUint64(4,true))!==44||z.getUint32(16,true)||z.getUint32(20,true)||position+56!==tail-20||z.getBigUint64(24,true)!==z.getBigUint64(32,true))fail("资源 ZIP64 目录无效");
      count=Number(z.getBigUint64(32,true));size=Number(z.getBigUint64(40,true));offset=Number(z.getBigUint64(48,true));tail=position;
    }else if(end.getUint16(8,true)!==count)fail("资源 ZIP 目录无效");
    if(!Number.isSafeInteger(offset)||!Number.isSafeInteger(size)||offset+size!==tail||count<2||count>500001)fail("资源 ZIP 目录无效");
    return {offset,count};
  }
  async function read(file,revision){
    const info=await local(file,0,MANIFEST);
    if(info.size>MAX_HEADER)fail("资源包清单过大");
    const header=JSON.parse(new TextDecoder().decode(await slice(file,info.start,info.size)));
    if(header.format!==2||header.revision!==revision)fail("资源包版本不匹配，请到百科下载当前版本");
    if(!Array.isArray(header.entries)||!header.entries.length||header.entries.length>500000)fail("资源包清单无效");
    let offset=0,bytes=0;const paths=new Set();
    for(const e of header.entries){
      if(!safePath(e.path)||paths.has(e.path))fail("资源包包含非法或重复路径");
      if(!Number.isSafeInteger(e.size)||e.size<0||e.size>MAX_FILE||e.offset!==offset||!/^[a-f0-9]{64}$/.test(e.sha256))fail("资源包文件信息无效");
      paths.add(e.path);offset+=30+e.path.length+e.size;bytes+=e.size;
    }
    const start=info.start+info.size,dir=await directory(file);
    if(header.bytes!==bytes||start+offset!==dir.offset||dir.count!==header.entries.length+1)fail("资源包不完整或包含多余文件");
    return {header,start,async data(entry){const record=await local(file,start+entry.offset,entry.path,entry.size);return slice(file,record.start,entry.size);}};
  }
  function entryURL(path,roots){
    if(!safePath(path))fail("资源路径无效");
    const tree=path.split("/")[0],base=new URL(roots[tree]);
    if(!/^https?:$/.test(base.protocol)||!base.pathname.endsWith("/"))fail("资源地址无效");
    return new URL(path.slice(tree.length+1),base).href;
  }
  function contentType(path){
    const ext=path.split('.').pop().toLowerCase();return ({png:"image/png",json:"application/json",wav:"audio/wav",mp3:"audio/mpeg",ogg:"audio/ogg",webp:"image/webp",jpg:"image/jpeg"})[ext]||"application/octet-stream";
  }
  root.StoneAgeResourcePack={read,entryURL,contentType};
  if(typeof module!=="undefined")module.exports=root.StoneAgeResourcePack;
})(typeof self!=="undefined"?self:globalThis);
