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
const ROOTS_KEY = "/__stoneage_asset_roots__";
const BOOTSTRAP_REVISION = "bootstrap";
let activeRevision = BOOTSTRAP_REVISION;
let assetRoots = [];

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
  await Promise.all(names
    .filter(name => name.startsWith(CACHE_PREFIX) && name !== cacheName())
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
    activeRevision = revision;
    event.waitUntil((async () => {
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
  const url = new URL(request.url);
  event.respondWith(isPublishedMarker(url) || isIndexRequest(url) ? networkFirst(request) : cacheFirst(request));
});
