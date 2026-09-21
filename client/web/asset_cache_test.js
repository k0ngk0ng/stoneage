"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");

const serviceWorker = fs.readFileSync(__dirname + "/runtimeassets/sw.js", "utf8");

function harness() {
  const listeners = new Map();
  const entries = new Map();
  let fetchCalls = 0;
  let putCalls = 0;
  const cache = {
    async match(request) {
      const key = typeof request === "string" ? request : request.url;
      return entries.get(key) || null;
    },
    async put(request, response) {
      putCalls++;
      entries.set(typeof request === "string" ? request : request.url, response);
    },
  };
  const caches = {
    async open() { return cache; },
    async match(request) { return cache.match(request); },
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
      return Promise.resolve(new Response(`network ${request.url}`, {status: 200}));
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
    seed(url, body = "cached") { entries.set(url, new Response(body, {status: 200})); },
    seedOpaque(url) { entries.set(url, {type: "opaque"}); },
    stats() { return {fetchCalls, putCalls}; },
  };
}

(async () => {
  const hitURL = "https://game.test/assets/hit.png";
  const hit = harness();
  hit.seed(hitURL);
  const hitResponse = await hit.dispatch(hitURL);
  assert.equal(await hitResponse.text(), "cached");
  assert.deepEqual(hit.stats(), {fetchCalls: 0, putCalls: 0}, "CacheStorage hit must avoid network and writes");

  const missURL = "https://game.test/assets/miss.png";
  const miss = harness();
  const first = await miss.dispatch(missURL);
  assert.equal(await first.text(), `network ${missURL}`);
  assert.deepEqual(miss.stats(), {fetchCalls: 1, putCalls: 1}, "cache miss must fetch and write exactly once");
  const second = await miss.dispatch(missURL);
  assert.equal(await second.text(), `network ${missURL}`);
  assert.deepEqual(miss.stats(), {fetchCalls: 1, putCalls: 1}, "the next request must hit the newly written cache");

  const concurrent = harness();
  await Promise.all([
    concurrent.dispatch(missURL, {cache: "default", headers: {Accept: "*/*"}}),
    concurrent.dispatch(missURL, {cache: "force-cache", headers: {Accept: "image/png"}}),
  ]);
  assert.deepEqual(concurrent.stats(), {fetchCalls: 1, putCalls: 1}, "prefetch and image cache hints must not duplicate the download");

  const opaqueURL = "https://game.test/assets/prefetched.png";
  const opaque = harness();
  opaque.seedOpaque(opaqueURL);
  const readable = await opaque.dispatch(opaqueURL);
  assert.equal(await readable.text(), `network ${opaqueURL}`);
  assert.deepEqual(opaque.stats(), {fetchCalls: 1, putCalls: 1}, "a CORS image must replace an incompatible opaque cache entry");

  console.log("service worker asset cache hit/miss byte path passed");
})().catch(error => {
  console.error(error);
  process.exitCode = 1;
});
