/*
 * StoneAge static-resource worker.
 *
 * The game page remains a normal network application: API/session requests,
 * TCP polling and the HTML shell are deliberately never cached here.  Only
 * the immutable client trees (assets, maps and audio) are placed in Cache
 * Storage.  A tiny publication revision, sent by index.html after reading
 * _client-version.json, gives each release its own cache namespace without
 * putting a release tag in any CDN URL.
 */
"use strict";

const CACHE_PREFIX = "stoneage-static-v1-";
const META_CACHE = "stoneage-static-meta-v1";
const REVISION_KEY = "/__stoneage_asset_revision__";
const PREVIOUS_STATE_KEY = "/__stoneage_asset_previous__";
const ROOTS_KEY = "/__stoneage_asset_roots__";
const BOOTSTRAP_REVISION = "bootstrap";
let activeRevision = BOOTSTRAP_REVISION;
let assetRoots = [];
let previousRevision = "";
let previousDeltaKnown = false;
let previousChangedAll = true;
let previousDeltaFrom = "";
let previousChanged = new Set();
let previousRemoved = new Set();
let assetProgressSequence = 0;
let storedStatePromise = null;
/* A repaint, prefetch, and media/image loader can all ask for one immutable
   object before the first response has reached Cache Storage.  Keep one
   network operation per strategy/URL until it settles; each fetch event gets
   its own Response clone below. */
const inflightResponses = new Map();
const prefetchQueue = new Map();
let prefetchRunning = 0;
function pumpPrefetch(){
  while(prefetchRunning<2&&prefetchQueue.size){
    const [url,job]=prefetchQueue.entries().next().value;
    prefetchQueue.delete(url);prefetchRunning++;
    const request=new Request(url,{method:"GET"});
    const writes=[];
    Promise.resolve().then(()=>isStaticRequest(request)?cacheFirst(request,job.clientId,promise=>writes.push(promise)):null)
      .then(async response=>{if(response)await cancelResponseBody(response);await Promise.allSettled(writes);}).catch(()=>{})
      .finally(()=>{prefetchRunning--;job.done();pumpPrefetch();});
  }
}

function validRevision(value) {
  const revision = String(value || "").trim();
  return /^[A-Za-z0-9][A-Za-z0-9._-]{7,127}$/.test(revision) ? revision : "";
}

function cacheName(revision = activeRevision) {
  return `${CACHE_PREFIX}${validRevision(revision) || BOOTSTRAP_REVISION}`;
}

function isStaticPath(url) {
  return /(?:^|\/)assets\//.test(url.pathname) ||
    /(?:^|\/)maps\//.test(url.pathname) ||
    /(?:^|\/)audio\//.test(url.pathname);
}

/* Same-origin published trees always live at the root.  Keep this stricter
   predicate separate from isStaticPath(), which also has to recognise a
   configured CDN prefix such as /stoneage/assets/.  Without the distinction
   a future /api/assets/... endpoint would be treated as immutable content and
   cached by the worker, making a dynamic API response survive logout or an
   update. */
function isLocalStaticPath(url) {
  return /^\/(?:assets|maps|audio)\//.test(url.pathname);
}

function isPublishedMarker(url) {
  /* Never treat an API endpoint that happens to share the marker filename as
     static content.  The real marker lives beside the published trees (or on
     the same-origin root in a local deployment). */
  if (url.origin === self.location.origin && /^\/api(?:\/|$)/i.test(url.pathname)) return false;
  return /(?:^|\/)_(?:client-version|client-manifest)\.json$/i.test(url.pathname);
}

function isIndexRequest(url) {
  /* Sprite/map indexes are stable URLs whose contents can change between
     releases.  They must revalidate even when the page supplied
     cache:"no-cache"; a worker otherwise turns that browser hint into a
     cache-first hit and an installed tab can keep an old 42 MB table forever. */
  return isStaticPath(url) && /\.json$/i.test(url.pathname);
}

function isAllowedExternalRoot(url) {
  return assetRoots.some(root => {
    try {
      const parsed = new URL(root);
      return parsed.origin === url.origin && url.pathname.startsWith(parsed.pathname);
    } catch (_) {
      return false;
    }
  });
}

