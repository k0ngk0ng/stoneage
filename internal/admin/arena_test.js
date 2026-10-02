"use strict";
const assert = require("node:assert/strict"), fs = require("node:fs"), vm = require("node:vm");
class Element {
  constructor() { this.children = []; this.textContent = ""; this.disabled = false; this.listeners = {}; }
  replaceChildren() { this.children = []; }
  appendChild(child) { this.children.push(child); }
  addEventListener(event, fn) { this.listeners[event] = fn; }
}
const nodes = new Map(["monitor","status","counts","queues","matches","prev","next","page"].map(id => [`arena-${id}`,new Element()]));
const document = {hidden:false,getElementById:id=>nodes.get(id),createElement:()=>new Element(),addEventListener() {}};
let interval, calls=0, fail=false;
const member = {name:"<img src=x onerror=alert(1)>",online:true,strategy:"learned"};
const fixture = {at_ms:1000000,queued_total:9,active_total:1,page_size:8,queues:[{id:"q",mode:1,wait_ms:3000,members:[member]}],matches:[{id:"m",mode:2,phase:"battle",turn:7,elapsed_ms:15000,teams:[{members:[member]},{members:[{name:"对手",online:false}]}]}]};
const context = {document,AbortController,Date,Math,Error,setTimeout,clearTimeout,setInterval:fn=>{interval=fn},fetch:async()=>{calls++;return {ok:!fail,json:async()=>fail?{error:"游戏不可用"}:fixture}}};
vm.runInNewContext(fs.readFileSync(__dirname+"/static/arena.js","utf8"),context);
const tick = () => new Promise(resolve=>setImmediate(resolve));
(async()=>{
 await tick();assert.equal(calls,1);assert.equal(nodes.get("arena-queues").children[0].children[3].textContent,"<img src=x onerror=alert(1)> [learned]");
 assert.match(nodes.get("arena-matches").children[0].children[2].textContent,/第 7 回合/);
 assert.equal(nodes.get("arena-next").disabled,false);
 await interval();assert.equal(calls,2);
 document.hidden=true;await interval();assert.equal(calls,2);document.hidden=false;
 fail=true;await interval();assert.match(nodes.get("arena-status").textContent,/上次快照/);assert.equal(nodes.get("arena-queues").children.length,1);
 console.log("arena monitor rendering, polling, safe text and stale-state tests passed");
})().catch(error=>{console.error(error);process.exitCode=1});
