"use strict";
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");
const html = fs.readFileSync(__dirname + "/index.html", "utf8");
const start = html.indexOf("  /* Resource index transport. */");
const end = html.indexOf("  function loadAssetManifest(", start);
assert(start >= 0 && end > start, "index transport must be independently testable");
const MiB = 1024 * 1024;
function harness(settings = {}) {
  const data = Buffer.from(JSON.stringify({data: "x".repeat(settings.small ? 100 : settings.medium ? 2 * MiB : 5 * MiB)}));
  const calls = [], stored = new Map(), writes = [];
  const key = value => typeof value === "string" ? value : value.url;
  const cache = {
    async match(request) {return stored.get(key(request))?.clone();},
    put(request, response) {
      const task = (async () => {
        if(settings.cacheFails) throw new Error("quota exceeded");
        const bytes = await response.arrayBuffer();
        stored.set(key(request), new Response(bytes, {status: response.status, headers: response.headers}));
      })();
      writes.push(task.catch(() => {}));return task;
    },
  };
  const caches = {open: async () => cache, match: cache.match};
  const context = {
    URL, Request, Response, Blob, Headers, ReadableStream, AbortController, DOMException, Uint8Array,
    Promise, TextDecoder, TextEncoder, setTimeout, clearTimeout, caches,
    window: {location: {href: "https://game.example/"}, caches, setTimeout, clearTimeout},
    assetState: {}, assetVersionPromise: Promise.resolve({revision: "resources-0001"}),
    fetch: async (input, init) => {
      const request = new Request(input, init);
      if(request.signal.aborted) throw new DOMException("aborted", "AbortError");
      calls.push(request);
      const headers = {"Content-Type": "application/json", "Content-Length": String(data.length), "Last-Modified": "Sun, 13 Sep 2026 01:00:00 GMT"};
      if(request.method === "HEAD") return new Response(null, {status: settings.headFails ? 405 : 200, headers});
      const range = request.headers.get("Range");
      if(range && !settings.ignoreRange) {
        const match = /^bytes=(\d+)-(\d+)$/.exec(range);
        assert(match, "range must be bounded");
        const from = +match[1], to = Math.min(+match[2], data.length - 1);
        assert(to - from + 1 <= MiB, "individual request must stay below observed 4 MiB failure");
        let bytes = data.subarray(from, to + 1);
        if(settings.truncate && from > 0) bytes = bytes.subarray(1);
        if(settings.changeDuringDownload && from > 0) headers["Last-Modified"] = "Sun, 13 Sep 2026 01:00:01 GMT";
        headers["Content-Length"] = String(bytes.length);
        // CDN CORS does not expose Content-Range. The normal case must work without it.
        return new Response(bytes, {status: 206, headers});
      }
      let position = 0;
      return new Response(new ReadableStream({pull(controller) {
        if(position >= data.length) return controller.close();
        if(position >= 4 * MiB) return controller.error(new TypeError("Load failed at 4 MiB"));
        const chunk = data.subarray(position, position + 65536);position += chunk.length;controller.enqueue(chunk);
      }}), {headers});
    },
  };
  vm.createContext(context);vm.runInContext(html.slice(start, end), context);
  return {context, calls, stored, writes, data};
}
async function flush(h) {for(let i = 0; i < 20; i++) await Promise.resolve();await Promise.all(h.writes);}
(async () => {
  const h = harness();
  const response = await h.context.fetchAssetIndex("/assets/manifest.json");
  const bytes = Buffer.from(await response.arrayBuffer());
  assert.deepEqual(bytes, h.data, "large manifest must survive a transport that rejects whole responses at 4 MiB");
  assert.equal(h.calls.filter(r => r.headers.has("Range")).length, Math.ceil(h.data.length / MiB));
  await flush(h);
  const count = h.calls.length;
  assert.deepEqual(Buffer.from(await (await h.context.fetchAssetIndex("/assets/manifest.json")).arrayBuffer()), h.data);
  assert.equal(h.calls.length, count, "complete versioned index cache hit must not contact CDN");
  h.context.assetVersionPromise = Promise.resolve({revision: "resources-0002"});
  await (await h.context.fetchAssetIndex("/assets/manifest.json")).arrayBuffer();
  assert(h.calls.length > count, "new resource revision must invalidate the old index");
  assert(h.calls.at(-1).url.includes("v=resources-0002"));
  for(const settings of [{truncate: true}, {changeDuringDownload: true}]) {
    const broken = harness(settings);
    await assert.rejects(async () => (await broken.context.fetchAssetIndex("/assets/manifest.json")).arrayBuffer());
    await flush(broken);
    assert.equal(broken.stored.size, 0, "incomplete or mixed-version index must never enter cache");
  }
  for(const settings of [{small: true}, {small: true, headFails: true}, {medium: true, ignoreRange: true}, {cacheFails: true}]) {
    const compatible = harness(settings);
    assert.deepEqual(Buffer.from(await (await compatible.context.fetchAssetIndex("/assets/manifest.json")).arrayBuffer()), compatible.data);
    await flush(compatible);
  }
  const pendingWrite = harness(), waitingController = new AbortController();
  vm.runInContext('assetIndexWrites.set("https://game.example/assets/manifest.json?v=resources-0001", new Promise(() => {}))', pendingWrite.context);
  const waiting = pendingWrite.context.fetchAssetIndex("/assets/manifest.json", {signal: waitingController.signal});
  await Promise.resolve();waitingController.abort();
  await assert.rejects(waiting, {name: "AbortError"});
  assert.equal(pendingWrite.calls.length, 0, "cancelled cache wait must not download");
  const aborted = harness(), controller = new AbortController();
  const active = await aborted.context.fetchAssetIndex("/assets/manifest.json", {signal: controller.signal});
  const reader = active.body.getReader();await reader.read();controller.abort();
  await assert.rejects(async () => {while(!(await reader.read()).done) {} });
  await flush(aborted);assert.equal(aborted.stored.size, 0);
  console.log("versioned index ranges, complete-cache reuse, consistency, failure and abort regressions passed");
})().catch(error => {console.error(error);process.exitCode = 1;});
