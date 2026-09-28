/* Ladder presentation consumes the shared, authoritative Go projection.
 * Actions use revisioned requests; a lost HTTP response never causes a new
 * mutation. Combat remains on the existing native battle controls. */
(function(root) {
  'use strict';
  const client = root.StoneAgeWebClient;
  if (!client) return;
  const state = { characterEpoch: 0, session: null, envelope: null, stream: '', cursor: 0, open: false,
    busy: false, polling: false, pending: null, pendingQueue: [], error: '', networkError: '', notice: '', contacts: [],
    contactsKnown: false, selectedContact: '', selectedStrategy: '', selectedMode: null, selectedPetMask: null, pets: [], strategies: [], receivedAt: 0, announced: '', orderedResult: '', controller: null };
  const phases = { idle:'尚未组队', lobby:'队伍准备', queued:'匹配中', countdown:'即将开战',
    battle:'战斗中', settling:'正在保存结算', result:'比赛结算', unavailable:'竞技场暂不可用' };
  const errors = { stale_revision:'队伍状态已变化，请查看最新状态后重新操作。',
    revision_conflict:'队伍状态已变化，请查看最新状态后重新操作。',
    leader_required:'此操作需要队长执行。', team_size_mismatch:'队伍人数须与所选模式一致。',
    member_not_ready:'请等待所有队员准备。', not_ready:'请等待所有队员准备。',
    player_busy:'角色正在进行其他活动，请先结束。', not_idle:'角色正在进行其他活动，请先结束。',
    offline:'队伍中有玩家离线。', room_locked:'当前队伍已锁定。',
    invitation_expired:'邀请已过期或失效。', invitation_pending:'已向这位玩家发出邀请。',
    contact_unavailable:'这张名片对应的玩家当前不可邀请。', already_in_room:'玩家已在竞技场队伍中。',
    contact_identity_required:'这张旧名片尚未确认角色身份，请与对方重新交换名片。',
    contact_identity_changed:'原名片对应的角色已变化，请确认对方并重新交换名片。',
    contact_slot_changed:'名片槽位已变化，请刷新名片并重新选择玩家。',
    result_or_match_pending:'请先完成当前比赛并确认结算。', invalid_loadout:'参战宠物配置已变化，请重新选择。',
    cooldown:'弃赛冷却尚未结束。', ladder_reserved:'角色已被竞技场比赛占用。',
    database_unavailable:'结算存储暂不可用，请稍后重试。' };
  const session = () => client.app.transport;
  const visible = () => ['world','battle','battle-result'].includes(client.app.phase);
  const snapshot = () => state.envelope?.snapshot;
  function generation() {
    const value = session()?.control;
    return Number((value?.control || value)?.generation || root.StoneAgeAutomation?.currentControl()?.generation || 0);
  }
  function storageKey() {
    const id = snapshot()?.self?.id;
    return id ? `stoneage.ladder.request:${client.app.selectedServer || ''}:${id}` : '';
  }
  function savePending() {
    const key = storageKey(); if (!key) return;
    try { if (state.pending) root.sessionStorage?.setItem(key, JSON.stringify({pending:state.pending,queued:state.pendingQueue}));
      else root.sessionStorage?.removeItem(key); } catch (_) { /* in-memory recovery still works */ }
  }
  function retainUnconfirmed(request) {
    if (session() !== state.session) reset(session());
    const same = value => value && value.request_id === request.request_id && value.revision === request.revision && value.operation === request.operation && value.argument === request.argument;
    if (!same(state.pending) && !state.pendingQueue.some(same)) {
      if (state.pending) state.pendingQueue.push(request); else state.pending=request;
    }
    savePending(); state.error='手动接管的策略尚未确认，请重试原请求。';render();
  }
  function finishPending() { state.pending=state.pendingQueue.shift()||null;savePending(); }
  function requestID() {
    const bytes = new Uint8Array(16); root.crypto.getRandomValues(bytes);
    return Array.from(bytes, value => value.toString(16).padStart(2,'0')).join('');
  }
  function apply(envelope) {
    if (!envelope?.snapshot || envelope.event === 'result_lookup' || envelope.event === 'contacts_lookup') return;
    if (state.envelope && envelope.revision < state.envelope.revision) return;
    if (state.envelope && envelope.revision === state.envelope.revision && envelope.server_time_ms < state.envelope.server_time_ms) return;
    const first = !state.envelope;
    const previous = state.envelope?.snapshot, next = envelope.snapshot;
    // Preserve drafts during unrelated roster/contact updates, but discard
    // them when their authoritative configuration or editing scope changes.
    const scopeChanged = previous?.self?.id !== next.self?.id || previous?.room?.id !== next.room?.id || previous?.phase !== next.phase;
    if (scopeChanged || previous?.room?.mode !== next.room?.mode || previous?.room?.leader_id !== next.room?.leader_id) state.selectedMode = null;
    if (scopeChanged || previous?.self?.pet_mask !== next.self?.pet_mask || previous?.self?.available_pet_mask !== next.self?.available_pet_mask) state.selectedPetMask = null;
    const phaseChanged = envelope.snapshot.phase !== state.envelope?.snapshot.phase;
    const changed = JSON.stringify(envelope.snapshot) !== JSON.stringify(state.envelope?.snapshot);
    // Replaying one stamped snapshot must not restart its local clock.
    if (first || envelope.server_time_ms > state.envelope.server_time_ms) state.receivedAt = Date.now();
    state.envelope = envelope;
    if (first) {
      try { const saved = JSON.parse(root.sessionStorage?.getItem(storageKey()) || 'null');
        const pending=saved?.pending||saved;
        // A takeover can finish before the initial status request. Merge its
        // in-memory request with saved requests instead of replacing either.
        const recovered=[pending,...(Array.isArray(saved?.queued)?saved.queued:[]),state.pending,...state.pendingQueue];
        const seen=new Set(), requests=recovered.filter(r=>{
          if(!r?.request_id||!r?.operation||!Number.isSafeInteger(r.revision))return false;
          const key=JSON.stringify([r.request_id,r.revision,r.operation,r.argument]);
          if(seen.has(key))return false;seen.add(key);return true;
        });
        state.pending=requests.shift()||null;state.pendingQueue=requests;
        savePending();
      } catch (_) { /* ignore malformed local state */ }
    }
    const countdown = envelope.snapshot.phase === 'countdown' && envelope.snapshot.match?.id;
    const announce = countdown && state.announced !== `countdown:${countdown}`;
    if (announce) { state.announced = `countdown:${countdown}`; state.open = true; }
    if (changed || announce) render();
    if (phaseChanged && panel) panel.scrollTop = 0;
  }
  function resultPending(id) {
    const battle=client.app.battleState;
    if(!id)return false;
    if(battle?.ladderID===id){
      if(client.app.battle)return !battle.ladderResultReady;
      if(battle.ladderResultReady)return false;
    }
    // Polling can finish while EN is still waiting on map/resources. Until
    // the ordered terminal packet arrives, no result (including ACK) is safe
    // to present merely because app.battle has not become true yet.
    return state.orderedResult!==id;
  }
  // Called only by the ordered native event pipeline. Polling may discover
  // a result before the browser has even received its final battle movie.
  function receive(envelope,transport) {
    if(transport!==session())return;
    if(transport!==state.session)reset(transport);
    apply(envelope);
    if(envelope?.event==='result_lookup'||envelope?.event==='contacts_lookup')return;
    const incoming=envelope?.snapshot,s=snapshot();
    if(incoming?.phase==='result'&&incoming.result?.id&&incoming.result.id===s?.result?.id){
      state.orderedResult=incoming.result.id;
      presentResult(incoming.result.id);
    }
  }
  function presentResult(id) {
    if(snapshot()?.phase!=='result'||snapshot()?.result?.id!==id||resultPending(id))return;
    const token=`result:${id}`;
    if(state.announced!==token){state.announced=token;state.open=true;}
    render();
  }
  function reset(next) {
    state.characterEpoch++;
    state.controller?.abort();
    Object.assign(state, { session:next, envelope:null, stream:'', cursor:0, pending:null, pendingQueue:[],
      error:'', networkError:'', notice:'', busy:false, polling:false, contacts:[], contactsKnown:false, selectedContact:'', selectedStrategy:'', selectedMode:null, selectedPetMask:null, pets:[], strategies:[], announced:'', orderedResult:'' });
    render();
  }
  async function http(path, options = {}) {
    const current = session(), epoch = state.characterEpoch;
    if (!current?.id || current.closed) throw new Error('游戏连接已关闭，请重新登录。');
    const response = await fetch(`${current.base}/api/sessions/${encodeURIComponent(current.id)}/${path}`, {
      ...options, cache:'no-store', headers:{'Content-Type':'application/json'},
    });
    const text = await response.text();
    let data; try { data = JSON.parse(text); } catch (_) { data = null; }
    if (session() !== current || current.closed || epoch !== state.characterEpoch) throw new Error('游戏会话已变化，请重新读取竞技场状态。');
    if (!response.ok) {
      const error = new Error(data?.code === 'outcome_unknown' ? '操作结果尚未确认，可重试原请求。' : text.trim() || `HTTP ${response.status}`);
      error.code = data?.code; error.status = response.status; throw error;
    }
    return data;
  }
  async function submit(request) {
    const reply=await http('ladder', {method:'POST', body:JSON.stringify({generation:generation(), request}), signal:AbortSignal.timeout(10000)});
    syncControl(reply); return reply;
  }
  function syncControl(value) {
    if (!value?.control || value.control.generation < generation()) return;
    if (root.StoneAgeAutomation?.updateFromTransport) root.StoneAgeAutomation.updateFromTransport(value);
    else if (session()?.setControl) session().setControl(value);
    else if (session()) session().control=value.control;
  }
  async function status() {
    const current = session();
    if (current !== state.session) reset(current);
    const epoch = state.characterEpoch;
    const reply = await submit({request_id:requestID(), revision:0, operation:'status', argument:''});
    if (current !== state.session || epoch !== state.characterEpoch) return;
    apply(reply);
    if (!reply.ok) throw new Error(errors[reply.code] || `竞技场暂不可用（${reply.code}）`);
    return reply;
  }
  async function perform(operation, argument = '', retry = false) {
    if (state.busy) return;
    if(operation==='ack'&&resultPending(snapshot()?.result?.id)){state.notice='最后一回合播放完毕后可确认结算。';render();return;}
    if (state.pending && !retry) { state.error = '上一项操作尚未确认，请先重试原请求。'; render(); return; }
    const current = session(); if (current !== state.session) reset(current);
    const epoch = state.characterEpoch;
    state.busy = true; state.error = ''; render();
    let request;
    try {
      if (!state.envelope) await status();
      if (current !== state.session || epoch !== state.characterEpoch) return;
      if (state.pending && !retry) throw new Error('已恢复一项尚未确认的操作，请先重试原请求。');
      request = retry ? state.pending : {request_id:requestID(), revision:state.envelope.revision, operation, argument:String(argument)};
      if (!request) return;
      state.pending = request; savePending();
      const reply = await submit(request);
      if (current !== state.session || epoch !== state.characterEpoch) return;
      finishPending(); apply(reply);
      const historical=reply.replay&&reply.server_boot&&reply.receipt_boot&&reply.server_boot!==reply.receipt_boot;
      state.notice=historical?'已确认重启前的操作结果；当前队伍和比赛状态以面板为准。':'';
      if (!reply.ok) throw new Error(errors[reply.code] || `操作未完成（${reply.code}）`);
      if (operation === 'strategy' && !historical) state.selectedStrategy = '';
      if (!historical && ['create','mode'].includes(request.operation)) state.selectedMode = null;
      if (!historical && request.operation === 'loadout') state.selectedPetMask = null;
      if (reply.automation_error) state.error=`操作已完成，自动战斗未启动：${reply.automation_error}`;
      if (!historical && operation === 'invite') state.notice = '邀请已发出，对方可在竞技场中接受。';
      if (!historical && operation === 'ack') state.announced = '';
    } catch (error) {
      if (current !== state.session || epoch !== state.characterEpoch) return;
      // A bridge validation/ownership rejection proves no write occurred.
      if (error.status && error.status < 500) finishPending();
      state.error = error.message;
    } finally { if (current === state.session && epoch === state.characterEpoch) { state.busy = false; render(); } }
  }
  async function refresh() {
    const current = session();
    if (current !== state.session) reset(current);
    const epoch = state.characterEpoch;
    if (state.polling || !visible() || !current?.id || current.closed) return;
    state.polling = true;
    const controller = new AbortController(); state.controller = controller;
    const timeout = root.setTimeout?.(()=>controller.abort(),25000);
    try {
      if (!state.envelope) await status();
      const query = `cursor=${state.cursor}&stream=${encodeURIComponent(state.stream)}&wait=${state.stream&&!state.open?'20s':'0s'}`;
      const data = await http(`ladder?${query}`, {signal:controller.signal});
      if (current !== state.session || epoch !== state.characterEpoch) return;
      const hadNetworkError = !!state.networkError; state.networkError = '';
      syncControl(data);
      const strategiesChanged=JSON.stringify(state.strategies)!==JSON.stringify(data.strategies||[]);
      state.strategies=data.strategies||[];
      if (data.gap) state.notice = '已恢复当前比赛状态。';
      state.stream = data.stream; state.cursor = data.cursor;
      const contextChanged = JSON.stringify(state.pets) !== JSON.stringify(data.pets || []);
      state.pets = data.pets || []; apply(data.snapshot);
      if (contextChanged || strategiesChanged || hadNetworkError) render();
    } catch (error) {
      if (current === state.session && epoch === state.characterEpoch && !controller.signal.aborted) {
        if (error.code === 'ladder_snapshot_required') state.envelope = null;
        state.networkError = error.message; render();
      }
    } finally { root.clearTimeout?.(timeout); if (current === state.session && epoch === state.characterEpoch) state.polling = false; }
  }
  async function refreshContacts() {
    const current=session();if(current!==state.session)reset(current);
    const epoch=state.characterEpoch;
    try {
      const reply=await submit({request_id:requestID(),revision:0,operation:'contacts',argument:''});
      if(current!==state.session||epoch!==state.characterEpoch)return;
      if(!reply.ok)throw new Error(errors[reply.code]||`名片读取失败（${reply.code}）`);
      state.contacts=reply.contacts||[];state.contactsKnown=true;
      if(!state.contacts.some(p=>p.id&&`${p.slot}:${p.id}`===state.selectedContact))state.selectedContact='';
      render();
    } catch (error) { if(current===state.session&&epoch===state.characterEpoch){state.contacts=[];state.contactsKnown=false;state.error=error.message;render();} }
  }
  function setOpen(value) {
    state.open = Boolean(value); render();
    if (value) { state.controller?.abort(); refresh(); if (!snapshot()?.match) refreshContacts(); }
    else root.document?.getElementById('ladder-toggle')?.focus();
  }
  function now() { return (state.envelope?.server_time_ms || Date.now()) + Date.now() - state.receivedAt; }
  function countdownText() {
    const s = snapshot();
    if (!s) return '正在读取竞技场状态…';
    if (s.phase === 'countdown') return `距离开战 ${Math.max(0,Math.ceil((s.match.start_at_ms-now())/1000))} 秒`;
    if (s.phase === 'queued') return `匹配中 · 已等待 ${Math.max(0,Math.floor((now()-s.room.queued_at_ms)/1000))} 秒`;
    if (s.cooldown_until_ms > now()) return `弃赛冷却 ${Math.ceil((s.cooldown_until_ms-now())/1000)} 秒`;
    return phases[s.phase] || s.phase;
  }
  function updateClocks() {
    const doc = root.document; if (!doc) return;
    const clock = doc.getElementById('ladder-clock'); if (clock) clock.textContent = countdownText();
    doc.querySelectorAll('#ladder-panel [data-reconnect-until]').forEach(label=>{
      label.textContent=`离线 · 重连剩余 ${Math.max(0,Math.ceil((Number(label.dataset.reconnectUntil)-now())/1000))} 秒`;
    });
    // Countdown notification happens when its snapshot is applied. Results
    // are presented only by the ordered packet/scene completion path, never
    // by polling ahead of the native battle movie.
  }

  let panel, toggle;
  function element(tag, text, className) {
    const node = root.document.createElement(tag); if (text != null) node.textContent = text;
    if (className) node.className = className; return node;
  }
  function button(text, action, disabled = false) {
    const node = element('button',text); node.type = 'button';
    node.disabled = state.busy || !!state.pending || disabled; node.onclick = action; return node;
  }
  function playerCard(player, suffix = '', showStatus = true) {
    const card = element('div',null,'ladder-member');
    card.append(element('strong', `${player.name || '玩家'}${suffix}`),
      element('span',`${player.rating} 分 · 战力 ${Math.round(player.power)}`));
    const offline = player.abandoned ? '已弃赛' : player.online ? (player.ready?'已准备':'未准备') : '离线';
    const label=element('span',offline,'ladder-muted');
    if (!player.online && !player.abandoned && snapshot()?.phase==='battle') {
      label.dataset.reconnectUntil=String((state.envelope.server_time_ms||now())+player.reconnect_remaining_ms);
      label.textContent=`离线 · 重连剩余 ${Math.max(0,Math.ceil((Number(label.dataset.reconnectUntil)-now())/1000))} 秒`;
    }
    if (showStatus) card.append(label); return card;
  }
  function render() {
    if (!panel) return;
    panel.hidden = !state.open; toggle.setAttribute('aria-expanded',String(state.open));
    const s = snapshot(); toggle.textContent = `竞技场${s?.invitations?.length ? ` · ${s.invitations.length}` : ''}`;
    if (!state.open) return;
    const body = panel.querySelector('[data-body]'); body.replaceChildren();
    const clock = element('p',countdownText(),'ladder-state'); clock.id = 'ladder-clock'; body.append(clock);
    if (state.error) body.append(element('p',state.error,'ladder-error'));
    if (state.networkError) body.append(element('p',state.networkError,'ladder-error'));
    if (state.pending) { const retry = button('重试原请求',()=>perform(state.pending.operation, state.pending.argument, true)); retry.disabled=state.busy; body.append(retry); }
    if (state.notice) body.append(element('p',state.notice,'ladder-muted'));
    const control = root.StoneAgeAutomation?.currentControl();
    if (control && !['manual','battle'].includes(control.mode)) {
      body.append(element('p','先结束自动任务或自动练级，再准备竞技场。','ladder-muted'),
        button('接管角色',async()=>{try{await root.StoneAgeAutomation.takeover();await status();}catch(error){state.error=error.message;render();}}));
    }
    if (!s) { const retry=button('重新读取',()=>status().catch(error=>{state.error=error.message;render();})); retry.disabled=false; body.append(retry); return; }
    const ratings = element('div',null,'ladder-ratings');
    for (let i=0;i<5;i++) ratings.append(element('span',`${i+1}v${i+1} · ${s.ratings?.[i] ?? '—'}`)); body.append(ratings);
    if (s.phase === 'result' && s.result) {
      if(resultPending(s.result.id)){body.append(element('p','最后一回合播放中，稍后显示结算。','ladder-muted'));return;}
      const result = s.result, me = result.members.find(p=>p.id===s.self.id);
      const label = !result.rated ? '本场不计积分' : result.winner_side < 0 ? '平局' : result.winner_side===me?.side ? '胜利' : '失败';
      body.append(element('h3',label),element('p',result.statistics_incomplete
        ? `${result.mode}v${result.mode} · 服务重启，比赛中断；回合、时长及统计未恢复。`
        : `${result.mode}v${result.mode} · ${result.turns} 回合 · ${Math.round(result.duration_ms/1000)} 秒`));
      for (const p of result.members) {
        const card = playerCard(p, p.side===me?.side?' · 我方':' · 对方', false), stats=p.statistics;
        card.append(element('p',`${p.rating_before} → ${p.rating_after}（${p.rating_delta>=0?'+':''}${p.rating_delta}）`));
        const rows=[['总伤害',stats.damage],['宠物伤害',stats.pet_damage],['总承伤',stats.damage_taken],['宠物承伤',stats.pet_damage_taken],['骑宠承伤',stats.ride_damage_taken],['治疗',stats.healing],['击杀人物',stats.player_kills],['击杀宠物',stats.pet_kills],['死亡',stats.deaths],['助攻',stats.assists],['复活',stats.revives]];
        if (!result.statistics_incomplete) card.append(element('p',rows.map(([name,value])=>`${name} ${value}`).join(' · '),'ladder-muted'));
        body.append(card);
      }
      body.append(button(s.room?'确认结算，返回队伍':'确认结算',()=>perform('ack')),button('查看战斗记录',()=>root.StoneAgeBattleJournal?.open()));
      return;
    }
    if (s.match && ['countdown','battle','settling'].includes(s.phase)) {
      const teams=element('div',null,'ladder-teams');
      s.match.teams.forEach((team,index)=>{const col=element('section');
        col.append(element('h3',index===s.match.side?'我方':'对方'),element('p',`平均 ${Math.round(team.rating)} 分 · 总战力 ${Math.round(team.power)}`,'ladder-muted'));
        team.members.forEach(p=>col.append(playerCard(p)));teams.append(col);});body.append(teams);
      body.append(element('p','掉线后可重新登录回到本场比赛，每人累计有 60 秒重连时间。战斗继续进行，回合超时人物防御、宠物待机。','ladder-muted'));
      if (s.phase==='battle') body.append(button('查看战斗记录',()=>root.StoneAgeBattleJournal?.open()));
      return;
    }
    const room=s.room, leader=room?.leader_id===s.self.id, editable=s.phase==='lobby';
    if (!room || leader) {
      const row=element('div',null,'ladder-row'), modes=element('select');modes.setAttribute('aria-label','竞技场模式');
      for(let i=1;i<=5;i++){const option=element('option',`${i}v${i}`);option.value=i;modes.append(option);}modes.value=state.selectedMode??room?.mode??1;
      modes.onchange=()=>{state.selectedMode=Number(modes.value);};
      modes.disabled=state.busy||!!state.pending||!!room&&!editable;
      row.append(modes,button(room?'更改模式':'创建队伍',()=>perform(room?'mode':'create',modes.value),!!room&&!editable));body.append(row);
    }
    if (room) {
      body.append(element('h3',`${room.mode}v${room.mode} · ${room.members.length}/${room.mode} 人`));
      for(const p of room.members){const card=playerCard(p,p.id===room.leader_id?' · 队长':'');
        if(leader&&p.id!==s.self.id&&editable)card.append(button('转让队长',()=>perform('leader',p.id)),button('移出队伍',()=>perform('kick',p.id)));body.append(card);}
      if (editable) {
        const loadout=element('fieldset');loadout.append(element('legend','登记参战宠物（含备用宠物与骑宠）'));
        for(let slot=0;slot<5;slot++)if(s.self.available_pet_mask&(1<<slot)){
          const label=element('label'),input=element('input'),pet=state.pets.find(p=>p.Slot===slot);
          input.type='checkbox';input.value=slot;input.checked=!!((state.selectedPetMask??s.self.pet_mask)&(1<<slot));input.disabled=state.busy||!!state.pending;
          input.onchange=()=>{const mask=state.selectedPetMask??s.self.pet_mask;state.selectedPetMask=input.checked?mask|(1<<slot):mask&~(1<<slot);};
          label.append(input,root.document.createTextNode(`${pet?.FreeName||pet?.Name||`宠物 ${slot+1}`}${s.self.ride_pet===slot?'（骑乘）':''}${s.self.active_pet===slot?'（出战）':''}`));loadout.append(label);
        }
        loadout.append(button('保存参战宠物',()=>{let mask=0;loadout.querySelectorAll('input:checked').forEach(input=>mask|=1<<Number(input.value));perform('loadout',mask);}));body.append(loadout);
        const invite=element('div',null,'ladder-row'),contacts=element('select');contacts.setAttribute('aria-label','从名片邀请');
        const empty=element('option',state.contactsKnown?'选择名片中的玩家':'请先刷新名片');empty.value='';contacts.append(empty);
        for(const p of state.contacts){const option=element('option',`${p.name}${!p.id?'（需重新交换名片）':p.online?'':'（离线）'}`);option.value=p.id?`${p.slot}:${p.id}`:'';option.disabled=!p.id;contacts.append(option);}
        contacts.value=state.selectedContact;contacts.onchange=()=>{state.selectedContact=contacts.value;};
        invite.append(contacts,button('邀请',()=>{if(contacts.value!=='')perform('invite',contacts.value);}),button('刷新名片',refreshContacts));body.append(invite);
        body.append(button(s.self.ready?'取消准备':'准备',()=>perform(s.self.ready?'unready':'ready')));
        if(leader)body.append(button('开始匹配',()=>perform('queue'),room.members.length!==room.mode||room.members.some(p=>!p.ready||!p.online)));
        body.append(button('退出队伍',()=>perform('leave')));
      } else if (leader && s.phase==='queued') body.append(button('取消匹配',()=>perform('cancel')));
    }
    for(const invite of s.invitations||[]){const row=element('div',null,'ladder-member');
      row.append(element('strong',`${invite.from.name} 邀请你参加 ${invite.mode}v${invite.mode}`),button('接受',()=>perform('accept',invite.id),!!room),button('拒绝',()=>perform('decline',invite.id)));body.append(row);}
  }
  root.StoneAgeLadder={resetCharacter:()=>reset(session()),state,apply,receive,presentResult,resultPending,perform,status,refresh,refreshContacts,setOpen,countdownText,retainUnconfirmed};
  if (!root.document) return;
  const doc=root.document,style=element('style');style.textContent=`
    #ladder-toggle{position:fixed;right:166px;top:12px;z-index:3000;padding:6px 10px;color:#ffe9a4;background:#2a2018ef;border:1px solid #b49357;border-radius:3px;font:12px/1.2 sans-serif}
    #ladder-panel{position:fixed;right:12px;top:48px;width:min(560px,calc(100vw - 24px));max-height:calc(100dvh - 64px);z-index:3200;overflow:auto;box-sizing:border-box;padding:16px;color:#eee0c2;background:#241d16fa;border:1px solid #a68b59;border-radius:8px;box-shadow:0 8px 32px #0008;font:13px/1.6 sans-serif}
    #ladder-panel[hidden],#ladder-toggle[hidden]{display:none}#ladder-panel header{position:sticky;top:-16px;z-index:1;background:#241d16;padding-top:8px;padding-bottom:8px;height:auto;border:0;display:flex;justify-content:space-between;align-items:center;gap:12px}#ladder-panel h2{margin:0;font-size:18px;color:#ffe4a0}#ladder-panel h3{font-size:14px;margin:12px 0 6px}#ladder-panel p{margin:6px 0;overflow-wrap:anywhere}#ladder-panel button,#ladder-panel select{font:inherit;color:#f5dfa7;border:1px solid #806b49;border-radius:4px;background:#382c20;padding:6px 10px;margin:3px 4px 3px 0;min-height:34px;max-width:100%}#ladder-panel button:disabled{opacity:.45}#ladder-panel .ladder-muted{color:#b6aa94;font-size:12px}#ladder-panel .ladder-error{color:#ffa49a}#ladder-panel .ladder-state{color:#ffdfa0;font-weight:bold}#ladder-panel .ladder-row{display:flex;align-items:center;flex-wrap:wrap}#ladder-panel .ladder-row select{flex:1;min-width:120px}#ladder-panel .ladder-ratings{display:flex;flex-wrap:wrap;gap:4px 12px;margin:10px 0;color:#d4c096}#ladder-panel .ladder-member{background:#ffffff08;padding:8px 10px;margin:6px 0;border-left:2px solid #9ba777;border-radius:4px;overflow-wrap:anywhere}#ladder-panel .ladder-member>span,#ladder-panel .ladder-member>strong{display:block}#ladder-panel .ladder-teams{display:grid;grid-template-columns:1fr 1fr;gap:12px}#ladder-panel .ladder-teams section{min-width:0}#ladder-panel fieldset{margin:12px 0;border:1px solid #806b49}#ladder-panel fieldset label{display:block;padding:4px 0}#ladder-panel input{accent-color:#ba9b61;margin-right:8px}@media(max-width:380px){#ladder-panel{padding:12px}#ladder-panel .ladder-teams{gap:6px}#ladder-panel .ladder-member{padding:6px}}`;
  doc.head.append(style);
  toggle=element('button','竞技场');toggle.id='ladder-toggle';toggle.type='button';toggle.hidden=true;toggle.setAttribute('aria-controls','ladder-panel');toggle.onclick=()=>setOpen(!state.open);(doc.getElementById('player-tools')||doc.body).append(toggle);
  panel=element('section');panel.id='ladder-panel';panel.setAttribute('data-game-ui','');panel.hidden=true;panel.setAttribute('role','dialog');panel.setAttribute('aria-label','竞技场');
  panel.innerHTML='<header><h2>竞技场</h2><button type="button" aria-label="关闭竞技场面板">关闭</button></header><div data-body></div>';doc.body.append(panel);
  panel.querySelector('header button').onclick=()=>setOpen(false);
  panel.addEventListener('keydown',event=>{event.stopPropagation();if(event.key==='Escape'){event.preventDefault();setOpen(false);}});
  root.setInterval(()=>{if(session()!==state.session)reset(session());toggle.hidden=!visible();if(toggle.hidden){state.controller?.abort();panel.hidden=true;return;}updateClocks();refresh();},1000);
})(typeof window==='object'?window:globalThis);
