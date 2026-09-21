"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const html = fs.readFileSync(path.join(__dirname, "runtimeassets/index.html"), "utf8");
const script = html.match(/<script>([\s\S]*?)<\/script>/)[1];
const directoryStart = script.indexOf("  let serverSelectionStage=");
const directoryEnd = script.indexOf("  function connectionFailureMessage", directoryStart);
const selectStart = script.indexOf("  async function selectServer", directoryEnd);
const selectEnd = script.indexOf('  $("server-window")', selectStart);
const transportStart = script.indexOf("  class HTTPTransport");
const transportEnd = script.indexOf("\n\n  /* Keep the browser's logical direction order", transportStart);
assert(directoryStart >= 0 && directoryEnd > directoryStart, "server directory source boundary missing");
assert(selectStart >= 0 && selectEnd > selectStart, "server selector source boundary missing");
assert(transportStart >= 0 && transportEnd > transportStart, "HTTP transport source boundary missing");

class FakeClassList {
  constructor() { this.values = new Set(); }
  toggle(name, force) {
    const enabled = force === undefined ? !this.values.has(name) : Boolean(force);
    if (enabled) this.values.add(name); else this.values.delete(name);
    return enabled;
  }
  contains(name) { return this.values.has(name); }
}

class FakeNode {
  constructor(id = "") {
    this.id = id;
    this.children = [];
    this.dataset = {};
    this.attributes = {};
    this.classList = new FakeClassList();
    this.textContent = "";
    this.disabled = false;
  }
  replaceChildren(...children) { this.children = children; }
  append(...children) { this.children.push(...children); }
  setAttribute(name, value) { this.attributes[name] = String(value); }
}

async function settle() {
  for (let index = 0; index < 12; index++) await Promise.resolve();
}

function makeHarness(initialFetch) {
  const fetchState = { implementation: initialFetch };
  const timers = new Map();
  let nextTimer = 0;
  const nodes = Object.fromEntries(["server-screen", "server-list", "server-back", "server-message", "server-status"].map(id => [id, new FakeNode(id)]));
  const shown = [];
  const app = {selectedServer: "local-line", charListLockRetryCount: 0, phase: "login", transport: null};
  const context = {
    app,
    connectionRetry: {active: false},
    serverScreen: nodes["server-screen"],
    $: id => nodes[id],
    show: screen => shown.push(screen),
    document: {createElement: () => new FakeNode()},
    window: {
      setTimeout(fn, delay) { const id = ++nextTimer; timers.set(id, {fn, delay}); return id; },
      clearTimeout(id) { timers.delete(id); },
    },
    fetch: (...args) => fetchState.implementation(...args),
    AbortController,
    Protocol: {
      fromBase64: () => Uint8Array.of(76, 0),
      toBase64: value => Buffer.from(value).toString("base64"),
    },
    connectLogin: async () => {},
    unlockAudio() {},
    playSoundEffect() {},
    setMusicMode() {},
    console: {debug() {}},
  };
  vm.createContext(context);
  const source = [
    script.slice(directoryStart, directoryEnd),
    script.slice(transportStart, transportEnd),
    script.slice(selectStart, selectEnd),
    `globalThis.__serverDirectoryTest={loadServerDirectory,openServerSelection,renderServerSelection,invalidateServerDirectory,invalidateServerSelectionView,selectServer,HTTPTransport,getState:()=>({state:serverDirectoryState,directory:serverDirectory,request:serverDirectoryRequest,stage:serverSelectionStage,viewToken:serverSelectionViewToken})};`,
  ].join("\n");
  vm.runInContext(source, context, {filename: "client/web/runtimeassets/index.html"});
  return {
    context,
    api: context.__serverDirectoryTest,
    app,
    nodes,
    shown,
    timers,
    fetchState,
    async fireNextTimer() {
      const [id, timer] = timers.entries().next().value || [];
      assert(timer, "expected a pending directory timeout");
      timers.delete(id);
      timer.fn();
      await settle();
    },
  };
}

