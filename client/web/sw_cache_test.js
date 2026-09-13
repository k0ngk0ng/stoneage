"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");

const serviceWorker = fs.readFileSync(__dirname + "/sw.js", "utf8");

const CACHE_PREFIX = "stoneage-static-v1-";

function cloneResponse(value) {
  return value && typeof value.clone === "function" ? value.clone() : value;
}

function keyOf(request) {
  return typeof request === "string" ? request : request.url;
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((res, rej) => { resolve = res; reject = rej; });
  return {promise, resolve, reject};
}

function chunkedResponse() {
  let controller;
  const stream = new ReadableStream({start(value) { controller = value; }});
  const response = new Response(stream, {
    status: 200,
    headers: {"Content-Type": "image/png"},
  });
  return {
    response,
    enqueue(value) { controller.enqueue(value); },
    close() { controller.close(); },
  };
}

/* A small Response test double that mirrors clone()'s tee semantics while
   counting branches that finish or are cancelled.  A leaked tee branch stays
   active forever, which makes response ownership leaks observable in tests. */
function trackedResponse(body, state) {
  const source = body instanceof ReadableStream ? body : new ReadableStream({
    start(controller) {
      controller.enqueue(new TextEncoder().encode(String(body || "network")));
      controller.close();
    },
  });
  const track = stream => {
    const reader = stream.getReader();
    let settled = false;
    state.activeBranches++;
    const settle = () => {
      if (!settled) {
        settled = true;
        state.activeBranches--;
      }
    };
    return new ReadableStream({
      async pull(controller) {
        try {
          const part = await reader.read();
          if (part.done) {
            settle();
            controller.close();
          } else {
            controller.enqueue(part.value);
          }
        } catch (error) {
          settle();
          controller.error(error);
        }
      },
      async cancel(reason) {
        settle();
        try { await reader.cancel(reason); } catch (_) {}
      },
    });
  };
  class TrackedResponse {
    constructor(stream) {
      this.body = stream;
      this.ok = true;
      this.status = 200;
      this.statusText = "";
      this.type = "default";
      this.headers = new Headers({"Content-Type": "image/png"});
    }
    clone() {
      state.cloneCalls++;
      const [left, right] = this.body.tee();
      this.body = track(left);
      return new TrackedResponse(track(right));
    }
    async arrayBuffer() { return (await consumeResponse(this)).buffer; }
    async text() { return new TextDecoder().decode(await this.arrayBuffer()); }
  }
  return new TrackedResponse(track(source));
}

async function consumeResponse(response) {
  if (!response) return new Uint8Array();
  if (response.body?.getReader) {
    const reader = response.body.getReader();
    const chunks = [];
    let size = 0;
    for (;;) {
      const part = await reader.read();
      if (part.done) break;
      if (part.value?.byteLength) {
        chunks.push(part.value);
        size += part.value.byteLength;
      }
    }
    const result = new Uint8Array(size);
    let offset = 0;
    for (const chunk of chunks) {
      result.set(chunk, offset);
      offset += chunk.byteLength;
    }
    return result;
  }
  if (typeof response.arrayBuffer === "function") return new Uint8Array(await response.arrayBuffer());
  return new Uint8Array();
}

function storedResponse(response, bytes) {
  if (response?.type === "opaque") return {type: "opaque"};
  return {
    body: new Uint8Array(bytes),
    status: response?.status || 200,
    statusText: response?.statusText || "",
    headers: [...(response?.headers?.entries?.() || [])],
  };
}

function restoreResponse(record) {
  if (record?.type === "opaque") return {type: "opaque"};
  return new Response(record?.body || "", {
    status: record?.status || 200,
    statusText: record?.statusText || "",
    headers: record?.headers || {},
  });
}

function timeout(label, milliseconds = 250) {
  return new Promise((_, reject) => setTimeout(() => reject(new Error(`${label} timed out`)), milliseconds));
}

