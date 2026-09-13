'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const html = fs.readFileSync(__dirname + '/index.html', 'utf8');
function section(start, end) { return html.slice(html.indexOf(start), html.indexOf(end, html.indexOf(start))); }
(async () => {
  const state = {images: new Map(), manifestBytes: 111, manifestTotalBytes: 222}, samples = [];
  let requestCount = 0;
  const context = {
    assetState: state, Uint8Array, performance: {now: () => 1000},
    Image: class {},
    fetch: async () => {requestCount++;return new Response(new Uint8Array([1,2,3,4,5]));},
    app: {}, rememberAssetResourceSize() {}, assetNetworkBytes() {return 0;},
    assetURL: file => `https://cdn.example/stoneage/assets/${file}`,
    prepareAssetSource: async file => `https://cdn.example/stoneage/assets/${file}`,
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
  const binaryProgressBefore = {manifestBytes: state.manifestBytes, manifestTotalBytes: state.manifestTotalBytes};
  const binaryChunks = [new Uint8Array([6,7]), new Uint8Array([8,9,10])];
  const binaryResponse = {headers: new Headers({'Content-Length':'5'}), body: {getReader: () => ({read: async () => binaryChunks.length ? {value: binaryChunks.shift()} : {done:true}})}};
  const binary = await context.readAssetResponseBytes(binaryResponse, null, null);
  assert.equal(binary.byteLength, 5);
  assert.deepEqual({manifestBytes: state.manifestBytes, manifestTotalBytes: state.manifestTotalBytes}, binaryProgressBefore, 'binary indexes must not overwrite phase-specific progress');
  assert.equal(state.receivedBytes, 3077, 'binary indexes must still contribute their actual bytes');
  const beforeImageBytes = state.receivedBytes;
  const image = context.loadAsset('actor.png');
  assert.equal(context.loadAsset('actor.png'), image, 'concurrent actor requests share the same image');
  for(let i=0;i<30;i++) await Promise.resolve();
  assert.equal(requestCount, 0, 'fixed image URLs must not require a page-side fetch for byte accounting');
  assert.equal(state.receivedBytes, beforeImageBytes, 'fixed image URLs must not trigger a duplicate page download');
  assert.equal(image.src, 'https://cdn.example/stoneage/assets/actor.png');
  assert.equal(image.src.startsWith('blob:'), false, 'shared images must never use temporary Blob URLs');
  image.onload();
  console.log('resource progress: streaming bytes, compressed totals, fixed actor URLs and no duplicate image fetch passed');
})().catch(error => {console.error(error);process.exitCode=1;});
