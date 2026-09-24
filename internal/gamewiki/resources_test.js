const {test}=require('node:test'),assert=require('node:assert/strict'),vm=require('node:vm'),fs=require('node:fs');
async function run(catalog){
  const requests=[],nodes={};const element=()=>({textContent:'',children:[],append(...n){this.children.push(...n);},replaceChildren(...n){this.children=n;}});
  nodes['resource-download']=element();nodes['media-config']={getAttribute(){return 'https://cdn.example/stoneage';}};
  const revision='a'.repeat(64);
  const context={URL,location:{href:'https://game.example/wiki/resources'},document:{getElementById:id=>nodes[id],createElement:element},fetch:async url=>{requests.push(String(url));return {ok:catalog!==null,json:async()=>String(url).endsWith('_client-version.json')?{revision}:catalog};}};
  await vm.runInNewContext(fs.readFileSync(__dirname+'/site/resources.js','utf8'),context);
  return {nodes,requests};
}
test('encyclopedia download uses current CDN revision without fetching ZIP bytes',async()=>{
  const revision='a'.repeat(64),path=`packs/${revision}/stoneage-resources.zip`;
  const {nodes,requests}=await run({revision,packages:[{path,bytes:1048576,unpacked_bytes:1234567}]});
  assert.equal(requests.length,2);assert(requests.every(u=>u.startsWith('https://cdn.example/stoneage/')));
  assert.equal(nodes['resource-download'].children[0].children[0].href,'https://cdn.example/stoneage/'+path);
});
test('missing, stale and unsafe download catalogs do not expose links',async()=>{
  for(const catalog of [null,{revision:'b'.repeat(64),packages:[{}]},{revision:'a'.repeat(64),packages:[{path:'https://evil.example/a.zip',bytes:1}]}]){
    const {nodes}=await run(catalog);assert.equal(nodes['resource-download'].children.length,0);assert(nodes['resource-download'].textContent.length>0);
  }
});