function harness(options = {}) {
  const listeners = new Map();
  const stores = new Map();
  const events = [];
  let fetchCalls = 0;
  let putCalls = 0;
  const fetchFactory = options.fetchFactory || (() => new Response("network", {status: 200}));
  const pendingPuts = [];
  const clientMessages = [];

  function storeFor(name) {
    let store = stores.get(name);
    if (!store) {
      store = new Map();
      stores.set(name, store);
    }
    return store;
  }

  function cacheFor(name) {
    const store = storeFor(name);
    return {
      async match(request) {
        const value = store.get(keyOf(request));
        return value ? cloneResponse(value) : null;
      },
      async put(request, response) {
        if (name.startsWith(CACHE_PREFIX)) {
          putCalls++;
          const wait = deferred();
          pendingPuts.push({name, request: keyOf(request), response, wait});
          if (options.rejectPuts) {
            wait.resolve();
            throw new TypeError("Cache.put() encountered a network error");
          }
          if (options.deferPuts) await wait.promise;
        }
        const bytes = await consumeResponse(response);
        store.set(keyOf(request), restoreResponse(storedResponse(response, bytes)));
      },
    };
  }

  const caches = {
    async open(name) { return cacheFor(name); },
    async match(request) {
      for (const store of stores.values()) {
        const value = store.get(keyOf(request));
        if (value) return cloneResponse(value);
      }
      return null;
    },
    async keys() { return [...stores.keys()]; },
    async delete(name) { return stores.delete(name); },
  };

  const self = {
    location: {origin: "https://game.test"},
    clients: {
      claim: async () => {},
      get: async id => id ? {id, postMessage(message) { clientMessages.push(message); }} : null,
    },
    skipWaiting: async () => {},
    addEventListener(type, listener) { listeners.set(type, listener); },
  };
  const context = {
    self,
    caches,
    fetch(request) {
      fetchCalls++;
      return Promise.resolve().then(() => fetchFactory(request, fetchCalls));
    },
    Request,
    Response,
    ReadableStream,
    URL,
    Headers,
    Promise,
    Map,
    Set,
    String,
    Number,
    Array,
    JSON,
    Date,
    console,
  };
  vm.createContext(context);
  vm.runInContext(serviceWorker, context, {filename: "sw.js"});

  function startDispatch(url, requestOptions = {}) {
    const {clientId = "", destination = "", ...requestInit} = requestOptions;
    const request = new Request(url, {method: "GET", ...requestInit});
    if (destination) Object.defineProperty(request, "destination", {value: destination});
    const event = {
      request,
      clientId,
      waitPromises: [],
      respondWith(value) { this.responsePromise = Promise.resolve(value); },
      waitUntil(value) { this.waitPromises.push(Promise.resolve(value)); },
    };
    events.push(event);
    listeners.get("fetch")(event);
    return event;
  }

  async function dispatch(url, requestOptions = {}) {
    return (await startDispatch(url, requestOptions).responsePromise);
  }

  async function sendMessage(data) {
    const event = {
      data,
      ports: [{postMessage() {}}],
      waitPromises: [],
      waitUntil(value) { this.waitPromises.push(Promise.resolve(value)); },
    };
    listeners.get("message")(event);
    await Promise.all(event.waitPromises);
  }

  async function waitBackground() {
    await Promise.all(events.flatMap(event => event.waitPromises));
  }

  return {
    dispatch,
    startDispatch,
    sendMessage,
    waitBackground,
    seed(url, body = "cached", name = `${CACHE_PREFIX}bootstrap`) {
      storeFor(name).set(url, new Response(body, {status: 200}));
    },
    seedResponse(url, response, name = `${CACHE_PREFIX}bootstrap`) {
      storeFor(name).set(url, response);
    },
    releasePuts() {
      for (const pending of pendingPuts.splice(0)) pending.wait.resolve();
    },
    messages() { return [...clientMessages]; },
    stats() { return {fetchCalls, putCalls}; },
  };
}

async function assertCacheFailureIsOptional(url) {
  const h = harness({rejectPuts: true});
  const first = h.dispatch(url);
  const second = h.dispatch(url);
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(h.stats().fetchCalls, 1, `duplicate network fetch for ${url}`);
  const responses = await Promise.all([first, second]);
  assert.equal(responses[0].status, 200);
  assert.equal(responses[1].status, 200);
  assert.equal(await responses[0].text(), "network");
  assert.equal(await responses[1].text(), "network");
  await h.waitBackground();
  assert.equal(h.stats().putCalls, 1, `expected one best-effort cache write for ${url}`);
}

async function assertNetworkResponseIsStreamedBeforeCacheWrite(url) {
  const network = chunkedResponse();
  const h = harness({deferPuts: true, fetchFactory: () => network.response});
  network.enqueue(new Uint8Array([1, 2, 3]));
  const event = h.startDispatch(url);
  const response = await Promise.race([event.responsePromise, timeout("network response")]);
  assert.equal(response.status, 200, "fetch response must resolve before Cache.put()");
  const reader = response.body.getReader();
  const first = await Promise.race([reader.read(), timeout("first response chunk")]);
  assert.deepEqual([...first.value], [1, 2, 3], "the first body chunk must be readable immediately");
  assert.equal(h.stats().putCalls, 1, "cache write should have started in the background");
  assert.equal(event.waitPromises.length > 0, true, "cache write must extend the fetch event");
  network.close();
  h.releasePuts();
  await h.waitBackground();
}

async function assertAllNetworkBranchesAreConsumed() {
  const state = {activeBranches: 0, cloneCalls: 0};
  const h = harness({fetchFactory: () => trackedResponse("streamed", state)});
  const event = h.startDispatch("https://game.test/assets/tracked.png", {clientId: "client-1", destination: "image"});
  const response = await Promise.race([event.responsePromise, timeout("tracked response")]);
  assert.equal(await response.text(), "streamed", "the fetch event must receive its own readable branch");
  await h.waitBackground();
  assert.equal(state.cloneCalls, 2, "cache and progress must each receive one branch");
  assert.equal(state.activeBranches, 0, "every generated response branch must be consumed or cancelled");
  assert.equal(h.messages().some(message => message.type === "asset-progress" && message.done), true, "network progress branch must complete");
}