function normalizedObjectKey(value) {
  return String(value || "").replace(/^\/+/, "").replace(/\\/g, "/");
}

/* Convert an absolute same-origin/CDN URL to the stable publication key used
   by _client-version.json (assets/foo.png, maps/100.MAP, audio/bgm/0.wav).
   Query strings are intentionally ignored: published object paths remain
   stable and cache invalidation is driven by the SHA-256 delta. */
function assetObjectKey(url) {
  for (const root of assetRoots) {
    try {
      const parsed = new URL(root);
      if (parsed.origin !== url.origin || !url.pathname.startsWith(parsed.pathname)) continue;
      const relative = decodeURIComponent(url.pathname.slice(parsed.pathname.length));
      const rootPath = parsed.pathname.replace(/\/+$/, "");
      const tree = rootPath.endsWith("/maps") ? "maps" : rootPath.endsWith("/audio") ? "audio" : "assets";
      return normalizedObjectKey(`${tree}/${relative}`);
    } catch (_) {
      /* An invalid root is ignored; normal network fallback remains intact. */
    }
  }
  const local = new URL(url.href);
  if (local.origin === self.location.origin) {
    for (const tree of ["assets", "maps", "audio"]) {
      const prefix = `/${tree}/`;
      if (local.pathname.startsWith(prefix)) return normalizedObjectKey(`${tree}/${local.pathname.slice(prefix.length)}`);
    }
  }
  return "";
}

function isStaticRequest(request) {
  if (request.method !== "GET") return false;
  const url = new URL(request.url);
  if (isPublishedMarker(url)) return true;
  if (url.origin === self.location.origin && isLocalStaticPath(url)) return true;
  if (isAllowedExternalRoot(url)) return true;
  /* Same-origin paths outside the three published roots belong to the web
     application (including /api/assets/*).  Do not let the resource-like
     destination fallback below turn a dynamic API response into an
     immutable Cache Storage entry.  A same-origin CDN prefix is already
     accepted by isAllowedExternalRoot() above. */
  if (url.origin === self.location.origin) return false;
  /* The page sends roots immediately after registration, but the first
     navigation can race that message.  CDN paths still carry one of the
     published tree names; only cache resource-like requests on this fallback
     so an unrelated /api/assets/... URL is never swallowed. */
  if (isStaticPath(url) && ["", "image", "audio", "font", "script", "style"].includes(request.destination)) return true;
  return false;
}

async function readMeta(key) {
  try {
    const cache = await caches.open(META_CACHE);
    const response = await cache.match(key);
    return response ? response.text() : "";
  } catch (_) {
    return "";
  }
}

async function writeMeta(key, value) {
  try {
    const cache = await caches.open(META_CACHE);
    await cache.put(key, new Response(value, {headers: {"Content-Type": "text/plain; charset=utf-8"}}));
  } catch (_) {
    /* Private browsing/WebViews can disable Cache Storage. Network remains the fallback. */
  }
}

function responseCloneForCache(response) {
  try {
    return response && typeof response.clone === "function" ? response.clone() : null;
  } catch (_) {
    return null;
  }
}

function cancelResponseBody(response) {
  try {
    const cancel = response?.body?.cancel;
    if (typeof cancel === "function") return Promise.resolve(cancel.call(response.body)).catch(() => {});
  } catch (_) {}
  return Promise.resolve();
}

/* Response.clone() tees a body.  If the original branch is never consumed or
   cancelled, a browser may keep buffering it while another branch is read.
   Allocate a chain of branches instead: each branch is
   handed to exactly one consumer, and the final branch goes to the fetch
   event.  No original body branch is left behind. */
function responseBranches(response, count) {
  const branches = [];
  let current = response;
  try {
    for (let index = 0; index < count - 1; index++) {
      const next = responseCloneForCache(current);
      if (!next) throw new TypeError("response body cannot be shared");
      branches.push(current);
      current = next;
    }
    if (count > 0) branches.push(current);
    return branches;
  } catch (error) {
    /* A clone can fail when a platform has already disturbed the stream.
       Cancel every branch that was created before surfacing the failure so a
       failed response cannot leave another tee buffering in the worker. */
    void Promise.all([...branches, current].map(cancelResponseBody));
    throw error;
  }
}

