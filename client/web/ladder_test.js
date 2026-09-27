const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const {webcrypto} = require('node:crypto');
const source = fs.readFileSync(require('node:path').join(__dirname, 'runtimeassets/ladder.js'), 'utf8');

function documentFixture() {
  class Node {
    constructor(tag){this.tag=tag;this.children=[];this.attributes={};this.dataset={};}
    append(...nodes){this.children.push(...nodes);}
    replaceChildren(...nodes){this.children=nodes;}
    setAttribute(key,value){this.attributes[key]=value;}
    addEventListener(){}
    set innerHTML(value){this.children=[new Node('header'),new Node('div')];this.children[0].append(new Node('button'));this.children[1].attributes['data-body']='';}
    all(){return this.children.flatMap(node=>[node,...node.all()]);}
    querySelector(selector){
      if(selector==='header button')return this.children[0].children[0];
      if(selector==='[data-body]')return this.all().find(node=>'data-body' in node.attributes);
    }
    querySelectorAll(selector){return this.all().filter(node=>selector==='input:checked'&&node.tag==='input'&&node.checked);}
  }
  return {head:new Node('head'),body:new Node('body'),getElementById(id){return this.body.all().find(node=>node.id===id);},createElement:tag=>new Node(tag),createTextNode:text=>Object.assign(new Node('#text'),{textContent:text})};
}

function fixture(document) {
  const calls=[], saved=new Map();let clock=100000;
  const app={phase:'world', selectedServer:'line', transport:{id:'a', base:'', closed:false, control:{mode:'manual',generation:3}}};
  let respond=async()=>({version:1,ok:true,revision:12,sequence:12,snapshot:{phase:'queued',self:{id:'player_a'}}});
  const root={StoneAgeWebClient:{app}, crypto:webcrypto, sessionStorage:{getItem:key=>saved.get(key),setItem:(key,value)=>saved.set(key,value),removeItem:key=>saved.delete(key)}};
  if(document)Object.assign(root,{document,setInterval:()=>0});
  vm.runInNewContext(source,{window:root,AbortSignal,AbortController,Uint8Array,Date:{now:()=>clock},fetch:async(url,options)=>{
    calls.push({url,...options});const value=await respond(url,options);
    return {ok:true,text:async()=>JSON.stringify(value)};
  }});
  const api=root.StoneAgeLadder;api.state.session=app.transport;
  api.apply({version:1,ok:true,revision:10,sequence:10,snapshot:{phase:'lobby',self:{id:'player_a'}}});
  return {api,app,calls,saved,advance:ms=>{clock+=ms;},respond:fn=>{respond=fn;}};
}

test('mode and pet controls preserve edits through a contact-triggered DOM rebuild',async()=>{
  const doc=documentFixture(),f=fixture(doc);
  f.api.state.open=true;
  const snapshot={phase:'lobby',self:{id:'player_a',pet_mask:3,available_pet_mask:3},room:{id:'room',mode:2,leader_id:'player_a',members:[]}};
  f.api.apply({ok:true,revision:11,snapshot});
  assert.ok(!doc.body.all().some(node=>node.attributes['aria-label']==='天梯战斗策略'));
  const mode=()=>doc.body.all().find(node=>node.attributes['aria-label']==='天梯模式');
  const pets=()=>doc.body.all().filter(node=>node.tag==='input');
  mode().value='5';mode().onchange();
  for(const input of pets()){input.checked=false;input.onchange();}
  f.respond(async()=>({ok:true,event:'contacts_lookup',contacts:[]}));
  await f.api.refreshContacts();
  assert.equal(Number(mode().value),5);
  assert.ok(pets().every(input=>!input.checked));
  f.respond(async()=>({ok:true,revision:12,snapshot:{...snapshot,self:{...snapshot.self,pet_mask:0}}}));
  await doc.body.all().find(node=>node.textContent==='保存参战宠物').onclick();
  assert.equal(JSON.parse(f.calls.at(-1).body).request.argument,'0');
});

