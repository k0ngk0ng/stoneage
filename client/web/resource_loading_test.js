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
  console.log("map filename and audio CORS regression tests passed");
})().catch(error => {console.error(error);process.exitCode = 1;});
