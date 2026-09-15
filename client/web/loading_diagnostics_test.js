'use strict';
const assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const html=fs.readFileSync(__dirname+'/index.html','utf8');
const section=(a,b)=>html.slice(html.indexOf(a),html.indexOf(b,html.indexOf(a)));
assert.doesNotMatch(html,/url\(['"]?[^)]*bitmap_9112/,'loading surface must not request artwork');
const nodes=new Map();
function node(id){if(!nodes.has(id))nodes.set(id,{style:{removeProperty(){}},classList:{toggle(){}},setAttribute(){},textContent:'',hidden:true});return nodes.get(id);}
const url=f=>'https://cdn.example/assets/'+f;
const app={floor:100,phase:'world',mapLoading:true,mapLoadingStartedAt:1000,mapLoadingStats:{},mapLayerCache:{required:new Set(['failed.png','pending.png','ready.png']),failed:new Set(['failed.png'])},actors:new Map()};
const state={manifestReady:true,fieldBootstrapSpritesReady:true,images:new Map([['failed.png',{_assetFailed:true,_assetRequestedAt:1000}],['pending.png',{_assetPhase:'source',_assetRequestedAt:1000}],['ready.png',{complete:true,naturalWidth:10}]]),resourceErrors:new Map([[url('failed.png'),{status:404}]])};
let stats={total:3,done:2,loaded:1,failed:1,pending:1,manifestBytes:0,manifestTotalBytes:0,fieldBootstrapBytes:0,fieldBootstrapTotalBytes:0};
const c={app,assetState:state,$:node,document:{getElementById(){return null;}},mapLoadingProgress:()=>stats,formatLoadingBytes:String,Date:{now:()=>17000},Math,Number,String,Boolean,Set,assetURL:url,mapImageReady:i=>!!(i?.complete&&i.naturalWidth),ASSET_MANIFEST_URL:url('manifest.json'),FIELD_BOOTSTRAP_SPRITE_MANIFEST_URL:url('bootstrap.json')};
vm.createContext(c);vm.runInContext(section('  function mapLoadingDiagnosticText(', '  function syncMapLoadingVisibility('),c);
const report=c.mapLoadingDiagnosticText();assert.match(report,/HTTP 404/);assert.match(report,/failed.png/);assert.match(report,/等待 16s · 等待本地缓存准备/);assert.doesNotMatch(report,/ready.png/);
c.renderMapLoadingProgress();assert.equal(node('world-loading-diagnostics').hidden,false);assert.match(node('world-loading-diagnostic-text').textContent,/failed.png/);
stats={...stats,total:1,done:1,failed:1,pending:0};c.renderMapLoadingProgress();assert.equal(node('world-loading-retry').hidden,false,'terminal failure must not hide recovery information');
console.log('Loading diagnostics identify failed HTTP resources and waiting cache preparation without retrying or fetching.');