test('configuration drafts survive unrelated updates and expire when their editing scope changes',()=>{
  const f=fixture();let revision=11;
  const snapshot={phase:'lobby',self:{id:'player_a',pet_mask:3,available_pet_mask:7},room:{id:'room_a',leader_id:'player_a',mode:2,members:[]}};
  const update=change=>f.api.apply({ok:true,revision:revision++,snapshot:{...snapshot,...change}});
  update({});f.api.state.selectedMode=5;f.api.state.selectedPetMask=0;
  update({room:{...snapshot.room,members:[{id:'teammate',ready:true}]}});
  assert.equal(f.api.state.selectedMode,5);assert.equal(f.api.state.selectedPetMask,0);
  update({self:{...snapshot.self,available_pet_mask:1}});
  assert.equal(f.api.state.selectedPetMask,null);assert.equal(f.api.state.selectedMode,5);
  update({room:{...snapshot.room,leader_id:'teammate'}});
  assert.equal(f.api.state.selectedMode,null);
  f.api.state.selectedMode=4;f.api.state.selectedPetMask=1;
  update({phase:'queued'});
  assert.equal(f.api.state.selectedMode,null);assert.equal(f.api.state.selectedPetMask,null);
});

test('configuration drafts retain rejected edits and clear after an acknowledged retry',async()=>{
  const f=fixture();f.api.state.selectedMode=4;
  const snapshot={phase:'lobby',self:{id:'player_a'}};
  f.respond(async()=>({ok:false,code:'team_size_mismatch',revision:11,snapshot}));
  await f.api.perform('mode',4);
  assert.equal(f.api.state.selectedMode,4);
  f.api.state.selectedPetMask=0;
  f.respond(async()=>{throw new Error('lost reply');});
  await f.api.perform('loadout',0);
  assert.equal(f.api.state.selectedPetMask,0);
  f.respond(async()=>({ok:true,revision:12,snapshot}));
  await f.api.perform('loadout',0,true);
  assert.equal(f.api.state.selectedPetMask,null);
  assert.equal(f.api.state.pending,null);
});

test('unknown write retains exact request for an explicit idempotent retry',async()=>{
  const f=fixture();f.respond(async()=>{throw new Error('lost response');});
  await f.api.perform('queue');
  const first=JSON.parse(f.calls[0].body);
  assert.equal(first.generation,3);assert.equal(first.request.revision,10);
  assert.equal(f.api.state.pending.request_id,first.request.request_id);
  assert.equal(f.saved.size,1);
  await f.api.perform('leave');assert.equal(f.calls.length,1,'unknown operation blocks another mutation');
  f.respond(async()=>({version:1,ok:true,revision:12,sequence:12,snapshot:{phase:'queued',self:{id:'player_a'}}}));
  await f.api.perform('queue','',true);
  assert.equal(f.calls[1].body,f.calls[0].body);
  assert.equal(f.api.state.pending,null);assert.equal(f.saved.size,0);
  assert.equal(f.api.state.envelope.snapshot.phase,'queued');
});

test('historical invitation receipt resolves uncertainty without advertising a current invitation',async()=>{
  const f=fixture();f.respond(async()=>{throw new Error('lost response');});
  await f.api.perform('invite','0:pc1_selected');
  const original=f.calls[0].body;
  f.respond(async()=>({version:1,ok:true,replay:true,server_boot:'new',receipt_boot:'old',revision:50,
    snapshot:{phase:'idle',self:{id:'player_a'},room:null}}));
  await f.api.perform('invite','0:pc1_selected',true);
  assert.equal(f.calls[1].body,original);
  assert.equal(f.api.state.pending,null);
  assert.equal(f.saved.size,0);
  assert.equal(f.api.state.envelope.snapshot.room,null);
  assert.match(f.api.state.notice,/重启前/);
  assert.doesNotMatch(f.api.state.notice,/邀请已发出/);
});

test('late receipts and historical lookups cannot rewind the displayed match',()=>{
  const f=fixture();
  f.api.apply({revision:20,snapshot:{phase:'battle'}});
  f.api.apply({revision:19,snapshot:{phase:'lobby'}});
  f.api.apply({revision:20,event:'result_lookup',snapshot:{phase:'battle',result:{id:'old'}}});
  assert.equal(f.api.state.envelope.snapshot.phase,'battle');
  assert.equal(f.api.state.envelope.snapshot.result,undefined);
});