function deferredPromise() {
  let resolve;
  let reject;
  const promise = new Promise((resolveValue, rejectValue) => {
    resolve = resolveValue;
    reject = rejectValue;
  });
  return {promise, resolve, reject};
}

function responseKey(request, strategy) {
  /* A preload (no-cors) and a JSON fetch (cors) must never share an opaque
     response. Keep mode/credentials in the key, while browser cache hints and
     Accept negotiation are intentionally ignored for immutable objects so a
     prefetch and the real image request can share one in-flight download. */
  const headers = Array.from(request.headers.entries()).filter(([name]) => !["accept", "cache-control", "pragma"].includes(name.toLowerCase()));
  return JSON.stringify([strategy,activeRevision,request.method,request.url,request.mode,request.credentials,request.redirect,headers]);
}

function runResponseConsumer(consumer, response) {
  try {
    return Promise.resolve(consumer(response)).catch(() => cancelResponseBody(response));
  } catch (_) {
    return cancelResponseBody(response);
  }
}

function coalescedResponse(request, strategy, operation, waitUntil) {
  const key = responseKey(request, strategy);
  let entry = inflightResponses.get(key);
  if (!entry) {
    const completion = deferredPromise();
    entry = {subscribers: [], completionPromise: completion.promise, completion, settled: false, lateResponse: null};
    inflightResponses.set(key, entry);
    /* Start the operation immediately, but keep every caller's subscription
       until the response arrives.  The network body is split into a finite
       chain of dedicated branches below.  Once those branches are assigned,
       a later caller can wait for the cache write and read a fresh CacheStorage
       Response; it never attaches a new tee to an already consumed body. */
    Promise.resolve().then(operation).then(result => {
      entry.settled = true;
      const response = result && Object.prototype.hasOwnProperty.call(result, "response") ? result.response : result;
      const consumers = Array.isArray(result?.consumers) ? result.consumers.filter(consumer => typeof consumer === "function") : [];
      entry.lateResponse = typeof result?.lateResponse === "function" ? result.lateResponse : null;
      const subscribers = entry.subscribers;
      if (!response) throw new TypeError("static resource response is empty");
      const branches = responseBranches(response, consumers.length + subscribers.length);
      let branchIndex = 0;
      const background = consumers.map(consumer => runResponseConsumer(consumer, branches[branchIndex++]));
      entry.subscribers.splice(0).forEach(subscriber => subscriber.resolve(branches[branchIndex++]));
      const finish = () => {
        if (inflightResponses.get(key) === entry) inflightResponses.delete(key);
        entry.completion.resolve();
      };
      Promise.all(background).then(finish, finish);
    }, error => {
      entry.settled = true;
      if (inflightResponses.get(key) === entry) inflightResponses.delete(key);
      entry.subscribers.splice(0).forEach(subscriber => subscriber.reject(error));
      entry.completion.resolve();
    }).catch(error => {
      /* Branch allocation can fail after the operation itself succeeded.  It
         must reject all waiting fetch events and still settle waitUntil. */
      entry.settled = true;
      if (inflightResponses.get(key) === entry) inflightResponses.delete(key);
      entry.subscribers.splice(0).forEach(subscriber => subscriber.reject(error));
      entry.completion.resolve();
    });
  }
  /* A response has already been assigned to the requests that were present
     when the operation completed.  Do not attach a new request to a consumed
     body.  For cache-backed operations, wait for the write and use a fresh
     cache response so a delayed duplicate still avoids a second CDN fetch. */
  if (entry.settled) {
    if (!entry.lateResponse) {
      if (inflightResponses.get(key) === entry) inflightResponses.delete(key);
      return coalescedResponse(request, strategy, operation, waitUntil);
    }
    const late = entry.completionPromise.then(async () => {
      let response = null;
      try { response = await entry.lateResponse(); } catch (_) {}
      if (response) return response;
      if (inflightResponses.get(key) === entry) inflightResponses.delete(key);
      return coalescedResponse(request, strategy, operation, waitUntil);
    });
    if (waitUntil) {
      try { waitUntil(entry.completionPromise); } catch (_) {}
    }
    return late;
  }
  const subscriber = deferredPromise();
  entry.subscribers.push(subscriber);
  if (waitUntil) {
    try { waitUntil(entry.completionPromise); } catch (_) {}
  }
  return subscriber.promise;
}

