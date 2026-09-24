"use strict";
(async()=>{
  const area=document.getElementById("resource-download");
  try{
    const configured=document.getElementById("media-config").getAttribute("content");
    const root=new URL(configured?configured.replace(/\/$/,"")+"/":"/",location.href);
    const get=async path=>{const response=await fetch(new URL(path,root),{cache:"no-cache"});if(!response.ok)throw new Error("当前资源包尚未发布，请稍后再试。");return response.json();};
    const version=await get("_client-version.json");
    if(!/^[a-f0-9]{64}$/.test(version.revision))throw new Error("暂时无法确认资源版本，请稍后重试。");
    const catalog=await get(`packs/${version.revision}/resource-packs.json`);
    if(catalog.revision!==version.revision||!Array.isArray(catalog.packages)||!catalog.packages.length)throw new Error("当前资源包正在更新，请稍后再试。");
    const nodes=[];
    for(const pack of catalog.packages){
      if(!new RegExp(`^packs/${version.revision}/[A-Za-z0-9_-]+\\.zip$`).test(pack.path)||!Number.isSafeInteger(pack.bytes)||pack.bytes<=0)throw new Error("资源包下载信息无效。");
      const card=document.createElement("article"),link=document.createElement("a"),info=document.createElement("p");
      link.href=new URL(pack.path,root).href;link.textContent="下载完整游戏资源 ZIP";link.className="resource-download-link";
      info.textContent=`下载大小 ${(pack.bytes/1048576).toFixed(1)} MB · 导入后约 ${(pack.unpacked_bytes/1048576).toFixed(1)} MB`;
      card.append(link,info);nodes.push(card);
    }
    area.replaceChildren(...nodes);
  }catch(error){area.textContent=error.message||"资源包查询失败，请刷新重试。";}
})();
