"use strict";
const assert=require("node:assert/strict"),fs=require("node:fs");
const source=fs.readFileSync(__dirname+"/static/ai.js","utf8");
const start=source.indexOf("    let recovery = null;");
const end=source.indexOf("    if (form && canWrite)",start);
assert(start>=0&&end>start);
class Element{
 constructor(){this.dataset={};this.handlers={};this.disabled=false;this.checked=false;this.textContent="";this.hidden=true;}
 addEventListener(name,handler){this.handlers[name]=handler;}
 scrollIntoView(){}
 fire(name="click"){return this.handlers[name]?.();}
}
const ids=["ai-recovery-panel","ai-recovery-ack","ai-recovery-submit","ai-recovery-message","ai-recovery-close","ai-recovery-summary"];
const nodes=Object.fromEntries(ids.map(id=>[id,new Element()]));
const open=new Element();open.dataset.id="p1";
const calls=[];
const api=(_root,url,method,body)=>new Promise((resolve,reject)=>calls.push({url,method,body,resolve,reject}));
new Function("document","api","profileRoot","canWrite","show","hide","message",source.slice(start,end))({getElementById:id=>nodes[id],querySelectorAll:()=>[open]},api,{},true,node=>{node.hidden=false},node=>{node.hidden=true},()=>{});
(async()=>{
 const status={profile_id:"p1",attempt_id:"attempt1",ready:true,reserved_tokens:17,execution:{request_id:"r1",state:"unknown"}};
 const loading=open.fire();calls[0].resolve({recovery:status});await loading;
 const ack=nodes["ai-recovery-ack"],submit=nodes["ai-recovery-submit"];
 assert(submit.disabled,"explicit acknowledgement is required");
 ack.checked=true;ack.fire("change");assert(!submit.disabled);
 const submitting=submit.fire();assert.equal(calls.length,2);assert(ack.disabled&&submit.disabled);
 ack.fire("change");assert(submit.disabled,"changing acknowledgement cannot enable duplicate POST");
 await submit.fire();assert.equal(calls.length,2);
 assert.equal(calls[1].body.reason,"accept_uncertain_outcome");assert.equal(calls[1].body.attempt_id,"attempt1");assert(!("actor" in calls[1].body));
 calls[1].reject(new Error("retry after interruption"));await submitting;assert(!submit.disabled&&!ack.disabled);
 const retry=submit.fire();calls[2].resolve({ok:true});await retry;
 assert(submit.disabled&&!ack.checked);assert.match(nodes["ai-recovery-message"].textContent,/未知结果和预算已保留/);
 await submit.fire();assert.equal(calls.length,3,"completed review cannot submit again");
 const stale=open.fire();nodes["ai-recovery-close"].fire();calls[3].resolve({recovery:status});await stale;
 assert(nodes["ai-recovery-panel"].hidden,"late GET cannot reopen a closed review panel");
 const running=open.fire();calls[4].resolve({recovery:{...status,ready:false,execution:{...status.execution,container_stopped:false}}});await running;
 assert.match(nodes["ai-recovery-summary"].textContent,/遗留模型容器尚未退出/);
 ack.checked=true;ack.fire("change");assert(submit.disabled,"a stopped profile cannot authorize review of a live container");
 await submit.fire();assert.equal(calls.length,5,"live-container status must not submit review");
 console.log("AI recovery acknowledgement, retry and stale-response tests passed");
})().catch(error=>{console.error(error);process.exitCode=1});
