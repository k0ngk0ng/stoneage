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
  if (url.origin === self.location.origin && isStaticPath(url)) return true;
  if (isAllowedExternalRoot(url)) return true;
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

async function removeOldCaches() {
  const names = await caches.keys();
  const keep = new Set([cacheName()]);
  if (validRevision(previousRevision)) keep.add(cacheName(previousRevision));
  await Promise.all(names
    .filter(name => name.startsWith(CACHE_PREFIX) && !keep.has(name))
    .map(name => caches.delete(name)));
}

async function networkFirst(request) {
  const cache = await caches.open(cacheName());
  try {
    const response = await fetch(request);
    if (response && (response.ok || response.type === "opaque")) {
      await cache.put(request, response.clone());
    }
    return response;
  } catch (error) {
    const current = await cache.match(request, {ignoreVary: true});
    if (current) return current;
    const fallback = await caches.match(request, {ignoreVary: true});
    if (fallback) return fallback;
    throw error;
  }
}

async function cacheFirst(request) {
  const cache = await caches.open(cacheName());
  const hit = await cache.match(request, {ignoreVary: true});
  if (hit) return hit;
  /* When a publication revision changes, unchanged objects are safe to reuse
     from the previous namespace because the uploader compared their SHA-256.
     Changed/removed objects are excluded and must be fetched from the new
     publication.  If the marker predates delta metadata, do not guess. */
  if (previousRevision && previousDeltaKnown && !previousChangedAll && previousDeltaFrom === previousRevision) {
    const key = assetObjectKey(new URL(request.url));
    if (key && !previousChanged.has(key) && !previousRemoved.has(key)) {
      try {
        const previousCache = await caches.open(cacheName(previousRevision));
        const previousHit = await previousCache.match(request, {ignoreVary: true});
        if (previousHit) {
          try { await cache.put(request, previousHit.clone()); } catch (_) { /* quota is optional */ }
          return previousHit;
        }
      } catch (_) {
        /* Storage failures fall through to the normal network path. */
      }
    }
  }
  try {
    const response = await fetch(request);
    if (response && (response.ok || response.type === "opaque")) {
      await cache.put(request, response.clone());
    }
    return response;
  } catch (error) {
    const fallback = await caches.match(request, {ignoreVary: true});
    if (fallback) return fallback;
    throw error;
  }
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
    await loadStoredState();
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
    })());
    return;
  }
  if (data.type === "set-asset-roots") {
    const roots = Array.isArray(data.roots) ? data.roots.filter(value => typeof value === "string").slice(0, 8) : [];
    assetRoots = roots;
    event.waitUntil(writeMeta(ROOTS_KEY, JSON.stringify(roots)));
    return;
  }
  if (data.type === "prefetch-assets" && Array.isArray(data.urls)) {
    const urls = data.urls.filter(value => typeof value === "string").slice(0, 32);
    event.waitUntil(Promise.all(urls.map(url => {
      try {
        const request = new Request(url, {method: "GET"});
        return isStaticRequest(request) ? cacheFirst(request).catch(() => null) : null;
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
  event.respondWith(isPublishedMarker(url) || isIndexRequest(url) ? networkFirst(request) : cacheFirst(request));
});
