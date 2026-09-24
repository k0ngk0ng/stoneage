(function(){
  "use strict";
  const runtimeRoot=new URL(".",document.currentScript?.src||location.href).href;
  let worker=null,nextID=1;
  const pending=new Map();
  const imageQueue=[];let activeImages=0;
  function pumpImages(){
    while(activeImages<6&&imageQueue.length){
      const start=imageQueue.shift();activeImages++;let finished=false;
      const done=()=>{if(finished)return;finished=true;activeImages--;pumpImages();};
      try{start(done);}catch(_){done();}
    }
  }
  function enqueueImage(start){imageQueue.push(start);pumpImages();}
  function run(type,args,onProgress){
    if(!worker){
      const source=`self.STONEAGE_RUNTIME_ROOT=${JSON.stringify(runtimeRoot)};importScripts(${JSON.stringify(new URL("resource-worker.js",runtimeRoot).href)});`;
      const bootstrap=URL.createObjectURL(new Blob([source],{type:"application/javascript"}));
      try{worker=new Worker(bootstrap);}finally{URL.revokeObjectURL(bootstrap);}
      worker.onmessage=({data})=>{
        const task=pending.get(data.id);if(!task)return;
        if(data.progress){task.onProgress?.(data.progress);return;}
        pending.delete(data.id);data.error?task.reject(new Error(data.error)):task.resolve(data.result);
      };
      worker.onerror=()=>{
        worker.terminate();worker=null;
        for(const task of pending.values())task.reject(new Error("资源处理器异常，请重试"));
        pending.clear();
      };
    }
    const id=nextID++;
    const promise=new Promise((resolve,reject)=>{
      pending.set(id,{resolve,reject,onProgress});
      try{worker.postMessage({id,type,args},args.buffer?[args.buffer]:args.bitmap?[args.bitmap]:[]);}
      catch(error){pending.delete(id);reject(error);}
    });
    promise.cancel=()=>worker?.postMessage({id,type:"cancel"});
    return promise;
  }
  const mb=bytes=>`${(bytes/1048576).toFixed(1)} MB`;
  async function parseIndex(buffer){
    const {token}=await run("index-open",{buffer});let result={};
    try{
      for(;;){
        const batch=await run("index-next",{token});
        for(const {path,value} of batch.pieces){
          if(!path.length){result=value;continue;}
          let parent=result;
          for(const key of path.slice(0,-1))parent=parent[key];
          Object.defineProperty(parent,path.at(-1),{value,writable:true,enumerable:true,configurable:true});
        }
        if(batch.done)return result;
        await new Promise(resolve=>setTimeout(resolve,0));
      }
    }finally{await run("index-close",{token}).catch(()=>{});}
  }
  let dialog=null;
  async function openPack(config){
    if(dialog){dialog.showModal();return;}
    const node=document.createElement("dialog");dialog=node;
    node.style.cssText="width:min(440px,85vw);max-height:85vh;overflow:auto;padding:20px;background:#fff6de;color:#352514;border:2px solid #765938;border-radius:8px;font:15px/1.6 sans-serif";
    node.innerHTML='<h2 style="margin:0 0 10px">导入资源</h2><p>提前保存地图、人物、宠物、骑乘、界面、音乐和音效，减少游玩时下载等待。取消后重新选择同一个包可以继续。</p><input type="file" accept=".zip,.samap" aria-label="选择资源包"><p data-size></p><progress style="width:100%" max="1" value="0"></progress><p role="status" aria-live="polite">请选择与当前资源版本一致的资源包。</p><div style="display:flex;gap:12px"><button data-start disabled>开始导入</button><button data-cancel disabled>取消导入</button><button data-close>关闭</button></div>';
    document.body.append(node);node.showModal();
    const downloads=document.createElement("p"),link=document.createElement("a");
    link.href="/wiki/resources";link.target="_blank";link.rel="noopener noreferrer";link.textContent="到百科下载游戏资源 ZIP";
    downloads.append(link);node.querySelector("input").before(downloads);
    const input=node.querySelector("input"),status=node.querySelector('[role="status"]'),size=node.querySelector("[data-size]"),start=node.querySelector("[data-start]"),cancel=node.querySelector("[data-cancel]"),close=node.querySelector("[data-close]"),progress=node.querySelector("progress");
    let task=null,selected=null,version=null,selection=0;
    input.onchange=async()=>{
      const generation=++selection;start.disabled=true;selected=null;
      const file=input.files?.[0];if(!file)return;
      try{
        const ready=await config();version=ready;
        if(!ready.controlled)throw new Error("请刷新页面以启用本地资源缓存后再导入");
        const pack=await run("inspect",{file,revision:ready.revision});
        const estimate=await navigator.storage?.estimate?.();
        if(generation!==selection)return;
        size.textContent=`文件 ${pack.count} 个 · ${mb(pack.bytes)}${estimate?.quota?` · 可用空间约 ${mb(Math.max(0,estimate.quota-estimate.usage))}`:""}`;
        selected=file;start.disabled=false;status.textContent="已识别资源包。已导入的文件会校验后跳过。";
      }catch(error){if(generation===selection)status.textContent=error.message;}
    };
    start.onclick=async()=>{
      if(!selected||task)return;
      start.disabled=true;input.disabled=true;cancel.disabled=false;close.disabled=true;
      try{
        await navigator.storage?.persist?.();
        let last=0;
        task=run("import",{file:selected,revision:version.revision,roots:version.roots},p=>{
          const now=performance.now();if(now-last<100&&p.completed<p.count)return;last=now;
          progress.value=p.bytes/p.total;status.textContent=`${p.completed}/${p.count} · ${mb(p.bytes)}/${mb(p.total)} · 复用 ${p.reused} 个`;
        });
        const result=await task;
        status.textContent=result.cancelled?`已取消，保留 ${result.completed} 个已完成文件，可重新选择资源包继续。`:`导入完成，共 ${result.completed} 个文件（复用 ${result.reused} 个）。游戏资源已可从本地读取。`;
      }catch(error){status.textContent=`导入未完成：${error.message}。已完成文件已保留，可重试。`;}
      finally{task=null;input.disabled=false;start.disabled=false;cancel.disabled=true;close.disabled=false;}
    };
    cancel.onclick=()=>{task?.cancel();cancel.disabled=true;};
    close.onclick=()=>{node.close();node.remove();dialog=null;};
    node.addEventListener("cancel",event=>{if(task)event.preventDefault();else{node.remove();dialog=null;}});
  }
  window.StoneAgeResources={run,openPack,parseIndex,enqueueImage};
})();
