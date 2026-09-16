"use strict";
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");
const source = fs.readFileSync(__dirname + "/static/ai.js", "utf8");

function fixture({available=true,canWrite=true}={}) {
  const notices = {hidden:true,textContent:"",classList:{toggle(){}}};
  const root = {dataset:{canWrite:String(canWrite),connectionTestAvailable:String(available),csrf:"fixture-csrf"},querySelector(){return notices;}};
  function button(id) { return {dataset:{id,modelName:id},textContent:"测试",disabled:false,setAttribute(){},addEventListener(type,fn){this[type]=fn;}}; }
  const buttons=[button("model-1"),button("model-2")];
  const requests=[];
  let respond;
  const context={document:{getElementById(id){return id==="ai-model-admin"?root:null;},querySelectorAll(selector){return selector===".ai-test-model"?buttons:[];}},window:{},fetch(url,options){requests.push({url,options});return new Promise(resolve=>{respond=resolve;});}};
  vm.runInNewContext(source,context);
  return {buttons,notices,requests,respond(value,status=200){respond({ok:status<400,json:async()=>value});}};
}

(async()=>{
  const f=fixture();
  const first=f.buttons[0].click();
  assert.equal(f.buttons[0].textContent,"测试中…");
  assert(f.buttons.every(b=>b.disabled));
  await f.buttons[1].click();
  assert.equal(f.requests.length,1,"must not send a second concurrent probe");
  assert.equal(f.requests[0].url,"/api/ai/models/model-1/test");
  assert.equal(f.requests[0].options.method,"POST");
  assert.equal(f.requests[0].options.headers["X-CSRF-Token"],"fixture-csrf");
  f.respond({ok:true,duration_ms:1250});await first;
  assert.match(f.notices.textContent,/model-1.*测试成功.*1\.3 秒/);
  assert(f.buttons.every(b=>!b.disabled));
  assert.equal(f.buttons[0].textContent,"测试");

  const failed=f.buttons[0].click();
  f.respond({error:"模型服务拒绝认证，请检查 API Key。",code:"authentication"},502);await failed;
  assert.match(f.notices.textContent,/拒绝认证/);
  assert(!f.buttons[0].disabled);

  const invalid=f.buttons[0].click();f.respond({});await invalid;
  assert.match(f.notices.textContent,/未收到有效测试结果/);
  assert.doesNotMatch(f.notices.textContent,/测试成功/);
  for(const options of [{available:false},{canWrite:false}]) {
    const blocked=fixture(options);await blocked.buttons[0].click();
    assert.equal(blocked.requests.length,0);
    assert(blocked.buttons.every(b=>b.disabled));
  }
  console.log("AI model test UI: pending, duplicate prevention, success timing, errors and permissions passed");
})().catch(e=>{console.error(e);process.exitCode=1;});
