'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const html = fs.readFileSync(__dirname + '/index.html', 'utf8');
function section(start, end) { return html.slice(html.indexOf(start), html.indexOf(end, html.indexOf(start))); }
(async () => {
  const state = {images: new Map()}, samples = [], urls = [], revoked = [];
  let requestCount = 0;
  const context = {
    assetState: state, Uint8Array, Blob, performance: {now: () => 1000},
    Image: class {}, URL: {createObjectURL(blob) {assert.equal(blob.size, 5);urls.push('blob:test');return 'blob:test';}, revokeObjectURL(url) {revoked.push(url);}},
    fetch: async () => {requestCount++;return new Response(new Uint8Array([1,2,3,4,5]));},
    app: {}, rememberAssetResourceSize() {}, assetNetworkBytes() {return 0;},
    settleMapAsset() {return false;}, renderMapLoadingProgress() {}, scheduleAssetRefresh() {},
  };
  vm.createContext(context);
  vm.runInContext(section('  async function readAssetResponseBytes(', '  function loadAssetManifest(') + section('  function loadAsset(file', '  function mapDefinition('), context);
  const chunks = [new Uint8Array(1024), new Uint8Array(2048)];
  const response = {headers: new Headers({'Content-Length':'30', 'Content-Encoding':'gzip'}), body: {getReader: () => ({read: async () => chunks.length ? {value: chunks.shift()} : {done:true}})}};
  const progress = {};
  const buffer = await context.readAssetResponseBytes(response, () => samples.push(state.receivedBytes), progress);
  assert.equal(buffer.byteLength, 3072);
  assert.equal(samples[0], 1024, 'received bytes must update before download completes');
  assert.equal(state.receivedBytes, 3072);
  assert.equal(progress.manifestTotalBytes, 3072, 'compressed Content-Length must not be used as decoded total');
  const image = context.loadAsset('actor.png');
  assert.equal(context.loadAsset('actor.png'), image, 'concurrent actor requests share the same image');
  for(let i=0;i<30;i++) await Promise.resolve();
  assert.equal(requestCount, 1, 'image progress must not require a second download');
  assert.equal(state.receivedBytes, 3077, 'actor image bytes are included');
  assert.equal(image.src, 'blob:test');
  image.onload();
  assert.deepEqual(revoked, ['blob:test'], 'decoded image must release its temporary object URL');
  console.log('resource progress: streaming bytes, compressed totals, actor images, deduplication and URL cleanup passed');
})().catch(error => {console.error(error);process.exitCode=1;});