test('result polling cannot skip the final native movie or acknowledge before it finishes',async()=>{
  const f=fixture();f.app.battle=true;f.app.phase='battle';f.app.battleState={ladderID:'match-a'};
  const result={revision:30,snapshot:{phase:'result',self:{id:'player_a'},result:{id:'match-a'}}};
  f.api.apply(result);assert.equal(f.api.resultPending('match-a'),true);
  f.api.presentResult('match-a');assert.equal(f.api.state.open,false);
  await f.api.perform('ack');assert.equal(f.calls.length,0);
  f.app.battle=false;f.app.battleState.ladderResultReady=true;
  f.api.presentResult('match-a');assert.equal(f.api.state.open,true);
  f.api.setOpen(false);f.api.receive(result,f.app.transport);
  assert.equal(f.api.state.open,false,'replayed result must not reopen a manually hidden panel');
  assert.equal(f.calls.length,0,'hiding is not ACK');
  f.respond(async()=>({ok:true,revision:31,snapshot:{phase:'lobby',self:{id:'player_a',ready:false},room:{id:'retained'}}}));
  await f.api.perform('ack');assert.equal(JSON.parse(f.calls[0].body).request.operation,'ack');
  assert.equal(f.api.state.envelope.snapshot.room.id,'retained');
});

test('ordered result on relogin opens without a battle, but an old transport cannot do so',()=>{
  const f=fixture(),result={revision:30,snapshot:{phase:'result',self:{id:'player_a'},result:{id:'m1'}}};
  f.api.receive(result,{id:'old'});assert.equal(f.api.state.envelope.revision,10);
  f.api.receive(result,f.app.transport);assert.equal(f.api.state.open,true);
  assert.equal(f.api.state.envelope.snapshot.result.id,'m1');
});

test('closing the countdown does not suppress the final result for the same match',()=>{
  const f=fixture(),match={id:'same-match'};
  f.api.apply({revision:20,snapshot:{phase:'countdown',self:{id:'player_a'},match}});
  assert.equal(f.api.state.open,true,'a match announces its countdown');
  f.api.setOpen(false);
  f.api.apply({revision:21,snapshot:{phase:'countdown',self:{id:'player_a'},match}});
  assert.equal(f.api.state.open,false,'countdown updates preserve manual dismissal');
  const result={revision:30,snapshot:{phase:'result',self:{id:'player_a'},result:match}};
  f.api.apply(result);
  assert.equal(f.api.state.open,false,'result polling before EN cannot open the result');
  f.app.battle=true;f.app.phase='battle';f.app.battleState={ladderID:match.id};
  f.api.receive(result,f.app.transport);
  assert.equal(f.api.state.open,false,'native result still waits for the movie');
  f.app.battle=false;f.app.phase='world';f.app.battleState.ladderResultReady=true;
  f.api.presentResult(match.id);
  assert.equal(f.api.state.open,true,'the result is a separate notification from countdown');
  f.api.setOpen(false);f.api.receive(result,f.app.transport);
  assert.equal(f.api.state.open,false,'duplicate results do not undo manual dismissal');
});

test('a polled result before EN cannot be acknowledged or released by an unrelated ordered packet',async()=>{
  const f=fixture(),result={revision:30,snapshot:{phase:'result',self:{id:'player_a'},result:{id:'m1'}}};
  f.api.state.open=true;f.api.apply(result);
  assert.equal(f.api.resultPending('m1'),true);
  await f.api.perform('ack');assert.equal(f.calls.length,0);
  f.api.receive({revision:20,snapshot:{phase:'countdown',match:{id:'m1'}}},f.app.transport);
  f.api.receive({...result,event:'result_lookup'},f.app.transport);
  assert.equal(f.api.resultPending('m1'),true,'older packets and history cannot confirm current result ordering');
  await f.api.perform('ack');assert.equal(f.calls.length,0);
  f.api.receive(result,f.app.transport);
  assert.equal(f.api.resultPending('m1'),false,'ordered result without a battle supports relogin/technical interruption');
  await f.api.perform('ack');assert.equal(JSON.parse(f.calls[0].body).request.operation,'ack');
});

test('unverified or recreated contacts require a new card exchange',async()=>{
  for(const code of ['contact_identity_required','contact_identity_changed']){
    const f=fixture();
    f.respond(async()=>({ok:false,code,revision:11,snapshot:{phase:'lobby',self:{id:'player_a'}}}));
    await f.api.perform('invite','0:pc1_selected');
    assert.match(f.api.state.error,/重新交换名片/);
    assert.equal(f.api.state.pending,null,'a definitive rejection must not become an unknown write');
    assert.equal(f.calls.length,1);
  }
});

