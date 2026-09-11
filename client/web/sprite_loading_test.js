"use strict";
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");
const html = fs.readFileSync(__dirname + "/index.html", "utf8");
function section(start, end) {
  const a = html.indexOf(start), b = html.indexOf(end, a);
  assert(a >= 0 && b > a);
  return html.slice(a, b);
}
function harness() {
  const calls = [], timers = [];
  let fail = true;
  const state = {images: new Map(), manifestReady: true};
  const context = {
    assetState: state, Promise, TextDecoder,
    app: {phase: "world", mapLoading: true, mapLoadingStats: {}, map: {tiles: [1]}},
    FIELD_BOOTSTRAP_SPRITE_MANIFEST_URL: "bootstrap", FIELD_SPRITE_MANIFEST_URL: "field", SPRITE_MANIFEST_URL: "full",
    fetch: async url => {
      calls.push(url);
      if (fail) throw new Error("network unavailable");
      return {ok: true};
    },
    readAssetResponseBytes: async () => new TextEncoder().encode('{"sprites":{"100025":{}}}'),
    addEvent() {}, renderBattleWorld() {}, renderMapLoadingProgress() {}, syncMapLoadingVisibility() {}, scheduleMapLoadingProgress() {},
    window: {setTimeout: fn => timers.push(fn), clearTimeout() {}},
    fieldBootstrapFrameReadiness: () => ({ready: false, total: 1, pending: 1}),
    $: () => null, mapPaletteNumber: () => -1, mapPaletteIsPending: () => false,
    renderWorld() {context.maybeFinishMapLoading();},
  };
  vm.createContext(context);
  vm.runInContext(section("  function setMapLoading(", "  function fieldBootstrapFrameReadiness(") + section("  function loadFieldBootstrapSpriteManifest(", "  function albumStorageKey(") + section("  function maybeFinishMapLoading(", "  function send("), context);
  return {context, state, calls, timers, recover() {fail = false;}};
}
function emptyMapProgressHarness() {
  const timers = [];
  let renders = 0;
  const context = {
    app: {mapLoading: true},
    assetState: {manifestReady: true, fieldBootstrapSpritesReady: true},
    mapLoadingProgress: () => ({total: 0, pending: 0, failed: 0}),
    renderMapLoadingProgress() {},
    renderWorld() {renders++;context.app.mapLoading = false;},
    window: {clearTimeout() {}, setTimeout(callback) {timers.push(callback);return timers.length;}},
  };
  vm.createContext(context);
  vm.runInContext(`let mapLoadingProgressTimer=0;${section("  function scheduleMapLoadingProgress(", "  function renderMapLoadingProgress(")}`, context);
  return {context, timers, get renders() {return renders;}};
}
async function settle() {for (let i = 0; i < 20; i++) await Promise.resolve();}
(async () => {
  const h = harness();
  h.context.setMapLoading(true);
  await settle();
  assert.deepEqual(h.calls, ["bootstrap", "full"], "failure-triggered repaint must not restart downloads");
  for (let i = 0; i < 100; i++) {
    h.context.setMapLoading(true);
    await h.context.loadFieldBootstrapSpriteManifest();
    await h.context.loadSpriteManifest();
  }
  assert.deepEqual(h.calls, ["bootstrap", "full"], "render/menu callers must respect failed state");
  assert.equal(h.timers.length, 0, "large failed manifests must not schedule endless retries");
  h.recover();
  h.state.images.set("failed-actor.png", {_assetFailed:true});
  h.state.images.set("ready-actor.png", {complete:true});
  h.context.retryMapLoading();
  assert.equal(h.state.images.has("failed-actor.png"), false, "manual retry must evict failed actor frames as well as map tiles");
  assert.equal(h.state.images.has("ready-actor.png"), true, "manual retry preserves decoded actor frames");
  const pending = h.context.loadFieldBootstrapSpriteManifest();
  assert.equal(pending, h.context.loadFieldBootstrapSpriteManifest(), "concurrent requests share one promise");
  await pending;
  assert.equal(h.state.fieldBootstrapSpritesReady, true);
  assert.deepEqual(h.calls, ["bootstrap", "full", "bootstrap"], "manual retry recovers using only the compact table");
  const fallback = harness();
  await fallback.context.loadFieldSpriteManifest();
  await fallback.context.loadFieldSpriteManifest();
  assert.deepEqual(fallback.calls, ["field", "full", "bootstrap"], "failed action table is not fetched on each render");
  const emptyMap = emptyMapProgressHarness();
  emptyMap.context.scheduleMapLoadingProgress();
  assert.equal(emptyMap.timers.length, 1, "map loading progress should schedule its watchdog");
  emptyMap.timers.shift()();
  assert.equal(emptyMap.renders, 1, "an empty map must still repaint after bootstrap actor decoding");
  console.log("sprite loading regression tests passed");
})().catch(error => {console.error(error); process.exitCode = 1;});