function directoryResponse() {
  return {
    ok: true,
    async json() {
      return {servers: [
        {id: "alpha", name: "Alpha 一线", disabled: false},
        {id: "beta", name: "Beta 白虎二线", disabled: false},
        {id: "maintenance", name: "维护线路", disabled: true},
      ]};
    },
  };
}

(async () => {
  let directoryCalls = 0;
  const harness = makeHarness(async () => { directoryCalls++; return directoryResponse(); });
  const first = harness.api.loadServerDirectory(true);
  const second = harness.api.loadServerDirectory(true);
  assert.strictEqual(first, second, "concurrent directory loads must share one promise");
  await Promise.all([first, second]);
  assert.equal(directoryCalls, 1, "concurrent directory loads made duplicate requests");

  harness.api.openServerSelection("group");
  assert.equal(harness.nodes["server-list"].children.length, 3, "configured lines were not rendered");
  let connectCalls = 0;
  harness.context.connectLogin = async () => { connectCalls++; };
  await harness.api.selectServer("beta", {dataset: {retry: "0"}});
  assert.equal(harness.app.selectedServer, "beta", "selected line ID was not retained");
  assert.equal(connectCalls, 1, "selected line did not start one login connection");
  harness.api.openServerSelection("group");
  await harness.api.selectServer("maintenance", {dataset: {retry: "0"}});
  assert.equal(harness.app.selectedServer, "beta", "disabled line changed the selected ID");
  await harness.api.selectServer("", null);
  assert.equal(harness.app.selectedServer, "beta", "missing line ID silently fell back to local-line");

  let transportRequest;
  harness.fetchState.implementation = async (url, options) => {
    transportRequest = {url, options};
    return {ok: true, async json() { return {id: "session-beta", greeting: "TAA="}; }};
  };
  const transport = new harness.api.HTTPTransport("", harness.app.selectedServer);
  await transport.connect();
  assert.equal(transportRequest.url, "/api/sessions");
  assert.deepEqual(JSON.parse(transportRequest.options.body), {server_id: "beta"}, "session creation did not carry selected line ID");
  await transport.connect();
  assert.deepEqual(JSON.parse(transportRequest.options.body), {server_id: "beta"}, "reconnect lost selected line ID");

  let failingCalls = 0;
  let abortReject;
  const failing = makeHarness((url, options) => {
    failingCalls++;
    if (failingCalls > 1) return Promise.resolve(directoryResponse());
    return new Promise((resolve, reject) => {
      abortReject = reject;
      options.signal.addEventListener("abort", () => reject(new Error("aborted")), {once: true});
    });
  });
  failing.api.openServerSelection("group");
  await settle();
  assert.equal(failingCalls, 1, "directory load did not start");
  await failing.fireNextTimer();
  await settle();
  assert.equal(failingCalls, 1, "directory failure started an implicit retry storm");
  assert.equal(failing.api.getState().state, "error");
  const retryButton = failing.nodes["server-list"].children[0];
  assert.equal(retryButton.textContent, "重试");
  assert.equal(retryButton.disabled, false, "directory failure did not expose an explicit retry");
  await failing.api.selectServer("", {dataset: {retry: "1"}});
  await settle();
  assert.equal(failingCalls, 2, "explicit retry did not issue exactly one new request");
  assert.equal(failing.api.getState().state, "ready");
  assert.equal(typeof abortReject, "function");

  let resolveLate;
  const late = makeHarness(() => new Promise(resolve => { resolveLate = resolve; }));
  late.api.openServerSelection("group");
  await settle();
  late.api.invalidateServerSelectionView();
  late.app.phase = "login";
  resolveLate(directoryResponse());
  await settle();
  assert.equal(late.app.phase, "login", "late directory response changed the login phase");
  assert.equal(late.shown.length, 1, "late directory response repainted the server screen");

  console.log("server directory selection/retry vectors passed");
})().catch(error => { console.error(error); process.exitCode = 1; });