test('contact reads preserve gameplay and invite retries retain the selected identity',async()=>{
  const f=fixture();
  const selected={slot:3,id:'pc1_selected',name:'阿布',online:true};
  f.respond(async()=>({ok:true,event:'contacts_lookup',revision:10,contacts:[selected],snapshot:{self:{id:'player_a'}}}));
  await f.api.refreshContacts();
  assert.equal(JSON.parse(f.calls[0].body).request.operation,'contacts');
  assert.equal(f.api.state.envelope.snapshot.phase,'lobby');
  assert.equal(f.api.state.contacts[0].id,'pc1_selected');
  f.respond(async()=>{throw new Error('lost response');});
  await f.api.perform('invite',`${selected.slot}:${selected.id}`);
  const original=JSON.parse(f.calls[1].body).request;
  f.api.state.contacts=[{...selected,id:'pc1_replacement'}];
  f.respond(async()=>({ok:false,code:'contact_slot_changed',revision:11,snapshot:{phase:'lobby',self:{id:'player_a'}}}));
  await f.api.perform('invite','',true);
  assert.equal(JSON.parse(f.calls[2].body).request.argument,original.argument);
  assert.match(f.api.state.error,/刷新名片/);
  assert.equal(f.api.state.pending,null);
});

test('contact refresh retains a selected identity but clears a reused slot',async()=>{
  const f=fixture(),selected={slot:3,id:'pc1_selected',name:'阿布',online:true};
  f.api.state.selectedContact='3:pc1_selected';
  f.respond(async()=>({ok:true,event:'contacts_lookup',revision:10,contacts:[selected],snapshot:{self:{id:'player_a'}}}));
  await f.api.refreshContacts();
  assert.equal(f.api.state.selectedContact,'3:pc1_selected');
  f.respond(async()=>({ok:true,event:'contacts_lookup',revision:10,contacts:[{...selected,id:'pc1_replacement'}],snapshot:{self:{id:'player_a'}}}));
  await f.api.refreshContacts();
  assert.equal(f.api.state.selectedContact,'','slot reuse must not select the replacement player');
});

test('response from retired transport does not mutate a newly logged in character',async()=>{
  const f=fixture();let release;
  f.respond(()=>new Promise(resolve=>{release=resolve;}));
  const action=f.api.perform('queue');
  f.app.transport={id:'b',base:'',closed:false,control:{generation:1}};
  f.api.state.session=f.app.transport;
  f.api.state.envelope={revision:2,snapshot:{phase:'idle',self:{id:'player_b'}}};
  release({revision:30,ok:true,snapshot:{phase:'queued'}});await action;
  assert.equal(f.api.state.envelope.snapshot.self.id,'player_b');
});

test('character logout on the same transport fences old replies and preserves identity-scoped requests',async()=>{
  const f=fixture();let finishA,finishB;
  f.respond(()=>new Promise(resolve=>{finishA=resolve;}));
  const actionA=f.api.perform('queue');
  const pendingA=JSON.parse(f.calls.at(-1).body).request;
  f.api.resetCharacter();
  assert.equal(f.api.state.pending,null);
  f.api.apply({ok:true,revision:1,snapshot:{phase:'idle',self:{id:'player_b'}}});
  assert.equal(f.api.state.envelope.snapshot.self.id,'player_b');
  f.respond(()=>new Promise(resolve=>{finishB=resolve;}));
  const actionB=f.api.perform('create',1);
  const pendingB=f.api.state.pending.request_id;
  finishA({ok:true,revision:100,control:{mode:'battle',generation:99},snapshot:{phase:'queued',self:{id:'player_a'}}});
  await actionA;
  assert.equal(f.api.state.envelope.snapshot.self.id,'player_b');
  assert.equal(f.api.state.pending.request_id,pendingB);
  assert.equal(f.api.state.busy,true,'old completion must not unlock the new character request');
  assert.equal(f.app.transport.control.generation,3);
  finishB({ok:true,revision:2,snapshot:{phase:'lobby',self:{id:'player_b'}}});
  await actionB;
  assert.equal(f.api.state.pending,null);
  assert.equal(f.saved.size,1,'original character uncertainty remains persisted');
  f.api.resetCharacter();
  f.api.apply({ok:true,revision:101,snapshot:{phase:'queued',self:{id:'player_a'}}});
  assert.equal(f.api.state.pending.request_id,pendingA.request_id);
  assert.equal(f.api.state.pending.revision,pendingA.revision);
});

test('restored unconfirmed request must be reconciled before any new action',async()=>{
  const f=fixture();f.api.state.envelope=null;
  f.saved.set('stoneage.ladder.request:line:player_a',JSON.stringify({request_id:'original',revision:8,operation:'ready',argument:''}));
  await f.api.perform('queue');
  assert.equal(f.calls.length,1,'only a read is sent while recovering');
  assert.equal(JSON.parse(f.calls[0].body).request.operation,'status');
  assert.equal(f.api.state.pending.request_id,'original');
});