async function cachePutBestEffort(cache, request, response) {
  if (!cache || !response || response.type === "error") {
    await cancelResponseBody(response);
    return false;
  }
  try {
    /* This Response is a dedicated branch allocated by coalescedResponse.
       Cache.put() consumes it directly, so no original/clone tee is left
       buffering behind the network response. */
    await cache.put(request, response);
    return true;
  } catch (_) {
    /* Cache Storage is an optional optimization.  Never turn a successful
       network response into a failed fetch event because storage rejected it.
       A rejected Cache.put() is allowed to leave its body untouched on some
       engines, so cancel the dedicated branch explicitly. */
    await cancelResponseBody(response);
    return false;
  }
}

async function cacheMatchBestEffort(cache, request) {
  if (!cache) return null;
  try {
    const response = await cache.match(request, {ignoreVary: true});
    /* A no-cors prefetch can leave an opaque response under the same URL.  A
       subsequent anonymous CORS <img> cannot decode that entry; treat it as
       a miss so the readable response replaces it instead of surfacing
       ERR_FAILED from the compositor. */
    if (response?.type === "opaque" && request?.mode === "cors") return null;
    return response;
  } catch (_) {
    return null;
  }
}

function responseBodyLength(response) {
  if (!response || response.type === "opaque") return 0;
  const encoding = response.headers?.get?.("Content-Encoding");
  if (encoding && encoding !== "identity") return 0;
  return Number(response.headers?.get?.("Content-Length")) || 0;
}

async function postAssetProgress(clientId, message) {
  if (!clientId) return;
  try {
    if (typeof self.clients?.get === "function") {
      const client = await self.clients.get(clientId);
      if (client?.postMessage) {
        client.postMessage(message);
        return;
      }
    }
    const clients = await self.clients?.matchAll?.({type: "window", includeUncontrolled: true}) || [];
    const client = clients.find(item => item?.id === clientId);
    client?.postMessage?.(message);
  } catch (_) {
    /* Progress is optional; the image response must remain usable. */
  }
}

function reportsAssetProgress(request) {
  return request?.destination === "image";
}

function assetProgressNeedsBody(request, response, clientId) {
  return Boolean(clientId && reportsAssetProgress(request) && response && response.type !== "opaque" && responseBodyLength(response) <= 0);
}

function reportCachedAsset(request, response, clientId) {
  if (!clientId || !reportsAssetProgress(request) || !response || response.type === "opaque") return Promise.resolve();
  const url = request.url;
  const known = responseBodyLength(response);
  if (known > 0) {
    return postAssetProgress(clientId, {type: "asset-progress", url, network: false, phase: "cache", loaded: known, size: known, done: true});
  }
  return (async () => {
    let reader = null;
    try {
      if (!response.body?.getReader) {
        const buffer = await response.arrayBuffer();
        const size = buffer.byteLength;
        if (size > 0) await postAssetProgress(clientId, {type: "asset-progress", url, network: false, phase: "cache", loaded: size, size, done: true});
        return;
      }
      reader = response.body.getReader();
      let loaded = 0;
      for (;;) {
        const part = await reader.read();
        if (part.done) break;
        loaded += part.value?.byteLength || 0;
      }
      if (loaded > 0) await postAssetProgress(clientId, {type: "asset-progress", url, network: false, phase: "cache", loaded, size: loaded, done: true});
    } catch (_) {
      try { await reader?.cancel(); } catch (_) {}
      /* A cache hit without readable headers still remains usable. */
    }
  })();
}

