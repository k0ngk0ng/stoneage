/* Read-only battle journal. Never schedules animations, submits commands or
 * changes the battle state; incoming packets are observed once at ingress. */
(function(root){
  'use strict';
  const client=root.StoneAgeWebClient;if(!client)return;
  let journal={battles:[]},open=false,selected=null,lastSession=null,pending=false;
  async function refresh(){
    const session=client.app.transport;
    if(session!==lastSession){journal={battles:[]};selected=null;lastSession=session;render();}
    if(pending||!open||!session?.id||session.closed)return;
    pending=true;
    try{
      const response=await fetch(`${session.base}/api/sessions/${encodeURIComponent(session.id)}/battle-log`,{cache:'no-store',signal:AbortSignal.timeout(5000)});
      if(!response.ok)throw new Error(`HTTP ${response.status}`);
      const data=await response.json();
      if(session!==client.app.transport)return;
      if(selected===journal.battles[0]?.id&&data.battles[0]?.id!==selected)selected=null;
      journal=data;render();
    }catch(error){if(session===client.app.transport&&open)summary.textContent='战斗记录暂时无法读取，稍后自动重试。';}
    finally{pending=false;}
  }
  const doc=root.document,style=doc.createElement('style');
  style.textContent=`
    #battle-panel-toggle{position:fixed;right:88px;top:12px;z-index:3000;padding:6px 10px;color:#ffe9a4;background:#2a2018ef;border:1px solid #b49357;border-radius:3px;font:12px/1.2 sans-serif}
    #battle-journal{position:fixed;right:12px;top:48px;width:min(440px,calc(100vw - 24px));max-height:calc(100dvh - 64px);z-index:3100;box-sizing:border-box;padding:16px;overflow:auto;color:#eee0c2;background:#241d16f7;border:1px solid #a68b59;border-radius:8px;box-shadow:0 8px 32px #0008;font:13px/1.6 sans-serif}
    #battle-journal[hidden]{display:none}#battle-journal header{height:auto;padding:0;background:none;border:0;display:flex;align-items:baseline;justify-content:space-between;gap:12px;margin-bottom:12px}#battle-journal h2{margin:0;font-size:17px;color:#ffe4a0}#battle-journal h3{margin:12px 0 6px;font-size:13px;color:#e5c487}#battle-journal button,#battle-journal select{font:inherit;color:#f5dfa7;border:1px solid #806b49;border-radius:4px;background:#382c20;padding:4px 8px}#battle-journal select{width:100%;box-sizing:border-box}#battle-journal .battle-muted{color:#b6aa94;font-size:12px}#battle-journal .battle-roster{display:grid;grid-template-columns:1fr 1fr;gap:6px}#battle-journal .battle-person{padding:6px 8px;background:#ffffff08;border-left:2px solid #83a96b;border-radius:3px;min-width:0;overflow-wrap:anywhere}#battle-journal .battle-person.enemy{border-color:#d99176}#battle-journal .battle-person b{display:block;font-size:12px}#battle-journal .battle-person span{display:block;font-size:12px;color:#d1c4ad}#battle-journal .battle-entries{max-height:260px;overflow:auto;overscroll-behavior:contain}#battle-journal .battle-entry{padding:7px 0;border-bottom:1px solid #ffffff12;overflow-wrap:anywhere}#battle-journal .battle-turn{color:#c9a56c;font-size:11px;margin-right:7px}
    @media(max-width:480px){#battle-journal{padding:12px}#battle-journal .battle-entries{max-height:32dvh}}
  `;doc.head.append(style);
  const toggle=doc.createElement('button');toggle.id='battle-panel-toggle';toggle.textContent='战斗';toggle.type='button';toggle.hidden=true;toggle.setAttribute('aria-controls','battle-journal');toggle.setAttribute('aria-expanded','false');doc.body.append(toggle);
  const panel=doc.createElement('aside');panel.id='battle-journal';panel.hidden=true;panel.setAttribute('aria-label','战斗面板');panel.innerHTML='<header><h2>战斗面板</h2><button type="button" aria-label="关闭战斗面板">关闭</button></header><select aria-label="选择战斗记录"></select><p class="battle-muted" data-summary></p><h3>参战信息 · 最近状态</h3><div class="battle-roster"></div><h3>回合记录</h3><div class="battle-entries"></div><p class="battle-muted">仅保留本次登录最近 20 场，每场最多 300 条。记录按服务器结算顺序展示，可能早于动画；未识别技能不猜测名称。</p>';doc.body.append(panel);
  const picker=panel.querySelector('select'),roster=panel.querySelector('.battle-roster'),logs=panel.querySelector('.battle-entries'),summary=panel.querySelector('[data-summary]');
  function setOpen(value){open=value;panel.hidden=!value;toggle.setAttribute('aria-expanded',String(value));if(value){render();refresh();}else toggle.focus();}
  toggle.onclick=()=>setOpen(!open);panel.querySelector('button').onclick=()=>setOpen(false);picker.onchange=()=>{selected=Number(picker.value);render();};
  panel.addEventListener('keydown',event=>{event.stopPropagation();if(event.key==='Escape'){event.preventDefault();setOpen(false);}});
  function render(){
    if(!open)return;
    const history=journal.battles,battle=history.find(b=>b.id===selected)||history[0];selected=battle?.id;
    picker.replaceChildren();for(const b of history){const option=doc.createElement('option');option.value=b.id;option.textContent=`第 ${b.id} 场 · ${new Date(b.started).toLocaleTimeString()} · ${b.result}`;picker.append(option);}if(battle)picker.value=String(battle.id);picker.disabled=!history.length;
    summary.textContent=battle?`第 ${battle.turn} 回合 · ${battle.result}${battle.trimmed?' · 较早记录已省略':''}`:'尚无战斗记录，进入战斗后自动记录。';
    roster.replaceChildren();
    const rows=battle?.roster||[],myNo=battle?.myNo;
    for(const p of rows){const card=doc.createElement('div');card.className='battle-person'+(myNo!==null&&myNo<20&&Math.floor(p.id/10)!==Math.floor(myNo/10)?' enemy':'');const name=doc.createElement('b');name.textContent=`${p.name} · Lv.${p.level}`;card.append(name);for(const value of [`体力 ${p.hp}/${p.maxHp}`,p.maxMp>0?`气力 ${p.mp}/${p.maxMp}`:'',p.ride?`骑宠 · ${p.petName||'未知'} 体力 ${p.petHp}/${p.petMaxHp}`:''])if(value){const line=doc.createElement('span');line.textContent=value;card.append(line);}roster.append(card);}
    const atEnd=logs.scrollHeight-logs.scrollTop-logs.clientHeight<24,oldTop=logs.scrollTop;logs.replaceChildren();for(const row of battle?.logs||[]){const line=doc.createElement('div');line.className='battle-entry';const turn=doc.createElement('span');turn.className='battle-turn';turn.textContent=`回合 ${row.turn}`;line.append(turn,doc.createTextNode(row.text));logs.append(line);}logs.scrollTop=atEnd?logs.scrollHeight:oldTop;
  }
  root.StoneAgeBattleJournal={open:()=>setOpen(true),render};
  root.setInterval(()=>{toggle.hidden=!['world','battle','battle-result'].includes(client.app.phase);if(toggle.hidden&&open)setOpen(false);if(open)refresh();},1000);
})(typeof window==='object'?window:globalThis);