test('polling an unchanged snapshot never restarts the authoritative countdown',()=>{
  const f=fixture();
  const envelope={revision:11,server_time_ms:100000,snapshot:{phase:'countdown',match:{start_at_ms:110000}}};
  f.api.apply(envelope);
  assert.match(f.api.countdownText(),/10 秒/);
  f.advance(4000);f.api.apply({...envelope});
  assert.match(f.api.countdownText(),/6 秒/);
  f.advance(6000);f.api.apply({...envelope});
  assert.match(f.api.countdownText(),/0 秒/);
});

test('strategy selection uses the new control generation and ignores a delayed older lease',async()=>{
  const f=fixture();
  f.respond(async()=>({ok:true,revision:11,control:{mode:'battle',generation:4},automation_active:true,snapshot:{phase:'lobby',self:{id:'player_a',strategy:'basic'}}}));
  await f.api.perform('ready');
  assert.equal(f.app.transport.control.generation,4);
  f.respond(async()=>({ok:true,revision:12,control:{mode:'manual',generation:5},snapshot:{phase:'battle',self:{id:'player_a',strategy:'manual'}}}));
  await f.api.perform('strategy','manual');
  assert.equal(JSON.parse(f.calls[1].body).generation,4);
  assert.equal(JSON.parse(f.calls[1].body).request.argument,'manual');
  assert.equal(f.app.transport.control.generation,5);
  f.respond(async()=>({ok:true,revision:11,control:{mode:'battle',generation:4},snapshot:{phase:'lobby',self:{id:'player_a',strategy:'basic'}}}));
  await f.api.status();
  assert.equal(f.app.transport.control.generation,5);
  assert.equal(f.api.state.envelope.snapshot.self.strategy,'manual');
});

test('accepted readiness with unavailable automation is confirmed and displays the host error',async()=>{
  const f=fixture();
  f.respond(async()=>({ok:true,revision:11,automation_error:'strategy unavailable',snapshot:{phase:'lobby',self:{id:'player_a',ready:true}}}));
  await f.api.perform('ready');
  assert.equal(f.api.state.pending,null);
  assert.match(f.api.state.error,/strategy unavailable/);
  assert.equal(f.api.state.envelope.snapshot.self.ready,true);
});

test('an uncertain takeover retains both its own request and an earlier uncertain mutation',async()=>{
  const f=fixture();f.respond(async()=>{throw new Error('lost response');});
  await f.api.perform('queue');const first=f.api.state.pending;
  const takeover={request_id:'takeover',revision:11,operation:'strategy',argument:'manual'};
  f.api.retainUnconfirmed(takeover);f.api.retainUnconfirmed(takeover);
  assert.equal(f.api.state.pending.request_id,first.request_id);
  assert.equal(f.api.state.pendingQueue.length,1);
  // Reload the same character before retrying either request.
  f.api.state.envelope=null;f.api.state.pending=null;f.api.state.pendingQueue=[];
  f.api.apply({revision:12,snapshot:{phase:'battle',self:{id:'player_a'}}});
  assert.equal(f.api.state.pending.request_id,first.request_id);
  assert.equal(f.api.state.pendingQueue[0].request_id,'takeover');
  f.respond(async()=>({ok:true,revision:12,snapshot:{phase:'battle',self:{id:'player_a'}}}));
  await f.api.perform('queue','',true);
  assert.equal(f.api.state.pending.request_id,'takeover');
  await f.api.perform('strategy','manual',true);
  assert.deepEqual(JSON.parse(f.calls.at(-1).body).request,takeover);
  assert.equal(f.api.state.pending,null);assert.equal(f.saved.size,0);
});

test('takeover preceding the first status merges an older persisted request',()=>{
  const f=fixture();f.api.state.envelope=null;
  const older={request_id:'old',revision:9,operation:'queue',argument:''};
  f.saved.set('stoneage.ladder.request:line:player_a',JSON.stringify(older));
  f.api.retainUnconfirmed({request_id:'takeover',revision:11,operation:'strategy',argument:'manual'});
  f.api.apply({revision:12,snapshot:{phase:'battle',self:{id:'player_a'}}});
  assert.equal(f.api.state.pending.request_id,'old');
  assert.equal(f.api.state.pendingQueue[0].request_id,'takeover');
});
