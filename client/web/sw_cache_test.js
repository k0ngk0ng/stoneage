"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");

const serviceWorker = fs.readFileSync(__dirname + "/sw.js", "utf8");

function harness() {
  const listeners = new Map();
  let fetchCalls = 0;
  let putCalls = 0;
  let releaseNetwork;
  const networkReady = new Promise(resolve => { releaseNetwork = resolve; });
  const cache = {
    async match() { return null; },
    async put() {
      putCalls++;
      throw new TypeError("Cache.put() encountered a network error");
    },
  };
  const caches = {
    async open() { return cache; },
    async match() { return null; },
    async keys() { return []; },
    async delete() { return true; },
  };
  const self = {
    location: {origin: "https://game.test"},
    clients: {claim: async () => {}},
    skipWaiting: async () => {},
    addEventListener(type, listener) { listeners.set(type, listener); },
  };
  const context = {
    self,
    caches,
    fetch(request) {
      fetchCalls++;
      return networkReady.then(() => new Response(`response for ${request.url}`, {status: 200}));
    },
    Request,
    Response,
    URL,
    Headers,
    Promise,
    Map,
    Set,
    String,
    Number,
    Array,
    JSON,
    console,
  };
  vm.createContext(context);
  vm.runInContext(serviceWorker, context, {filename: "sw.js"});

  async function dispatch(url, options = {}) {
    const event = {
      request: new Request(url, {method: "GET", ...options}),
      respondWith(value) { this.responsePromise = Promise.resolve(value); },
    };
    listeners.get("fetch")(event);
    return event.responsePromise;
  }

  return {
    dispatch,
    releaseNetwork,
    stats() { return {fetchCalls, putCalls}; },
  };
}

async function assertCacheFailureIsOptional(url) {
  const h = harness();
  const first = h.dispatch(url);
  const second = h.dispatch(url);
  /* Let both fetch events reach the shared in-flight operation before the
     deferred network response is released. */
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(h.stats().fetchCalls, 1, `duplicate network fetch for ${url}`);
  h.releaseNetwork();
  const responses = await Promise.all([first, second]);
  assert.equal(responses[0].status, 200);
  assert.equal(responses[1].status, 200);
  assert.equal(await responses[0].text(), `response for ${url}`);
  assert.equal(await responses[1].text(), `response for ${url}`);
  assert.equal(h.stats().putCalls, 1, `expected one best-effort cache write for ${url}`);
}

(async () => {
  /* JSON indexes take the network-first path; binary objects take cache-first.
     Both must return the successful network response when Cache.put rejects. */
  await assertCacheFailureIsOptional("https://game.test/assets/field-bootstrap-sprites.json");
  await assertCacheFailureIsOptional("https://game.test/assets/bitmaps/bitmap_8827.png");
  const h = harness();
  const requests = [
    h.dispatch("https://game.test/assets/sprites.json", {mode: "cors"}),
    h.dispatch("https://game.test/assets/sprites.json", {mode: "no-cors"}),
  ];
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(h.stats().fetchCalls, 2, "different request modes must not share responses");
  h.releaseNetwork();
  await Promise.all(requests);
  console.log("service worker cache regression tests passed");
})().catch(error => {
  console.error(error);
  process.exitCode = 1;
});
