/* Shared by the resource worker and Node contract tests. */
(function(root){
  "use strict";
  const MAX_HEADER=16*1024*1024,MAX_ENTRY=32*1024*1024;
  function validate(header,payloadBytes,revision){
    if(header?.format!==1||header.revision!==revision)throw new Error("地图包版本不匹配，请下载当前版本地图包");
    if(!Array.isArray(header.entries)||!header.entries.length||header.entries.length>100000)throw new Error("地图包目录无效");
    let offset=0;const paths=new Set();
    for(const entry of header.entries){
      if(!/^(?:assets\/bitmaps\/[A-Za-z0-9_-]+\.png|maps\/[0-9]+\.(?:DAT|dat|MAP|map))$/.test(entry.path)||paths.has(entry.path))throw new Error("地图包包含非法或重复路径");
      if(!Number.isSafeInteger(entry.size)||entry.size<=0||entry.size>MAX_ENTRY||entry.offset!==offset||!/^[a-f0-9]{64}$/.test(entry.sha256))throw new Error("地图包文件信息无效");
      paths.add(entry.path);offset+=entry.size;
    }
    if(!Number.isSafeInteger(offset)||offset!==payloadBytes||header.bytes!==offset)throw new Error("地图包不完整或包含多余数据");
    return header;
  }
  async function read(file,revision){
    const prefix=await file.slice(0,12).arrayBuffer();
    if(prefix.byteLength!==12||new TextDecoder().decode(prefix.slice(0,8))!=="SAMAP001")throw new Error("不是有效的 StoneAge 地图包");
    const size=new DataView(prefix).getUint32(8,true);
    if(!size||size>MAX_HEADER||size+12>file.size)throw new Error("地图包目录损坏");
    const header=JSON.parse(await file.slice(12,12+size).text());
    return {header:validate(header,file.size-12-size,revision),start:12+size};
  }
  async function sha256(bytes){return Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256",bytes)),v=>v.toString(16).padStart(2,"0")).join("");}
  function entryURL(path,roots){
    const tree=path.startsWith("maps/")?"maps":"assets",base=new URL(roots[tree]);
    if(!/^https?:$/.test(base.protocol)||!base.pathname.endsWith("/"))throw new Error("资源地址无效");
    return new URL(path.slice(tree.length+1),base).href;
  }
  root.StoneAgeMapPack={read,validate,sha256,entryURL};
  if(typeof module!=="undefined")module.exports=root.StoneAgeMapPack;
})(typeof self!=="undefined"?self:globalThis);