function reportNetworkAsset(request, response, clientId) {
  if (!clientId || !reportsAssetProgress(request) || !response || response.type === "opaque") return Promise.resolve();
  const requestId = `${Date.now().toString(36)}-${++assetProgressSequence}`;
  const url = request.url;
  const total = responseBodyLength(response);
  return (async () => {
    let loaded = 0, lastReport = 0;
    let reader = null;
    const notify = (phase, done = false, force = false) => {
      const now = Date.now();
      if (!force && !done && now - lastReport < 100) return;
      lastReport = now;
      void postAssetProgress(clientId, {type: "asset-progress", url, network: true, requestId, phase, loaded, total, size: done ? loaded : 0, done});
    };
    notify("start", false, true);
    try {
      if (!response.body?.getReader) {
        const buffer = await response.arrayBuffer();
        loaded = buffer.byteLength;
      } else {
        reader = response.body.getReader();
        for (;;) {
          const part = await reader.read();
          if (part.done) break;
          loaded += part.value?.byteLength || 0;
          notify("progress");
        }
      }
      notify("complete", true, true);
    } catch (_) {
      try { await reader?.cancel(); } catch (_) {}
      notify("complete", true, true);
    }
  })();
}

async function globalCacheMatchBestEffort(request) {
  try {
    return await caches.match(request, {ignoreVary: true});
  } catch (_) {
    return null;
  }
}

function ensureStoredState() {
  if (!storedStatePromise) {
    storedStatePromise = loadStoredState().catch(() => {}).then(() => true);
  }
  return storedStatePromise;
}

async function removeOldCaches() {
  const names = await caches.keys();
  const keep = new Set([cacheName()]);
  if (validRevision(previousRevision)) keep.add(cacheName(previousRevision));
  await Promise.all(names
    .filter(name => name.startsWith(CACHE_PREFIX) && !keep.has(name))
    .map(name => caches.delete(name)));
}

async function networkFirst(request, clientId, waitUntil) {
  // Imported indexes have been verified against this exact publication.
  // Reuse them until the revision changes; markers still revalidate online.
  await ensureStoredState();
  if(isIndexRequest(new URL(request.url))){
    try{
      const cache=await caches.open(cacheName()),hit=await cacheMatchBestEffort(cache,request);
      if(hit?.headers.get("X-Stoneage-Resource-Revision")===activeRevision)return hit;
    }catch(_) { /* Network fallback remains available when storage fails. */ }
  }
  return coalescedResponse(request, "network-first", async () => {
    let cache = null;
    try { cache = await caches.open(cacheName()); } catch (_) { /* network remains usable */ }
    try {
      const response = await fetch(request);
      if (response && (response.ok || response.type === "opaque")) {
        const consumers = cache ? [branch => cachePutBestEffort(cache, request, branch)] : [];
        const lateResponse = cache ? () => cacheMatchBestEffort(cache, request) : null;
        return {response, consumers, lateResponse};
      }
      void postAssetProgress(clientId, {type: "asset-error", url: request.url, status: Number(response?.status) || 0, message: `HTTP ${response?.status || 0}`});
      return {response};
    } catch (error) {
      void postAssetProgress(clientId, {type: "asset-error", url: request.url, status: 0, message: String(error)});
      const current = await cacheMatchBestEffort(cache, request);
      if (current) return {response: current};
      const fallback = await globalCacheMatchBestEffort(request);
      if (fallback) return {response: fallback};
      throw error;
    }
  }, waitUntil);
}

