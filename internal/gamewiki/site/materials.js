"use strict";
/* Shared by the encyclopedia and the standalone, offline ZIP preview. */
window.WikiMaterials = (() => {
  const names = {0:"攻击",1:"受伤",2:"死亡",3:"站立",4:"行走",5:"坐下",6:"挥手",7:"高兴",8:"生气",9:"伤心",10:"防御",11:"点头",12:"投掷"};
  const directions = ["下","左下","左","左上","上","右上","右","右下"];
  const node = (tag,text,cls) => { const n=document.createElement(tag); if(text!=null)n.textContent=text;if(cls)n.className=cls;return n; };
  const button = (label,fn) => {const n=node("button",label);n.type="button";n.onclick=fn;return n;};
  const json = async (url,signal) => {const r=await fetch(url,{signal});if(!r.ok)throw new Error("素材资料暂时无法读取，请重试。");return r.json();};
  let directory;
  async function reference(key,signal,url) {
    if(!directory)directory=json(url("wiki/materials/catalog.json")).catch(e=>{directory=null;throw e;});
    const {revision}=await directory;
    if(!/^materials-[a-f0-9]{16}$/.test(revision))throw new Error("素材索引无效");
    const hash=await crypto.subtle.digest("SHA-256",new TextEncoder().encode(key));
    const shard=new Uint8Array(hash)[0].toString(16).padStart(2,"0");
    const data=await json(url(`wiki/materials/${revision}/${shard}.json`),signal);
    return {data:data[key],base:`wiki/materials/${revision}/`};
  }
  function mount(entry,article,options) {
    const section=node("details",null,"materials-panel"),summary=node("summary","素材参考与下载"),body=node("div",null,"materials-body");
    section.append(summary,body);article.append(section);
    let disposeView=()=>{},controller,opened=false,disposed=false;
    async function open(){
      if(opened||disposed)return;opened=true;controller=new AbortController();
      body.replaceChildren(node("p","正在读取素材…","materials-status"));
      try {
        const found=options.reference?{data:options.reference,base:""}:await reference(entry.key,controller.signal,options.mediaURL);
        if(disposed||!section.open)return;
        if(!found.data){body.replaceChildren(node("p","该条目的素材尚未收录。"));return;}
        body.replaceChildren();
        disposeView=render(body,found.data,{...options,signal:controller.signal,loadSprite:async v=>{
          if(options.sprites)return options.sprites[String(v.graphic)];
          if(!/^sprite-\d+\.json$/.test(v.file))throw new Error("动画路径无效");
          return json(options.mediaURL(found.base+v.file),controller.signal);
        }});
      }catch(e){if(e.name!=="AbortError"&&!disposed){body.replaceChildren(node("p",e.message),button("重试",()=>{opened=false;open();}));}}
    }
    section.addEventListener("toggle",()=>{
      if(section.open)open();else{controller?.abort();disposeView();disposeView=()=>{};opened=false;body.replaceChildren();}
    });
    if(options.reference){section.open=true;open();}
    return ()=>{disposed=true;controller?.abort();disposeView();body.replaceChildren();};
  }
  function render(host,ref,options){
    let disposed=false,sequence=0,raf=0,rows=[],loaded=new Map(),sprite=null,playing=true,start=0,paused=0;
    const url=options.mediaURL;
    const status=node("p",null,"materials-status");status.setAttribute("role","status");
    if(ref.download){const link=node("a",`${ref.missing?.length ? "下载已收录素材 ZIP" : "下载完整素材包 ZIP"} · ${(ref.download.bytes/1048576).toFixed(1)} MB`,"materials-download");link.href=url(ref.download.path);host.append(link);}
    if(ref.missing?.length)host.append(node("p","尚缺："+ref.missing.join("、"),"materials-status"));
    const sharedAudio=node("div",null,"materials-audio");
    for(const sound of ref.sounds||[]){const item=node("div"),audio=node("audio");audio.controls=true;audio.preload="none";item.append(node("p",sound.name||`音效 ${sound.tone}`),audio);sharedAudio.append(item);options.ensureCache().then(()=>{if(!disposed)audio.src=url(sound.path);});}
    if(sharedAudio.children.length)host.append(sharedAudio);
    const gallery=node("div",null,"materials-gallery");
    for(const image of ref.images||[]){const figure=node("figure"),link=node("a"),img=node("img"),caption=node("figcaption",`${image.caption} · ${image.width}×${image.height}`);link.href=url(image.path);link.target="_blank";link.rel="noopener";img.alt=image.caption;img.loading="lazy";img.decoding="async";options.ensureCache().then(()=>{if(!disposed)img.src=link.href;});link.append(img);figure.append(link,caption);gallery.append(figure);}
    if(gallery.children.length)host.append(gallery);
    const controls=node("div",null,"materials-controls"),views=node("div",null,"materials-views"),audioArea=node("div",null,"materials-audio");
    const shape=node("select"),action=node("select"),direction=node("select"),clock=node("select"),scrub=node("input"),loop=node("input");
    function selectOption(s,text,value){const o=node("option",text);o.value=value;s.append(o);}
    function label(text,input){const n=node("label",text+" ");n.append(input);return n;}
    for(const v of ref.variants||[])selectOption(shape,v.name,String(v.graphic));
    selectOption(direction,"全部方向","all");directions.forEach((d,i)=>selectOption(direction,`${i} · ${d}`,String(i)));
    selectOption(clock,"战斗 / 站立 60Hz","16.6666666667");selectOption(clock,"地图移动 8ms","8");
    loop.type="checkbox";loop.checked=true;scrub.type="range";scrub.min="0";scrub.max="0";scrub.value="0";
    const pause=button("暂停",()=>{if(playing){paused=performance.now()-start;const a=rows[0]?.animation;scrub.value=a?Math.min(a.frames.length-1,Math.floor(paused/(a.frame_ms*Number(clock.value)))%a.frames.length):0;}else start=performance.now()-Number(scrub.value)*(rows[0]?.animation.frame_ms||1)*Number(clock.value);playing=!playing;pause.textContent=playing?"暂停":"播放";});
    controls.append(label("造型",shape),label("动作",action),label("方向",direction),pause,button("重播",()=>{start=performance.now();scrub.value="0";}),label("循环预览",loop),label("时钟",clock),label("逐帧",scrub));
    const stats=node("p",null,"materials-stats");
    if(ref.variants?.length){host.append(controls,status,stats,views,audioArea);const help=node("details",null,"materials-spec"),s=node("summary","制作规格与帧事件");help.append(s,node("p","透明 PNG；绘制位置 = 角色锚点 + (x+xoffset, y+yoffset)。frame_ms 保存原生 tick，不是毫秒。攻击、受伤、死亡、防御通常只播一次；这里可循环方便观察。"),node("p","声音值 1–9999 是音效编号，10000–10099 是命中事件，10100 及以上是连击事件。命中事件不是 WAV 音效，也不会自行增加技能或伤害次数。图鉴与动画图号分别配置。骑乘造型是否可用仍受人物、宠物和骑乘资格限制。"));host.append(help);}
    const imageCache=new Map(); // Bounded to the current selection, shared across all eight views.
    async function loadFrames(actions,token){
      await options.ensureCache();
      if(disposed||token!==sequence)return;
      const files=[...new Set(actions.flatMap(a=>a.frames.map(f=>f.file)))];
      const wanted=new Set(files);
      for(const file of imageCache.keys())if(!wanted.has(file))imageCache.delete(file);
      let next=0;
      await Promise.all(Array.from({length:Math.min(6,files.length)},async()=>{
        while(next<files.length&&!disposed&&token===sequence){
          const file=files[next++];
          if(!imageCache.has(file))imageCache.set(file,new Promise((resolve,reject)=>{const img=new Image();img.onload=()=>resolve(img);img.onerror=()=>{if(token===sequence)imageCache.delete(file);reject(new Error("部分动画图片未能加载，请重试。"));};const path=url("assets/"+file);if(!path){reject(new Error("动画图片路径无效"));return;}img.src=path;}));
          const img=await imageCache.get(file);if(token===sequence&&!disposed)loaded.set(file,img);
        }
      }));
    }
    async function showAction(){
      if(!sprite||disposed)return;
      const token=++sequence;cancelAnimationFrame(raf);rows=[];loaded.clear();views.replaceChildren();status.textContent="正在加载当前动作…";
      try{
        const actions=sprite.actions.filter(a=>String(a.action)===action.value&&(direction.value==="all"||String(a.direction)===direction.value));
        if(!actions.length)throw new Error("这个方向没有对应动作。");
        await loadFrames(actions,token);if(disposed||token!==sequence)return;
        const b=sprite.bounds,w=b[2]-b[0]+32,h=b[3]-b[1]+32,scale=Math.min(1,512/w,512/h);
        for(const animation of actions){const card=node("div",null,"materials-view"),canvas=node("canvas"),info=node("p"),title=node("h4",`方向 ${animation.direction} · ${directions[animation.direction]||animation.direction}`);canvas.width=Math.ceil(w*scale);canvas.height=Math.ceil(h*scale);card.append(title,canvas,info);views.append(card);rows.push({animation,canvas,info,scale,ox:16-b[0],oy:16-b[1]});}
        scrub.max=Math.max(...actions.map(a=>a.frames.length))-1;scrub.value="0";start=performance.now();status.textContent="";raf=requestAnimationFrame(draw);
      }catch(e){if(!disposed&&token===sequence){status.textContent=e.message;status.append(button("重试",showAction));}}
    }
    function draw(now){
      if(disposed)return;
      if(!document.hidden){for(const row of rows){const {animation:a,canvas,info,scale,ox,oy}=row;const duration=Math.max(1,a.frame_ms)*Number(clock.value);let index=playing?Math.floor((now-start)/duration):Number(scrub.value);index=loop.checked?index%a.frames.length:Math.min(index,a.frames.length-1);index=Math.max(0,Math.min(index,a.frames.length-1));const f=a.frames[index],img=loaded.get(f.file),ctx=canvas.getContext("2d");ctx.clearRect(0,0,canvas.width,canvas.height);ctx.imageSmoothingEnabled=false;ctx.strokeStyle="#b95131";ctx.beginPath();ctx.moveTo((ox-5)*scale,oy*scale);ctx.lineTo((ox+5)*scale,oy*scale);ctx.moveTo(ox*scale,(oy-5)*scale);ctx.lineTo(ox*scale,(oy+5)*scale);ctx.stroke();if(img)ctx.drawImage(img,(ox+f.x+f.xoffset)*scale,(oy+f.y+f.yoffset)*scale,img.width*scale,img.height*scale);info.textContent=`帧 ${index+1}/${a.frames.length} · ${a.frame_ms} tick · ${(duration).toFixed(1)}ms · 事件 ${f.sound||0}`;}}
      raf=requestAnimationFrame(draw);
    }
    async function showShape(){
      sprite=null;action.disabled=true;direction.disabled=true;
      const token=++sequence;cancelAnimationFrame(raf);rows=[];loaded.clear();imageCache.clear();views.replaceChildren();action.replaceChildren();audioArea.querySelectorAll("audio").forEach(a=>{a.pause();a.removeAttribute("src");a.load();});audioArea.replaceChildren();status.textContent="正在读取动画配置…";
      try{const variant=ref.variants.find(v=>String(v.graphic)===shape.value);const data=await options.loadSprite(variant);if(disposed||token!==sequence)return;sprite=data;
        for(const id of [...new Set(sprite.actions.map(a=>a.action))].sort((a,b)=>a-b))selectOption(action,`${id} · ${names[id]||"动作"}`,String(id));
        if(sprite.actions.some(a=>a.action===3))action.value="3";
        action.disabled=false;direction.disabled=false;
        stats.textContent=`图号 ${sprite.graphic} · ${sprite.unique_frames} 张独立图片 · ${sprite.frame_count} 帧引用`;
        for(const sound of sprite.sounds||[]){const item=node("div"),p=node("p",`音效 ${sound.tone}`);item.append(p);if(sound.path){const audio=node("audio");audio.controls=true;audio.preload="none";options.ensureCache().then(()=>{if(!disposed&&audio.isConnected)audio.src=url(sound.path);});item.append(audio);}else item.append(node("p","对应音频尚未收录"));audioArea.append(item);}
        showAction();
      }catch(e){if(!disposed&&token===sequence&&e.name!=="AbortError"){status.textContent=e.message;status.append(button("重试",showShape));}}
    }
    shape.onchange=showShape;action.onchange=direction.onchange=showAction;clock.onchange=()=>{start=performance.now();};scrub.oninput=()=>{playing=false;pause.textContent="播放";};
    if(ref.variants?.length)showShape();
    return ()=>{disposed=true;sequence++;cancelAnimationFrame(raf);rows=[];loaded.clear();imageCache.clear();for(const audio of host.querySelectorAll("audio")){audio.pause();audio.removeAttribute("src");audio.load();}};
  }
  return {mount};
})();
