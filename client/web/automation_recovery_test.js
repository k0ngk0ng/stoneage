const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const source = fs.readFileSync(__dirname + '/runtimeassets/automation.js', 'utf8');
function fixture(fetcher) {
 const requests=[];
 const transport={id:'new-session',base:'',closed:false,setControl(value){this.control=value;return value;}};
 const app={transport}; const root={StoneAgeWebClient:{app}};
 vm.runInNewContext(source,{window:root,fetch:async(url,options)=>{requests.push({url,options});return fetcher?fetcher(url,options):{ok:true,json:async()=>({control:{generation:2,mode:'quest'},automation_active:true,automation_mode:'quest',automation_recovery:null})};}});
 return {app,transport,requests,api:root.StoneAgeAutomation};
}
test('reconnected manual session explicitly resumes the offered quest handle',async()=>{
 const {api,requests}=fixture();
 api.updateFromTransport({control:{generation:1,mode:'manual'},automation_recovery:{handle:'old-task',mode:'quest'}});
 assert.equal(api.currentControl().recovery.handle,'old-task');
 assert.equal(requests.length,0);
 api.state.lastError='previous uncertain recovery';
 await api.resume();
 assert.equal(api.state.lastError,'');
 assert.match(requests[0].url,/\/new-session\/resume$/);
 const body=JSON.parse(requests[0].options.body);
 assert.equal(body.recovery_handle,'old-task');assert.equal(body.mode,'quest');assert.equal(body.generation,1);
 assert.equal(api.currentControl().recovery,null);
 assert.equal(api.currentControl().automationActive,true);
});
test('normalizing a paused quest repeatedly preserves its mode',async()=>{
 const {api,requests}=fixture();
 api.updateFromTransport({control:{generation:1,mode:'paused'},automation_active:true,automation_mode:'quest'});
 assert.equal(api.currentControl().automationMode,'quest');
 await api.resume();
 const body=JSON.parse(requests[0].options.body);
 assert.equal(body.mode,'quest');assert.equal(body.recovery_handle,undefined);
});
test('explicit discard names the old handle and current generation',async()=>{
 const {api,requests}=fixture();
 api.updateFromTransport({control:{generation:1,mode:'manual'},automation_recovery:{handle:'old-task',mode:'leveling'}});
 await api.takeover();
 const body=JSON.parse(requests[0].options.body);
 assert.equal(body.recovery_handle,'old-task');assert.equal(body.generation,1);
});
test('late response from prior connection cannot replace current controls',async()=>{
 let resolve;
 const {api,app}=fixture(()=>new Promise(r=>{resolve=r;}));
 api.updateFromTransport({control:{generation:10,mode:'paused'},automation_mode:'quest'});
 const pending=api.resume();
 app.transport={id:'replacement',base:'',control:{generation:1,mode:'manual'}};
 api.updateFromTransport(app.transport.control);
 resolve({ok:true,json:async()=>({control:{generation:11,mode:'quest'},automation_active:true})});
 await assert.rejects(pending,/会话已变化/);
 assert.equal(api.currentControl().generation,1);assert.equal(api.currentControl().mode,'manual');
});
test('HTTP transport retains nested recovery offers and clears them after handoff',()=>{
 const html=fs.readFileSync(__dirname+'/index.html','utf8');
 const start=html.indexOf('  class HTTPTransport');
 const end=html.indexOf("  /* Keep the browser's logical direction",start);
 assert(start>=0 && end>start);
 const context={app:{}};
 vm.runInNewContext(html.slice(start,end)+'\nglobalThis.Transport=HTTPTransport;',context);
 const transport=new context.Transport();
 transport.setControl({control:{control:{generation:1,mode:'manual'},automation_recovery:{handle:'old-task',mode:'quest'}}});
 assert.equal(transport.control.automation_recovery.handle,'old-task');
 transport.setControl({control:{generation:2,mode:'quest'},automation_recovery:null,automation_active:true});
 assert.equal(transport.control.automation_recovery,null);
 transport.setControl({control:{control:{generation:1,mode:'manual'},automation_recovery:{handle:'old-task',mode:'quest'}}});
 assert.equal(transport.control.generation,2);assert.equal(transport.control.automation_recovery,null);
});