async function cacheFirst(request, clientId, waitUntil) {
  await ensureStoredState();
  return coalescedResponse(request, "cache-first", async () => {
    let cache = null;
    try { cache = await caches.open(cacheName()); } catch (_) { /* network remains usable */ }
    const hit = await cacheMatchBestEffort(cache, request);
    if (hit) {
      const consumers = assetProgressNeedsBody(request, hit, clientId) ? [branch => reportCachedAsset(request, branch, clientId)] : [];
      if (consumers.length === 0 && clientId && reportsAssetProgress(request)) {
        /* A cached response with a trustworthy Content-Length can report its
           completed size without creating another body branch. */
        void reportCachedAsset(request, hit, clientId);
      }
      const lateResponse = cache ? () => cacheMatchBestEffort(cache, request) : null;
      return {response: hit, consumers, lateResponse};
    }
    /* When a publication revision changes, unchanged objects are safe to reuse
       from the previous namespace because the uploader compared their SHA-256.
       Changed/removed objects are excluded and must be fetched from the new
       publication.  If the marker predates delta metadata, do not guess. */
    if (previousRevision && previousDeltaKnown && !previousChangedAll && previousDeltaFrom === previousRevision) {
      const key = assetObjectKey(new URL(request.url));
      if (key && !previousChanged.has(key) && !previousRemoved.has(key)) {
        try {
          const previousCache = await caches.open(cacheName(previousRevision));
          const previousHit = await cacheMatchBestEffort(previousCache, request);
          if (previousHit) {
            const consumers = [];
            if (cache) consumers.push(branch => cachePutBestEffort(cache, request, branch));
            if (assetProgressNeedsBody(request, previousHit, clientId)) consumers.push(branch => reportCachedAsset(request, branch, clientId));
            else if (clientId && reportsAssetProgress(request)) void reportCachedAsset(request, previousHit, clientId);
            const lateResponse = cache ? () => cacheMatchBestEffort(cache, request) : null;
            return {response: previousHit, consumers, lateResponse};
          }
        } catch (_) {
          /* Storage failures fall through to the normal network path. */
        }
      }
    }
    try {
      const response = await fetch(request);
      if (response && (response.ok || response.type === "opaque")) {
        const consumers = [];
        if (cache) consumers.push(branch => cachePutBestEffort(cache, request, branch));
        if (clientId && reportsAssetProgress(request) && response.type !== "opaque") consumers.push(branch => reportNetworkAsset(request, branch, clientId));
        const lateResponse = cache ? () => cacheMatchBestEffort(cache, request) : null;
        return {response, consumers, lateResponse};
      }
      void postAssetProgress(clientId, {type: "asset-error", url: request.url, status: Number(response?.status) || 0, message: `HTTP ${response?.status || 0}`});
      return {response};
    } catch (error) {
      void postAssetProgress(clientId, {type: "asset-error", url: request.url, status: 0, message: String(error)});
      const fallback = await globalCacheMatchBestEffort(request);
      if (fallback) return {response: fallback};
      throw error;
    }
  }, waitUntil);
}

async function loadStoredState() {
  const revision = validRevision(await readMeta(REVISION_KEY));
  if (revision) activeRevision = revision;
  try {
    const raw = await readMeta(PREVIOUS_STATE_KEY);
    const parsed = JSON.parse(raw || "null");
    if (parsed && validRevision(parsed.revision)) {
      previousRevision = parsed.revision;
      previousDeltaKnown = parsed.deltaKnown === true;
      previousChangedAll = parsed.changedAll !== false;
      previousDeltaFrom = validRevision(parsed.deltaFrom);
      previousChanged = new Set(Array.isArray(parsed.changed) ? parsed.changed.map(normalizedObjectKey) : []);
      previousRemoved = new Set(Array.isArray(parsed.removed) ? parsed.removed.map(normalizedObjectKey) : []);
    }
  } catch (_) {
    previousRevision = "";
    previousDeltaKnown = false;
    previousChangedAll = true;
    previousDeltaFrom = "";
    previousChanged = new Set();
    previousRemoved = new Set();
  }
  try {
    const raw = await readMeta(ROOTS_KEY);
    const parsed = JSON.parse(raw || "[]");
    if (Array.isArray(parsed)) assetRoots = parsed.filter(value => typeof value === "string").slice(0, 8);
  } catch (_) {
    assetRoots = [];
  }
}

self.addEventListener("install", event => {
  event.waitUntil(self.skipWaiting());
});

self.addEventListener("activate", event => {
  event.waitUntil((async () => {
    await ensureStoredState();
    await removeOldCaches();
    await self.clients.claim();
  })());
});

