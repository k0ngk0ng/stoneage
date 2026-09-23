'use strict';
const assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const html=fs.readFileSync(__dirname+'/runtimeassets/index.html','utf8');
function section(a,b){const start=html.indexOf(a),end=html.indexOf(b,start);assert(start>=0&&end>start);return html.slice(start,end);}
const images=new Map(),calls=[];
const manifest={bitmap_aliases:{40511:'242327',15431:'338263'},bitmaps:{
  242327:{bmp_number:40511,file:'sky.png'},338263:{bmp_number:15431,file:'crystal.png'},
  15538:{bmp_number:0,file:'actor.png'},26001:{bmp_number:26001,file:'ground.png'},
}};
const h={assetState:{manifest},loadAsset(file,options){assert.equal(options.map,true);if(!images.has(file))images.set(file,{complete:false,naturalWidth:0});return images.get(file);}};
vm.createContext(h);
vm.runInContext(section('  function resolveMapBitmapInfo(', '  function resolveBitmapInfo(')+section('  function skyBackground(', '  function drawWorldGroundToBuffer('),h);
assert.equal(h.resolveMapBitmapInfo(15431).file,'crystal.png');
assert.equal(h.resolveMapBitmapInfo(15538),null,'physical actor frame must not be used as map art');
manifest.bitmap_aliases[15538]='15538';
assert.equal(h.resolveMapBitmapInfo(15538),null,'an alias must also match the logical graphic');
manifest.bitmap_aliases[26001]='missing-physical-entry';
assert.equal(h.resolveMapBitmapInfo(26001).file,'ground.png','older manifests store valid records under logical IDs');
assert.equal(h.resolveMapBitmapInfo('3c47'),null,'map graphics do not use character hex/base62 guessing');
assert.equal(h.resolveMapBitmapInfo(99),null);
const map={floor:5581,fullWidth:150,fullHeight:150,tiles:[0],width:1,height:1};
let sky=h.skyBackground(map);
assert.equal(sky.ready,false,'empty DAT must wait for the separate background');
const ctx={canvas:{width:640,height:480},drawImage(...args){calls.push(args);}};
assert.equal(h.drawSkyBackground(ctx,map,[75,75],sky),false);
Object.assign(sky.image,{complete:true,naturalWidth:1604,naturalHeight:1204});
sky=h.skyBackground(map);
assert.equal(sky.ready,true);
h.drawSkyBackground(ctx,map,[75,75],sky);
assert.deepEqual(calls[0].slice(1),[482,362,640,480,0,0,640,480]);
h.drawSkyBackground(ctx,map,[-1000,1000],sky);
assert.deepEqual(calls[1].slice(1,5),[0,724,640,480],'source rectangle stays inside the original image');
assert.equal(h.skyBackground({floor:104}).image,sky.image,'shared background reuses cached loader image');
assert.equal(images.size,1);
assert.equal(h.skyBackground({floor:30022}),null,'leaving the sky floor must not retain its background');
assert.equal(h.drawSkyBackground(ctx,{floor:30022},[0,0],null),false);
// Verify that loading readiness uses the same dependency as the renderer.
Object.assign(h,{app:{mapLoading:true,map,phase:'world'},$:()=>null,renderMapLoadingProgress(){},mapPaletteNumber:()=>-1,mapPaletteIsPending:()=>false,
  setMapLoading(active,message){h.message=message;},fieldBootstrapFrameReadiness:()=>({ready:false,total:1,pending:1})});
h.assetState.manifestReady=true;h.assetState.fieldBootstrapSpritesReady=true;
vm.runInContext(section('  function maybeFinishMapLoading(', '  function send('),h);
sky.image.complete=false;
h.maybeFinishMapLoading();assert.match(h.message,/天空背景/);
sky.image._assetFailed=true;h.maybeFinishMapLoading();assert.match(h.message,/天空背景加载失败/);
sky.image.complete=true;sky.image._assetFailed=false;h.maybeFinishMapLoading();assert.match(h.message,/人物/);
console.log('logical map graphics, empty sky floor, camera crop, loading failure and floor transition passed');
