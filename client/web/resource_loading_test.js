"use strict";
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");
const html = fs.readFileSync(__dirname + "/runtimeassets/index.html", "utf8");
function section(start, end) {
  const a = html.indexOf(start), b = html.indexOf(end, a);
  assert(a >= 0 && b > a);
  return html.slice(a, b);
}
function mapHarness(index, validFile) {
  const calls = [];
  const buffer = new ArrayBuffer(14);
  const view = new DataView(buffer);
  view.setInt32(0, 1, true);view.setInt32(4, 1, true);
  const context = {
    app: {floor: 200, phase: "login"}, autoMapDataCache: new Map(), autoMapDataRequests: new Map(),
    DataView, Uint16Array, renderMapDetails() {}, addEvent() {},
    fetch: async url => {
      calls.push(url);
      // Include invalid 200 responses to exercise parse failures, not just 404s.
      return {ok: true, arrayBuffer: async () => url.endsWith(validFile) ? buffer : new ArrayBuffer(0)};
    },
  };
  vm.createContext(context);
  const source = section("  function parseAutoMapData(", "  function parseAutoMapColorTable(")
    .replace("const AUTO_MAP_FILES={};", "const AUTO_MAP_FILES=" + JSON.stringify(index) + ";");
  vm.runInContext(source, context);
  return {context, calls};
}
function audioHarness() {
  const audios = [], connected = [];
  class Audio {
    constructor(...args) {assert.equal(args.length, 0, "src must not start loading before CORS is set");audios.push(this);this.events = {};}
    set src(value) {assert.equal(this.crossOrigin, "anonymous");this.url = value;}
    setAttribute() {}
    addEventListener(name, handler) {this.events[name] = handler;}
    play() {return Promise.resolve();}
    remove() {this.removed = true;}
  }
  const context = {
    Audio, app: {systemSettings: {se: true, seStereo: true}},
    soundFileForTone: () => "effect.wav", audioURL: () => "https://cdn.example/audio/se/effect.wav", seVolume: () => .5,
    soundState: {unlocked: true, activeSE: new Set(), context: {
      destination: {},
      createMediaElementSource(audio) {connected.push(audio);return {connect: panner => panner, disconnect() {}};},
      createStereoPanner() {return {pan: {value: 0}, connect() {}, disconnect() {}};},
    }},
  };
  vm.createContext(context);
  vm.runInContext(section("  function playSoundEffect(", "  function mapMusicFromWindow("), context);
  context.playSoundEffect(217);
  assert.equal(connected[0], audios[0]);
  assert.equal(audios[0].url, "https://cdn.example/audio/se/effect.wav");
  audios[0].events.ended();
  assert.equal(context.soundState.activeSE.size, 0);
  assert.equal(audios[0].removed, true);
}
async function manifestHarness() {
  const calls = [], timers = new Map();let timerId = 0, resolveVersion;
  const context = {
    URL, Promise, AbortController, Date, TextDecoder, Response, Blob, Request, Headers, ReadableStream, DOMException,
    assetVersionPromise: new Promise(resolve => {resolveVersion = resolve;}),
    assetState: {}, ASSET_MANIFEST_URL: "https://cdn.example/stoneage/assets/manifest.json",
    window: {location: {href: "https://game.example/"},
      setTimeout(fn) {timers.set(++timerId, fn);return timerId;}, clearTimeout(id) {timers.delete(id);}},
    app: {phase: "character-list"}, addEvent() {},
    fetch: async (url, options) => {if(options?.method === "HEAD") return new Response(null, {headers: {"Content-Length": "100"}});calls.push({url, options});throw new TypeError("Load failed");},
  };
  vm.createContext(context);
  vm.runInContext(section("  /* Resource index transport. */", "  function loadCreationSpriteManifest("), context);
  const first = context.loadAssetManifest();
  assert.equal(first, context.loadAssetManifest(), "concurrent callers share one download");
  assert.equal(calls.length, 0, "wait for resource revision before first request");
  resolveVersion({revision: "resources-0001"});
  await first;
  assert.equal(calls[0].url, "https://cdn.example/stoneage/assets/manifest.json?v=resources-0001");
  assert.equal(calls[0].options.cache, "no-cache");
  for(let i = 0; i < 20; i++) await context.loadAssetManifest();
  assert.equal(calls.length, 1, "failed manifest must not restart on repaint or screen transitions");
  assert.equal(timers.size, 0, "failure must not schedule automatic downloads");
  context.assetState.manifestFailed = false;
  await context.loadAssetManifest();
  assert.equal(calls.length, 2, "explicit retry releases the failure latch");
  assert.equal(calls[1].url, calls[0].url, "unchanged resources keep the same URL");
  context.assetVersionPromise = Promise.resolve({revision: "resources-0002"});
  await context.fetchAssetIndex("/assets/sprites.json?other=kept").catch(() => {});
  assert.equal(calls[2].url, "https://game.example/assets/sprites.json?other=kept&v=resources-0002");
  context.assetVersionPromise = Promise.resolve(null);
  await context.fetchAssetIndex("/assets/manifest.json").catch(() => {});
  assert.equal(calls[3].url, "https://game.example/assets/manifest.json", "local packs without version markers remain usable");
  for(const name of ["ASSET_MANIFEST_URL", "CREATION_SPRITE_MANIFEST_URL", "FIELD_BOOTSTRAP_SPRITE_MANIFEST_URL", "FIELD_SPRITE_MANIFEST_URL", "SPRITE_MANIFEST_URL"])
    assert(html.includes("fetchAssetIndex(" + name + ","), name + " must use the shared version lookup");
}
(async () => {
  const indexed = mapHarness({200: ["200.dat", "200.MAP"]}, "200.dat");
  const data = await indexed.context.requestAutoMapData(200);
  assert.equal(data.source, "200.dat");
  assert.deepEqual(indexed.calls, ["/maps/200.dat"], "known filename must avoid uppercase 404 probe");
  await indexed.context.requestAutoMapData(200);
  assert.equal(indexed.calls.length, 1, "decoded map cache remains effective");
  const fallback = mapHarness({}, "200.MAP");
  assert.equal((await fallback.context.requestAutoMapData(200)).source, "200.MAP");
  const invalid = mapHarness({200: ["200.dat"]}, "none");
  assert.equal(await invalid.context.requestAutoMapData(200), null);
  assert.equal(invalid.calls.length, 4, "invalid map candidates must each be attempted only once");
  assert.equal(new Set(invalid.calls).size, 4);
  audioHarness();
  await manifestHarness();
  console.log("map filename and audio CORS regression tests passed");
})().catch(error => {console.error(error);process.exitCode = 1;});