self.addEventListener("message", event => {
  const data = event.data || {};
  if (data.type === "set-asset-version") {
    const revision = validRevision(data.revision);
    if (!revision) return;
    event.waitUntil((async () => {
      let ok = false;
      try {
        await ensureStoredState();
        if (revision !== activeRevision) {
          previousRevision = activeRevision;
          previousDeltaKnown = data.deltaKnown === true;
          previousDeltaFrom = validRevision(data.deltaFrom);
          /* Reuse is valid only when the publisher compared this revision
             against the exact namespace currently in the browser.  A missing
             or mismatched base deliberately falls back to network loading. */
          previousChangedAll = data.changedAll === true || !previousDeltaKnown || !previousDeltaFrom || previousDeltaFrom !== previousRevision;
          previousChanged = new Set(Array.isArray(data.changed) ? data.changed.map(normalizedObjectKey) : []);
          previousRemoved = new Set(Array.isArray(data.removed) ? data.removed.map(normalizedObjectKey) : []);
          await writeMeta(PREVIOUS_STATE_KEY, JSON.stringify({
            revision: previousRevision,
            deltaFrom: previousDeltaFrom,
            deltaKnown: previousDeltaKnown,
            changedAll: previousChangedAll,
            changed: [...previousChanged],
            removed: [...previousRemoved],
          }));
          activeRevision = revision;
        }
        await writeMeta(REVISION_KEY, revision);
        await caches.open(cacheName());
        await removeOldCaches();
        ok = true;
      } catch (_) { /* a storage failure leaves the normal network path */ }
      const port = event.ports?.[0];
      if (port?.postMessage) port.postMessage({type: "asset-config-result", ok});
    })());
    return;
  }
  if (data.type === "set-asset-roots") {
    const roots = Array.isArray(data.roots) ? data.roots.filter(value => typeof value === "string").slice(0, 8) : [];
    event.waitUntil((async () => {
      let ok = false;
      try {
        await ensureStoredState();
        assetRoots = roots;
        await writeMeta(ROOTS_KEY, JSON.stringify(roots));
        ok = true;
      } catch (_) { /* optional metadata storage */ }
      const port = event.ports?.[0];
      if (port?.postMessage) port.postMessage({type: "asset-config-result", ok});
    })());
    return;
  }
  if (data.type === "migrate-asset") {
    event.waitUntil((async () => {
      let ok = false;
      try {
        await ensureStoredState();
        const url = new URL(String(data.url || ""));
        const dataURL = String(data.dataURL || "");
        if (!/^data:image\//i.test(dataURL) || !isStaticRequest(new Request(url.href, {method: "GET"}))) throw new Error("invalid asset migration");
        const cache = await caches.open(cacheName());
        const request = new Request(url.href, {method: "GET"});
        const existing = await cacheMatchBestEffort(cache, request);
        if (existing) ok = true;
        else {
          const response = await fetch(dataURL);
          if (!response.ok) throw new Error("local asset migration failed");
          ok = await cachePutBestEffort(cache, request, response);
        }
      } catch (_) { /* stale localStorage entries are safe to discard later */ }
      const port = event.ports?.[0];
      if (port?.postMessage) port.postMessage({type: "asset-migration-result", ok});
    })());
    return;
  }
  if (data.type === "prefetch-assets" && Array.isArray(data.urls)) {
    const urls = data.urls.filter(value => typeof value === "string").slice(0, 32);
    const clientId = event.source?.id || "";
    event.waitUntil(Promise.all(urls.map(url => {
      try {
        const request = new Request(url, {method: "GET"});
        if(!isStaticRequest(request)||prefetchQueue.has(url))return null;
        if(prefetchQueue.size>=32){const [key,old]=prefetchQueue.entries().next().value;prefetchQueue.delete(key);old.done();}
        return new Promise(done=>{prefetchQueue.set(url,{clientId,done});pumpPrefetch();});
      } catch (_) {
        return null;
      }
    })));
  }
});

self.addEventListener("fetch", event => {
  const request = event.request;
  if (!isStaticRequest(request)) return;
  /* Chromium's media loader issues byte-range requests and expects the
     response's 206/Content-Range contract to be preserved.  Cache Storage
     cannot safely replay a cached full 200 (or a previously cached 206) for
     that request, which leaves HTMLAudioElement with a media source error.
     Let range/media requests go through the browser's normal HTTP cache and
     network path; immutable audio headers still avoid repeat downloads while
     ordinary fetch/prefetch requests continue to use Cache Storage below. */
  if (request.destination === "audio" || request.headers.has("range")) {
    event.respondWith(fetch(request));
    return;
  }
  const url = new URL(request.url);
  const strategy = isPublishedMarker(url) || isIndexRequest(url) ? networkFirst : cacheFirst;
  const keepAlive = promise => { try { event.waitUntil?.(promise); } catch (_) {} };
  event.respondWith(strategy(request, event.clientId || "", keepAlive));
});