async function assertCloneFailureSettlesSubscribers() {
  const state = {activeBranches: 0, cloneCalls: 0};
  const response = trackedResponse("unshareable", state);
  response.clone = () => {
    state.cloneCalls++;
    throw new TypeError("clone failed");
  };
  const h = harness({fetchFactory: () => response});
  const event = h.startDispatch("https://game.test/assets/clone-failure.png");
  await assert.rejects(event.responsePromise, /response body cannot be shared/);
  await h.waitBackground();
  assert.equal(state.cloneCalls, 1, "branch allocation should stop at the first clone failure");
  assert.equal(state.activeBranches, 0, "clone failure must cancel the original body and settle all subscribers");
}

async function assertInFlightSurvivesDeferredCacheWrite() {
  const firstNetwork = chunkedResponse();
  const h = harness({deferPuts: true, fetchFactory: () => firstNetwork.response});
  firstNetwork.enqueue(new Uint8Array([9]));
  const firstEvent = h.startDispatch("https://game.test/assets/dedup.png");
  const firstResponse = await Promise.race([firstEvent.responsePromise, timeout("deduplicated response")]);
  const secondEvent = h.startDispatch("https://game.test/assets/dedup.png");
  assert.equal(h.stats().fetchCalls, 1, "a request arriving during Cache.put() must share the network response");
  assert.equal((await firstResponse.body.getReader().read()).value[0], 9);
  assert.equal(h.stats().putCalls, 1, "coalesced requests must write the cache once");
  firstNetwork.close();
  h.releasePuts();
  const secondResponse = await Promise.race([secondEvent.responsePromise, timeout("deduplicated second response")]);
  assert.deepEqual([...new Uint8Array(await secondResponse.arrayBuffer())], [9]);
  await h.waitBackground();
}

async function assertPreviousCacheMigrationIsBackgrounded() {
  const url = "https://game.test/assets/unchanged.png";
  const oldNetwork = chunkedResponse();
  const h = harness({deferPuts: true, fetchFactory: () => {
    throw new Error("previous cache hit should not fetch the network");
  }});
  await h.sendMessage({type: "set-asset-version", revision: "revision-1"});
  h.seedResponse(url, oldNetwork.response, `${CACHE_PREFIX}revision-1`);
  await h.sendMessage({
    type: "set-asset-version",
    revision: "revision-2",
    deltaKnown: true,
    deltaFrom: "revision-1",
    changedAll: false,
    changed: [],
    removed: [],
  });
  oldNetwork.enqueue(new Uint8Array([7, 8]));
  const event = h.startDispatch(url);
  const response = await Promise.race([event.responsePromise, timeout("previous-cache response")]);
  const first = await Promise.race([response.body.getReader().read(), timeout("previous-cache first chunk")]);
  assert.deepEqual([...first.value], [7, 8]);
  assert.equal(h.stats().fetchCalls, 0, "unchanged previous cache entry must avoid the CDN");
  assert.equal(h.stats().putCalls, 1, "previous cache entry should migrate in the background");
  oldNetwork.close();
  h.releasePuts();
  await h.waitBackground();
}

(async () => {
  /* JSON indexes use network-first and binary assets use cache-first. */
  await assertCacheFailureIsOptional("https://game.test/assets/field-bootstrap-sprites.json");
  await assertCacheFailureIsOptional("https://game.test/assets/bitmaps/bitmap_8827.png");

  const hitURL = "https://game.test/assets/hit.png";
  const hit = harness();
  hit.seed(hitURL);
  const hitResponse = await hit.dispatch(hitURL);
  assert.equal(await hitResponse.text(), "cached");
  assert.deepEqual(hit.stats(), {fetchCalls: 0, putCalls: 0}, "CacheStorage hit must avoid network and writes");

  const modes = harness();
  const modeURL = "https://game.test/assets/mode.png";
  await Promise.all([
    modes.dispatch(modeURL, {mode: "cors", headers: {Accept: "image/png"}}),
    modes.dispatch(modeURL, {mode: "no-cors", headers: {Accept: "*/*"}}),
  ]);
  assert.deepEqual(modes.stats(), {fetchCalls: 2, putCalls: 2}, "different request modes must not share opaque/readable responses");

  await assertNetworkResponseIsStreamedBeforeCacheWrite("https://game.test/assets/stream.png");
  await assertNetworkResponseIsStreamedBeforeCacheWrite("https://game.test/assets/stream.json");
  await assertAllNetworkBranchesAreConsumed();
  await assertCloneFailureSettlesSubscribers();
  await assertInFlightSurvivesDeferredCacheWrite();
  await assertPreviousCacheMigrationIsBackgrounded();

  const opaqueURL = "https://game.test/assets/prefetched.png";
  const opaque = harness();
  opaque.seedResponse(opaqueURL, {type: "opaque"});
  const readable = await opaque.dispatch(opaqueURL, {mode: "cors"});
  assert.equal(await readable.text(), "network");
  assert.deepEqual(opaque.stats(), {fetchCalls: 1, putCalls: 1}, "a CORS image must replace an incompatible opaque cache entry");

  console.log("service worker cache streaming, background write, deduplication, migration and hit tests passed");
})().catch(error => {
  console.error(error);
  process.exitCode = 1;
});
