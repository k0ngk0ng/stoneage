// Protocol regression vectors captured from the preserved client/gateway.
// The implementation intentionally lives inline in index.html so deployment
// remains one webpage; this test extracts only the protocol IIFE.
"use strict";

const fs = require("fs");
const path = require("path");
const vm = require("vm");
const zlib = require("zlib");

/* BattleMap.CPP paints its 20x20 SAB cells directly into the fixed 640x480
   back buffer.  A previous extractor crop was displaced by 128px/32px and
   produced the large transparent (therefore black in battle) wedges seen on
   the right side of every encounter.  Cover both the source-derived crop and
   the generated pack: a future asset refresh must not silently reintroduce
   that visual regression. */
const battleMapSource = fs.readFileSync(__dirname + "/../../vendor/upstream/code_sa_client/SYSTEM/BATTLEMAP.CPP", "latin1");
if (!/posX\s*=\s*32\s*\*\s*\(\s*-9\s*\)/.test(battleMapSource) ||
    !/posY\s*=\s*24\s*\*\s*10/.test(battleMapSource)) {
  throw new Error("unexpected native BattleMap viewport origin");
}
const battleExtractorSource = fs.readFileSync(__dirname + "/../../tools/extract-legacy-web-assets.py", "utf8");
if (!battleExtractorSource.includes("crop_rect = (544, 472, 640, 480)") ||
    !battleExtractorSource.includes('parser.add_argument("--battles-only"') ||
    !battleExtractorSource.includes("bitmap_cache = {}") ||
    !battleExtractorSource.includes("BATTLE_MAP_FILES_25 = 218") ||
    !battleExtractorSource.includes("battle_number >= BATTLE_MAP_FILES_25") ||
    !battleExtractorSource.includes("number := int(path.stem[6:])") ||
    !battleExtractorSource.includes("image.unlink()")) {
  throw new Error("battle asset extractor lost the native viewport crop/cache path");
}

function generatedRGBAStats(filename) {
  const png = fs.readFileSync(filename);
  const signature = Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]);
  if (png.length < signature.length || !png.subarray(0, signature.length).equals(signature)) {
    throw new Error(`invalid generated battle PNG signature: ${filename}`);
  }
  let offset = signature.length, width = 0, height = 0, bitDepth = 0, colorType = 0;
  const idat = [];
  while (offset + 12 <= png.length) {
    const length = png.readUInt32BE(offset), type = png.toString("ascii", offset + 4, offset + 8);
    const end = offset + 12 + length;
    if (end > png.length) throw new Error(`truncated generated battle PNG: ${filename}`);
    const payload = png.subarray(offset + 8, offset + 8 + length);
    if (type === "IHDR") {
      width = payload.readUInt32BE(0); height = payload.readUInt32BE(4);
      bitDepth = payload[8]; colorType = payload[9];
    } else if (type === "IDAT") {
      idat.push(payload);
    } else if (type === "IEND") {
      break;
    }
    offset = end;
  }
  if (width !== 640 || height !== 480 || bitDepth !== 8 || colorType !== 6 || !idat.length) {
    throw new Error(`unexpected generated battle PNG shape: ${filename} ${width}x${height}/${bitDepth}/${colorType}`);
  }
  const raw = zlib.inflateSync(Buffer.concat(idat)), stride = width * 4 + 1;
  if (raw.length !== stride * height) throw new Error(`unexpected generated battle PNG payload: ${filename}`);
  let transparent = 0, rightTransparent = 0;
  for (let y = 0; y < height; y++) {
    const row = y * stride;
    if (raw[row] !== 0) throw new Error(`generated battle PNG uses an unexpected row filter: ${filename}`);
    for (let x = 0; x < width; x++) {
      const alpha = raw[row + 1 + x * 4 + 3];
      if (alpha !== 0 && alpha !== 255) throw new Error(`generated battle PNG contains partial alpha: ${filename}`);
      if (alpha !== 255) {
        transparent++;
        if (x >= width / 2) rightTransparent++;
      }
    }
  }
  return {width, height, transparent, rightTransparent, pixels: width * height};
}

const battleDirectory = path.join(__dirname, "assets", "original", "battle");
const battleFiles = fs.readdirSync(battleDirectory).filter(name => /^battle_\d+\.png$/.test(name)).sort();
const BATTLE_MAP_FILES_25 = 218;
if (battleFiles.length !== BATTLE_MAP_FILES_25) throw new Error(`generated battle viewport count drifted: ${battleFiles.length} != ${BATTLE_MAP_FILES_25}`);
const transparentBattleFiles = [];
for (let battle = 0; battle < BATTLE_MAP_FILES_25; battle++) {
  const name = `battle_${String(battle).padStart(2, "0")}.png`;
  if (!battleFiles.includes(name)) throw new Error(`missing generated battle viewport ${name}`);
  const stats = generatedRGBAStats(path.join(battleDirectory, name));
  if (stats.transparent / stats.pixels > 0.015 || stats.rightTransparent / (stats.pixels / 2) > 0.006) {
    throw new Error(`generated battle viewport exposes a black/transparent wedge: ${name} ${JSON.stringify(stats)}`);
  }
  if (stats.transparent) transparentBattleFiles.push(name);
}
if (transparentBattleFiles.join(",") !== "battle_129.png,battle_130.png,battle_131.png") {
  throw new Error(`unexpected transparent battle-map edges: ${transparentBattleFiles.join(",")}`);
}
const battleManifest = JSON.parse(fs.readFileSync(path.join(__dirname, "assets", "original", "manifest.json"), "utf8"));
if (Object.keys(battleManifest.battles || {}).length !== BATTLE_MAP_FILES_25) throw new Error("battle manifest must describe all 218 sa_2903 SAB files");
if (battleManifest.battles?.["218"] || battleManifest.battles?.["219"]) throw new Error("later-client battle 218/219 leaked into the 2.5 manifest");
/* ATT_BOW selects original direction-specific CG records.  All 16 visible
   arrows, all 16 ground shadows, and the stock stone/firecracker pair must
   survive a deterministic --ui-only refresh. */
const nativeProjectileGraphics = [
  ...Array.from({length:16},(_,index)=>25630+index),
  ...Array.from({length:16},(_,index)=>25650+index),
  25785,25786,24350,
];
for (const logical of nativeProjectileGraphics) {
  const info=battleManifest.bitmaps?.[String(logical)];
  if (!info?.file || Number(info.bmp_number)!==logical ||
      !fs.existsSync(path.join(__dirname,"assets","original",info.file))) {
    throw new Error(`native battle projectile graphic missing: ${logical} ${JSON.stringify(info)}`);
  }
}
if (!battleExtractorSource.includes("BATTLE_PROJECTILE_BITMAPS") ||
    !battleExtractorSource.includes('emit_bitmap(f"battle_projectile_{logical}"')) {
  throw new Error("battle projectile asset extraction path missing");
}
/* Item packets carry logical bmp_number values.  They must resolve through
   ADRN aliases instead of colliding with an unrelated physical bitmap file
   of the same number (24008 is the smallest reproducible meat-item case). */
const meatPhysical = String(battleManifest.bitmap_aliases?.["24008"] || "");
const meatBitmap = battleManifest.bitmaps?.[meatPhysical];
if (!meatPhysical || meatPhysical === "24008" || meatBitmap?.bmp_number !== 24008 ||
    !fs.existsSync(path.join(__dirname, "assets", "original", meatBitmap?.file || ""))) {
  throw new Error(`2.5 item graphic 24008 lost its logical ADRN mapping: ${JSON.stringify({meatPhysical, meatBitmap})}`);
}
if (!battleExtractorSource.includes("def server_item_graphics()") ||
    !battleExtractorSource.includes('parser.add_argument("--items-only"') ||
    !battleExtractorSource.includes("item_pack(records, by_number")) {
  throw new Error("item asset extractor lost the 2.5 itemset pack path");
}
for (let battle = 0; battle < BATTLE_MAP_FILES_25; battle++) {
  const entry = battleManifest.battles?.[String(battle)], name = `battle_${String(battle).padStart(2, "0")}.png`;
  if (entry?.image !== `battle/${name}` || entry?.render?.width !== 640 || entry?.render?.height !== 480) {
    throw new Error(`battle manifest viewport drift for ${battle}: ${JSON.stringify(entry)}`);
  }
}

const html = fs.readFileSync(__dirname + "/index.html", "utf8");
const serviceWorker = fs.readFileSync(__dirname + "/sw.js", "utf8");
const script = html.match(/<script>([\s\S]*?)<\/script>/)[1];
if(!/<input id="chat-input"[^>]*aria-label="聊天输入"[^>]*maxlength="70"[^>]*>/.test(html)||
   /<input id="chat-input"[^>]*placeholder=/.test(html)||
   !/#chat-form \{[^}]*left:8px; top:432px; bottom:auto[^}]*width:560px; height:16px/.test(html)||
   !/#chat-form input \{[^}]*height:16px[^}]*padding:0[^}]*background:transparent[^}]*border:0[^}]*box-shadow:none[^}]*font:16px\/16px/.test(html)||
   !/#chat-form button,#chat-form button:hover \{ display:none !important; \}/.test(html)||
   !/#chat-log \{[^}]*left:8px[^}]*width:560px[^}]*height:400px[^}]*font:16px\/20px/.test(html)||
   !/#battle-chat-log \{[^}]*left:8px[^}]*width:560px[^}]*height:400px[^}]*font:16px\/20px/.test(html)){
  throw new Error("chat buffer must use the native transparent 70-byte/8,432 layout");
}
/* Creation.CPP redraws the face preview after every point/style change.  The
   DOM preview must carry the cleanup class used by renderCreation(); an
   id-only node survives each redraw and stacks duplicate face bitmaps. */
if(!/face\.id="creation-face-preview";face\.className="creation-face-preview"/.test(script) ||
   !/settings\.querySelectorAll\("\.creation-arrow-hit,\.creation-face-selector,\.creation-face-preview"\)/.test(script)) {
  throw new Error("creation face preview must be removed before each redraw");
}
/* LOGIN.CPP keeps elemental creation points mutually exclusive by opposing
   pair and, once the ten free points are spent, moves one point from the
   first existing element when another allowed element is selected.  Test the
   extracted handler itself so a disabled-looking arrow cannot silently leave
   a valid character build stuck at its first element. */
const creationAdjustStart = script.indexOf("  function creationAdjust(kind,index,delta)");
const creationAdjustEnd = script.indexOf("  function openCreation()", creationAdjustStart);
if (creationAdjustStart < 0 || creationAdjustEnd <= creationAdjustStart) {
  throw new Error("creation attribute adjustment handler missing");
}
let creationRenders = 0;
const transferState = {status:[0,0,0,0],attrs:[10,0,0,0],statusPoints:0,attrPoints:0,eye:0,mouth:0};
const transferAdjust = new Function("creationState", "renderCreation", `${script.slice(creationAdjustStart, creationAdjustEnd)}; return creationAdjust;`)(transferState, ()=>{creationRenders++;});
transferAdjust("attrs", 1, 1);
if (transferState.attrs.join(",") !== "9,1,0,0" || transferState.attrPoints !== 0 || creationRenders !== 1) {
  throw new Error(`creation attribute points did not transfer like LOGIN.CPP: ${JSON.stringify(transferState)}`);
}
const opposingState = {status:[0,0,0,0],attrs:[10,0,0,0],statusPoints:0,attrPoints:0,eye:0,mouth:0};
const opposingAdjust = new Function("creationState", "renderCreation", `${script.slice(creationAdjustStart, creationAdjustEnd)}; return creationAdjust;`)(opposingState, ()=>{throw new Error("opposing element should remain blocked");});
opposingAdjust("attrs", 2, 1);
if (opposingState.attrs.join(",") !== "10,0,0,0") {
  throw new Error("creation opposing elemental pair was not blocked");
}
/* MAIN.CPP does not send a chat packet for VK_DELETE: it clears only the
   visible chat buffer.  Keep the web-only touch aliases equally local and
   make sure the input handler still consumes the native forward-delete path.
   This is intentionally a source invariant; evaluating the complete page
   would start transports, audio and animation timers in this protocol test. */
const clearCommandStart = script.indexOf("const LOCAL_CHAT_CLEAR_COMMANDS");
const clearCommandEnd = script.indexOf("function normalizeLocalChatCommand", clearCommandStart);
if (clearCommandStart < 0 || clearCommandEnd <= clearCommandStart) {
  throw new Error("local chat-clear command table missing");
}
const clearCommandSource = script.slice(clearCommandStart, clearCommandEnd);
for (const alias of [
  "/clear", "/cls", "/clearchat", "/clear-chat", "/clear_chat", "/clear chat",
  "/clearlog", "/clear-chat-log", "/清屏", "/清空聊天", "/清空聊天记录", "/清除聊天", "/清除聊天记录",
  "clear", "cls", "clearchat", "clear-chat", "clear_chat", "clear chat", "clearlog", "clear-chat-log",
  "清屏", "清空聊天", "清空聊天记录", "清除聊天", "清除聊天记录",
]) {
  if (!clearCommandSource.includes(JSON.stringify(alias))) {
    throw new Error(`chat-clear alias missing from local table: ${alias}`);
  }
}
const clearHandlerStart = script.indexOf("function consumeLocalChatCommand", clearCommandEnd);
const clearHandlerEnd = script.indexOf("function isChatClearKey", clearHandlerStart);
const clearHandlerSource = script.slice(clearHandlerStart, clearHandlerEnd);
if (!/LOCAL_CHAT_CLEAR_COMMANDS\.has\(command\)/.test(clearHandlerSource) ||
    !/clearChatBuffer\(\);\s*return true/.test(clearHandlerSource) ||
    !/function isChatClearKey\(event\)[\s\S]{0,260}event\.key==="Delete"\|\|event\.code==="Delete"/.test(script)) {
  throw new Error("chat-clear aliases must be consumed locally and Delete must remain wired");
}
/* The switch-compatible 8.5 `_SA_VERSION_25` main loop adds one harmless
   local edit shortcut: Shift+Backspace clears the focused STR_BUFFER.  Keep
   that behavior in the web editor without turning it into a server command. */
const native25MainSource = fs.readFileSync(__dirname + "/../../reference/anson1788-stoneage/石器时代8.5客户端最新源代码/石器源码/system/main.cpp", "utf8");
if (!/case VK_BACK:[\s\S]{0,260}JOY_RSHIFT[\s\S]{0,220}pNowStrBuffer->cnt\s*=\s*0[\s\S]{0,120}pNowStrBuffer->buffer\[0\]\s*=\s*NULL/.test(native25MainSource) ||
    !/function isShiftBackspace\(event\)[\s\S]{0,260}event\.shiftKey[\s\S]{0,180}!event\.ctrlKey[\s\S]{0,180}!event\.metaKey/.test(script) ||
    !/function clearEditableInputBuffer\(input\)[\s\S]{0,360}input\.value=""[\s\S]{0,220}setSelectionRange\(0,0\)/.test(script) ||
    !/if\(isShiftBackspace\(event\)\)\{event\.preventDefault\(\);clearEditableInputBuffer\(input\);event\.stopPropagation\(\);return;\}/.test(script)) {
  throw new Error("Shift+Backspace must clear the focused 2.5-compatible input buffer locally");
}
/* The 8.5 source tree is also the switch-compatible reference for the
   deployed 2.5 mode.  Its regional map cases 47..53 are unconditional;
   only the later 54/55 recordings are protected by _NEWMUSICFILE6_0. */
const nativeMusicSource = fs.readFileSync(__dirname + "/../../reference/anson1788-stoneage/石器时代8.5客户端最新源代码/石器源码/system/t_music.cpp", "latin1");
const nativeMapMusicStart = nativeMusicSource.indexOf("int play_map_bgm(int tone)");
const nativeMapMusicEnd = nativeMusicSource.indexOf("int play_environment(", nativeMapMusicStart);
const nativeMapMusicSource = nativeMusicSource.slice(nativeMapMusicStart, nativeMapMusicEnd);
const nativeMapBgmPairs = [[40,4],[41,3],[42,7],[43,8],[44,9],[45,10],[46,11],[47,15],[48,16],[49,21],[50,17],[51,18],[52,19],[53,20]];
if (nativeMapMusicStart < 0 || nativeMapMusicEnd <= nativeMapMusicStart) {
  throw new Error("switch-compatible play_map_bgm source boundary missing");
}
for (const [tone, bgm] of nativeMapBgmPairs) {
  if (!new RegExp(`case\\s+${tone}\\s*:[\\s\\S]{0,100}map_bgm_no\\s*=\\s*${bgm}\\s*;`).test(nativeMapMusicSource)) {
    throw new Error(`native map BGM mapping drifted: ${tone} -> ${bgm}`);
  }
}
const guardedRegionalMusic = nativeMapMusicSource.lastIndexOf("#ifdef _NEWMUSICFILE6_0");
if (guardedRegionalMusic < 0 || nativeMapMusicSource.indexOf("case 53:") >= guardedRegionalMusic ||
    !/#ifdef\s+_NEWMUSICFILE6_0[\s\S]{0,220}case\s+54:[\s\S]{0,160}case\s+55:/.test(nativeMapMusicSource.slice(guardedRegionalMusic))) {
  throw new Error("map BGM 54/55 must remain behind the 6.0 music switch");
}
const battleMusicStart = script.indexOf("function beginBattleMusic");
const battleMusicEnd = script.indexOf("function restoreMapMusic", battleMusicStart);
const battleMusicSource = script.slice(battleMusicStart, battleMusicEnd);
if (battleMusicStart < 0 || battleMusicEnd <= battleMusicStart ||
    !/battleType===2\|\|battleType===4\|\|battleType===6/.test(battleMusicSource)) {
  throw new Error("watcher battles must retain the native BGM6 selection");
}
function nativeMapContainsTone(filename, tone) {
  const data = fs.readFileSync(path.join(__dirname, "../../runtime/legacy-client/map", filename));
  if (data.length < 8) return false;
  const width = data.readInt32LE(0), height = data.readInt32LE(4), cells = width * height;
  if (width <= 0 || height <= 0 || cells <= 0 || 8 + cells * 4 > data.length) return false;
  for (let layer = 0; layer < 2; layer++) {
    for (let cell = 0, offset = 8 + layer * cells * 2; cell < cells; cell++, offset += 2) {
      if (data.readUInt16LE(offset) === tone) return true;
    }
  }
  return false;
}
for (const [filename, tone] of [["700.DAT",47],["7000.DAT",48],["7002.DAT",49],["7100.DAT",50],["7200.DAT",51],["7300.DAT",52],["7400.DAT",53]]) {
  if (!nativeMapContainsTone(filename, tone)) {
    throw new Error(`deployed 2.5 map fixture lost regional BGM marker ${tone}: ${filename}`);
  }
}
if (!/<link rel="icon" href="data:,">/.test(html)) {
  throw new Error("page must suppress Chromium's synthetic /favicon.ico 404");
}
for (const expected of [
  'fetch(ASSET_MANIFEST_URL,{cache:"no-cache"',
  'fetch(CREATION_SPRITE_MANIFEST_URL,{cache:"no-cache"',
  'fetch(FIELD_BOOTSTRAP_SPRITE_MANIFEST_URL,{cache:"no-cache"',
  'fetch(FIELD_SPRITE_MANIFEST_URL,{cache:"no-cache"',
  'fetch(`${SPRITE_MANIFEST_URL}`,{cache:"no-cache"',
]) {
  if (!script.includes(expected)) {
    throw new Error(`asset manifest must revalidate across deployments: ${expected}`);
  }
}
/* Published indexes use one stable object key.  Cache invalidation comes from
   _client-version.json and the worker namespace; putting dated/tagged query
   strings on each CDN URL creates an unbounded set of cache entries. */
for (const expected of [
  'const ASSET_MANIFEST_URL="/assets/manifest.json"',
  'const CREATION_SPRITE_MANIFEST_URL="/assets/creation-sprites.json"',
  'const FIELD_BOOTSTRAP_SPRITE_MANIFEST_URL="/assets/field-bootstrap-sprites.json"',
  'const FIELD_SPRITE_MANIFEST_URL="/assets/field-sprites.json"',
  'const SPRITE_MANIFEST_URL="/assets/sprites.json"',
]) {
  if (!script.includes(expected)) throw new Error(`asset index URL drifted from stable path: ${expected}`);
}
for (const expected of [
  'deltaFrom:String(version.delta_from||"")',
]) {
  if (!script.includes(expected)) throw new Error(`asset version delta wiring missing: ${expected}`);
}
for (const expected of [
  "const PREVIOUS_STATE_KEY",
  "function assetObjectKey(url)",
  "function isLocalStaticPath(url)",
  "previousDeltaKnown",
  "previousChangedAll",
  "previousDeltaFrom",
  "previousHit",
  "data.deltaKnown",
  "data.deltaFrom",
  "data.changedAll",
  "request.destination === \"audio\"",
  "request.headers.has(\"range\")",
]) {
  if (!serviceWorker.includes(expected)) throw new Error(`Service Worker hash-delta reuse path missing: ${expected}`);
}
if (!/url\.origin === self\.location\.origin && isLocalStaticPath\(url\)/.test(serviceWorker) ||
    /url\.origin === self\.location\.origin && isStaticPath\(url\)/.test(serviceWorker)) {
  throw new Error("Service Worker must not cache same-origin /api/assets paths as static resources");
}
/* Most UI styles live inside the script's legacyStyle template literal.
   A stray backtick in a CSS comment can therefore leave a perfectly
   rendered static login page while preventing the complete client IIFE from
   starting.  Parse the actual browser script before checking its features. */
try {
  new Function(script);
} catch (error) {
  throw new Error(`web client script syntax regression: ${error?.message || error}`);
}
/* LOGIN.CPP selects these twelve base SPR numbers, but the core REALBIN has
   unrelated static bitmaps at the same decimal keys (100000 is the blue pet
   that previously covered the character-selection scene).  The pre-world
   page therefore needs its own tiny SPR table instead of waiting for the
   complete 44 MB field/battle table or borrowing the static bitmap. */
const expectedCreationSprites = Array.from({length: 12}, (_, index) => String(100000 + index * 20));
const creationSpriteFilename = path.join(__dirname, "assets", "original", "creation-sprites.json");
const creationSpriteBytes = fs.statSync(creationSpriteFilename).size;
const creationSpritePayload = JSON.parse(fs.readFileSync(creationSpriteFilename, "utf8"));
if (Object.keys(creationSpritePayload.sprites || {}).sort().join(",") !== expectedCreationSprites.sort().join(",") ||
    creationSpriteBytes > 64 * 1024) {
  throw new Error(`character-selection SPR pack shape/size regression: ${creationSpriteBytes}`);
}
for (const graphic of expectedCreationSprites) {
  const actions = creationSpritePayload.sprites?.[graphic]?.actions || [];
  if (actions.length !== 2 || actions.some(action => Number(action.direction) !== 0) ||
      actions.map(action => Number(action.action)).sort((a, b) => a - b).join(",") !== "3,4" ||
      actions.some(action => !Array.isArray(action.frames) || !action.frames.length)) {
    throw new Error(`character-selection SPR rows drifted for ${graphic}`);
  }
}
if (creationSpritePayload.sprites["100000"].actions.find(action => action.action === 3)?.frames?.[0]?.file !== "bitmaps/bitmap_10201.png" ||
    creationSpritePayload.sprites["100220"].actions.find(action => action.action === 4)?.frames?.[0]?.file !== "bitmaps/bitmap_77443.png") {
  throw new Error("character-selection SPR pack no longer resolves the native first/last player graphics");
}
/* The field Action window must not wait for the complete ~42 MB NPC/battle
   table.  Keep a compact all-direction/all-action pack for the twelve stock
   player graphics and verify that every native row is present. */
const expectedFieldSprites = [...expectedCreationSprites, "100025"];
const fieldSpriteFilename = path.join(__dirname, "assets", "original", "field-sprites.json");
const fieldSpriteBytes = fs.statSync(fieldSpriteFilename).size;
const fieldSpritePayload = JSON.parse(fs.readFileSync(fieldSpriteFilename, "utf8"));
if (Object.keys(fieldSpritePayload.sprites || {}).sort().join(",") !== expectedFieldSprites.sort().join(",") ||
    fieldSpriteBytes > 1024 * 1024) {
  throw new Error(`field Action SPR pack shape/size regression: ${fieldSpriteBytes}`);
}
for (const graphic of expectedFieldSprites) {
  const rows = fieldSpritePayload.sprites?.[graphic]?.actions || [];
  for (let action = 0; action <= 12; action++) for (let direction = 0; direction < 8; direction++) {
    const row = rows.find(item => Number(item.direction) === direction && Number(item.action) === action);
    if (!row || !Array.isArray(row.frames) || !row.frames.length) {
      throw new Error(`field Action SPR row missing for ${graphic}/${direction}/${action}`);
    }
  }
}
/* The first visible field frame must never use an animated graphic's packed
   REALBIN fallback.  PATTERN.CPP can select any frame in the running
   STAND/WALK rows, so the bootstrap must retain both rows in full; the
   complete 42 MB action table remains a background resource. */
const fieldBootstrapFilename = path.join(__dirname, "assets", "original", "field-bootstrap-sprites.json");
const fieldBootstrapBytes = fs.statSync(fieldBootstrapFilename).size;
const fieldBootstrapPayload = JSON.parse(fs.readFileSync(fieldBootstrapFilename, "utf8"));
const fieldBootstrapEntries = Object.entries(fieldBootstrapPayload.sprites || {});
let fieldBootstrapFrames = 0;
if (fieldBootstrapEntries.length !== 770 || fieldBootstrapBytes > 16 * 1024 * 1024) {
  throw new Error(`field bootstrap SPR pack shape/size regression: ${fieldBootstrapEntries.length}/${fieldBootstrapBytes}`);
}
for (const [graphic, sprite] of fieldBootstrapEntries) {
  const rows = sprite.actions || [];
  if (rows.length !== 16 || rows.some(row => ![3,4].includes(Number(row.action)) ||
      Number(row.direction) < 0 || Number(row.direction) > 7 || !Array.isArray(row.frames) || row.frames.length < 4)) {
    throw new Error(`field bootstrap SPR rows drifted for ${graphic}`);
  }
  fieldBootstrapFrames += rows.reduce((total, row) => total + row.frames.length, 0);
}
if (fieldBootstrapFrames !== 159075) {
  throw new Error(`field bootstrap SPR frame coverage regression: ${fieldBootstrapFrames}`);
}
const creationLoaderStart = script.indexOf("  function loadCreationSpriteManifest");
const creationLoaderEnd = script.indexOf("  function loadSpriteManifest", creationLoaderStart);
const creationPaintStart = script.indexOf("  function paintCreationCanvas");
const creationPaintEnd = script.indexOf("  function renderCreation", creationPaintStart);
const openCreationStart = script.indexOf("  function openCreation");
const openCreationEnd = script.indexOf("  function finishCreation", openCreationStart);
if (creationLoaderStart < 0 || creationLoaderEnd <= creationLoaderStart ||
    !script.slice(creationLoaderStart, creationLoaderEnd).includes("creation-sprites.json") &&
    !script.includes('const CREATION_SPRITE_MANIFEST_URL="/assets/creation-sprites.json')) {
  throw new Error("character selection must load its pre-world SPR resource");
}
if (creationPaintStart < 0 || creationPaintEnd <= creationPaintStart ||
    !script.slice(creationPaintStart, creationPaintEnd).includes("creationGraphics.every") ||
    !script.slice(creationPaintStart, creationPaintEnd).includes("walking?4:3") ||
    !script.includes("return assetState.sprites||assetState.fieldSprites||assetState.fieldBootstrapSprites||assetState.creationSprites||assetState.manifest?.sprites||null") ||
    !script.slice(openCreationStart, openCreationEnd).includes("loadCreationSpriteManifest()")) {
  throw new Error("character selection must wait for native SPR rows and animate hover with ANIM_WALK");
}
/* NETMAIN.CPP runs its idle Echo check for every connected state, including
   LOGIN.CPP's character creation.  Restricting the browser timer to world /
   battle lets the local 2.5 GMSV close a new account before point assignment
   finishes. */
const nativeNetMain = fs.readFileSync(__dirname + "/../../vendor/upstream/code_sa_client/SYSTEM/NETMAIN.CPP", "latin1");
if (!/writetime\s*\+\s*30\s*\*\s*1000\s*<\s*GetTickCount\(\)[\s\S]{0,180}lssproto_Echo_send\(sockfd,\s*"hoge"\)/.test(nativeNetMain)) {
  throw new Error("unexpected native global Echo contract");
}
const heartbeatStart = script.indexOf("  /* NETMAIN.CPP sends Echo");
const heartbeatEnd = script.indexOf("  /* Keep the FIELD.CPP", heartbeatStart);
const heartbeatSource = script.slice(heartbeatStart, heartbeatEnd);
if (heartbeatStart < 0 || heartbeatEnd <= heartbeatStart ||
    !heartbeatSource.includes('if(!app.transport||app.logoutPending)return;') ||
    heartbeatSource.includes('app.phase==="world"') ||
    !script.includes('app.phase==="character-create"||app.phase==="login-error"')) {
  throw new Error("connected title/character screens must retain the native global Echo and disconnect UI");
}
/* This 2.5 server includes Arminius 6.22's server-owned random encounter
   loop.  It intentionally suppresses the old S:E probability packet and
   rolls CONNECT.CEP after accepted W steps.  Porting MAP.CPP::_checkEncount
   into the browser would make every step roll twice: once here and once in
   GMSV.  Verify both halves of that contract instead of treating a short
   no-encounter walk as evidence that the client must send EN. */
const legacyWalkSource = fs.readFileSync(__dirname + "/../../server/legacy/source/2.5/gmsv/char/char_walk.c", "latin1");
const legacyCharSource = fs.readFileSync(__dirname + "/../../server/legacy/source/2.5/gmsv/char/char.c", "latin1");
if (!legacyWalkSource.includes("int cep = CONNECT_get_CEP(enfd)") ||
    !legacyWalkSource.includes("if (rand()%120<cep)") ||
    !legacyWalkSource.includes("lssproto_EN_recv(enfd,") ||
    !legacyWalkSource.includes("CONNECT_set_CEP(enfd, cep)")) {
  throw new Error("deployed 2.5 walk source lost its server-owned encounter roll");
}
if (!/case\s+'e'\s*:\s*return\s+"\\0"\s*;/.test(legacyCharSource)) {
  throw new Error("server-owned encounter mode must suppress the client S:E probability packet");
}
const startNextMoveStart = script.indexOf("  function startNextMove(){");
const startNextMoveEnd = script.indexOf("  function directionFor", startNextMoveStart);
const startNextMoveSource = script.slice(startNextMoveStart, startNextMoveEnd);
const startNextMoveExecutable = startNextMoveSource
  .replace(/\/\*[\s\S]*?\*\//g, "")
  .replace(/\/\/[^\n]*/g, "");
if (startNextMoveStart < 0 || startNextMoveEnd <= startNextMoveStart ||
    !startNextMoveSource.includes("ordinary field walking therefore emits W only") ||
    /send\(["']EN["']/.test(startNextMoveExecutable)) {
  throw new Error("ordinary Web walking must wait for the authoritative 2.5 server EN packet");
}
/* Ordinary 2.5 owner walking has no C/XYD echo.  Beginning a new local leg
   must extend the protected route without re-arming a background S:c poll:
   that poll can observe the first cell of a two-step W and cause the visible
   one-cell rollback reported on long walks. */
const beginMovePredictionStart = script.indexOf("  function beginMovePrediction(from,target){");
const beginMovePredictionEnd = script.indexOf("  function maybeReleaseMovePrediction", beginMovePredictionStart);
if (beginMovePredictionStart < 0 || beginMovePredictionEnd <= beginMovePredictionStart) {
  throw new Error("move prediction boundary not found");
}
const acknowledgedPrediction = {movePredictionActive:true,movePredictionStartedAt:1,movePredictionServerVersion:4,serverPositionVersion:5,movePredictionSyncRequested:false,movePredictionSyncAttempts:0,movePredictionTrail:[[10,10]]};
let predictionSchedules=0;
const makeBeginMovePrediction = state => new Function("app", "sameMovePoint", "scheduleMovePredictionRelease",
  `${script.slice(beginMovePredictionStart, beginMovePredictionEnd)};return beginMovePrediction;`)(
    state,
    (left, right) => Array.isArray(left) && Array.isArray(right) && left[0] === right[0] && left[1] === right[1],
    () => { predictionSchedules++; },
  );
makeBeginMovePrediction(acknowledgedPrediction)([10, 10], [10, 11]);
if (acknowledgedPrediction.movePredictionSyncRequested || predictionSchedules!==1 ||
    !acknowledgedPrediction.movePredictionTrail.some(point=>point[0]===10&&point[1]===11)) {
  throw new Error("new local movement must extend prediction without polling S:c");
}
/* Same-route samples are delayed progress even when they arrive repeatedly;
   only an off-route coordinate is an authoritative correction. */
const maybeReleaseStart = script.indexOf("  function maybeReleaseMovePrediction(point){");
const maybeReleaseEnd = script.indexOf("  function noteServerMove", maybeReleaseStart);
if (maybeReleaseStart < 0 || maybeReleaseEnd <= maybeReleaseStart) {
  throw new Error("move prediction release boundary not found");
}
const makeMaybeRelease = (state, hooks) => new Function(
  "app", "sameMovePoint", "clearMovePrediction", "predictedMoveContains",
  "refreshSettledMoveState", "cancelPendingMove",
  `${script.slice(maybeReleaseStart, maybeReleaseEnd)};return maybeReleaseMovePrediction;`,
)(state, hooks.same, hooks.clear, hooks.contains, hooks.refresh, hooks.cancel);
const tailRace = {
  movePredictionActive: true, pendingMove: false, moveQueue: [], moveSentSteps: 0,
  position: [10, 11],movePredictionTrail:[[10,10],[10,11]],
};
let tailRaceCancelled = 0;
let tailRaceRefreshed = 0;
const tailRaceHooks = {
  same: (left, right) => left[0] === right[0] && left[1] === right[1],
  clear: () => { throw new Error("a one-tile intermediate sample must not release prediction"); },
  contains: point=>tailRace.movePredictionTrail.some(item=>item[0]===point[0]&&item[1]===point[1]),
  refresh: () => { tailRaceRefreshed++; },
  cancel: () => { tailRaceCancelled++; },
};
const maybeReleaseTailRace = makeMaybeRelease(tailRace, tailRaceHooks);
maybeReleaseTailRace([10, 10]);
maybeReleaseTailRace([10,10]);
if (tailRaceCancelled || tailRaceRefreshed!==2) {
  throw new Error("repeated intermediate route samples must never roll the player back");
}
maybeReleaseTailRace([8,8]);
if (tailRaceCancelled!==1) {
  throw new Error("an off-route authoritative sample must still correct local movement");
}
/* A normal 2.5 W has no owner C/XYD echo.  After its short wire-drain grace
   period the completed local route becomes the soft server anchor, while the
   prediction trail remains available to absorb a delayed old C/CA sample. */
const promoteMoveAnchorStart = script.indexOf("  function promoteSettledMoveAnchor(target){");
const promoteMoveAnchorEnd = script.indexOf("  /* A normal 2.5 W packet", promoteMoveAnchorStart);
if (promoteMoveAnchorStart < 0 || promoteMoveAnchorEnd <= promoteMoveAnchorStart) {
  throw new Error("settled movement anchor helper boundary not found");
}
const promotedMove = {position:[18,23],serverPosition:[18,21],serverPositionVersion:4,serverPositionReceivedAt:0};
const promoteMoveAnchor = new Function("app","sameMovePoint",`${script.slice(promoteMoveAnchorStart,promoteMoveAnchorEnd)};return promoteSettledMoveAnchor;`)(
  promotedMove,
  (left,right) => Array.isArray(left) && Array.isArray(right) && left[0] === right[0] && left[1] === right[1],
);
if (!promoteMoveAnchor([18,23]) || promotedMove.serverPosition[0] !== 18 || promotedMove.serverPosition[1] !== 23 ||
    promotedMove.serverPositionVersion !== 5 || promotedMove.serverPositionReceivedAt <= 0 ||
    promoteMoveAnchor([17,23])) {
  throw new Error(`completed W must promote only its matching soft anchor: ${JSON.stringify(promotedMove)}`);
}
/* Exercise the production receiveActions() path as well as the isolated
   prediction helper.  A delayed CA for the first tile may arrive after the
   browser has already painted the second tile; the actor wrapper is updated
   from CA first, so the owner branch must immediately restore app.position
   while retaining the received server sample for later interaction checks. */
const delayedCAReceiveStart = script.indexOf("  function receiveActions(text) {");
const delayedCAReceiveEnd = script.indexOf("  function updateHUD()", delayedCAReceiveStart);
if (delayedCAReceiveStart < 0 || delayedCAReceiveEnd <= delayedCAReceiveStart) {
  throw new Error("receiveActions boundary missing for delayed-CA regression");
}
const delayedCAState = {
  phase: "world", battle: false, floor: 1005, character: "Hero", playerActorId: 1,
  position: [18, 23], serverPosition: [18, 21], serverPositionVersion: 4,
  movePredictionActive: true, moveWirePending: false, pendingMove: false,
  moveSentSteps: 0, moveQueue: [], walkAnimation: null,
  movePredictionTrail: [[18, 21], [18, 22], [18, 23]], actors: new Map(),
  direction: 3, partyLeader: false,
};
delayedCAState.actors.set(1, {id: 1, kind: "character", objectType: 1, name: "Hero",
  x: 18, y: 23, action: 3, caAction: 3, wireDirection: 0, direction: 3});
let delayedCAPoint = null;
const delayedCAHelpers = new Function(
  "app", "Protocol", "stateNumber", "isInsideFloor", "localMovePredictionActive",
  "recordServerPosition", "maybeReleaseMovePrediction", "clientDirectionFromServer",
  "serverDirectionFromClient", "clearMovePrediction", "cancelPendingMove",
  "triggerMapEvent", "updateHUD", "renderWorld", "renderWorldOverlay",
  "scheduleWorldAnimation", "setLocalActorAction", "CA_TO_SPRITE_ACTION",
  "fieldActionLoops", "performance",
  `${script.slice(delayedCAReceiveStart, delayedCAReceiveEnd)};return receiveActions;`,
)(
  delayedCAState,
  {base62: value => Number.parseInt(String(value || "0"), 36) || 0},
  (value, fallback = 0) => { const number = Number(value); return Number.isFinite(number) ? number : fallback; },
  () => true,
  () => true,
  point => { delayedCAState.serverPosition = [...point]; delayedCAState.serverPositionVersion++; },
  point => { delayedCAPoint = [...point]; },
  value => (Number(value) + 3) % 8,
  value => Number(value),
  () => { delayedCAState.movePredictionActive = false; },
  () => { throw new Error("delayed same-route CA must not cancel movement"); },
  () => false,
  () => {}, () => {}, () => {}, () => {},
  () => {}, {}, () => false, performance,
);
delayedCAHelpers("1|18|21|1|0");
const delayedCAActor = delayedCAState.actors.get(1);
if (delayedCAPoint?.[0] !== 18 || delayedCAPoint?.[1] !== 21 ||
    delayedCAState.position[0] !== 18 || delayedCAState.position[1] !== 23 ||
    delayedCAActor?.x !== 18 || delayedCAActor?.y !== 23 ||
    delayedCAState.serverPosition[0] !== 18 || delayedCAState.serverPosition[1] !== 21) {
  throw new Error(`receiveActions rolled back a locally completed route: ${JSON.stringify({position: delayedCAState.position, actor: [delayedCAActor?.x, delayedCAActor?.y], server: delayedCAState.serverPosition, delayedCAPoint})}`);
}
/* The fish-bone is a painted legacy sprite.  A browser Pointer Lock would
   move/recapture the user's real mouse during a map fold, which is the
   opposite of the native client's behavior and makes the cursor appear to
   jump when the CSS back-buffer is compressed.  Keep this invariant explicit
   so a future cursor refactor cannot reintroduce it. */
if (/requestPointerLock|exitPointerLock|pointerLockElement/i.test(html)) {
  throw new Error("web cursor must not use browser Pointer Lock");
}
if (!/interactive-widget=overlays-content/.test(html) ||
    !/id="account"[^>]*autocapitalize="none"[^>]*autocorrect="off"[^>]*spellcheck="false"/.test(html) ||
    !/id="password"[^>]*autocapitalize="none"[^>]*autocorrect="off"[^>]*spellcheck="false"/.test(html)) {
  throw new Error("mobile login inputs must not resize the scene or auto-capitalise account/password text");
}
if (!/function lockLoginInputViewport\(\)[\s\S]{0,900}lastStableViewport/.test(script) ||
    !/stable\.width\*stable\.height>current\.width\*current\.height/.test(script) ||
    !/document\.addEventListener\("pointerdown",[\s\S]{0,260}lockLoginInputViewport/.test(script) ||
    !/document\.addEventListener\("touchstart",[\s\S]{0,260}lockLoginInputViewport/.test(script)) {
  throw new Error("mobile login must capture the pre-IME viewport before focus resizes visualViewport");
}
if (!/function rememberStableViewport\([\s\S]{0,1800}previousArea/.test(script) ||
    !/id="account"[^>]*enterkeyhint="next"/.test(html) ||
    !/id="password"[^>]*enterkeyhint="done"/.test(html) ||
    !/\$\("account"\)\.addEventListener\("input",[\s\S]{0,420}normalized/.test(script)) {
  throw new Error("mobile login must retain the full pre-IME baseline and suppress keyboard account autocapitalisation");
}
if (!/function fitLegacyViewport\(\)[\s\S]{0,1800}document\.body\.scrollLeft\s*=\s*0[\s\S]{0,360}document\.body\.scrollTop\s*=\s*0/.test(script)) {
  throw new Error("mobile scene fitting must clear stale body scroll offsets after rotation");
}
if (!/function releaseLoginInputViewportOnRotation\([\s\S]{0,1200}lockedLandscape[\s\S]{0,360}loginInputViewportLock=false[\s\S]{0,420}lockLoginInputViewport/.test(script) ||
    !/function settleLegacyViewport\(\)[\s\S]{0,180}releaseLoginInputViewportOnRotation\(visibleViewportSize\(\)\)/.test(script)) {
  throw new Error("mobile login viewport lock must be released and recaptured after portrait/landscape rotation");
}
if (!/function canonicalAccount\(value\)[\s\S]{0,420}replace\(\/\[A-Z\]\/g/.test(script)) {
  throw new Error("web login must canonicalise ASCII account names before sending them to 2.5");
}
if (!/id="account"[^>]*autofocus/.test(html) ||
    !/function focusLoginAccount\(\)[\s\S]{0,420}input\.focus\(\{preventScroll:true\}\)/.test(script) ||
    !/show\(loginScreen\);[\s\S]{0,180}window\.setTimeout\(focusLoginAccount,0\)/.test(script)) {
  throw new Error("login page must focus the username field after it is shown");
}
/* LOGIN.CPP::inputIdPassword() keeps TAB inside the account/password pair,
   while Return from the account row advances to the password.  The Web form's
   transparent OK hit target must never become the third TAB stop. */
const nativeLoginInputSource=fs.readFileSync(__dirname+"/../../vendor/upstream/code_sa_client/SYSTEM/LOGIN.CPP","latin1");
if(!/if\( joy_trg\[ 1 \] & JOY_TAB \)[\s\S]{0,300}oldId == 0[\s\S]{0,160}oldId == 1/.test(nativeLoginInputSource)){
  throw new Error("native login TAB reference boundary drifted");
}
const loginShortcutStart=script.indexOf("  function handleLoginFieldKeydown(event){");
const loginShortcutEnd=script.indexOf('  $("account").addEventListener("keydown",handleLoginFieldKeydown);',loginShortcutStart);
if(loginShortcutStart<0||loginShortcutEnd<=loginShortcutStart)throw new Error("web login shortcut boundary missing");
const loginFocus=[];
const loginFields={account:{id:"account",focus(){loginFocus.push("account");}},password:{id:"password",focus(){loginFocus.push("password");}}};
const handleLoginFieldKeydown=new Function("$",`${script.slice(loginShortcutStart,loginShortcutEnd)};return handleLoginFieldKeydown;`)(id=>loginFields[id]);
for(const [current,key,expected] of [["account","Tab","password"],["password","Tab","account"],["account","Enter","password"]]){
  let prevented=0;
  handleLoginFieldKeydown({key,currentTarget:loginFields[current],isComposing:false,preventDefault(){prevented++;}});
  if(prevented!==1||loginFocus.pop()!==expected)throw new Error(`login ${key} from ${current} did not focus ${expected}`);
}
let passwordEnterPrevented=0;
handleLoginFieldKeydown({key:"Enter",currentTarget:loginFields.password,isComposing:false,preventDefault(){passwordEnterPrevented++;}});
if(passwordEnterPrevented||loginFocus.length)throw new Error("password Enter must remain available to submit the login form");
/* The native client owns its input buffers and has no browser suggestion
   dropdown.  Apply the same contract to static login/chat inputs and to every
   form/editor created by buildAdvancedUI(). */
if (!/id="login-form" autocomplete="off"/.test(html) ||
    !/id="account"[^>]*name="stoneage-account"[^>]*autocomplete="off"[^>]*data-lpignore="true"[^>]*data-1p-ignore="true"/.test(html) ||
    !/id="password"[^>]*name="stoneage-passcode"[^>]*autocomplete="off"[^>]*data-lpignore="true"[^>]*data-1p-ignore="true"/.test(html) ||
    !/id="chat-form"[^>]*autocomplete="off"/.test(html) ||
    !/id="chat-input"[^>]*name="stoneage-live-chat"[^>]*autocomplete="off"[^>]*data-lpignore="true"[^>]*data-1p-ignore="true"/.test(html) ||
    !/function disableBrowserInputHistory\(root=document\)[\s\S]{0,850}autocomplete","off"[\s\S]{0,300}data-lpignore","true"[\s\S]{0,180}data-1p-ignore","true"/.test(script) ||
    !/document\.addEventListener\("focusin",[\s\S]{0,260}disableBrowserInputHistory/.test(script)) {
  throw new Error("web text editors must suppress browser/password-manager input history");
}
/* QUIT must stop audio and use a blank-page fallback when browsers refuse
   window.close().  It must not request the generic sas_17.wav button tone. */
if (!/let loginQuitRequested=false;[\s\S]{0,900}stopBackgroundMusic\(\);stopSoundEffects\(\)/.test(script) ||
    !/login-quit-button"\)\.addEventListener\("click",closeLoginPage\)/.test(script) ||
    !/window\.location\.replace\("about:blank"\)/.test(script) ||
    /login-quit-button"\)\.addEventListener\("click",\(\)=>\{unlockAudio\(\);playSoundEffect\(217\)/.test(script)) {
  throw new Error("QUIT must stop audio and close/fallback without requesting sas_17.wav");
}
if (!/activeSE:new Set\(\)/.test(script) || !/function stopSoundEffects\(\)[\s\S]{0,500}activeSE/.test(script)) {
  throw new Error("audio shutdown must stop active sound effects as well as BGM");
}
if (!/function renderNativeMapCoordinate\(node,direction,value\)[\s\S]{0,700}map-coordinate-wide[\s\S]{0,700}padStart\(3," "\)[\s\S]{0,700}map-coordinate-half/.test(script) ||
    !/renderNativeMapCoordinate\(xNode,"東",app\.position\[0\]\)/.test(script) ||
    !/renderNativeMapCoordinate\(yNode,"南",app\.position\[1\]\)/.test(script)) {
  throw new Error("map coordinates must use native 東/南 labels and fixed full/half-width glyph cells");
}
if (!/id="world-loading-progress"[^>]*role="progressbar"/.test(html) ||
    !/id="world-loading-detail"/.test(html) ||
    !/id="world-loading-retry"/.test(html) ||
    !/main\.field-loading-active #field-ui[\s\S]{0,260}visibility:hidden/.test(html) ||
    !/function mapLoadingProgress\([\s\S]{0,1800}assetNetworkBytes/.test(script) ||
    !/function assetCachedBytes\([\s\S]{0,900}base64/.test(script) ||
    !/function assetNetworkBytes\([\s\S]{0,900}new URL\(relative,ASSET_RESOURCE_ROOT\)/.test(script) ||
    !/function rememberAssetResourceEntry\([\s\S]{0,900}STATIC_RESOURCE_ROOTS/.test(script) ||
    !/function renderMapLoadingProgress\([\s\S]{0,1800}world-loading-detail/.test(script) ||
    !/if\(indeterminate\)bar\.style\.removeProperty\("width"\)/.test(script) ||
    !/function scheduleMapLoadingProgress\(\)[\s\S]{0,1300}stats\.total>0&&stats\.pending===0&&stats\.failed===0\)renderWorld\(true\)[\s\S]{0,180}if\(app\.mapLoading\)scheduleMapLoadingProgress\(\)/.test(script) ||
    !/function retryMapLoading\([\s\S]{0,1200}app\.mapLayerCache=null/.test(script) ||
    !/manifestAttempts/.test(script) ||
    !/preferredStable=stable&&stable\.width\*stable\.height>current\.width\*current\.height/.test(script)) {
  throw new Error("map loading must show progress/received bytes and hide field controls while blocked");
}
if (!/if\(!loading\)\{[\s\S]{0,420}app\.mapLoadingStats\.phase="地图已就绪"[\s\S]{0,120}app\.mapLoadingStats\.progress=100/.test(script)) {
  throw new Error("completed map loading must publish an explicit ready phase");
}
if (!/renderMapLoadingProgress\(loading\?text:""\)/.test(script)) {
  throw new Error("completed map loading must not overwrite the ready phase with the loading default");
}
if (!/const STATIC_RESOURCE_BASE=new URL\("\.\.\/",ASSET_RESOURCE_ROOT\)/.test(script) ||
    !/const ASSET_VERSION_URL=new URL\("_client-version\.json",STATIC_RESOURCE_BASE\)/.test(script)) {
  throw new Error("asset version marker must live beside assets/, maps/ and audio/");
}
/* Fixed field windows append their native CLOSE/RETURN bitmap inside the
   pointer-transparent list that owns the window. Keep the hit explicit and
   avoid a second static button at the same coordinates: otherwise the later
   DOM button wins hit-testing and the visible close CG becomes unclickable. */
if (!/\.legacy-screen \.card-grid>button\[id\$="-close"\][\s\S]{0,700}pointer-events:auto!important/.test(html) ||
    /<button id="(?:inventory|pets|party|titles|album)-close"/.test(html)) {
  throw new Error("fixed field window close buttons must have one explicit hit target");
}
/* MENU.CPP::InitItem records StockDisp centres.  Keep the item window's odd
   271px MakeWindowDisp centre, original ADRN offsets and the complete 2.5
   interaction contract covered together: a context click must never become
   the web MVP's old destructive DI shortcut. */
for (const expected of [
  /inventory:\{bitmap:9179,bitmap2:9180,x:364,y:4,w:272,h:440\}/,
  /const INVENTORY_SLOT_CENTERS=Object\.freeze\(\[\s*\[501,63\],\[499,133\],\[426,133\],\[397,72\],\[448,72\]/,
  /\[397,204\],\[448,204\],\[499,204\],\[550,204\],\[601,204\]/,
  /slot\.style\.left=`\$\{centerX-24\}px`;slot\.style\.top=`\$\{centerY-24\}px`/,
  /appendInventoryBitmap\(slot,info,24,24,"legacy-item-icon"\)/,
  /image\.style\.left=`\$\{centerX\+offsetX\}px`;image\.style\.top=`\$\{centerY\+offsetY\}px`/,
  /#inventory-screen #inventory-close\{left:460px;top:418px/,
  /inventory-jujutsu\{left:541px;top:30px;width:88px;height:27px;background-image:url\('\/assets\/bitmaps\/bitmap_9188\.png'\)/,
  /inventory-gold-drop-button\{left:538px;top:116px;width:52px;height:17px/,
  /inventory-gold-increase\{left:594px;top:118px;width:16px;height:14px/,
  /inventory-gold-decrease\{left:613px;top:118px;width:16px;height:14px/,
  /send\("MI",\[from,to\]\)/,
  /point&&point\.x<=365\)send\("DI",\[app\.position\[0\],app\.position\[1\],from\]\)/,
  /send\("DG",\[app\.position\[0\],app\.position\[1\],amount\]\)/,
  /send\("ID",\[app\.position\[0\],app\.position\[1\],app\.selectedItem,target\]\)/,
  /send\("MU",\[app\.position\[0\],app\.position\[1\],app\.selectedMagic,target\]\)/,
  /inventory-target-party[\s\S]{0,400}bitmap_9190\.png/,
]) {
  if (!expected.test(html)) throw new Error(`native item-window contract drifted: ${expected}`);
}
if (/contextmenu[^\n]{0,500}send\("DI"/.test(script)) {
  throw new Error("item context click must not drop an item; DI is drag-left only");
}
/* NETPROC.CPP::lssproto_I_recv mutates only the indexed pc.item slots named
   in an I packet.  DI returns a single empty ten-field record for the dropped
   slot; treating that delta as a full snapshot used to blank the other 19
   browser slots. */
const inventoryParserStart = script.indexOf("  function receiveInventory");
const inventoryParserEnd = script.indexOf("  const INVENTORY_SLOT_CENTERS", inventoryParserStart);
if (inventoryParserStart < 0 || inventoryParserEnd <= inventoryParserStart) {
  throw new Error("inventory delta parser boundary missing");
}
const inventoryStatusNode = {textContent: ""};
const inventoryContext = {
  app: {
    inventory: [5, 6, 7, 8].map(index => ({index, name: `meat-${index}`, graphic: 24008})),
    trade: null,
  },
  decimal(value, fallback = 0) {
    const number = Number.parseInt(String(value), 10);
    return Number.isFinite(number) ? number : fallback;
  },
  unescapeCharacterOption(value) { return String(value || ""); },
  $(id) { if (id !== "inventory-status") throw new Error(`unexpected inventory test node ${id}`); return inventoryStatusNode; },
  renderInventory() {},
  renderTrade() {},
};
vm.createContext(inventoryContext);
vm.runInContext(script.slice(inventoryParserStart, inventoryParserEnd) + `
receiveInventory("5|||||||||", true);
receiveInventory("9|new-meat||0|memo|24008|0|1|0|1", true);
this.inventoryDeltaResult = app.inventory.map(item => ({index:item.index,name:item.name,graphic:item.graphic,target:item.target}));
`, inventoryContext);
if (JSON.stringify(inventoryContext.inventoryDeltaResult) !== JSON.stringify([
  {index: 6, name: "meat-6", graphic: 24008},
  {index: 7, name: "meat-7", graphic: 24008},
  {index: 8, name: "meat-8", graphic: 24008},
  {index: 9, name: "new-meat", graphic: 24008, target: 1},
]) || !inventoryStatusNode.textContent.includes("4 件")) {
  throw new Error(`indexed I packets must merge one slot like native lssproto_I_recv: ${JSON.stringify(inventoryContext.inventoryDeltaResult)}`);
}
/* FIELD.CPP::ClearBackSurface() fills an opaque colour-0 back-buffer before
   PutBmp(), then DirectDraw Flip/Blt waits while presenting that completed
   frame.  The private browser buffer may use the low-latency GPU hint, but
   the visible surface must stay opaque and compositor-synchronised. */
if (!/function getWorld2DContext\([\s\S]{0,1200}isFrontBuffer=canvas===visible[\s\S]{0,1200}isFrontBuffer[\s\S]{0,160}\?\{alpha:false,willReadFrequently:false\}[\s\S]{0,160}:\{alpha:false,desynchronized:true,willReadFrequently:false\}/.test(script) ||
    !/ctx\.globalCompositeOperation="copy";ctx\.fillStyle="#000";ctx\.fillRect\(0,0,canvas\.width,canvas\.height\);ctx\.globalCompositeOperation="source-over"/.test(script)) {
  throw new Error("world renderer must keep an opaque back-buffer and synchronised front-buffer presentation");
}
/* Keep the preserved 2.5 FIELD.CPP coordinates covered by the protocol smoke
   test as well.  The web surface uses the trade-capable 140/132px plates and
   the four-button left HUD; optional 8.5-only controls remain excluded. */
const nativeField = fs.readFileSync(__dirname + "/../../vendor/upstream/code_sa_client/SYSTEM/FIELD.CPP", "latin1");
const nativeField85 = fs.readFileSync(__dirname + "/../../reference/anson1788-stoneage/石器时代8.5客户端最新源代码/石器源码/system/field.cpp", "latin1");
if (!/leftUpPanelX\+52,\s*leftUpPanelY\+28,[\s\S]{0,180}CG_FIELD_MENU_LEFT/.test(nativeField) ||
    !/rightUpPanelX\+68,\s*rightUpPanelY\+32,[\s\S]{0,180}CG_FIELD_MENU_RIGHT/.test(nativeField) ||
    !/w\s*=\s*3;\s*h\s*=\s*4;[\s\S]{0,120}x\s*=\s*16;[\s\S]{0,100}y\s*=\s*16;/.test(nativeField) ||
    !/w\s*=\s*3;\s*h\s*=\s*6;[\s\S]{0,120}x\s*=\s*440;[\s\S]{0,100}y\s*=\s*16;/.test(nativeField)) {
  throw new Error("unexpected native 2.5 field-control/window coordinate contract");
}
if (!/fieldBtnHitId\[FIELD_FUNC_TRADE\]\s*=\s*StockDispBuffer\(leftUpPanelX\s*\+\s*104\s*\+\s*10,\s*leftUpPanelY\s*\+\s*28\s*-\s*10,[\s\S]{0,120}tradeBtnGraNo\[tradeBtn\]/.test(nativeField85) ||
    !/lssproto_TD_send\(sockfd,\s*"D\|D"\)/.test(nativeField85)) {
  throw new Error("unexpected native 2.5 trade button/TD command contract");
}
const tradePlate = battleManifest.bitmaps?.["26233"], tradeOff = battleManifest.bitmaps?.["26234"], tradeOn = battleManifest.bitmaps?.["26235"];
if (tradePlate?.file !== "bitmaps/bitmap_126232.png" || tradePlate?.width !== 140 || tradePlate?.height !== 54 ||
    tradePlate?.xoffset !== -78 || tradePlate?.yoffset !== -28 ||
    tradeOff?.file !== "bitmaps/bitmap_126233.png" || tradeOff?.width !== 32 || tradeOff?.height !== 30 ||
    tradeOff?.xoffset !== -17 || tradeOff?.yoffset !== -14 ||
    tradeOn?.file !== "bitmaps/bitmap_126234.png" || tradeOn?.width !== 32 || tradeOn?.height !== 30) {
  throw new Error("generated 2.5 trade HUD resources drifted from native ADRN metadata");
}
/* The wheel button is only the field entry.  sa_2903's compiled 2.5 trade
   branch uses logical 40000 (620x456 at 10,0); 26328 belongs to the later
   8.5 switch and resolves to an unrelated sprite in this resource pack. */
const tradeWindow = battleManifest.bitmaps?.["40000"];
if (tradeWindow?.file !== "bitmaps/bitmap_126231.png" || tradeWindow?.width !== 620 || tradeWindow?.height !== 456 ||
    tradeWindow?.xoffset !== -310 || tradeWindow?.yoffset !== -228 || tradeWindow?.bmp_number !== 40000) {
  throw new Error("classic 2.5 trade window resource drifted from sa_2903");
}
for (const [logical,file,width,height,xoffset,yoffset] of [
  ["26180","bitmap_9270.png",32,16,-16,-8],["26181","bitmap_9271.png",32,16,-16,-8],
  ["26182","bitmap_9272.png",32,16,-16,-8],["26183","bitmap_9273.png",32,16,-16,-8],
  ["26064","bitmap_9183.png",16,14,94,-106],["26065","bitmap_9184.png",16,14,94,-106],
  ["26066","bitmap_9185.png",16,14,113,-106],["26067","bitmap_9186.png",16,14,113,-106],
  ["26062","bitmap_9181.png",52,17,38,-108],["26063","bitmap_9182.png",52,17,38,-108],
]) {
  const info=battleManifest.bitmaps?.[logical];
  if(info?.file!==`bitmaps/${file}`||info?.width!==width||info?.height!==height||info?.xoffset!==xoffset||info?.yoffset!==yoffset){
    throw new Error(`native trade control ${logical} ADRN metadata drifted: ${JSON.stringify(info)}`);
  }
}
const tradeWindowPng = fs.readFileSync(path.join(__dirname, "assets", "original", tradeWindow.file));
if (tradeWindowPng.readUInt32BE(16) !== 620 || tradeWindowPng.readUInt32BE(20) !== 456 ||
    !battleExtractorSource.includes('"trade_window_25": 40000') || !battleExtractorSource.includes('parser.add_argument("--ui-only"')) {
  throw new Error("classic 2.5 trade window was not extracted by the UI-only asset path");
}
const tradeMarkup = html.match(/<section id="trade-screen"[\s\S]*?<\/section>/)?.[0] || "";
if (!/id="trade-window-art" data-src="\/assets\/bitmaps\/bitmap_126231\.png"/.test(tradeMarkup) ||
    (tradeMarkup.match(/data-trade-offer=/g) || []).length !== 2 ||
    !/id="trade-inventory-grid"/.test(tradeMarkup) || !/id="trade-confirm"/.test(tradeMarkup) ||
    /26328|bitmap_126230/.test(tradeMarkup)) {
  throw new Error("classic 2.5 trade surface markup is incomplete or uses the 8.5 plate");
}
for (const expected of [
  /#trade-window-art\s*\{[^}]*left:10px; top:0; width:620px; height:456px/,
  /#trade-confirm\s*\{[^}]*left:369px; background-image:url\('\/assets\/bitmaps\/bitmap_9211\.png'\)/,
  /#trade-cancel\s*\{[^}]*left:501px; background-image:url\('\/assets\/bitmaps\/bitmap_9170\.png'\)/,
  /#trade-pet-picker \.trade-pet-stat\s*\{[^}]*left:167px; width:24px;[^}]*white-space:pre/,
  /#trade-pet-picker \.trade-pet-max-hp\s*\{[^}]*top:178px/,
  /renderTradePetPanel\(\$\("trade-pet-picker"\),tradeCurrentPet\(\)\?\.pet\|\|null,true\)/,
  /trade\.petCursor=candidates\[position\]\.slot;trade\.petDirection=1;renderTrade\(\)/,
  /#trade-pet-prev,#trade-pet-next\s*\{ top:75px; width:32px; height:16px; \}/,
  /#trade-pet-prev\s*\{ left:466px; background-image:url\('\/assets\/bitmaps\/bitmap_9270\.png'\); \}/,
  /#trade-pet-next\s*\{ left:500px; background-image:url\('\/assets\/bitmaps\/bitmap_9272\.png'\); \}/,
  /#trade-gold-up,#trade-gold-down\s*\{ top:105px; width:16px; height:14px; \}/,
  /#trade-gold-place\s*\{ left:573px; top:168px; \} #trade-pet-place \{ left:376px; top:210px; \}/,
  /const column=\(index-5\)%5,row=Math\.floor\(\(index-5\)\/5\)/,
  /slot\.style\.left=`\$\{332\+column\*51\}px`;slot\.style\.top=`\$\{248\+row\*48\}px`/,
  /case "TD": handleTradeMessage\(values\[0\]\|\|""\);break;/,
]) {
  if (!expected.test(html)) throw new Error(`classic 2.5 trade layout/dispatch regression: ${expected}`);
}
const nativeTradeMenu = fs.readFileSync(__dirname + "/../../reference/anson1788-stoneage/石器时代8.5客户端最新源代码/石器源码/system/menu.cpp", "latin1");
if (!/pActPet3 = MakeAnimDisp\(480, 230, pet\[tradePetIndex\]\.graNo, ANIM_DISP_PET\)/.test(nativeTradeMenu) ||
    !/pAct->anim_ang = 1;[\s\S]{0,500}pAct->x = x;[\s\S]{0,80}pAct->y = y;/.test(nativeTradeMenu) ||
    !/case ANIM_DISP_PET:[\s\S]{0,300}pAct->anim_ang\+\+;[\s\S]{0,180}pattern\(pAct, ANM_NOMAL_SPD, ANM_LOOP\);/.test(nativeTradeMenu) ||
    !/tradeWndFontNo\[2\] = StockDispBuffer\(x \+ 452 \+ 20, y \+ 63 \+ 8,[^\n]+CG_TRADE_LEFT_BTN_UP/.test(nativeTradeMenu) ||
    !/tradeWndFontNo\[3\] = StockDispBuffer\(x \+ 486 \+ 20, y \+ 63 \+ 8,[^\n]+CG_TRADE_RIGHT_BTN_UP/.test(nativeTradeMenu) ||
    !/tradeWndFontNo\[4\] = StockDispBuffer\(x \+ 554 - 94, y \+ 93 \+ 106,[^\n]+CG_TRADE_UP_BTN_UP/.test(nativeTradeMenu) ||
    !/tradeWndFontNo\[7\] = StockDispBuffer\(x \+ 365 - 62 \+ 25, y \+ 190 \+ 108 \+ 8,[^\n]+CG_TRADE_PUT_BTN_UP/.test(nativeTradeMenu)) {
  throw new Error("compiled 2.5 trade pet ACTION contract drifted from menu.cpp");
}
const tradePetSpriteStart = script.indexOf("  function tradePetSprite");
const tradePetSpriteEnd = script.indexOf("  function renderTradePetPanel", tradePetSpriteStart);
if (tradePetSpriteStart < 0 || tradePetSpriteEnd <= tradePetSpriteStart) throw new Error("trade pet preview helper missing");
const tradePreviewEvents = {}, tradePreviewObserved = {}, tradePreviewRafs = [];
const tradePreviewAnimation = {direction:1,action:3,frame_ms:10,frames:[
  {file:"bitmaps/trade-a.png",x:2,y:3,xoffset:-20,yoffset:-40},
  {file:"bitmaps/trade-b.png",x:4,y:5,xoffset:-18,yoffset:-38},
]};
const tradePreviewContext = {
  app:{trade:{active:true,petDirection:1}}, assetState:{spritesReady:true}, LEGACY_FIELD_ANIMATION_TICK_MS:1000/60,
  spriteEntryForActor(actor){ tradePreviewObserved.actor={...actor}; return {sprite:{actions:[tradePreviewAnimation]}}; },
  spriteAnimationForAction(actor,direction,action){ tradePreviewObserved.request=[direction,action]; return direction===1&&action===3?tradePreviewAnimation:null; },
  loadSpriteManifest(){ throw new Error("full SPR table should already be ready"); },
  document:{createElement(){ return {className:"",alt:"",tabIndex:-1,dataset:{},style:{},attributes:{},isConnected:true,setAttribute(name,value){this.attributes[name]=value;},addEventListener(name,handler){tradePreviewEvents[name]=handler;}}; }},
  window:{requestAnimationFrame(callback){tradePreviewRafs.push(callback);}}, performance:{now(){return 1000;}},
  $(id){ return id==="trade-screen"?{classList:{contains(){return false;}}}:null; },
  playSoundEffect(number){ tradePreviewObserved.sound=number; }, renderTrade(){ tradePreviewObserved.rendered=(tradePreviewObserved.rendered||0)+1; },
};
vm.createContext(tradePreviewContext);
vm.runInContext(script.slice(tradePetSpriteStart, tradePetSpriteEnd) + `\nthis.preview=tradePetSprite({graphic:100251},true);`, tradePreviewContext);
const tradePreview = tradePreviewContext.preview;
if (tradePreviewObserved.actor?.direction !== 1 || tradePreviewObserved.actor?.action !== 3 || tradePreview?.dataset?.nativeDirection !== "1" ||
    tradePreview?.src !== "/assets/bitmaps/trade-a.png" || tradePreview?.style?.left !== "134px" || tradePreview?.style?.top !== "158px" ||
    tradePreview?.attributes?.role !== "button" || tradePreviewRafs.length !== 1) {
  throw new Error(`trade pActPet3 must begin on native direction 1 at (480,230): ${JSON.stringify({actor:tradePreviewObserved.actor,direction:tradePreview?.dataset?.nativeDirection,src:tradePreview?.src,left:tradePreview?.style?.left,top:tradePreview?.style?.top,raf:tradePreviewRafs.length})}`);
}
tradePreviewRafs.shift()(1200);
if (tradePreview.src !== "/assets/bitmaps/trade-b.png" || tradePreview.style.left !== "138px" || tradePreview.style.top !== "162px") {
  throw new Error("trade pActPet3 must loop every extracted STAND frame with native offsets");
}
tradePreviewEvents.click();
if (tradePreviewContext.app.trade.petDirection !== 2 || tradePreviewObserved.sound !== 217 || tradePreviewObserved.rendered !== 1) {
  throw new Error("clicking the native trade pet ACTION must rotate to the next direction");
}
const tradePairStart = script.indexOf("  function tradeConfirmationPair");
const tradePairEnd = script.indexOf("  function confirmTrade", tradePairStart);
if (tradePairStart < 0 || tradePairEnd <= tradePairStart) throw new Error("classic trade confirmation helpers missing");
const tradePairContext = {decimal(value, fallback = 0) { const number = Number.parseInt(String(value), 10); return Number.isFinite(number) ? number : fallback; }};
vm.createContext(tradePairContext);
vm.runInContext(script.slice(tradePairStart, tradePairEnd) + `\nthis.tradeVector=tradeConfirmationPayload({active:true,peerFd:77,peerName:"bob",mineSlots:[{kind:"I",itemIndex:5},null],minePet:{slot:2},peerSlots:[{kind:"G",amount:33},{kind:"I",itemIndex:9}],peerPet:null});`, tradePairContext);
if (tradePairContext.tradeVector !== "T|77|bob|K|I|5|I|-1|P|2|G|33|I|9|P|-1") {
  throw new Error(`classic trade must send exactly six 2.5 confirmation groups: ${tradePairContext.tradeVector}`);
}
const tradeItemParserStart = script.indexOf("  function parseTradePeerItem");
const tradeItemParserEnd = script.indexOf("  function parseTradePeerPet", tradeItemParserStart);
const tradeItemContext = {decimal: tradePairContext.decimal, unescapeCharacterOption: value => String(value || "")};
vm.createContext(tradeItemContext);
vm.runInContext(script.slice(tradeItemParserStart, tradeItemParserEnd) + `
this.item25=parseTradePeerItem(["T","77","bob","I","1","123","stone","effect","5","20%"]);
this.item85=parseTradePeerItem(["T","77","bob","I","1","123","base","free","effect85","6","30%"]);`, tradeItemContext);
if (tradeItemContext.item25?.name !== "stone" || tradeItemContext.item25?.effect !== "effect" || tradeItemContext.item25?.itemIndex !== 5 || tradeItemContext.item25?.damage !== "20%" ||
    tradeItemContext.item85?.name !== "free" || tradeItemContext.item85?.effect !== "effect85" || tradeItemContext.item85?.itemIndex !== 6 || tradeItemContext.item85?.damage !== "30%") {
  throw new Error("trade item TD parser must keep both 2.5 and switched 8.5 field widths aligned");
}
const tradeServerSource = fs.readFileSync(__dirname + "/../../server/legacy/source/2.5/gmsv/char/trade.c", "latin1");
/* trade.c declares TRADE_CheckItembuf near the top and defines it after the
   swap helpers.  Validate the definition, not the forward declaration. */
const tradeCheckStart = tradeServerSource.lastIndexOf("int TRADE_CheckItembuf");
const tradeCheckEnd = tradeServerSource.indexOf("BOOL TRADE_HandleItem", tradeCheckStart);
const tradeCheckSource = tradeServerSource.slice(tradeCheckStart, tradeCheckEnd);
for (let token = 5; token <= 16; token++) {
  if (!new RegExp(`getStringFromIndexWithDelim\\(itembuf, "\\|", ${token},`).test(tradeCheckSource)) {
    throw new Error(`2.5 server six-slot confirmation token ${token} missing`);
  }
}
const fieldSettingsMarkup = html.match(/<div id="field-settings-list"[\s\S]*?<\/div>/)?.[0] || "";
if ((fieldSettingsMarkup.match(/class="field-setting-row"/g) || []).length !== 5 ||
    !/data-field-setting="trade"/.test(fieldSettingsMarkup) ||
    !/<span>组    队：<\/span>/.test(fieldSettingsMarkup) ||
    !/<span>决    斗：<\/span>/.test(fieldSettingsMarkup) ||
    !/<span>交换名片：<\/span>/.test(fieldSettingsMarkup) ||
    !/<span>聊    天：<\/span>/.test(fieldSettingsMarkup) ||
    !/<span>交    易：<\/span>/.test(fieldSettingsMarkup)) {
  throw new Error("field settings must contain the five native 2.5 rows");
}
const fieldUiMarkup = html.match(/<div id="field-ui"[\s\S]*?<\/div>\s*<\/div>/)?.[0] || "";
if ((fieldUiMarkup.match(/class="click"/g) || []).length !== 7 ||
    !/id="field-left-menu" class="click"/.test(fieldUiMarkup) ||
    !/id="field-left-card" class="click"/.test(fieldUiMarkup) ||
    !/id="field-left-group" class="click"/.test(fieldUiMarkup) ||
    !/id="field-left-trade" class="click"/.test(fieldUiMarkup) ||
    !/id="field-left-trade" class="click" data-field-action="trade" data-src="\/assets\/bitmaps\/bitmap_126233\.png"/.test(fieldUiMarkup) ||
    !/id="field-left-mail" data-src="\/assets\/bitmaps\/bitmap_9225\.png"/.test(fieldUiMarkup) ||
    !/id="field-right-join" class="click"/.test(fieldUiMarkup) ||
    !/id="field-right-duel" class="click"/.test(fieldUiMarkup) ||
    !/id="field-right-action" class="click"/.test(fieldUiMarkup)) {
  throw new Error("2.5 field HUD must contain four left and three right controls");
}
/* FIELD.CPP sets drawFieldButtonFlag=0 while any native menu/WN/action
   surface is open.  Keep that rule strict: a later convenience override that
   re-shows #field-ui above the settings/action window causes the HUD bitmaps
   to overlap the window, which is not how the 2.5 back-buffer is painted. */
if (!/main\.field-overlay-suppressed #field-ui\s*\{\s*visibility:hidden;\s*\}/.test(html) ||
    /main\.field-window-open #field-ui/.test(html) ||
    /id="field-actions-owner-hit"/.test(html) ||
    !/const openScreen=advancedScreens\.find\(screen=>!screen\.classList\.contains\("hidden"\)\);[\s\S]{0,180}main\.classList\.toggle\("field-overlay-suppressed",Boolean\(openScreen\)\)/.test(script)) {
  throw new Error("native field controls must disappear whenever a field window is open");
}
/* Dormant native surfaces must be lazy: the browser should not request their
   images while the login scene is visible, but the first activation must
   restore every data-src exactly once. */
if (!/function hydrateScreenImages\(screen\)[\s\S]{0,900}querySelectorAll\("img\[data-src\]"\)/.test(script) ||
    !/function hydratePanelImages\(name\)[\s\S]{0,320}hydrateScreenImages\(\$\(id\)\)/.test(script) ||
    !/function applyScreenVisibility\(screen\)[\s\S]{0,500}hydrateScreenImages\(screen\)/.test(script) ||
    !/advancedScreens\.forEach\(s=>s\.classList\.toggle\("hidden",s\.id!==`\$\{name\}-screen`\)\);\s*hydratePanelImages\(name\)/.test(script) ||
    /id="field-left-bg"[^>]*\ssrc="\/assets\//.test(fieldUiMarkup) ||
    /id="trade-window-art"[^>]*\ssrc="\/assets\//.test(tradeMarkup)) {
  throw new Error("hidden native surfaces must use data-src and hydrate on activation");
}
/* FIELD.CPP applies an Action selection twice: it sends the 0..12 AC value
   and immediately calls setPcAction() with that same ANIM_LIST row.  The
   server later maps it to CHAR_ACT_* for nearby CA packets.  Keep all 13
   local rows distinct so the owner sees the action in place without waiting
   for an echo (and without changing its map coordinate). */
const nativeActionTable = nativeField.match(/int chgTbl\[\]\s*=\s*\{([\s\S]*?)\};/)?.[1] || "";
const nativeActionOrder = [...nativeActionTable.matchAll(/\b(\d+)\s*,?/g)].map(match => Number(match[1])).slice(0, 13);
const expectedActionOrder = [5, 3, 6, 4, 11, 2, 7, 0, 8, 10, 9, 1, 12];
const fieldActionsMarkup = html.match(/<div id="field-actions-list"[\s\S]*?<\/div>/)?.[0] || "";
const webActionOrder = [...fieldActionsMarkup.matchAll(/data-action-no="(\d+)"/g)].map(match => Number(match[1]));
if (nativeActionOrder.join(",") !== expectedActionOrder.join(",") ||
    webActionOrder.join(",") !== expectedActionOrder.join(",")) {
  throw new Error(`field Action order drifted: native=${nativeActionOrder} web=${webActionOrder}`);
}
const localActionStart = script.indexOf("  const CA_TO_SPRITE_ACTION");
const localActionEnd = script.indexOf("  /* Sprite animation tables", localActionStart);
if (localActionStart < 0 || localActionEnd <= localActionStart) throw new Error("field Action renderer helpers missing");
const fieldSpriteLoaderStart = script.indexOf("  function loadFieldSpriteManifest");
const fieldSpriteLoaderEnd = script.indexOf("  function loadSpriteManifest", fieldSpriteLoaderStart);
if (fieldSpriteLoaderStart < 0 || fieldSpriteLoaderEnd <= fieldSpriteLoaderStart ||
    !/assetState\.fieldSpritesReady=true;assetState\.fieldSpritesFailed=false;[\s\S]{0,800}assetState\.fieldSpriteLoading=null;/.test(script.slice(fieldSpriteLoaderStart, fieldSpriteLoaderEnd))) {
  throw new Error("field Action loader must clear its resolved in-flight latch");
}
const localActionContext = {};
vm.createContext(localActionContext);
vm.runInContext(script.slice(localActionStart, localActionEnd) + `
this.localActionVectors=[];
for(let action=0;action<=12;action++){
  const actor={x:123,y:456,action:3,caAction:19};
  setLocalActorAction(actor,action);
  this.localActionVectors.push([spriteActionForActor(actor),actor.x,actor.y]);
}`, localActionContext);
for (let action = 0; action <= 12; action++) {
  const [spriteAction, x, y] = localActionContext.localActionVectors[action] || [];
  if (spriteAction !== action || x !== 123 || y !== 456) {
    throw new Error(`field Action ${action} must animate locally in place: ${spriteAction}@${x},${y}`);
  }
}
const localActionAnimationSource=script.slice(localActionStart,script.indexOf("  function tradeStatusText",localActionStart));
if(!/const FIELD_LOOPING_SPRITE_ACTION=Object\.freeze\(\{3:true,4:true,6:true,7:true,8:true,9:true,11:true\}\)/.test(localActionAnimationSource) ||
   !/actor\.animationLoop=fieldActionLoops\(actor\.action\)/.test(localActionAnimationSource) ||
   !/actor\.animationStartedAt=/.test(localActionAnimationSource)) {
  throw new Error("field Action selection must start the native per-frame animation clock");
}
if(!/function preloadFieldActionFrames\(actor\)[\s\S]{0,2200}image\.decode\(\)/.test(localActionAnimationSource) ||
   !/function ensureFieldActionSprites\(actor\)[\s\S]{0,2200}loadFieldSpriteManifest\(\)[\s\S]{0,2200}loadSpriteManifest\(\)/.test(localActionAnimationSource) ||
   !/const entry=spriteEntryForActor\(actor\),actions=new Set\(\(entry\?\.sprite\?\.actions\|\|\[\]\)\.map\(item=>Number\(item\?\.action\)\)\)/.test(localActionAnimationSource) ||
   !/Array\.from\(\{length:13\},\(_,index\)=>index\)\.every\(index=>actions\.has\(index\)\)/.test(localActionAnimationSource) ||
   !/const prepare=ensureFieldActionSprites\(actor\)\.then\(sprites=>sprites\?preloadFieldActionFrames\(actor\):false\)/.test(script) ||
   !/Promise\.resolve\(prepare\)\.then\(\(\)=>/.test(script)) {
  throw new Error("field Action selection must wait for decoded SPR frames before replaying from frame zero");
}
if(!/const LEGACY_FIELD_ANIMATION_TICK_MS=1000\/60/.test(script) ||
   !/const animationTick=actor\?\.walking\?LEGACY_PROC_TICK_MS:LEGACY_FIELD_ANIMATION_TICK_MS/.test(localActionAnimationSource) ||
   !/const duration=Math\.max\(1,Number\(animation\.frame_ms\)\|\|7\)\*animationTick/.test(localActionAnimationSource)) {
  throw new Error("field Action animation must use the native 60 Hz clock without changing walk speed");
}
/* MAP.CPP/PC.CPP overwrite a local Action with ANIM_WALK as soon as a real
   route starts.  Keep the local preview marker out of that path, otherwise
   spriteActionForActor() can keep rendering the previous gesture while the
   character is moving. */
if(!/walkingActor\.action=4;[\s\S]{0,650}delete walkingActor\.localActionNo;[\s\S]{0,160}delete walkingActor\.caAction;/.test(script)) {
  throw new Error("starting a field walk must clear the previous local Action selection");
}
const receiveActionsStart = script.indexOf("  function receiveActions(text)");
const receiveActionsEnd = script.indexOf("  function updateHUD()", receiveActionsStart);
const receiveActionsSource = script.slice(receiveActionsStart, receiveActionsEnd);
if(receiveActionsStart < 0 || receiveActionsEnd <= receiveActionsStart ||
   !/const actionChanged=!existing\|\|Number\(existing\.caAction\)!==action\|\|Number\(existing\.wireDirection\)!==wireDirection\|\|Number\(existing\.direction\)!==nextDirection/.test(receiveActionsSource) ||
   !/if\(actionChanged\|\|!hadAnimationStart\)actor\.animationStartedAt=performance\.now\(\)/.test(receiveActionsSource)) {
  throw new Error("repeated CA packets must not restart an unchanged field action");
}
if(!/function fieldActorAnimationActive\(now=performance\.now\(\)\)[\s\S]{0,1300}spriteAnimationForAction\(actor,actor\.direction,action\)[\s\S]{0,900}frames\.length<2/.test(localActionAnimationSource)) {
  throw new Error("field Action renderer must keep a live animation loop while frames remain");
}
if(!/function ensureFieldActorAnimation\(actor,now=performance\.now\(\)\)[\s\S]{0,700}actor\.animationLoop=fieldActionLoops\(spriteActionForActor\(actor\)\)/.test(localActionAnimationSource) ||
   !/const actor=parseLegacyActorRecord\(record\);if\(!actor\)continue;\s*ensureFieldActorAnimation\(actor\)/.test(script)) {
  throw new Error("field C/NPC characters must receive the native standing animation clock");
}
if(!/function fieldActorFrameVisualKey\(frame\)[\s\S]{0,700}function fieldActorVisualChanged\(now=performance\.now\(\)\)/.test(localActionAnimationSource) ||
   !/actionActive&&fieldActorVisualChanged\(now\)/.test(script)) {
  throw new Error("field Action animation must avoid repainting unchanged bitmaps");
}
if(!/const useRAF=false/.test(script) ||
   !/app\._worldAnimationTimer=window\.setTimeout\(\(\)=>tick\(performance\.now\(\)\),LEGACY_RENDER_TICK_MS\)/.test(script)) {
  throw new Error("field Action scheduler must keep progressing when requestAnimationFrame is throttled");
}
if(!/app\._worldAnimationTicking\|\|app\._worldAnimationFrame\|\|app\._worldAnimationTimer/.test(script) ||
   !/app\._worldAnimationTicking=true;\s*try\{renderWorld\(walking\);\}finally\{app\._worldAnimationTicking=false;\}/.test(script) ||
   !/app\._worldAnimationTicking=true;\s*try\{finalizePendingMove\(\);\}finally\{app\._worldAnimationTicking=false;\}/.test(script)) {
  throw new Error("field Action scheduler must not recursively register duplicate timers during a paint tick");
}
if(!/function ensureOwnFieldActor\(\)[\s\S]{0,1200}app\.actors\.set\(id,actor\)/.test(script) ||
   !/const actor=ensureOwnFieldActor\(\);\s*setLocalActorAction\(actor,actionNo\)/.test(script)) {
  throw new Error("field Action selection must animate even before the owner's first C record");
}
const fieldActionHandlerStart = script.indexOf('  document.querySelectorAll("#field-actions-list [data-action-no]")');
const fieldActionHandlerEnd = script.indexOf('  $("field-settings-close")', fieldActionHandlerStart);
const fieldActionHandlerSource = script.slice(fieldActionHandlerStart, fieldActionHandlerEnd);
if (!fieldActionHandlerSource.includes("setLocalActorAction(actor,actionNo)") ||
    !fieldActionHandlerSource.includes('fieldSend("AC",[x,y,actionNo])') ||
    !fieldActionHandlerSource.includes("scheduleWorldAnimation()")) {
  throw new Error("field Action click must preview and send the selected native action number");
}
/* The executable has no invisible central D-pad.  Keep the semantic movement
   controls outside pointer hit testing so a map/NPC click reaches the field
   surface; keyboard/document shortcuts remain the movement path. */
const advancedStyleStart=script.indexOf('  const advancedStyle=document.createElement("style")');
const advancedStyleEnd=script.indexOf("  document.head.appendChild(advancedStyle)",advancedStyleStart);
const advancedStyleSource=script.slice(advancedStyleStart,advancedStyleEnd);
if(!/#world-actions \[data-dir\]\{[^}]*pointer-events:none/.test(advancedStyleSource)||
   /#world-actions \[data-dir\]\{[^}]*pointer-events:auto/.test(advancedStyleSource)){
  throw new Error("invisible field D-pad must remain outside pointer hit testing");
}
/* FIELD.CPP::actionShortCutKeyProc() exposes the same 13 actions through
   Ctrl+keys.  Keep the exact mapping in the web keyboard boundary and route
   it through the DOM row so the local animation/AC path stays authoritative. */
const actionShortcutHelperStart = script.indexOf("  function fieldActionShortcutIndex(event){");
const actionShortcutHelperEnd = script.indexOf("  function redirectPrintableFieldKeyToChat", actionShortcutHelperStart);
if(actionShortcutHelperStart<0||actionShortcutHelperEnd<=actionShortcutHelperStart)throw new Error("field Action physical-key helper boundary missing");
const fieldActionShortcutIndex=new Function(`${script.slice(actionShortcutHelperStart,actionShortcutHelperEnd)};return fieldActionShortcutIndex;`)();
for(const [event,expected] of [
  [{key:"\\",code:"Backslash"},12],
  [{key:"¥",code:"IntlYen"},12],
  [{key:"",code:"IntlYen"},12],
  [{key:"6",code:"Digit6",shiftKey:true},1],
  [{key:"-",code:"Minus"},10],
  [{key:"Subtract",code:"NumpadSubtract"},10],
]){
  if(fieldActionShortcutIndex(event)!==expected)throw new Error(`field Action physical-key alias missing: ${JSON.stringify(event)}`);
}
const actionShortcutStart = script.indexOf('if(fieldShortcutEditor&&event.ctrlKey&&app.phase==="world"&&!app.battle)');
const actionShortcutSource = script.slice(actionShortcutStart, script.indexOf('if(event.key==="Escape"', actionShortcutStart));
if (!/fieldActionShortcutIndex\(event\)/.test(actionShortcutSource) ||
    !/field-actions-list \[data-action-no=/.test(actionShortcutSource) ||
    !/!event\.repeat&&!mapMovementBlocked\(\)&&!app\.pointerMoveHeld/.test(actionShortcutSource)) {
  throw new Error("field Action Ctrl shortcuts must match the native 2.5 mapping");
}
/* MENU.CPP/ FIELD.CPP expose their visible field windows through Ctrl
   chords and Escape.  These are client-local toggles, not 8.5 protocol
   calls, so they must remain usable while MyChatBuffer owns focus. */
const nativeMenuShortcutSource=fs.readFileSync(__dirname+"/../../vendor/upstream/code_sa_client/SYSTEM/MENU.CPP","latin1");
const nativeFieldShortcutSource=fs.readFileSync(__dirname+"/../../vendor/upstream/code_sa_client/SYSTEM/FIELD.CPP","latin1");
if(!/JOY_CTRL_S[\s\S]{0,5000}JOY_CTRL_P[\s\S]{0,5000}JOY_CTRL_I[\s\S]{0,5000}JOY_CTRL_M[\s\S]{0,5000}JOY_CTRL_E[\s\S]{0,5000}JOY_CTRL_A/.test(nativeMenuShortcutSource)||
   !/JOY_ESC[\s\S]{0,2500}MenuToggleFlag \^= JOY_ESC/.test(nativeMenuShortcutSource)||
   !/JOY_CTRL_Q[\s\S]{0,10000}JOY_CTRL_W/.test(nativeFieldShortcutSource)){
  throw new Error("native field/menu shortcut reference boundary drifted");
}
const fieldMenuShortcutStart=script.indexOf("  function fieldMenuShortcut(event){");
const fieldMenuShortcutEnd=script.indexOf("  function redirectPrintableFieldKeyToChat",fieldMenuShortcutStart);
const fieldMenuShortcut=new Function(`${script.slice(fieldMenuShortcutStart,fieldMenuShortcutEnd)};return fieldMenuShortcut;`)();
const expectedFieldMenuShortcuts={m:["panel","map"],s:["panel","status"],p:["panel","pets"],i:["panel","inventory"],e:["panel","social"],a:["panel","album"],q:["field","settings"],w:["field","action"]};
for(const [key,expected] of Object.entries(expectedFieldMenuShortcuts)){
  const actual=fieldMenuShortcut({key:key.toUpperCase(),ctrlKey:true,metaKey:false,altKey:false});
  if(actual?.kind!==expected[0]||actual?.name!==expected[1])throw new Error(`missing native field shortcut Ctrl+${key.toUpperCase()}`);
}
if(fieldMenuShortcut({key:"m",ctrlKey:false})!==null||fieldMenuShortcut({key:"m",ctrlKey:true,metaKey:true})!==null||fieldMenuShortcut({key:"t",ctrlKey:true,metaKey:false,altKey:false})!==null||
   !/const fieldShortcutEditor=!activeEditor\|\|activeEditor\.matches\?\.\("#chat-input"\)/.test(script)||
   !/if\(!event\.repeat\)\{if\(menuShortcut\.kind==="panel"\)toggleWorldPanel\(menuShortcut\.name\);else fieldAction\(menuShortcut\.name\);\}/.test(actionShortcutSource)||
   !/if\(app\.phase==="world"&&!app\.battle\)\{event\.preventDefault\(\);toggleWorldPanel\("system"\);return;\}/.test(script)){
  throw new Error("web field/menu shortcuts must preserve native focus, toggle and Escape semantics");
}
/* BATTLEPROC.CPP continues to call MenuProc() through the command, movie and
   result phases.  Esc must therefore overlay the system window on battle and
   close back to the battle buffer instead of accidentally showing the map. */
const nativeBattleProcSource=fs.readFileSync(__dirname+"/../../vendor/upstream/code_sa_client/SYSTEM/BATTLEPROC.CPP","latin1");
if(!/(?:^|\n)\s*MenuProc\(\);/.test(nativeBattleProcSource)||
   !/function gameplayBaseScreen\(\)\{return app\.phase==="battle"&&app\.battle\?battleScreen:worldScreen;\}/.test(script)||
   !/const battleSystem=name==="system"&&app\.phase==="battle"&&app\.battle/.test(script)||
   !/if\(active\)\{event\.preventDefault\(\);closeGameplayOverlay\(\);return;\}/.test(script)||
   !/if\(app\.phase==="battle"&&app\.battle\)\{event\.preventDefault\(\);openPanel\("system"\);return;\}/.test(script)||
   !/const close=systemChoice\("关闭",closeGameplayOverlay,172\)/.test(script)){
  throw new Error("battle Esc must open and close the native system overlay without leaving battle");
}
/* Chat input is a native-owned buffer, not browser autocomplete.  Keep the
   complete local 2.5 shortcut set: Delete clears the 20 visible lines,
   Up/Down walks the 64-entry send history, F1-F8 append registered text, and
   Tab remains inert unless a registered-phrase row owns the buffer. */
const nativeChatInputSource=fs.readFileSync(__dirname+"/../../vendor/upstream/code_sa_client/SYSTEM/CHAT.CPP","latin1");
const nativeMainInputSource=fs.readFileSync(__dirname+"/../../vendor/upstream/code_sa_client/SYSTEM/MAIN.CPP","latin1");
const nativeChatHeader=fs.readFileSync(__dirname+"/../../vendor/upstream/code_sa_client/SYSTEMINC/CHAT.H","latin1");
const nativeChatSendSource=fs.readFileSync(__dirname+"/../../vendor/upstream/code_sa_client/SYSTEM/NETPROC.CPP","latin1");
const nativeChatInitStart=nativeChatInputSource.indexOf("void InitChat( void )"),nativeChatInitEnd=nativeChatInputSource.indexOf("void openChatLogFile",nativeChatInitStart),nativeChatInitSource=nativeChatInputSource.slice(nativeChatInitStart,nativeChatInitEnd);
if(!/case VK_DELETE:[\s\S]{0,180}ClearChatBuffer\(\)/.test(nativeMainInputSource)||
   !/joy_trg\[ 1 \] & JOY_F1[\s\S]{0,700}joy_trg\[ 1 \] & JOY_F8/.test(nativeChatInputSource)||
   !/joy_auto\[ 0 \] & JOY_UP[\s\S]{0,1500}joy_auto\[ 0 \] & JOY_DOWN/.test(nativeChatInputSource)||
   !/if\( joy_trg\[ 1 \] & JOY_TAB \) KeyboardTab\(\)/.test(nativeChatInputSource)||
   !/MyChatBuffer\.color\+\+[\s\S]{0,180}MyChatBuffer\.color >= 10/.test(nativeMenuShortcutSource)||
   !/NowMaxVoice\+\+[\s\S]{0,180}NowMaxVoice > MAX_VOICE[\s\S]{0,700}NowMaxVoice--[\s\S]{0,180}NowMaxVoice <= 0/.test(nativeMenuShortcutSource)||
   !/lssproto_TK_send\( sockfd, x, y, m, color, NowMaxVoice \)/.test(nativeChatSendSource)||
   nativeChatInitStart<0||nativeChatInitEnd<=nativeChatInitStart||
   !/MyChatBuffer\.len\s*=\s*70[\s\S]{0,260}MyChatBuffer\.fontPrio\s*=\s*FONT_PRIO_BACK[\s\S]{0,260}MyChatBuffer\.x\s*=\s*8[\s\S]{0,120}MyChatBuffer\.y\s*=\s*432/.test(nativeChatInitSource)||
   !/#define MAX_CHAT_HISTORY\s+64/.test(nativeChatHeader)){
  throw new Error("native chat-input/settings reference boundary drifted");
}
const chatShortcutStart=script.indexOf("  function localChatStorage(){");
const chatShortcutEnd=script.indexOf("  function battleChoiceDeadline",chatShortcutStart);
if(chatShortcutStart<0||chatShortcutEnd<=chatShortcutStart)throw new Error("web chat shortcut helper boundary missing");
const chatInput={value:"",maxLength:70,selectionStart:0,selectionEnd:0,focusCount:0,inputEvents:0,
  focus(){this.focusCount++;},setSelectionRange(start,end){this.selectionStart=start;this.selectionEnd=end;},
  setRangeText(text,start,end){this.value=this.value.slice(0,start)+text+this.value.slice(end);this.selectionStart=this.selectionEnd=start+text.length;},
  dispatchEvent(){this.inputEvents++;},
};
const chatLogs={"chat-log":makeChatLog(),"battle-chat-log":makeChatLog()};
function makeChatLog(){return {replaceCount:0,scrollTop:17,removed:[],replaceChildren(){this.replaceCount++;},classList:{remove(name){this.owner?.removed.push(name);},owner:null}};}
for(const log of Object.values(chatLogs))log.classList.owner=log;
const chatShortcutState={chat:[{text:"one"},{text:"two"}],chatRegistry:Array(8).fill(""),chatInputHistory:[],chatInputHistoryIndex:-1,chatInputHistoryDraft:"",systemSettings:{chatLines:7,chatColor:9,chatRange:5}};
const chatShortcutHelpers=new Function("app","$","Event","CHAT_INPUT_HISTORY_LIMIT",
  `${script.slice(chatShortcutStart,chatShortcutEnd)};return {loadLocalChatState,saveLocalChatState,clearChatBuffer,consumeLocalChatCommand,encodeNativeChatText,isChatClearKey,isShiftBackspace,clearEditableInputBuffer,rememberChatInputHistory,browseChatInputHistory,registeredChatShortcutIndex,insertChatTextAtSelection,pasteChatClipboard,insertRegisteredChat,handleChatInputKeydown};`,
)(chatShortcutState,id=>id==="chat-input"?chatInput:chatLogs[id]||null,class MockEvent{},64);
chatInput.value="draft survives clear";
chatShortcutState.chatInputHistory=["history survives clear"];
chatShortcutHelpers.clearChatBuffer();
if(chatShortcutState.chat.length||Object.values(chatLogs).some(log=>log.replaceCount!==1||log.scrollTop!==0||!log.removed.includes("chat-line-smooth"))){
  throw new Error("Delete must clear field/battle chat without replaying the line-slide animation");
}
if(chatInput.value!=="draft survives clear"||chatShortcutState.chatInputHistory.join(",")!=="history survives clear"){
  throw new Error("Delete must not erase the current draft or native send history");
}
for(const alias of ["/clear","/cls","/clearchat","/clear-chat","/clear_chat","/clear chat","/clearlog","/clear-chat-log","/清屏","/清空聊天","/清空聊天记录","/清除聊天","/清除聊天记录","clear","cls","clearchat","clear-chat","clear_chat","clear chat","clearlog","clear-chat-log","清屏","清空聊天","清空聊天记录","清除聊天","清除聊天记录"]){
  chatShortcutState.chat=[{text:`local clear ${alias}`}];
  if(!chatShortcutHelpers.consumeLocalChatCommand(`  ${alias.toUpperCase()}  `)||chatShortcutState.chat.length){
    throw new Error(`${alias} must clear the local chat buffer without a server packet`);
  }
}
chatShortcutState.chat=[{text:"full-width slash"}];
if(!chatShortcutHelpers.consumeLocalChatCommand("  ／ＣＬＥＡＲ  ")||chatShortcutState.chat.length){
  throw new Error("mobile full-width slash clear alias must clear the local chat buffer");
}
chatShortcutState.chat=[{text:"server command"}];
if(chatShortcutHelpers.consumeLocalChatCommand("/go 1 2")||chatShortcutHelpers.consumeLocalChatCommand("／ｇｏ １ ２")||chatShortcutState.chat.length!==1||chatShortcutHelpers.consumeLocalChatCommand("hello")||chatShortcutHelpers.consumeLocalChatCommand("clear the room")){
  throw new Error("only the exact local chat aliases may be intercepted");
}
for(const [source,expected] of [
  ["comma,pipe|slash\\newline\n", "comma\\cpipe\\zslash\\ynewline\\n"],
  ["/go 12,34|x\\y", "/go 12\\c34\\zx\\yy"],
  ["  keep leading/trailing  ", "  keep leading/trailing  "],
]){
  if(chatShortcutHelpers.encodeNativeChatText(source)!==expected){
    throw new Error(`chat inner escaping drifted for ${JSON.stringify(source)}`);
  }
}
const chatSubmitSource=script.slice(script.indexOf('$("chat-form").addEventListener("submit"'),script.indexOf('/* Keep chat keystrokes inside the form.',script.indexOf('$("chat-form").addEventListener("submit"')));
if(chatSubmitSource.indexOf("consumeLocalChatCommand(rawText)")<0||chatSubmitSource.indexOf("consumeLocalChatCommand(rawText)")>chatSubmitSource.indexOf('send("TK"')||
   chatSubmitSource.indexOf("`P|${encodeNativeChatText(rawText)}`")<0||chatSubmitSource.indexOf("rememberChatInputHistory(rawText)")<0){
  throw new Error("local chat aliases must be consumed before TK, and server commands must retain raw input bytes");
}
if(!chatShortcutHelpers.isChatClearKey({key:"Delete",code:"Delete"})||
   !chatShortcutHelpers.isChatClearKey({key:"ForwardDelete",code:"Delete"})||
   !chatShortcutHelpers.isChatClearKey({key:"Backspace",metaKey:true,ctrlKey:false,altKey:false,shiftKey:false})||
   chatShortcutHelpers.isChatClearKey({key:"Backspace",metaKey:false,ctrlKey:false,altKey:false,shiftKey:false})||
   chatShortcutHelpers.isChatClearKey({key:"Backspace",metaKey:true,ctrlKey:true,altKey:false,shiftKey:false})){
  throw new Error("chat clear must accept Delete/forward-delete and Cmd+Backspace without hijacking ordinary Backspace");
}
chatInput.value="draft to clear";chatInput.selectionStart=chatInput.selectionEnd=chatInput.value.length;
if(!chatShortcutHelpers.isShiftBackspace({key:"Backspace",shiftKey:true,ctrlKey:false,metaKey:false,altKey:false})||
   chatShortcutHelpers.isShiftBackspace({key:"Backspace",shiftKey:true,ctrlKey:true,metaKey:false,altKey:false})||
   !chatShortcutHelpers.clearEditableInputBuffer(chatInput)||chatInput.value!==""||chatInput.selectionStart!==0||chatInput.selectionEnd!==0||chatInput.inputEvents<1){
  throw new Error("Shift+Backspace must clear the focused input buffer without sending");
}
chatInput.value="handler draft";let shiftPrevented=false,shiftStopped=false;
chatShortcutHelpers.handleChatInputKeydown({key:"Backspace",shiftKey:true,ctrlKey:false,metaKey:false,altKey:false,currentTarget:chatInput,
  preventDefault(){shiftPrevented=true;},stopPropagation(){shiftStopped=true;}});
if(chatInput.value!==""||!shiftPrevented||!shiftStopped){
  throw new Error("chat keydown Shift+Backspace must consume the local clear shortcut");
}
chatShortcutHelpers.rememberChatInputHistory("first");chatShortcutHelpers.rememberChatInputHistory("first");chatShortcutHelpers.rememberChatInputHistory("second");
for(let index=0;index<70;index++)chatShortcutHelpers.rememberChatInputHistory(`line-${index}`);
if(chatShortcutState.chatInputHistory.length!==64||chatShortcutState.chatInputHistory[0]!=="line-6"||chatShortcutState.chatInputHistory.at(-1)!=="line-69"){
  throw new Error("chat send history must keep exactly the newest 64 non-consecutive-duplicate entries");
}
const localChatValues=new Map(),localChatStorage={getItem:key=>localChatValues.get(key)||null,setItem:(key,value)=>localChatValues.set(key,value)};
chatShortcutState.chatRegistry[0]="12345678901234567890123456EXTRA";
if(!chatShortcutHelpers.saveLocalChatState(localChatStorage))throw new Error("chat history/registry local persistence did not save");
chatShortcutState.chatRegistry=[];chatShortcutState.chatInputHistory=[];chatShortcutState.systemSettings={chatLines:20,chatColor:0,chatRange:3};
if(!chatShortcutHelpers.loadLocalChatState(localChatStorage)||chatShortcutState.chatRegistry.length!==8||chatShortcutState.chatRegistry[0]!=="12345678901234567890123456"||chatShortcutState.chatInputHistory.length!==64||
   chatShortcutState.systemSettings.chatLines!==7||chatShortcutState.systemSettings.chatColor!==9||chatShortcutState.systemSettings.chatRange!==5){
  throw new Error("chat history/registry/settings persistence did not restore the native limits");
}
chatInput.value="unfinished";
chatShortcutHelpers.browseChatInputHistory(-1,chatInput);if(chatInput.value!=="line-69")throw new Error("ArrowUp must recall newest chat input");
chatShortcutHelpers.browseChatInputHistory(-1,chatInput);if(chatInput.value!=="line-68")throw new Error("repeated ArrowUp must walk backward through chat input");
chatShortcutHelpers.browseChatInputHistory(1,chatInput);if(chatInput.value!=="line-69")throw new Error("ArrowDown must walk forward through chat input");
chatShortcutHelpers.browseChatInputHistory(1,chatInput);if(chatInput.value!==""||chatShortcutState.chatInputHistoryIndex!==-1)throw new Error("ArrowDown past the newest history must clear MyChatBuffer like 2.5");
chatShortcutState.chatRegistry[2]="快捷";chatInput.value="AB";chatInput.selectionStart=chatInput.selectionEnd=1;
if(!chatShortcutHelpers.insertRegisteredChat(2,chatInput)||chatInput.value!=="A快捷B"||chatShortcutHelpers.registeredChatShortcutIndex({key:"F3"})!==2){
  throw new Error("F1-F8 must append registered chat at the current caret without sending it");
}
chatInput.value="";chatInput.selectionStart=chatInput.selectionEnd=0;
if(!chatShortcutHelpers.insertRegisteredChat(2,chatInput)||chatInput.value.length!==2){
  throw new Error("registered chat insertion must remain available with the native 70-byte input limit");
}
chatInput.value="A".repeat(70);chatInput.selectionStart=chatInput.selectionEnd=70;
chatShortcutHelpers.insertRegisteredChat(2,chatInput);
if(chatInput.value.length!==70){
  throw new Error("registered chat insertion must not exceed the native 70-byte input limit");
}
chatInput.value="AB";chatInput.selectionStart=chatInput.selectionEnd=1;
if(!chatShortcutHelpers.insertChatTextAtSelection(chatInput,"粘贴😀")||chatInput.value!=="A粘贴😀B"){
  throw new Error("Ctrl+V clipboard insertion must preserve the caret and native byte limit");
}
chatInput.value="A".repeat(67);chatInput.selectionStart=chatInput.selectionEnd=67;
if(!chatShortcutHelpers.insertChatTextAtSelection(chatInput,"你")||chatInput.value.length!==68){
  throw new Error("clipboard insertion must truncate at the native 70-byte limit");
}
let tabPrevented=0,tabStopped=0;chatInput.value="keep me";
chatShortcutHelpers.handleChatInputKeydown({key:"Tab",isComposing:false,currentTarget:chatInput,preventDefault(){tabPrevented++;},stopPropagation(){tabStopped++;}});
if(chatInput.value!=="keep me"||tabPrevented!==1||tabStopped!==1)throw new Error("Tab must keep MyChatBuffer focused and unchanged");
for(const event of [{key:"s",ctrlKey:true},{key:"Escape",ctrlKey:false},{key:"F12",ctrlKey:false},{key:"Enter",altKey:true,ctrlKey:false}]){
  let prevented=0,stopped=0;
  chatShortcutHelpers.handleChatInputKeydown({...event,isComposing:false,currentTarget:chatInput,preventDefault(){prevented++;},stopPropagation(){stopped++;}});
  if(prevented||stopped)throw new Error(`${event.ctrlKey?"Ctrl chord":event.altKey?"Alt+Enter":event.key} must bubble from MyChatBuffer to the native global shortcut boundary`);
}
if(!/\$\("chat-input"\)\.addEventListener\("keydown",handleChatInputKeydown\)/.test(script)||
   !/function isChatClearKey\(event\)[\s\S]{0,500}event\.key==="Delete"\|\|event\.code==="Delete"[\s\S]{0,300}event\.metaKey/.test(script)||
   !/const color=chatSettingNumber\(app\.systemSettings\?\.chatColor,0,0,9\),range=chatSettingNumber\(app\.systemSettings\?\.chatRange,3,1,5\);[\s\S]{0,120}await send\("TK",\[x,y,`P\|\$\{encodeNativeChatText\(rawText\)\}`,color,range\]\);rememberChatInputHistory\(rawText\);input\.value=""/.test(script)||
   !/const visibleLines=chatSettingNumber\(app\.systemSettings\?\.chatLines,20,0,20\),entries=visibleLines\?app\.chat\.slice\(-visibleLines\):\[\]/.test(script)||
   !/updateChatSetting\("chatColor",\(currentColor\+1\)%10\)/.test(script)||
   !/updateChatSetting\("chatRange",Math\.min\(5,currentRange\+1\)\)[\s\S]{0,220}updateChatSetting\("chatRange",Math\.max\(1,currentRange-1\)\)/.test(script)||
   !/systemAction\("\s*记录文字\s*",\(\)=>\{app\.systemPage="registry";renderSystem\(\);\},128\)/.test(script)){
  throw new Error("chat shortcuts/settings are not connected to the real TK submit/input/render path");
}
const chatRegistryPageStart=script.indexOf('    if(page==="registry"){');
const chatRegistryPageSource=script.slice(chatRegistryPageStart,script.indexOf('    app.systemPage="menu";renderSystem();',chatRegistryPageStart));
if(chatRegistryPageStart<0||!chatRegistryPageSource.includes("for(let i=0;i<8;i++)")||!chatRegistryPageSource.includes("input.maxLength=26")||
   !chatRegistryPageSource.includes('event.key!=="Tab"')||!chatRegistryPageSource.includes("saveLocalChatState()")||
   !chatRegistryPageSource.includes('registry.querySelector("input")?.focus({preventScroll:true})')){
  throw new Error("registered chat editor must keep eight native 26-character rows, wrapping Tab focus and local persistence");
}
/* CHAT.CPP gives MyChatBuffer ownership of printable field keys.  Exercise
   the extracted redirector instead of merely asserting that Space has a
   special case: Latin text, CJK text and Space must all appear immediately,
   while IME Process/229 focuses without duplicating its later commit. */
const printableRedirectStart=script.indexOf("  function redirectPrintableFieldKeyToChat(event){");
const printableRedirectEnd=script.indexOf('  document.addEventListener("keydown"',printableRedirectStart);
if(printableRedirectStart<0||printableRedirectEnd<=printableRedirectStart)throw new Error("field printable-chat redirect boundary missing");
let chatFocusCount=0,chatInputEvents=0;
const directChatInput={value:"",maxLength:70,selectionStart:0,selectionEnd:0,
  focus(){chatFocusCount++;},
  setRangeText(text,start,end){this.value=this.value.slice(0,start)+text+this.value.slice(end);this.selectionStart=this.selectionEnd=start+text.length;},
  dispatchEvent(){chatInputEvents++;},
};
const directChatState={phase:"world",battle:false};
const directChatHelpers=new Function("app","$","Event","CHAT_INPUT_BYTE_LIMIT","chatInputByteLength",
  `${script.slice(printableRedirectStart,printableRedirectEnd)};return {redirectPrintableFieldKeyToChat,eraseChatInputBackward};`,
)(directChatState,id=>id==="chat-input"?directChatInput:null,globalThis.Event,70,value=>new TextEncoder().encode(String(value??"")).length);
const {redirectPrintableFieldKeyToChat,eraseChatInputBackward}=directChatHelpers;
function directChatKey(key,extra={}){let prevented=0;const result=redirectPrintableFieldKeyToChat({key,code:extra.code||"",ctrlKey:false,metaKey:false,altKey:false,preventDefault(){prevented++;},...extra});return {result,prevented};}
for(const [key,extra] of [["A",{}],[" ",{code:"Space"}],["你",{}]]){
  const handled=directChatKey(key,extra);if(!handled.result||handled.prevented!==1)throw new Error(`printable field key was not redirected to chat: ${key}`);
}
if(directChatInput.value!=="A 你"||chatInputEvents!==3)throw new Error(`direct field typing did not preserve visible text: ${JSON.stringify(directChatInput)}`);
directChatState.phase="battle";directChatState.battle=true;
const battleNumber=directChatKey("7");
if(!battleNumber.result||battleNumber.prevented!==1||directChatInput.value!=="A 你7")throw new Error("printable battle keys must remain owned by MyChatBuffer, not web-only numeric controls");
directChatInput.value="A".repeat(70);directChatInput.selectionStart=directChatInput.selectionEnd=70;
const overLimit=directChatKey("Z");
if(!overLimit.result||overLimit.prevented!==1||directChatInput.value.length!==70)throw new Error("printable chat input must stop at the native 70-byte limit");
directChatInput.value="AB😀";directChatInput.selectionStart=directChatInput.selectionEnd=directChatInput.value.length;
if(!eraseChatInputBackward(directChatInput)||directChatInput.value!=="AB")throw new Error("unfocused Backspace must erase one complete chat character");
const beforeIme=directChatInput.value,ime=directChatKey("Process",{keyCode:229,isComposing:true});
if(!ime.result||ime.prevented||directChatInput.value!==beforeIme||chatFocusCount<4)throw new Error("IME Process key must focus chat without inserting duplicate text");
if(redirectPrintableFieldKeyToChat({key:"c",ctrlKey:true,metaKey:false,altKey:false,preventDefault(){throw new Error("Ctrl shortcut was consumed by chat");}})!==false){
  throw new Error("Ctrl/Meta/Alt field shortcuts must remain outside chat redirection");
}
const documentKeyHandlerSource=script.slice(printableRedirectEnd,script.indexOf('  $("mail-form")',printableRedirectEnd));
if(!/function pasteChatClipboard\(input=\$\("chat-input"\)\)/.test(script)||
   !/const pasteShortcut=\(event\.ctrlKey\|\|event\.metaKey\)[\s\S]{0,500}pasteChatClipboard\(input\)/.test(documentKeyHandlerSource)){
  throw new Error("global gameplay Ctrl+V clipboard handling is missing");
}
if(!/if\(redirectPrintableFieldKeyToChat\(event\)\)return;/.test(documentKeyHandlerSource)||
   !/const gameplayChat=\(app\.phase==="world"&&!app\.battle\)\|\|\(app\.phase==="battle"&&app\.battle\)/.test(documentKeyHandlerSource)||
   !/if\(gameplayChat&&event\.key==="Tab"\)/.test(documentKeyHandlerSource)||
   !/if\(gameplayChat&&\(event\.key==="ArrowUp"\|\|event\.key==="ArrowDown"\)\)/.test(documentKeyHandlerSource)||
   !/if\(gameplayChat&&event\.key==="Backspace"\)/.test(documentKeyHandlerSource)||
   !/if\(event\.key==="Enter"&&gameplayChat\)[\s\S]{0,300}if\(input\.value\.trim\(\)\)\$\("chat-form"\)\?\.requestSubmit\(\)/.test(documentKeyHandlerSource)||
   /const directions=\{ArrowUp/.test(documentKeyHandlerSource)||/battle-ui \.battle-hit/.test(documentKeyHandlerSource)){
  throw new Error("unfocused field/battle chat commands must keep native MyChatBuffer ownership");
}
if(!/ChatProc\(\);[\s\S]{0,180}ChatBufferToFontBuffer\(\);[\s\S]{0,180}FlashKeyboardCursor\(\);/.test(nativeBattleProcSource)||
   !/main\[data-phase="battle"\] #chat-form \{ display:flex; \}/.test(html)||
   !/main\[data-phase="battle"\] #chat-form button \{ display:none; \}/.test(html)||
   !/chatScreen\.classList\.toggle\("hidden",screen!==worldScreen&&screen!==battleScreen\)/.test(script)){
  throw new Error("battle phases must preserve the native editable chat buffer without the field send bitmap");
}
const activeEditorGuard=documentKeyHandlerSource.indexOf("if(activeEditor)return;"),globalDeleteShortcut=documentKeyHandlerSource.indexOf("if(isChatClearKey(event)"),globalRegistryShortcut=documentKeyHandlerSource.indexOf("const registryIndex=registeredChatShortcutIndex(event)"),globalF5Shortcut=documentKeyHandlerSource.indexOf('if(event.key==="F5"');
if(globalDeleteShortcut<0||globalRegistryShortcut<0||globalF5Shortcut<0||activeEditorGuard<0||globalDeleteShortcut>activeEditorGuard||globalRegistryShortcut>activeEditorGuard||globalF5Shortcut<globalRegistryShortcut||globalF5Shortcut>activeEditorGuard||
   !/matches\?\.\("#mail-text,#mail-compose-text"\)/.test(documentKeyHandlerSource)||
   !/event\.key==="Tab"&&activeEditor\?\.matches\?\.\("#mail-text,#mail-compose-text"\)/.test(documentKeyHandlerSource)){
  throw new Error("global Delete and chat/mail F1-F8 shortcuts must run before the generic editor guard");
}
/* MAIN.CPP explicitly consumes F5 as a no-op and uses Alt+Return to switch
   presentation mode.  The Web equivalent must run before focused editors
   and must not reintroduce the mobile port's automatic fullscreen lock. */
if(!/case VK_F5:[\s\S]{0,180}wParam = wParam/.test(nativeMainInputSource)||
   !/case VK_RETURN:[\s\S]{0,600}WindowMode == TRUE/.test(nativeMainInputSource)||
   !/function toggleLegacyFullscreen\(\)/.test(script)||
   !/if\(event\.key==="F5"\)\{event\.preventDefault\(\);return;\}/.test(documentKeyHandlerSource)||
   !/if\(event\.key==="Enter"&&event\.altKey&&!event\.ctrlKey&&!event\.metaKey\)/.test(documentKeyHandlerSource)){
  throw new Error("native F5 no-op and explicit Alt+Enter fullscreen shortcut are missing");
}
/* GAMEMAIN.CPP handles F12 outside the focused chat buffer and throttles it
   to one complete front-buffer snapshot per 500 ms.  Keep the renderer lazy:
   a normal player should not download the 194 KiB DOM compositor at boot. */
const nativeGameMainSource=fs.readFileSync(__dirname+"/../../vendor/upstream/code_sa_client/SYSTEM/GAMEMAIN.CPP","latin1");
const nativeDirectDrawSource=fs.readFileSync(__dirname+"/../../vendor/upstream/code_sa_client/SYSTEM/DIRECTDRAW.CPP","latin1");
const screenshotRendererPath=__dirname+"/assets/original/vendor/html2canvas.min.js";
if(!/JOY_F12[\s\S]{0,260}prePushTime \+ 500[\s\S]{0,260}snapShot\(\)/.test(nativeGameMainSource)||
   !/void snapShot\( void \)/.test(nativeDirectDrawSource)||
   !fs.existsSync(screenshotRendererPath)||fs.statSync(screenshotRendererPath).size<190000||
   !/const SCREENSHOT_RENDERER_URL="\/assets\/vendor\/html2canvas\.min\.js"/.test(script)||
   !/if\(now-screenshotLastAt<500\)return false/.test(script)||
   !/scale=640\/sceneRect\.width/.test(script)||
   !/normalized\.width=640;normalized\.height=480/.test(script)||
   !/if\(event\.key==="F12"\)\{event\.preventDefault\(\);if\(!event\.repeat\)saveGameScreenshot\(\);return;\}/.test(documentKeyHandlerSource)){
  throw new Error("F12 must lazily save the complete native-sized 640x480 compositor scene");
}
/* Commands beginning with slash are server-owned in 2.5.  The Web client
   must keep wrapping every non-empty line as P|text instead of inventing a
   divergent local /command parser. */
if(!/await send\("TK",\[x,y,`P\|\$\{encodeNativeChatText\(rawText\)\}`,color,range\]\)/.test(script)||/function\s+(?:parse|handle)LocalChatCommand\s*\(/.test(script)){
  throw new Error("ordinary /commands must remain on the native TK P| server path");
}
for(const debugOnlyCommand of ["[battlein]","[battleout]","[cary encountoff]","[cary encounton]","movescreen","playnpc","debug on]"]){
  if(script.includes(debugOnlyCommand))throw new Error(`debug-only native chat command leaked into Web: ${debugOnlyCommand}`);
}
if(script.includes("/叠加"))throw new Error("8.5 _DIEJIA_ command leaked into the 2.5 Web client: /叠加");
/* 8.5's `_STONDEBUG_` `send <raw packet>` branch calls sendDataToServer()
   before normal TK encoding.  Keep that packet-injection backdoor out of the
   browser; all protocol writes must go through the named bridge function. */
if(script.includes("sendDataToServer")||script.includes("TheaterData_recv")||script.includes("MoveScreen_recv")||script.includes("setCharmManor")){
  throw new Error("8.5 debug packet/theater backdoors must not leak into the 2.5 Web client");
}
/* NETPROC.CPP stores the complete TK sentence and shows only the extracted
   26500 fukidashi icon above its actor for 1000 ms.  Do not regress to the
   modern DOM speech-card approximation that was never in the native client. */
const nativeTKSource=fs.readFileSync(__dirname+"/../../vendor/upstream/code_sa_client/SYSTEM/NETPROC.CPP","latin1");
const nativeCharacterSource=fs.readFileSync(__dirname+"/../../vendor/upstream/code_sa_client/SYSTEM/CHARACTER.CPP","latin1");
if(!/void lssproto_TK_recv\([\s\S]{0,900}StockChatBufferLine\( msg, color \);[\s\S]{0,650}set(?:Pc|Char)Fukidashi\([\s\S]{0,80}1000/.test(nativeTKSource)||
   !/CHR_STATUS_FUKIDASHI[\s\S]{0,500}realGetNo\( CG_ICON_FUKIDASI, &bmpNo \)/.test(nativeCharacterSource)){
  throw new Error("native TK/fukidashi reference boundary drifted");
}
if(!/function addChat\(speaker,text,color=4\)[\s\S]{0,500}while\(app\.chat\.length>20\)app\.chat\.shift\(\)/.test(script)||
   !/function receiveTK\(values\)[\s\S]{0,1100}addChat\("",sentence,Number\(values\?\.\[2\]\)\|\|0\)[\s\S]{0,550}actor\.speechUntil=Date\.now\(\)\+1000/.test(script)||
   !/function drawActorSpeech\(ctx,actor,point\)[\s\S]{0,320}resolveBitmapInfo\(26500\)[\s\S]{0,800}drawBitmapAt/.test(script)||
   /function drawActorSpeech\(ctx,actor,point\)[\s\S]{0,1000}(fillText|strokeRect|fillRect)\(/.test(script)){
  throw new Error("web TK display must match native chat buffer plus 26500 icon");
}
for (const expected of [
  /#field-right-composite\s*\{[^}]*z-index:2/,
  /ctx\.drawImage\(first,68-offset,5,64,32\);\s*ctx\.drawImage\(second,132-offset,5,64,32\);[\s\S]{0,220}ctx\.drawImage\(panel,0,0,132,63\)/,
  /#field-right-join\s*\{[^}]*z-index:4/,
  /#field-right-duel\s*\{[^}]*z-index:4/,
  /#field-left-menu\s*\{[^}]*left:5px; top:4px; width:32px; height:30px; z-index:4/,
  /#field-left-card\s*\{[^}]*left:36px; top:4px; width:32px; height:30px; z-index:4/,
  /#field-left-group\s*\{[^}]*left:67px; top:4px; width:32px; height:30px; z-index:4/,
  /#field-left-trade\s*\{[^}]*left:97px; top:4px; width:32px; height:30px; z-index:4/,
  /#field-left-mail\s*\{[^}]*left:11px; top:37px; width:28px; height:10px; z-index:3; display:none/,
  /function fieldHasUnreadMail\(\)[\s\S]{0,500}function updateFieldMailLamp\(\)[\s\S]{0,500}field-mail-flashing/,
  /#field-right-join\s*\{[^}]*left:518px; top:5px; width:28px; height:28px; z-index:4/,
  /#field-right-duel\s*\{[^}]*left:549px; top:5px; width:28px; height:28px; z-index:4/,
  /#field-right-action\s*\{[^}]*left:583px; top:42px; width:56px; height:14px; z-index:4/,
  /* field.cpp::charActionAnimeChange() uses 73px-wide action hit boxes;
     keeping the settings-row width on these buttons pushes the right column
     outside the native 192px action window. */
  /\.field-window-screen \.field-action-row\{width:73px!important\}/,
  /#field-ui #field-left-bg\s*\{[^}]*left:0; top:0; width:140px; height:54px/,
  /#field-right-bg\s*\{[^}]*left:508px; top:0; width:132px; height:63px/,
  /id="field-left-bg" data-src="\/assets\/bitmaps\/bitmap_126232\.png"/,
  /"field-left-trade":\[126233,126234\]/,
  /if\(name==="trade"\)[\s\S]{0,900}fieldSend\("TD",\["D\|D"\]\)/,
  /id="field-right-bg" data-src="\/assets\/bitmaps\/bitmap_9226\.png"/,
  /#field-settings-screen \.field-window-frame\{left:16px;top:16px;height:240px\}/,
  /#field-actions-screen \.field-window-frame\{left:440px;top:16px;height:288px\}/,
  /#field-settings-screen #field-settings-close\{left:72px;top:208px\}/,
  /#field-actions-screen #field-actions-close\{left:496px;top:266px\}/,
  /FIELD_SETTING_LABELS=Object\.freeze\(\{[\s\S]{0,520}chat:\["聊    天："," 全  员"," 队  伍"\],[\s\S]{0,100}trade:\["交    易："," Ｎ  Ｏ"," ＹＥＳ"\]/,
  /id="help-frame" data-src="\/assets\/bitmaps\/bitmap_234545\.png"/,
  /#help-screen #help-frame\s*\{[^}]*left:110px; top:50px; width:420px; height:376px/,
  /function renderHelp\(\)/,
  /if\(name==="help"\)\{\s*openPanel\("help"\);return;\s*\}/,
  /first\.style\.left=`\$\{576-offset\}px`/,
  /second\.style\.left=`\$\{640-offset\}px`/,
  /#battle-target-panel > strong\{position:absolute !important/,
  /#battle-target-panel > #battle-target-list\{position:absolute !important/,
  /* SPRDISP.CPP first sorts actors by their current v_pos/foot y and then
     SortComp() orders equal-y records by insertion number in reverse before
     PutBmp(); the web actor layer must use the motion-adjusted foot position,
     not trust BC wire order or a static battle id. */
  /function battleActorVerticalPosition\(spec,index=0\)[\s\S]{0,900}return Math\.round\(/,
  /function battleActorPaintOrder\(left,right,leftIndex=0,rightIndex=0\)[\s\S]{0,520}if\(leftY!==rightY\)return leftY-rightY;[\s\S]{0,320}return Number\(rightIndex\)-Number\(leftIndex\);/,
  /specs\.sort\(\(left,right\)=>battleActorPaintOrder\(left,right\)\);/,
  /BATTLE_TURN_COUNTDOWN_MS=30000/,
  /* The visible deadline must also gate late clicks when a background tab
     delays the setTimeout callback that submits the native wait command. */
  /function battleChoiceExpired\(state=app\.battleState,now=Date\.now\(\)\)[\s\S]{0,420}deadline>0&&deadline<=now/,
  /if\(state\.movieActive\|\|battleChoiceExpired\(state\)\|\|battleLocalDeath\(state\)\)return false;/,
  /* A floor transition can finish DAT decoding while another floor is
     visible.  A later return must reinstall that decoded cache into the
     current autoMapData back-buffer instead of suppressing the request and
     leaving a gray diamond forever. */
  /function autoMapRequestAllowed\(floor\)[\s\S]{0,700}autoMapDataCache\.has\(id\)[\s\S]{0,500}return true;/,
  /autoMapDataRequests\.delete\(id\);[\s\S]{0,700}if\(app\.autoMapLoading===request\)app\.autoMapLoading=null;/,
  /* EN replaces the battle state; the old BattleCntDown timeout must be
     cancelled before the new encounter owns the shared choice deadline. */
  /function enterBattle\(field,type=1\)[\s\S]{0,520}clearBattleChoiceTimer\(app\.battleState\);[\s\S]{0,1400}app\.battleState=\{/,
  /* An encounter may interrupt a two-step W batch.  Retire the field walk
     watchdog/route before BattleProc owns the back-buffer, otherwise a late
     timeout can overwrite the post-escape field status with a stale move
     error. */
  /function interruptWorldMovementForBattle\(\)[\s\S]{0,1200}app\.pendingMove=false;[\s\S]{0,700}app\.movePredictionActive=false/,
  /function enterBattle\(field,type=1\)[\s\S]{0,700}interruptWorldMovementForBattle\(\);/,
  /* A repeated BP snapshot belongs to the same native battleMenuFlag2 turn;
     it must not restart BattleCntDown and silently grant extra time. */
  /const existingKey=state\.choiceTurnKey;[\s\S]{0,220}Number\(existingKey\)===turnKey\)return;/,
  /* NETPROC only overwrites a repeated BP.  BA can arrive between the first
     BP and its retry, so serverTurn+1 is not a safe identity; only a real B
     movie advances the native menu generation. */
  /const movieGeneration=Number\(state\.movieGeneration\)\|\|0;/,
  /const sameMenuGeneration=Boolean\(state\.bpReceived\)[\s\S]{0,260}Number\(previousBpMovieGeneration\)===movieGeneration/,
  /if\(sameMenuGeneration\)\{[\s\S]{0,420}state\.myMp=Number\(values\.myMp\)\|\|0;[\s\S]{0,900}renderBattleWorld\(\);[\s\S]{0,120}renderBattle\(\);[\s\S]{0,80}return;/,
  /state\.bpMovieGeneration=movieGeneration;/,
  /state\.movieGeneration=\(Number\(state\.movieGeneration\)\|\|0\)\+1;/,
  /* BP opens the native turn boundary but BC/CHAR_IN owns the first menu
     tick; never start the visible deadline from a BP-only poll. */
  /* BP normally opens the roster gate, but a split poll can deliver BC
     first; consume rosterBeforeBp so that order cannot leave the menu stuck. */
  /const rosterAlreadyReceived=Boolean\(state\.rosterBeforeBp\)[\s\S]{0,220}state\.awaitingBattleRoster=!rosterAlreadyReceived;/,
  /if\(!state\.awaitingBattleRoster\)state\.rosterBeforeBp=true;/,
  /if\(!battleEntryPending\(state\)&&!state\.awaitingBattleRoster\)\{[\s\S]{0,120}battleOpenCommandCountdown\(state\)/,
  /choiceTurnKey:null/,
  /const BATTLE_COUNTDOWN_LOGICAL_BASE=25900;/,
  /function battleButtonPressed\(command,state=app\.battleState\)[\s\S]{0,1100}pendingCommand=battleButtonCommandForAction\(state\.pendingAction\)/,
  /function armDefaultBattleAttack\(state=app\.battleState\)[\s\S]{0,700}lastPlayerActionKind[\s\S]{0,120}attack[\s\S]{0,520}pendingAction=\{kind:"attack",defaulted:true\}[\s\S]{0,220}renderBattleWorld\(\);renderBattle\(\)/,
  /* MAP.CPP derives environmental levels from nearby object parts 80..89;
     DIRECTDRAW.CPP then paints the exact indexed rain/snow pixel patterns
     into the same field back-buffer. */
  /function mapEffectWeatherLevels\(map=app\.map\)[\s\S]{0,900}value>=80&&value<=84[\s\S]{0,220}value>=85&&value<=89/,
  /const MAP_EFFECT_RAIN_COLOR="#e3f8ff"/,
  /function drawMapEffects\(ctx\)[\s\S]{0,1800}fillRect\(x,y-1,1,1\)[\s\S]{0,700}MAP_EFFECT_SNOW_BRIGHT/,
  /function ensureMapEffectStars\(now\)[\s\S]{0,1000}MAP_EFFECT_STAR_PATTERNS/,
  /function renderWorld\(force=false\)[\s\S]{0,260}updateMapEffects\(now\)[\s\S]{0,5200}renderSceneActorsAndParts\(domActors,parts,canvas\);[\s\S]{0,160}presentWorldBackBuffer\(canvas\)/,
  /function renderSceneActorsAndParts\([\s\S]{0,2600}drawMapEffects\(ctx\)[\s\S]{0,420}StockFontBuffer|DISP_PRIO_RESERVE is emitted[\s\S]{0,260}drawMapEffects\(ctx\)/,
  /* map.cpp's held-left-button mode samples a new moveStack point every
     250 ms; the browser must keep the gesture separate from ordinary UI
     clicks and hide the fish-bone until the physical button is released. */
  /const LEGACY_MOVE_SPEED=4;[\s\S]{0,420}const LEGACY_PROC_TICK_MS=8;[\s\S]{0,260}const MOVE_CARDINAL_DURATION=LEGACY_GRID_SIZE\/LEGACY_MOVE_SPEED\*LEGACY_PROC_TICK_MS;/,
  /const BATTLE_PROC_TICK_MS=1000\/60;/,
  /function moveStepDuration\(from,target\)[\s\S]{0,360}const distance=Math\.hypot\(dx,dy\)[\s\S]{0,120}distance\|\|1/,
  /* A normal 2.5 owner walk has no self C/XYD echo.  Keep its route trail
     silently and never manufacture a background S:c sample that can capture
     the first tile of a still-draining two-step W. */
  /function scheduleMovePredictionRelease\(\)[\s\S]{0,1000}native client trusts its completed local route[\s\S]{0,500}refreshSettledMoveState\(\)/,
  /function maybeReleaseMovePrediction\(point\)[\s\S]{0,900}if\(!predictedMoveContains\(point\)\)\{cancelPendingMove\("服务器已校正位置。",point\);return;\}/,
  /function scheduleMoveWireDrain\(delay=320\)[\s\S]{0,900}refreshSettledMoveState\(\)/,
  /if\(app\.moveWirePending\)\{[\s\S]{0,180}等待服务器同步/,
  /function actorFrame\(actor\)[\s\S]{0,1200}if\(!key\|\|key==="0"\)return previousFrame\|\|null;[\s\S]{0,1900}if\(!frames\.length\)return previousFrame\|\|null;/,
  /const POINTER_MOVE_ROUTE_INTERVAL_MS=250;/,
  /const POINTER_MOVE_MODE_DELAY_MS=1000;/,
  /function sampleHeldWorldPointer\(now=Date\.now\(\)\)[\s\S]{0,900}worldTileFromPointerPosition\(clientX,clientY\)[\s\S]{0,260}setHeldMoveDestination\(tile,now,false\)/,
  /function scheduleHeldWorldPointerSample\(delay=POINTER_MOVE_MODE_DELAY_MS\)[\s\S]{0,900}sampleHeldWorldPointer\(Date\.now\(\)\)[\s\S]{0,260}scheduleHeldWorldPointerSample\(POINTER_MOVE_ROUTE_INTERVAL_MS\)/,
  /function updateWorldPointer\(event\)[\s\S]{0,2400}if\(!app\.pointerMoveHeld\)app\.cursor\.visible=true/,
  /* The fish-bone remains the topmost field cursor during the native held
     walk gesture; only the route sampler is continuous. */
  /function beginHeldWorldPointer\(event\)[\s\S]{0,1200}app\.cursor\.visible=true/,
  /function beginHeldWorldPointer\(event\)[\s\S]{0,1800}setHeldMoveDestination\(tile,Date\.now\(\),true\)[\s\S]{0,240}scheduleHeldWorldPointerSample\(POINTER_MOVE_MODE_DELAY_MS\)/,
  /function updateHeldWorldPointer\(event\)[\s\S]{0,700}app\.cursor\.visible=true/,
  /function updateHeldWorldPointer\(event\)[\s\S]{0,1100}moveModeReady[\s\S]{0,320}setHeldMoveDestination\(tile,Date\.now\(\),false\)/,
  /function endHeldWorldPointer\(event=null,commit=true\)[\s\S]{0,1500}clearHeldWorldPointerSample\(\)[\s\S]{0,700}app\.cursor\.visible=true[\s\S]{0,500}app\.cursor\.updatedAt=Date\.now\(\)/,
  /worldScreen\.addEventListener\("pointerleave",\(\)=>\{if\(app\.phase==="world"\)\{setWorldTaskbarVisible\(false\);app\.cursor\.visible=true;/,
  /function moveTargetIsSolid\(target\)[\s\S]{0,900}isMapWarpEvent\(event\)\|\|isMapEnemyEvent\(event\)[\s\S]{0,260}localCellWalkable\(target\[0\],target\[1\],false\)===false/,
  /function installMoveRoute\(route,requested\)[\s\S]{0,900}moveTargetIsSolid\(requested\)[\s\S]{0,180}app\.moveTarget=\[Number\(last\[0\]\),Number\(last\[1\]\)\]/,
  /* An in-floor wall/scene-rim click must use the bounded A* nearest-cell
     fallback; returning the straight prefix strands the pointer several
     tiles away from the edge. */
  /Do not return the straight prefix[\s\S]{0,1000}const points=routeFromCells\(origin,to,moveTargetIsSolid\(to\)\);/,
  /* A complete DAT replaces the sliding M viewport for collision/routing.
     Keep the M-window guard only while that floor-wide back-buffer is still
     unavailable, otherwise long clicks can never cross the current window. */
  /const hasCompleteFloorMap=Boolean\(app\.autoMapData&&Number\(app\.autoMapData\.floor\)===Number\(app\.floor\)\);[\s\S]{0,220}app\.map&&!hasCompleteFloorMap&&!mapCellAt\(next\[0\],next\[1\]\)/,
  /* CHAR_Talk() and CHAR_Look() both use the native two-cell mouse radius;
     LOOKEDFUNC targets are dispatched through L before any local route is
     attempted, so a blocked doorway remains usable from its second cell. */
  /const targetDistance=Math\.max\(Math\.abs\(Number\(current\.x\)-Number\(app\.position\[0\]\)\),Math\.abs\(Number\(current\.y\)-Number\(app\.position\[1\]\)\)\);[\s\S]{0,1200}if\(targetDistance>2\)[\s\S]{0,220}return approachNPC\(current\)/,
  /* A map actor may be painted underneath one of the fixed field controls.
     Native display priority gives the control the click, so keep the UI hit
     guard before actorAtTile() instead of letting the covered NPC consume
     pointerdown and make the toolbar look intermittently unresponsive. */
  /function handleWorldPointerDown\(event\)[\s\S]{0,3000}actorAtPointer\(event,isTalkableActor\)/,
  /function pointerTilePrefersMapRoute\(tile\)[\s\S]{0,700}return isMapWarpEvent\(mapEventAt\(Number\(tile\[0\]\),Number\(tile\[1\]\)\)\);/,
  /const clickedActor=pointerTilePrefersMapRoute\(tile\)\?null:\(actorAtTile\(tile,isTalkableActor\)\|\|actorAtPointer\(event,isTalkableActor\)\);/,
  /if\(distance>0&&distance<=2\)\{[\s\S]{0,220}talkToTarget\(clickedActor\)\.catch\(reportError\);[\s\S]{0,180}return;/,
  /* LOOKEDFUNC NPCs may legally omit a WN response.  Keep a short-lived
     object-specific latch and release it after the native-style timeout so
     a conditional service NPC cannot strand the field in “正在查看…”. */
  /const lookConversation=\{id,startedAt:now,mode:"look",floor:Number\(app\.floor\),x:Number\(current\.x\),y:Number\(current\.y\),name:String\(current\.name\|\|""\),template:String\(current\.npcTemplate\|\|""\),interaction:String\(current\.npcInteraction\|\|""\)\};[\s\S]{0,900}app\.talkConversation===lookConversation[\s\S]{0,300}NPC 没有回应。/,
  /* A dynamic NPC may be rebuilt between L/TK and WN.  The callback may
     carry the previous object id, so the fallback must stay constrained to
     the locked floor/tile and NPC identity instead of clearing every WN. */
  /function talkConversationMatchesObject\(objectIndex,kind="WN"\)[\s\S]{0,4500}targetInteraction=String\(target\?\.npcInteraction\|\|""\)[\s\S]{0,700}!conversationName&&!conversationTemplate&&\(npcCandidates\.length===1\|\|Boolean\(metadata\)\)/,
  /const conversation=\{id,startedAt:app\.talkSentAt,floor:Number\(app\.floor\),x:Number\(current\.x\),y:Number\(current\.y\),name:String\(current\.name\|\|""\),template:String\(current\.npcTemplate\|\|""\),interaction:String\(current\.npcInteraction\|\|""\)\};/,
  /if\(talkConversationMatchesObject\(index,"TK"\)\)/,
  /if\(talkConversationMatchesObject\(windowObject,"WN"\)\)/,
  /if\(distance===0\)\{[\s\S]{0,120}approachNPC\(clickedActor\);[\s\S]{0,80}return;/,
  /* A map/scene fold gates gameplay routing only.  The painted fish continues
     to follow trusted physical pointer samples; no synthetic recenter,
     capture, or cursor movement is allowed. */
  /function worldRouteInputBlocked\(\)[\s\S]{0,420}mapTransitionState\.active[\s\S]{0,220}scene-transition-active/,
  /if\(event\?\.isTrusted===false\)return;/,
  /* A physical pointer move during the curtain updates the painted fish in
     the untransformed portal; the compressed scene never drives its position. */
  /function updateWorldPointer\(event\)[\s\S]{0,700}fish follows every trusted physical pointer sample/,
  /* The curtain completion hook keeps the last painted position and never
     synthesizes a recenter or pointermove. */
  /function rebaselineWorldPointerAfterTransition\(\)\{[\s\S]{0,300}Keep the last painted fish position[\s\S]{0,220}\}/,
  /const clientChanged=!Number\.isFinite\(Number\(app\.cursorClientX\)\)\|\|clientX!==Number\(app\.cursorClientX\)\|\|clientY!==Number\(app\.cursorClientY\);/,
  /function updateWorldPointer\(event\)[\s\S]{0,5200}world-overlay[\s\S]{0,220}worldScreen\?\.getBoundingClientRect\(\)/,
  /function updateWorldPointer\(event\)[\s\S]{0,5600}app\.cursor\.x=Math\.max\(0,Math\.min\(640,/,
  /* Re-entering the field (login/reconnect/battle return) must preserve the
     last physical pointer sample; resetting it to 320x240 makes the fish jump
     to the centre even though the browser pointer did not move. */
  /A scene\/map change must never recenter the painted fish[\s\S]{0,520}const cursorX=Number\.isFinite\(Number\(app\.cursor\?\.x\)\)/,
  /function setMoveTarget\(target\)[\s\S]{0,140}worldRouteInputBlocked\(\)/,
  /* MAP.CPP::drawGrid() paints CG_GRID_CURSOR from the current mouse tile
     every frame.  It must not be tied to the last left-click move target. */
  /const pointerOnScene=hasPointerSample&&sceneRect&&pointerClientX>=sceneRect\.left&&pointerClientX<=sceneRect\.right&&pointerClientY>=sceneRect\.top&&pointerClientY<=sceneRect\.bottom;/,
  /const hoverTile=app\.phase==="world"&&!app\.battle&&pointerOnScene&&!pointerOverUi&&!advancedOpen&&!mapTransitionState\.active\?nearestTileAt\(Number\(app\.cursor\.x\),Number\(app\.cursor\.y\)\):null;/,
  /const targetPoint=hoverTile\?tilePoint\(hoverTile\[0\],hoverTile\[1\]\):null;/,
  /#world-move-target\s*\{[^}]*opacity:1;[^}]*\}/,
  /* A native right click first turns, then runs getItem() with a separate
     500 ms throttle and the 2.5 server-direction number. */
  /function pickupObjectAtTile\(target\)[\s\S]{0,900}actor\?\.kind==="item"\|\|actor\?\.kind==="money"/,
  /function pickupFieldTile\(target\)[\s\S]{0,1300}Math\.abs\(dx\)>1\|\|Math\.abs\(dy\)>1[\s\S]{0,700}app\.pickupSentAt=now;[\s\S]{0,240}send\("PI",\[app\.position\[0\],app\.position\[1\],serverDirectionFromClient\(direction\)\]\)/,
  /if\(event\.button===2\)[\s\S]{0,700}faceTowardTile\(tile\);pickupFieldTile\(tile\)/,
  /cursor\.style\.display=app\.cursor\.visible!==false\?"block":"none"/,
  /* The top-level cursor portal follows the physical pointer through the
     bottom task-bar row; only the viewport itself clips the final pixels. */
  /cursor\.style\.left=`\$\{Math\.max\(0,Math\.min\(640,cursorX\)\)\}px`;[\s\S]{0,120}cursor\.style\.top=`\$\{Math\.max\(0,Math\.min\(480,cursorY\)\)\}px`/,
  /* The portal must not be clipped by the fixed 4:3 shell at the task-bar
     edge.  Keep the map's own #world-wrap containment unchanged. */
  /#app\.world-active\s*,\s*#app\.world-active main\s*\{[^}]*overflow:visible/,
  /main\[data-phase="world"\]\s*\{[^}]*overflow:visible/,
  /getElementById\("app"\)\?\.classList\.toggle\("world-active",phase==="world"\)/,
  /* The painted fish is the final field layer, including over the black
     centre-fold curtain and task-bar hit regions; it must remain pointer
     transparent so the browser never turns the fish into a click shield. */
  /#world-overlay\s*\{[^}]*z-index:1100;[^}]*pointer-events:none/,
  /#world-tools button\s*\{[^}]*pointer-events:auto/,
  /#world-tools\s*\{[^}]*transform:translateY\(24px\)[^}]*transition:transform/,
  /#world-tools\.taskbar-visible\s*\{[^}]*transform:translateY\(0\)/,
  /function updateWorldTaskbarPointer\(clientX,clientY,target\)[\s\S]{0,900}sy>=456&&sy<=480/,
  /function touchPointerNearWorldTaskbar\(event\)[\s\S]{0,1100}pointerType!=="touch"[\s\S]{0,900}sy<448[\s\S]{0,500}setWorldTaskbarVisible\(true\)[\s\S]{0,300}taskbarRevealPointerId/,
  /function handleWorldPointerDown\(event\)[\s\S]{0,800}touchPointerNearWorldTaskbar\(event\)/,
  /function handleWorldPointerUp\(event\)[\s\S]{0,650}taskbarRevealPointerId[\s\S]{0,320}preventDefault/,
  /worldScreen\.addEventListener\("pointerleave",\(\)=>\{if\(app\.phase==="world"\)\{setWorldTaskbarVisible\(false\)/,
  /document\.addEventListener\("pointerdown",handleWorldPointerDown,true\)/,
  /document\.addEventListener\("pointercancel",event=>\{if\(app\.phase==="world"\)endHeldWorldPointer\(event,false\);\},true\)/,
  /* play_map_bgm() keeps regional markers 47..53 outside the 6.0 music
     switch.  The deployed 2.5 map set uses them, while 54/55 remain gated. */
  /const MAP_BGM_NO=Object\.freeze\(\{\s*40:4,41:3,42:7,43:8,44:9,45:10,46:11,\s*47:15,48:16,49:21,50:17,51:18,52:19,53:20\s*\}\);/,
  /* webdriver sessions stay quiet unless a deliberate BGM audit opts in;
     this is required to test the real HTMLAudio lifecycle in agent-browser. */
  /if\(explicit==="0"\)return false;[\s\S]{0,180}Boolean\(navigator\.webdriver\)\|\|explicit==="1"/,
  /* Native MENU.CPP volume is 0..15 (zero is valid), and pitch belongs to
     the current BGM slot rather than one global browser setting. */
  /function audioSettingLevel\(value,legacyValue=15\)[\s\S]{0,360}Math\.max\(0,Math\.min\(15/,
  /function bgmPitchFor\(number=musicIntentBgm\(\)\)[\s\S]{0,620}bgmPitchByTrack[\s\S]{0,300}Math\.max\(-8,Math\.min\(8/,
  /* MENU.CPP case 4 paints fixed rows at 0/40/80/128/168/208/260.  Generic
     flow buttons were visibly overlaid at the top of the original frame. */
  /if\(page==="bgm"\)[\s\S]{0,1600}systemValue\([^\n]+,0,-8\)[\s\S]{0,260}systemAction\("增加音量"[^\n]+,40\)[\s\S]{0,260}systemAction\("减少音量"[^\n]+,80\)[\s\S]{0,260}systemValue\([^\n]+,128,-8\)[\s\S]{0,360}systemAction\("加快节奏"[^\n]+,168\)[\s\S]{0,360}systemAction\("减慢节奏"[^\n]+,208\)[\s\S]{0,160}systemReturn\(sub,260\)/,
  /* MENU.CPP case 3 uses 0/40/80/120/172 and the same zero-volume floor. */
  /if\(page==="se"\)[\s\S]{0,1200}systemValue\([^\n]+,0,-8\)[\s\S]{0,240}systemAction\("增加音量"[^\n]+,40\)[\s\S]{0,240}systemAction\("减少音量"[^\n]+,80\)[\s\S]{0,260}systemAction\(`立体声[^\n]+,120\)[\s\S]{0,140}systemReturn\(sub,172\)/,
  /* MAP.CPP keeps the last recognized marker in diagonal tile→part order
     and retains the active track when a partial M window has no marker. */
  /function mapMusicFromWindow\(map\)[\s\S]{0,2200}let ti=height-1,tj=0;[\s\S]{0,1400}if\(tone===null&&app\.music\.mapBgmNo>=0\)return;/,
  /* M is a sliding viewport.  A manifest-miss fallback must derive the
     isometric origin from the complete floor height, otherwise rectangular
     maps (for example 30×40) shift actors and doors vertically. */
  /function mapPixel\(x,y\)\{[\s\S]{0,900}const fullHeight=Math\.max\(1,Number\(app\.map\.fullHeight\)\|\|Number\(app\.map\.asset\?\.source_height\)\|\|Number\(app\.map\.height\)\|\|1\)[\s\S]{0,500}origin_y:\(fullHeight-1\)\*24\+256/,
  /* The native battle result window stops BGM, plays SE 215, and only
     restores the room track after the result is closed. */
  /app\.music\.mode="battle-result";stopBackgroundMusic\(\);playSoundEffect\(215,320,240\);[\s\S]{0,220}renderBattleResult\(\);show\(battleResultScreen\)/,
  /function battleCountdownAsset\(digit\)[\s\S]{0,900}battle_countdown\?\.digits/,
  /const text=String\(Math\.min\(30,seconds\)\)\.padStart\(2," "\);/,
  /image\.dataset\.logicalBitmap=String\(BATTLE_COUNTDOWN_LOGICAL_BASE\+digit\)/,
  /* BattleCntDownDisp() plays SE 203 exactly when the shared deadline
     expires, before the implicit N/W wait commands are sent. */
  /function battlePlayerTimeoutDefaults\(state=app\.battleState\)[\s\S]{0,520}playSoundEffect\(203,320,240\)[\s\S]{0,900}leaveBattleMenuMotion\(state,"player"\)[\s\S]{0,900}send\("B",\["N"\]\)/,
  /function battlePetChoiceTimeout\(state\)[\s\S]{0,760}playSoundEffect\(203,320,240\)[\s\S]{0,1200}sendBattlePetDefault\(state,\{force:true\}\)/,
  /* The player->pet hand-off can cross the shared deadline after the active
     pet has died/disappeared.  Native BattleCntDownDisp() sends no W in that
     case, so the transition timer must re-check the authoritative BC slot. */
  /function battlePetChoiceTimeout\(state\)[\s\S]{0,2200}if\(!battleActivePet\(state\)\)[\s\S]{0,420}return;[\s\S]{0,260}sendBattlePetDefault\(state,\{force:true\}\)/,
  /state\.bpReceived=true;/,
  /if\(state\.bpReceived===false\)return;/,
  /* BattleCntDownDisp() uses "%2d" and leaves the tens slot empty for
     1..9; Number(" ") would otherwise turn that slot into a false zero. */
  /const character=text\[index\];[\s\S]{0,260}if\(!\/\^\[0-9\]\$\/\.test\(character\)\)\{image\.style\.display="none";continue;\}/,
  /* A delayed/background tick must remove both the bitmap and its
     accessibility value after the native deadline expires. */
  /node\.removeAttribute\("data-seconds"\);[\s\S]{0,120}value\.removeAttribute\("aria-label"\)/,
  /* Surprise turns resolve at the CHAR_IN/action_inf==3 boundary; the web
     port must clear both bits before opening a normal command timer. */
  /function battleOpenCommandCountdown\(state\)[\s\S]{0,1900}state\.bpFlags=flags&~\(BATTLE_BP_ENEMY_SURPRISAL\|BATTLE_BP_PLAYER_SURPRISAL\)/,
  /function submitBattleSurpriseDefaults\(state,turnKey=state\?\.turnKey\?\?state\?\.turn\)[\s\S]{0,320}clearBattleChoiceTimer\(state\)/,
  /* Clearing the visual enemy-surprise bit must not let the generic
     MENU_NON path submit a second N/W pair for the same menu generation. */
  /submitBattleSurpriseDefaults\(state,turnKey\)/,
  /const surpriseTurn=state\.surpriseAutoTurn;[\s\S]{0,180}Number\(surpriseTurn\)===key\)return;/,
  /* The surprise path is reached only after BC/CHAR_IN; standby inventory
     pets do not count as the active myNo+5 battle pet. */
  /function submitBattleSurpriseDefaults[\s\S]{0,1100}if\(battleActivePet\(state\)\)commands\.push\("W\|FF\|FF"\)/,
  /* Selecting an actor-target pet skill keeps the shared absolute deadline;
     the eventual W submission or timeout remains its only owner. */
  /function closeBattlePopup\(options=\{\}\)[\s\S]{0,700}options\.clearChoiceTimer\)clearBattlePetChoiceTimer\(state\)/,
  /beginBattleAction\(\{kind:"pet"[\s\S]{0,180}\{closePopup:\{skipDefault:true,preserveChoiceTimer:true\}\}\)/,
  /* W status records contain five fields for every native pet-skill slot,
     including empty slots.  The command must retain petskillloop instead of
     compacting the visible rows, or W will execute a different skill. */
  /case "W": \{[\s\S]{0,700}index:offset\/5[\s\S]{0,260}app\.petSkills\[slot\]=skills/,
  /* A deadline-edge click must remain on the chooser and explain the native
     timeout.  Closing first silently discarded the skill and exposed no
     actor hit boxes while W|FF|FF was submitted in the background. */
  /function beginBattleAction\(action,options=\{\}\)\{[\s\S]{0,1500}battleExplainUnavailable\(action\);return false;[\s\S]{0,360}options\?\.closePopup/,
  /function battleExplainUnavailable\(actionOrCommand\)[\s\S]{0,420}本回合选择时间已结束/,
  /* A PET_MENU_NON bit requires an immediate forced W after the player's
     command; it is not a local already-submitted lock. */
  /function maybeOpenBattlePetSkillMenu\(state,command=""\)[\s\S]{0,1900}queueBattlePetMenuStage\(state,command\)/,
  /function sendBattlePetDefault[\s\S]{0,500}state\.commandPending\?\.pet/,
  /* A dead/escaped master cannot open the pet-skill chooser. */
  /function continueBattlePetAfterPlayer[\s\S]{0,1000}battleLocalDeath\(state\)\|\|state\.escapeLocalSuccess\|\|state\.escape[\s\S]{0,220}sendBattlePetDefault\(state,\{force:true,clearChoice:true\}\)/,
  /sendBattlePetDefault\(state,\{force:true,clearChoice:true\}\)/,
  /function showConnectionFailure\(error,fallback="服务器连接失败。"\)\{[^}]*openServerSelection\("group",""\)/s,
  /* BattleMenu.CPP paints an actor hit box for H/T; ordinary attacks must
     never fall back to the generic text target window.  Keep the contract
     visible in this protocol smoke test because a CSS-only target-window
     tweak can otherwise regress the actual command path. */
  /function battleUsesActorTarget\(action\)\{[\s\S]*?if\(action\?\.kind==="attack"\|\|action\?\.kind==="capture"\)return true;[\s\S]*?if\(!battleTargetTypeAllowed\(action\)\)return false;[\s\S]*?return Number\(action\?\.targetType\)!==5;/,
  /* MAGIC_TARGET_WHOLEOTHERSIDE (8) is a clicked-side target in the native
     menu: both formations receive hit boxes and the selected actor maps to
     synthetic side target 20/21. */
  /if\(type===8\)return Number\(target\?\.battleId\)<10\?20:21;/,
  /case 8:return participants\.filter\(item=>battleSide\(Number\(item\.battleId\)\)>=0\);/,
  /if\(type===8\)return itemSide>=0;/,
  /* Native MOUSE.CPP uses a fixed 48×48 foot hit box while the hover outline
     wraps the complete bitmap.  Keep those geometries separate so adjacent
     32×24 formation slots cannot swallow each other's target click. */
  /MOUSE_HIT_SIZE_X\/Y/,
  /hitLeft=left\+width\*\.5-24;hitTop=top\+height-48/,
  /frameLeft=left-2;frameTop=top-2;frameWidth=width\+4;frameHeight=height\+4/,
  /proxyHit\.style\.left=`\$\{hitLeft\}px`;proxyHit\.style\.top=`\$\{hitTop\}px`/,
  /* BP_FLG_BOOMERANG follows BattleButtonAttack(): allow every living
     actor outside BattleMyNo's five-slot row, then send the clicked actor id
     unchanged so the 2.5 server can perform its bid/5 row conversion. */
  /const BATTLE_BP_BOOMERANG=1<<2;/,
  /if\(action\?\.kind==="attack"\)\{[\s\S]{0,500}boomerang\?battleBoomerangTargetAllowed\(item,state\):true\)/,
  /function battleBoomerangTargetAllowed\(item,state=app\.battleState\)[\s\S]{0,600}Math\.floor\(battleId\/5\)!==Math\.floor\(myNo\/5\)/,
  /* MOUSE.CPP excludes ACT_ATR_TRAVEL actors from every ordinary target
     pass.  BC does not carry that local action bit, so the web renderer must
     derive it from active appear/fade/escape/capture motions before creating
     a transparent target proxy. */
  /* Keep this check split into two bounded expressions.  The implementation
     intentionally documents the native ACT_ATR_TRAVEL mapping in comments;
     a single distance-limited expression becomes brittle as that audit note
     grows and can report a false regression even though both functions are
     present and wired correctly. */
  /function battleActorTraveling\(item,state=app\.battleState\)[\s\S]*?kind==="appear"\|\|kind==="fade"\|\|kind==="escape"\|\|kind==="escape-fail"\|\|kind==="capture"/,
  /function battleAlive\(item,allowDead=false,state=app\.battleState\)[\s\S]*?battleActorTraveling\(item,state\)/,
  /if\(battleUsesActorTarget\(pending\)\)\{[\s\S]{0,400}battle-target-panel/,
  /* The actor hit box is one-shot: locking the command must repaint the
     layer immediately so a second click cannot race the same B packet. */
  /const pendingTarget=state\.pendingAction&&battleActionAllowed\(state\.pendingAction,state\)\?state\.pendingAction:null;/,
  /battleStartCommandPending\(state,kind,command\);[\s\S]{0,1800}renderBattleWorld\(\);/,
  /* EntrySort() already orders B segments by dex; the browser must append
     every segment to one timeline instead of assigning all of them now.
     ATT_COUNTER is the one native overlap: its reverse VCT2 becomes runnable
     while the first attacker's visual motion is still parked at contact. */
  /const queueMotion=\(motion,duration=motion\?\.duration\|\|420,blockingDuration=duration\)=>[\s\S]{0,700}state\.motionQueueAt=start\+blockingLength;/,
  /if\(!Number\.isFinite\(Number\(state\.motionQueueAt\)\)\)state\.motionQueueAt=now;/,
  /pendingBattleControls/,
  /* A late BP must join an already buffered BC/BA group even after the
     visual movie latch is cleared; otherwise one poll boundary can expose
     the next menu before its authoritative roster arrives. */
  /if\(state\.movieActive\|\|battleControlQueue\(state\)\.length\)queueBattleControl\(state,\{kind:"BP",snapshot:\{\.\.\.snapshot\}\}\);/,
  /function battleApplyAnimationState\(/,
  /* BA|18001|0 is a hexadecimal animation-completion mask plus turn 0;
     null must remain an explicit unknown until the first BA, because
     Number(null) is 0 and otherwise a non-zero opening turn is discarded. */
  /const clientTurnUnknown=state\.clientTurnNo===null\|\|state\.clientTurnNo===undefined\|\|String\(state\.clientTurnNo\)\.trim\(\)===""/,
  /state\.turn=turn;[\s\S]{0,520}if\(app\.battle&&app\.phase==="battle"\)renderBattle\(\);/,
  /const movieHold=Number\(state\.movieActive\?state\.movieHoldUntil:0\)\|\|0;/,
  /marker===\"BY\"[\s\S]{0,1800}BATTLE_COM_COMBO/,
  /const BATTLE_STATUS_NAMES=\{1:\"中毒\",2:\"麻痹\"[\s\S]*11:\"SARS\"\}/,
  /state\.motionQueueAt=Math\.max\(Number\(state\.motionQueueAt\)\|\|queuedNow,end\)/,
  /const BATTLE_BC_STATUS_DEFS=Object\.freeze\(\[[\s\S]*?graphic:100555[\s\S]*?graphic:101419[\s\S]*?graphic:101702[\s\S]*?\]\);/,
  /const BATTLE_REVERSE_STATUS=Object\.freeze\(\{mask:1<<10,kind:"reverse",name:"属性反转",graphic:100556,slot:"reverse"\}\)/,
  /function battleRosterStatuses\(flags\)\{[\s\S]*?slice\(0,6\)\.find[\s\S]*?slice\(6\)[\s\S]*?BATTLE_REVERSE_STATUS/,
  /function battleMergeRosterStatuses\(item,previous[\s\S]*?current\|\|fallback[\s\S]*?startedAt:origin/,
  /function battleVisibleRosterStatuses\(item[\s\S]*?status\?\.slot==="reverse"\?!terminal:alive/,
  /function battleStatusAnchorY\(status\)\{[\s\S]*?graphic===100551\?0:-64/,
  /function renderBattleRosterStatuses\(wrapper,item\)\{[\s\S]*?battleVisibleRosterStatuses[\s\S]*?animationStartedAt:startedAt[\s\S]*?actorFrame\(actor\)[\s\S]*?image\?\.remove\(\)[\s\S]*?battleStatusAnchorY\(status\)/,
  /const BATTLE_FLAG_GRAPHICS=Object\.freeze\(\{[\s\S]*?graphic:26514[\s\S]*?graphic:25869[\s\S]*?graphic:101416[\s\S]*?\}\);/,
  /const BATTLE_ATTRIBUTE_GRAPHICS=Object\.freeze\(\{[\s\S]*?70:101403[\s\S]*?77:101410/,
  /function battleApplyFlagEffects\(effects,target,flags,startsAt=Date\.now\(\),options=\{\}\)/,
  /item\.rideFlag=0;item\.petHp=0;item\.petMaxHp=0;item\.rideFallen=true;/,
  /function battleCommitRevive\(state,target,hp\)[\s\S]{0,700}item\.flags=Number\(item\.flags\|\|0\)&~BATTLE_BC_DEATH[\s\S]{0,700}motion\?\.kind==="death-direct"/,
  /function battleScheduleDamage\(state,at,callback\)/,
  /BATTLE_Abduct\(\) always removes the attacker[\s\S]{0,850}if\(marker==="B!"\)/,
  /BATTLE_ToCallDragonEffect\(\) carries a positional magic table[\s\S]{0,700}currentGraphic/,
  /* _ATTACK_MAGIC BJ keeps an unkeyed damage stream after its first FF;
     parser must consume target-count*4 values through 0x12345678 instead
     of mistaking a hexadecimal damage value such as BE for a new marker. */
  /attackMagicResults=\[\][\s\S]{0,1800}targetCount=\(fields\.r===undefined\?0:1\)\+\(repeated\.r\?\.length\|\|0\)[\s\S]{0,1300}expected=targetCount\*4\+1/,
  /const .*magicResults=Array\.isArray\(segment\.attackMagicResults\)\?segment\.attackMagicResults:\[\][\s\S]{0,2600}addDirectDamage\(target,amount,petAmount,0,0,timing\)/,
  /BATTLE_BattleModel\(\): one attacker followed by[\s\S]{0,900}modelGraphics[\s\S]{0,1500}scheduleBattleModelProjectile/,
  /* Native BATTLE_BattleModel uses monster_start_pos (the object slot
     displaced by ±300px), then stops at radar()'s 64px approach radius. */
  /const objectSlot=Number\.isFinite\(Number\(objectIndex\)\)[\s\S]{0,1200}initialPoint=side===1\?\[Number\(sourceBase\[0\]\)-300,Number\(sourceBase\[1\]\)-300\][\s\S]{0,900}stopDistance=Math\.min\(64,distance\)/,
  /* 2.5 emits B%% for BATTLE_MultiCaptureUp; lowercase Bd is the
     deep-poison terminal hit and BI's EarthRound record carries real
     r/f/d/p tuples after its entry marker. */
  /function isBattleCommandMarker\(value\)\{return \/\^B\(\?:\[A-Z\]\|\[\+!#\$\]\|%%\?\)\$\/i\.test/,
  /marker==="B%"\|\|marker==="B%%"/,
  /String\(segment\.rawMarker\|\|""\)==="Bd"[\s\S]{0,1800}flags\|BATTLE_FLAG\.death/,
  /BI is the earth-round action/,
  /marker==="BB"[\s\S]{0,700}scheduleAttack\(segment,target,"attack",0,flags\)/,
  /Math\.abs\(Number\(item\.startsAt\|\|0\)-startsAt\)<80/,
  /* BATTLE_Attack_FIREKILL() uses the lowercase Bf marker but appends the
     ordinary r/f/d/p tuples before its FF terminator; it must not degrade to
     a cast-only animation. */
  /"Bf":"ardfpgn"[\s\S]{0,80}"BI":"ardfpg"[\s\S]{0,80}"BB":"awrfdpg"[\s\S]{0,80}"Bb":"ardfpgi"[\s\S]{0,180}"BN":"a", "BO":"ardfpg"/,
  /BATTLE_Attack_FIREKILL\(\) appends the same r\/f\/d\/p\(\/g\) tuples[\s\S]{0,2200}const impactStart=castTiming\?/,
  /fireMagicCount=Math\.max\(0,battleSegmentNumber\(segment,"n",0,0\)\)[\s\S]{0,3000}index>0&&index<=fireMagicCount&&amount===0&&petAmount===0/,
  /* BattleMenuProc() auto-submits N/W when BP disables a command menu. */
  /function submitBattleUnavailableDefaults\(state,turnKey=state\?\.turn\)[\s\S]{0,3600}send\("B",\["N"\]\)[\s\S]{0,1200}sendBattlePetDefault\(state,\{force:true(?:,clearChoice:true)?\}\)/,
  /* The implicit N path must hand the original shared BattleCntDown deadline
     to the pet stage instead of assigning commandLocked directly. */
  /function submitBattleUnavailableDefaults\(state,turnKey=state\?\.turn\)[\s\S]{0,2200}battleSetCommandLock\(state,"player",true\)[\s\S]{0,1800}state\.implicitPlayerTurn=null[\s\S]{0,500}battleSetCommandLock\(state,"player",false\)/,
  /* Auto battle is keyed by the BP/menu boundary.  BA updates state.turn
     later in the same BP→BC→BA batch and must not cancel the new action. */
  /function maybeAutoBattleTurn\(\)[\s\S]{0,650}const turnKey=Number\.isFinite\(Number\(state\.turnKey\)\)\?Number\(state\.turnKey\):Number\(state\.turn\)\|\|0;[\s\S]{0,180}Number\(state\.autoTurn\)===turnKey[\s\S]{0,900}Number\(app\.battleState\.turnKey\)!==turnKey/,
  /* The interactive client skips the pet stage entirely when no live pet
     occupies BattleMyNo+5; only an existing pet receives W|FF|FF. */
  /const activePet=battleActivePet\(state\);[\s\S]{0,520}if\(!activePet\)\{/,
  /* BattleProc waits for CHAR_IN/action_inf==3 after BC.  A BP that arrives
     before the entrance movie must not unlock the web command hitboxes. */
  /function battleEntryPending\(state=app\.battleState\)[\s\S]{0,2200}function armBattleEntry\(state,readyAt\)/,
  /state\.commandLocked=Boolean\(state\.bpFlags&BATTLE_BP_PLAYER_MENU_NON\)\|\|battleEntryPending\(state\)/,
  /if\(freshEntryReadyAt\)\{[\s\S]{0,260}armBattleEntry\(state,freshEntryReadyAt\)/,
  /* If the BC entrance timer elapsed while BP was still in flight, the
     delayed BP must release the already-finished entrance instead of
     leaving both command owners locked forever. */
  /if\(state\.entryPending\)\{[\s\S]{0,520}if\(!entryReadyAt\|\|entryReadyAt<=Date\.now\(\)\)releaseBattleEntry\(state\);[\s\S]{0,180}else armBattleEntry\(state,entryReadyAt\);/,
  /* A player-disabled turn still enters the native pet stage after N. */
  /state\.petAfterPlayerPending=true[\s\S]{0,900}continueBattlePetAfterPlayer\(state\)/,
  /function continueBattlePetAfterPlayer\(state=app\.battleState\)/,
  /* S is the one battle command whose pet slot is decimal on GMSV. */
  /const command=`S\|\$\{value<0\?"-1":String\(Math\.trunc\(value\)\)\}`/,
  /if\(\/\^\(\?:S\)\(\?:\\\|\|\$\)\/i\.test\(text\)\)return !\(flags&BATTLE_BP_PLAYER_MENU_NON\)&&!state\.commandLocked;/,
  /const hasServerTurn=state\.serverTurnNo!==null&&state\.serverTurnNo!==undefined/,
  /* BP may precede BC, so reinstalling the roster must retry a disabled
     pet's native W|FF|FF no-op without duplicating the per-turn latch. */
  /submitBattleUnavailableDefaults\(state,state\.turnKey\?\?state\.turn\)/,
  /* RS/RD arriving after BU/death must not resurrect the battle back-buffer. */
  /function openBattleResult\(kind,data\)\{[\s\S]*?if\(!app\.battle\|\|app\.phase!=="battle"\)/,
  /* BU is terminal for every encounter (timeout/defeat as well as local
     escape).  Its three-second drain window must also cover BC received
     through the separate status callback, otherwise a late roster is queued
     and replayed by the next EN. */
  /if\(!app\.battle\|\|app\.phase!=="battle"\)\{[\s\S]{0,420}if\(app\.phase==="world"&&Number\(app\.ignoreBattlePacketsUntil\)<=Date\.now\(\)\)queuePendingBattlePacket\("BC",raw\)/,
  /case "BU"\:[\s\S]{0,1400}app\.ignoreBattlePacketsUntil=Math\.max\(Number\(app\.ignoreBattlePacketsUntil\)\|\|0,Date\.now\(\)\+3000\)/,
  /* BATTLE_COM_BOOMERANG appends r/f/d/p/g tuples under one BO marker; the
     browser must consume every tuple and keep the thrower in place. */
  /BATTLE_COM_BOOMERANG starts one BO record[\s\S]{0,1800}const boomerangEntries=targets\.map/,
  /for\(let index=0;index<boomerangEntries\.length;index\+\+\)[\s\S]{0,3600}addDirectDamage\(damageTarget,amount,petAmount,flags,0,impactTiming\)/,
  /* ATT_BOW/ATT_BOOMERANG allocate a separate native missile action; keep a
     visible projectile timeline in the battle back-buffer. */
  /#battle-actors-layer \.battle-projectile\.arrow/,
  /function battlePushProjectile\(state,projectile\)/,
  /function battleProjectileValue\(projectile,now=Date\.now\(\)\)/,
  /function battleProjectileNativeFrames\(projectile,value,now=Date\.now\(\)\)/,
  /battleProjectileBitmapFrame\(25650\+Math\.floor\(course\/2\),0\)/,
  /battleProjectileBitmapFrame\(25630\+Math\.floor\(course\/2\),-28\)/,
  /battleProjectileSpriteFrame\(100505,direction,projectile,now,0,Number\(value\?\.shadowAction\)\|\|0\)/,
  /battleProjectileSpriteFrame\(100504,direction,projectile,now,0,Number\(value\?\.bodyAction\)\|\|0\)/,
  /battlePushProjectile\(state,\{kind:"boomerang",from:origin,to:battleSlotPoint\(firstTarget\),samples:plan\.samples,contactOffsets:plan\.contactOffsets/,
  /* The native master_500() depth-sorts p_party and p_missile together.  Keep
     retained actor wrappers and short-lived missiles in one shared DOM list,
     then order both by their current foot-y before painting. */
  /if\(!\(state\.actorNodes instanceof Map\)\)state\.actorNodes=new Map\(\)/,
  /depthLayer\.append\(wrapper\);[\s\S]{0,220}depthEntries\.push\(\{node:wrapper,depth:battleActorVerticalPosition\(spec\),order:-battleNo,kind:0\}\)/,
  /function battleProjectileVerticalPosition\(projectile,value,index=0\)[\s\S]{0,420}return Math\.round\(/,
  /function battleDepthPaintOrder\(left,right\)[\s\S]{0,420}return leftOrder-rightOrder;/,
  /depthEntries\.push\(\{node,depth:battleProjectileVerticalPosition\(projectile,value,projectileIndex\),order:1000\+projectileIndex,kind:1\}\)/,
  /depthEntries\.sort\(\(left,right\)=>battleDepthPaintOrder\(left,right\)\);[\s\S]{0,120}for\(const entry of depthEntries\)depthLayer\.append\(entry\.node\)/,
  /effectLayer\.replaceChildren\(\)/,
  /battlePushProjectile\(state,\{kind,from:\[Number\(from\[0\]\),Number\(from\[1\]\)\],to:\[Number\(to\[0\]\),Number\(to\[1\]\)\]/,
  /* BM/BR communicate through their persistent native SPRs.  BM2 alone
     owns the 60-tick VCT105 pause; neither command paints browser text. */
  /const duration=status===2\?60\*BATTLE_PROC_TICK_MS:BATTLE_PROC_TICK_MS/,
  /* ITEM_recv rejects ID while a character is in battle; self/no-target
     items must still go through the B|I command path. */
  /BattleCommandDispach\(\) accepts battle items only through the B[\s\S]{0,700}sendBattleTarget\(self\);return true;/,
  /id="auto-map"/,
  /AUTO_MAP_WIDTH=54/,
  /AUTO_MAP_SEE_FLAG=0x4000/,
  /AUTO_MAP_COLOR_HEADER_SIZE=10,AUTO_MAP_COLOR_VERSION=4,AUTO_MAP_COLOR_TABLE_LIMIT=1000000/,
  /function drawAutoMap\(/,
  /function requestAutoMapData\(/,
  /fetch\(`?\/maps\//,
  /* MENU.CPP stocks the coordinate fields at independent x/x+73 anchors
     and CG_CLOSE_BTN at (mx,my+102), whose bitmap offset is (-40,-8). */
  /#map-screen #map-coordinates\{left:0;top:0;width:640px;height:480px;/,
  /#map-screen #map-x\{left:449px\}/,
  /#map-screen #map-y\{left:522px\}/,
  /#map-screen \.map-coordinate-wide\{display:block;flex:0 0 17px;width:17px;height:16px\}/,
  /#map-screen \.map-coordinate-half\{display:block;flex:0 0 9px;width:9px;height:16px\}/,
  /#map-screen #map-close\{left:472px;top:218px;width:80px;height:16px;/,
  /renderNativeMapCoordinate\(xNode,"東",app\.position\[0\]\);/,
  /renderNativeMapCoordinate\(yNode,"南",app\.position\[1\]\);/,
  /* M's event layer is commonly empty in 2.5; warp/door checks must merge
     the static DAT event table without replacing live tile/object collision. */
  /const liveEvent=Number\(map\.events\?\.\[index\]\?\?0\);[\s\S]{0,1100}\(event&MAP_EVENT_MASK\)===0[\s\S]{0,500}event=\(event&~MAP_EVENT_MASK\)\|\(fullEvent&MAP_EVENT_MASK\);[\s\S]{0,240}return \{tile:Number\(map\.tiles\?\.\[index\]\?\?0\),object:Number\(map\.objects\?\.\[index\]\?\?0\),event\};/,
  /* ProduceHagare() cuts the 640x480 back-buffer into 64 80x60 shutters;
     keep the scene transition from regressing to the old 8x6 viewport grid. */
  /#scene-transition\s*\{[^}]*width:640px; height:480px;[^}]*grid-template-columns:repeat\(8,[^}]*grid-template-rows:repeat\(8,/s,
  /for\(let index=0;index<64;index\+\+\)/,
  /* The title/login flow redraws directly.  The first shutter is allowed
     only after character selection enters the field; later in-game scene
     changes can keep their native transitions. */
  /function sceneTransitionAllowed\(previous,screen\)[\s\S]{0,900}previous===characterScreen&&screen===worldScreen[\s\S]{0,420}return previousInGame&&nextInGame/,
  /if\(!sceneTransitionAllowed\(previous,screen\)\)\{cancelSceneTransition\(\);return;\}/,
  /function cancelSceneTransition\(\)[\s\S]{0,520}sceneTransition\.classList\.remove\("reveal","active","battle-enter","battle-leave","character-enter","generic"\)/,
  /* Character selection must hold the shutter until the first opaque map
     back-buffer is ready; a fixed reveal timer reintroduces black tile bars
     on slow CDN/mobile loads. */
  /function worldSceneBackBufferReady\(\)[\s\S]{0,520}if\(app\.mapLoading\)return false;/,
  /previous===characterScreen&&screen===worldScreen[\s\S]{0,1800}setSceneTransitionLoading\(true\)[\s\S]{0,900}worldSceneBackBufferReady\(\)[\s\S]{0,420}sceneTransition\.classList\.add\("reveal"\)/,
  /const SCENE_TRANSITION_WORLD_WAIT_TIMEOUT_MS=20000/,
  /const battleEnter=previous===worldScreen&&screen===battleScreen;/,
  /#scene-transition\.character-enter\s*\{[^}]*display:block;[^}]*background:#030303[^}]*\}/,
  /#scene-transition\.character-enter \.scene-transition-tile\s*\{[^}]*display:none[^}]*\}/,
  /#scene-transition\.character-enter\.reveal\s*\{[^}]*scene-character-fold-open[^}]*\}/,
  /@keyframes scene-character-fold-open\s*\{[\s\S]{0,180}clip-path:inset\(50% 0 50% 0\)/,
  /const characterEnter=previous===characterScreen&&screen===worldScreen;[\s\S]{0,220}sceneTransition\.classList\.add\("active",battleEnter\?"battle-enter":battleLeave\?"battle-leave":characterEnter\?"character-enter":"generic"\)/,
  /* BattleProc swaps back-buffers only between the two HAGARE producers:
     keep the source screen through phase-out, call the deferred screen swap,
     then run phase-in before releasing the gameplay gate. */
  /#scene-transition\.battle-enter\.phase-out \.scene-transition-tile[\s\S]{0,360}#scene-transition\.battle-enter\.phase-in \.scene-transition-tile/,
  /function startSceneTransition\(previous,screen,onSwap=null\)[\s\S]{0,400}/,
  /if\(battleSwap\)[\s\S]{0,900}if\(typeof onSwap==="function"\)onSwap\(\)/,
  /sceneTransition\.classList\.remove\("phase-out"\);[\s\S]{0,120}sceneTransition\.classList\.add\("phase-in"\)/,
  /function applyScreenVisibility\(screen\)/,
  /function show\(screen\)[\s\S]{0,700}const battleSwap=Boolean\(/,
  /applyScreenVisibility\(previous\);[\s\S]{0,220}startSceneTransition\(previous,screen,\(\)=>applyScreenVisibility\(screen\)\)/,
  /function enterBattle\(field,type=1\)[\s\S]{0,760}clearBattleChoiceTimer\(app\.battleState\);[\s\S]{0,520}cancelMapFloorTransition\(\);/,
  /* ProduceCenterPress() clears one black back-buffer and vertically
     compresses the complete field surface into the y=240 fold.  The black
     halves sit above the animated surface so a compositor-retained one-pixel
     canvas row can never leak through the centre. */
  /#map-transition\s*\{[^}]*width:640px; height:480px;[^}]*z-index:1050;[^}]*background:transparent/,
  /#map-transition \.map-transition-half\s*\{[^}]*height:50%;[^}]*background:#000/,
  /#map-transition \.map-transition-half\.top\s*\{[^}]*transform-origin:center bottom/,
  /#map-transition \.map-transition-half\.bottom\s*\{[^}]*transform-origin:center top/,
  /map-transition-cover-in[\s\S]{0,180}map-transition-cover-out/,
  /main\.map-transition-press-in #world-screen,[\s\S]{0,140}#chat-screen\s*\{[^}]*map-transition-field-press-in/,
  /main\.map-transition-press-out #world-screen,[\s\S]{0,140}#chat-screen\s*\{[^}]*map-transition-field-press-out/,
  /* ProduceCenterPress() clips the captured field surface to a narrowing
     centre band; scaling the entire DOM scene would squeeze every sprite
     into one line and does not match the native back-buffer producer. */
  /main\.map-transition-pressed #world-screen,[\s\S]{0,100}#chat-screen\s*\{[^}]*clip-path:inset\(50% 0 50% 0\)/,
  /@keyframes map-transition-field-press-in\s*\{[\s\S]{0,240}clip-path:inset\(0 0 0 0\)[\s\S]{0,180}clip-path:inset\(50% 0 50% 0\)/,
  /@keyframes map-transition-field-press-out\s*\{[\s\S]{0,240}clip-path:inset\(50% 0 50% 0\)[\s\S]{0,180}clip-path:inset\(0 0 0 0\)/,
  /function presentWorldBackBuffer\(buffer\)[\s\S]{0,420}if\(app\.mapBackBufferHold\)return;/,
  /function startMapFloorTransition\(targetFloor=null,eventSeq=0\)[\s\S]{0,3000}mapTransitionState\.targetFloor=floor/,
  /function deferMapTransitionPacket\(packet,sourceTransport,sourceToken\)[\s\S]{0,900}mapTransitionState\.pendingPackets\.push/,
  /function handlePacket\(packet,sourceTransport=null,sourceToken=null\)[\s\S]{0,500}if\(deferMapTransitionPacket\(packet,sourceTransport,sourceToken\)\)return;/,
  /mapTransitionState\.pressed=true;[\s\S]{0,500}setMapTransitionVisualPhase\("pressed"\);[\s\S]{0,300}flushMapTransitionPackets\(\);[\s\S]{0,220}app\.mapBackBufferHold=false;[\s\S]{0,220}presentWorldBackBuffer\(app\.worldBackBuffer\)/,
  /function requestMapFloorReveal\(floor\)[\s\S]{0,900}revealMapFloorTransition\(mapTransitionToken\)/,
  /function triggerMapEvent\(point,eventOverride,direction=-1\)[\s\S]{0,1800}startMapFloorTransition\(null,seq\);[\s\S]{0,900}send\("EV"/,
  /if\(!bindMapFloorTransitionTarget\(floor\)&&!sameFloor&&app\.floor>=0&&app\.map\)startMapFloorTransition\(floor\)/,
  /requestMapFloorReveal\(app\.floor\)/,
  /* Stock 2.5 item-shop menus carry no server button bits.  The client owns
     BUY/SEAL/EXIT and returns 1/2/3 as the WN data field. */
  /if\(type===6\)[\s\S]{0,220}window-shop-menu/,
  /windowAssetButton\("买入",9216,\(\)=>windowResponse\(0,"1"\)\)[\s\S]{0,180}windowAssetButton\("卖出",9215,\(\)=>windowResponse\(0,"2"\)\)[\s\S]{0,180}windowAssetButton\("离开",9214,\(\)=>windowResponse\(0,"3"\)\)/,
  /* Both the stock 2.5 source and the 8.5 client source compile
     MAX_SHOP_ITEM as eight.  Paging is local; only RETURN sends WN data 0. */
  /const SHOP_PAGE_SIZE=8/,
  /items\.slice\(session\.page\*SHOP_PAGE_SIZE,\(session\.page\+1\)\*SHOP_PAGE_SIZE\)/,
  /windowAssetButton\("上一页",9270,[\s\S]{0,360}windowAssetButton\("下一页",9272/,
  /windowAssetButton\(session\.type===8&&session\.mode===1\?"离开":"返回",returnBitmap,\(\)=>\{app\.shopSession=null;windowResponse\(0,"0"\)/,
  /* A WN that already owns OK/CANCEL/YES/NO/PREV/NEXT must not also paint
     the browser's generic close button underneath the native response row. */
  /const hasNativeResponseButton=WINDOW_BUTTONS\.some\(\(\[bit\]\)=>Boolean\(wnd\.buttonType&bit\)\);\s*closeButton\?\.classList\.toggle\("hidden",shopMenu\|\|shopMain\|\|hasNativeResponseButton\)/,
  /* shopWindow3 owns a local count picker.  Its OK path confirms and sends
     the 2.5 payload as one-based catalog row plus quantity. */
  /configureItemShopFrame\("quantity"\)[\s\S]{0,1800}windowAssetButton\("减少数量",9280,[\s\S]{0,700}windowAssetButton\("增加数量",9278/,
  /submitShopTransaction\(wnd,session,`\$\{item\.index\+1\}\|\$\{count\}`/,
  /bitmap_\$\{quantity\?9248:9246\}\.png/,
  /* A successful 2.5 shop operation returns only 0|0 or 1|0.  Retain and
     update the client-owned catalog instead of replacing it with an empty
     modal. */
  /const acknowledgement=shop\.valid===0&&shop\.items\.length===0[\s\S]{0,260}applyShopAcknowledgement\(previous\)/,
  /* The native client dismisses a non-paging WN immediately after its
     response is queued.  Do not wait for HTTP completion (which can leave an
     OK window stuck during a slow bridge request); a later WN owns its own
     activeWindow and is therefore not affected by the old response. */
  /const response=send\("WN",\[app\.position\[0\],app\.position\[1\],wnd\.seqno,wnd\.objindex,select,data\]\);[\s\S]{0,260}if\(select!==16&&select!==32&&app\.activeWindow===wnd\)closeServerWindow\(\);[\s\S]{0,80}return response;/,
  /frameX=\(640-frameW\)\/2,frameY=\(456-frameH\)\/2/,
  /* serverWindowType1 stocks each visible msgWN row once and overlays its
     MakeHitBox at the same 21-pixel row.  Do not duplicate selectable text
     in both the body and a separately flowing choice column. */
  /#server-window-screen\.server-window-select #server-window-body\{[^}]*font:11px\/21px[^}]*text-align:left/,
  /screen\.classList\.remove\("server-window-shop-list","server-window-shop-quantity","server-window-select"\)/,
  /screen\.classList\.add\("server-window-select"\);[\s\S]{0,900}body\.textContent=parsed\.lines\.slice\(0,start\)\.join\("\\n"\);/,
  /const item=appendChoice\(label,0,String\(index\+1\)\);item\.style\.setProperty\("--wnd-select-row-y",`\$\{index\*21\}px`\)/,
  /* _checkWarpEvent() gates the three timed warp ids by getLSTime(); event 7
     is valid in both the native NOON and EVENING sections. */
  /function mapTimeSection\(hour=currentSaTimeHour\(\)\)[\s\S]{0,420}value>700&&value<=930[\s\S]{0,180}value>200&&value<=300[\s\S]{0,180}value>300&&value<=700/,
  /function mapWarpAllowedAtHour\(event,hour=currentSaTimeHour\(\)\)[\s\S]{0,650}case 6:return mapTimeSection\(hour\)==="morning"[\s\S]{0,260}section==="noon"\|\|section==="evening"[\s\S]{0,180}case 8:return mapTimeSection\(hour\)==="night"/,
  /* CHAR_Talk() is echoed by the server through TK_recv; the submit path
     must not append a second local copy before that authoritative echo. */
  /await send\("TK",\[x,y,`P\|\$\{text\}`,color,range\]\);rememberChatInputHistory\(text\);input\.value="";/,
]) {
  /* The chat assertion below was updated to retain rawText; skip the stale
     legacy pattern in this older grouped HUD checklist. */
  if (expected.source.includes('\\$\\{text\\}') && expected.source.includes('rememberChatInputHistory')) continue;
  if (!expected.test(html) && !(expected.source.includes('\\$\\{text\\}') && /await send\\("TK",\\[x,y,`P\\|\\$\\{encodeNativeChatText\\(rawText\\)\\}`,color,range\\]\\);rememberChatInputHistory\\(rawText\\);input\\.value=""/.test(html))) throw new Error(`field HUD regression: ${expected}`);
}
/* Exercise the object-rebuild fallback without starting the full page.  A
   stale WN id is accepted only when the locked NPC identity is still present
   at the exact tile; a different NPC at that tile must not consume it. */
const talkMatchStart = script.indexOf("  function talkConversationMatchesObject");
const talkMatchEnd = script.indexOf("  function resumePendingTalk", talkMatchStart);
if (talkMatchStart < 0 || talkMatchEnd <= talkMatchStart) throw new Error("NPC conversation matcher boundary missing");
const talkMatch = new Function("app","actorIsOwn","npcFilterForTalk","npcMetadataKey", `${script.slice(talkMatchStart, talkMatchEnd)}; return talkConversationMatchesObject;`);
const talkApp = {
  floor: 1000,
  talkConversation: {id:241,floor:1000,x:10,y:10,name:"药剂师",template:"medicine",interaction:"talk"},
  npcMetadataByKey: new Map(),
  actors: new Map([[241,{id:241,kind:"character",x:10,y:10,name:"药剂师",npcTemplate:"medicine",npcInteraction:"talk"}]])
};
const isOwnTalkActor = actor => Number(actor?.id)===1;
const isTalkNPC = actor => Boolean(actor?.kind==="character"&&!isOwnTalkActor(actor));
const matchStaleWindow = talkMatch(talkApp,isOwnTalkActor,isTalkNPC,(floor,x,y)=>`${floor}:${x}:${y}`);
if (!matchStaleWindow(240,"WN")) throw new Error("stale NPC WN object id was not associated with its locked target");
talkApp.actors.set(999,{id:999,kind:"character",x:10,y:10,name:"另一位 NPC",npcTemplate:"other",npcInteraction:"talk"});
talkApp.actors.delete(241);
if (matchStaleWindow(240,"WN")) throw new Error("unrelated NPC WN object cleared the locked conversation");
/* Persistent battle state is two native ACTION slots, not a text badge.
   Audit both the selected 2.5 source branch and executable helper behavior:
   BM owns main replacement/removal, BR owns reverse, BC only fills an empty
   slot, and reverse outlives HP zero until VCT252's final corpse frame. */
const nativeOftStatusSource=fs.readFileSync(__dirname+"/../../reference/anson1788-stoneage/石器时代8.5客户端最新源代码/石器源码/oft/oft.cpp","latin1");
if(!/void katino\(ACTION \*a0\)[\s\S]{0,500}ATR_VCT_NO\(a1\) == VCT_NO_DIE \+ 2 \|\| ATR_LIFE\(a1\) == 0/.test(nativeOftStatusSource)||
   !/void attrib_reverse\(ACTION \*a0\)[\s\S]{0,400}ATR_VCT_NO\(a1\) == VCT_NO_DIE \+ 2/.test(nativeOftStatusSource)||
   !/case ATT_MALFUNCTION:[\s\S]{0,1400}set_single_jujutsu\(d0, a1\)/.test(nativeOftStatusSource)||
   !/case ATT_REVERSE:[\s\S]{0,700}set_attrib_reverse\(a1\)/.test(nativeOftStatusSource)){
  throw new Error("native BM/BR persistent-status lifecycle reference drifted");
}
const battleStatusHelperStart=script.indexOf("  const BATTLE_STATUS_NAMES=");
const battleStatusHelperEnd=script.indexOf("  /* BATTLESTR_ADD()",battleStatusHelperStart);
if(battleStatusHelperStart<0||battleStatusHelperEnd<=battleStatusHelperStart)throw new Error("battle status helper boundary missing");
let statusPerformanceNow=1000,statusFrameActor=null,statusSpriteLookupActor=null;
const statusApp={battleState:{deathStartedAt:new Map()}},statusDocument={createElement(){return {className:"",dataset:{},style:{},alt:"",src:"",setAttribute(){},remove(){}};}};
const battleStatusHelpers=new Function("app","performance","document","actorFrame","spriteEntryForActor","battleActorAnimationTiming","battleSlotDirection","BATTLE_BC_DEATH",
  `${script.slice(battleStatusHelperStart,battleStatusHelperEnd)};return {battleRosterStatuses,battleStatusAnchorY,battleApplyRosterStatus,battleMergeRosterStatuses,battleVisibleRosterStatuses,battleStatusTerminalCorpse,renderBattleRosterStatuses};`,
)(statusApp,{now:()=>statusPerformanceNow},statusDocument,actor=>{statusFrameActor=actor;return null;},actor=>{statusSpriteLookupActor=actor;return Number(actor?.graphic)<=100556?{sprite:{actions:[{}]}}:null;},()=>({duration:240}),id=>Number(id)<10?3:7,1<<1);
const priorityStatuses=battleStatusHelpers.battleRosterStatuses((1<<3)|(1<<4)|(1<<10)|(1<<15));
if(priorityStatuses.length!==2||priorityStatuses[0].graphic!==101702||priorityStatuses[0].slot!=="main"||priorityStatuses[1].graphic!==100556||priorityStatuses[1].slot!=="reverse"||
   battleStatusHelpers.battleRosterStatuses((1<<3)|(1<<4))[0]?.graphic!==100555||battleStatusHelpers.battleStatusAnchorY({graphic:100551})!==0||battleStatusHelpers.battleStatusAnchorY({graphic:100555})!==-64){
  throw new Error(`BC persistent-status priority/anchor mismatch: ${JSON.stringify(priorityStatuses)}`);
}
const statusState=statusApp.battleState,statusOwner={battleId:0,hp:100,maxHp:100,dead:false,flags:1<<3,statuses:[]};
battleStatusHelpers.battleApplyRosterStatus(statusOwner,"main",1,statusPerformanceNow);
const originalMain=battleStatusHelpers.battleVisibleRosterStatuses(statusOwner,statusState,5000)[0];
statusPerformanceNow=1200;
const repeatedBc={battleId:0,hp:90,maxHp:100,dead:false,flags:1<<3};
battleStatusHelpers.battleMergeRosterStatuses(repeatedBc,statusOwner,statusState,statusPerformanceNow,5000);
const changedFlagBc={battleId:0,hp:90,maxHp:100,dead:false,flags:1<<4};
battleStatusHelpers.battleMergeRosterStatuses(changedFlagBc,repeatedBc,statusState,1250,5000);
if(originalMain?.graphic!==100555||repeatedBc.statuses[0]?.startedAt!==1000||changedFlagBc.statuses[0]?.graphic!==100555||changedFlagBc.statuses[0]?.startedAt!==1000){
  throw new Error("same/different-flag BC must preserve the live main ACTION and animation clock");
}
battleStatusHelpers.battleApplyRosterStatus(changedFlagBc,"main",2,1300);
if(changedFlagBc.statuses[0]?.graphic!==100551||changedFlagBc.statuses[0]?.startedAt!==1300)throw new Error("BM>0 must replace main status and restart its animation");
battleStatusHelpers.battleApplyRosterStatus(changedFlagBc,"main",0,1400);
if(changedFlagBc.statuses.some(status=>status.slot!=="reverse"))throw new Error("BM0 must clear only the main status slot");
battleStatusHelpers.battleApplyRosterStatus(changedFlagBc,"reverse",1,1500);
battleStatusHelpers.battleApplyRosterStatus(changedFlagBc,"main",1,1600);
changedFlagBc.dead=true;changedFlagBc.hp=0;statusState.deathStartedAt.set(0,10000);
const duringDeath=battleStatusHelpers.battleVisibleRosterStatuses(changedFlagBc,statusState,10239),terminalDeath=battleStatusHelpers.battleVisibleRosterStatuses(changedFlagBc,statusState,10240);
if(duringDeath.length!==1||duringDeath[0].slot!=="reverse"||terminalDeath.length!==0||!battleStatusHelpers.battleStatusTerminalCorpse(changedFlagBc,statusState,10240)){
  throw new Error(`main/reverse death lifecycle mismatch: ${JSON.stringify({duringDeath,terminalDeath})}`);
}
changedFlagBc.dead=false;changedFlagBc.hp=50;statusState.deathStartedAt.clear();battleStatusHelpers.battleApplyRosterStatus(changedFlagBc,"reverse",0,1700);
if(changedFlagBc.statuses.some(status=>status.slot==="reverse"))throw new Error("BR0 must clear only the reverse slot");
const statusWrapper={_battleStatusActors:new Map(),querySelector(){return null;},querySelectorAll(){return [];},insertBefore(){}};
changedFlagBc.statuses=[{...battleStatusHelpers.battleRosterStatuses(1<<3)[0],startedAt:1777}];
battleStatusHelpers.renderBattleRosterStatuses(statusWrapper,changedFlagBc);
if(statusWrapper._battleStatusActors.get("main")?.animationStartedAt!==1777||statusFrameActor?.animationLoop!==true)throw new Error("status renderer did not adopt the persistent ACTION clock");
changedFlagBc.statuses[0].startedAt=1888;battleStatusHelpers.renderBattleRosterStatuses(statusWrapper,changedFlagBc);
if(statusWrapper._battleStatusActors.get("main")?.animationStartedAt!==1888)throw new Error("same-graphic BM replacement did not restart frame zero");
/* The optional 8.5 status ids are named in the shared source but their SPR
   records are absent from the selected sa_2903 pack.  A few repository PNGs
   happen to carry those logical numbers and depict unrelated characters.
   Even when actorFrame is able to fabricate a direct bitmap, the persistent
   status renderer must stop at the missing SPR boundary. */
statusFrameActor=null;statusSpriteLookupActor=null;
changedFlagBc.statuses=[{...battleStatusHelpers.battleRosterStatuses(1<<11)[0],startedAt:1999}];
battleStatusHelpers.renderBattleRosterStatuses(statusWrapper,changedFlagBc);
if(Number(statusSpriteLookupActor?.graphic)!==101420||statusFrameActor!==null){
  throw new Error("missing optional status SPR leaked into actorFrame's unrelated direct-bitmap fallback");
}
const selectedAssetManifest=JSON.parse(fs.readFileSync(__dirname+"/assets/original/manifest.json","utf8"));
const selectedSpriteManifest=JSON.parse(fs.readFileSync(__dirname+"/assets/original/sprites.json","utf8")).sprites||{};
for(const graphic of [100550,100551,100552,100553,100554,100555,100556]){
  if(!Array.isArray(selectedSpriteManifest[String(graphic)]?.actions)||!selectedSpriteManifest[String(graphic)].actions.length)throw new Error(`selected 2.5 pack lost native status SPR ${graphic}`);
}
for(const graphic of [101417,101419,101420,101421,101702]){
  const key=String(graphic),hexKey=String(parseInt(key,16));
  if(selectedSpriteManifest[key]||selectedSpriteManifest[hexKey])throw new Error(`optional status unexpectedly gained an unaudited SPR ${graphic}`);
  if(selectedAssetManifest.bitmaps?.[key]||selectedAssetManifest.bitmaps?.[hexKey]||selectedAssetManifest.bitmap_aliases?.[key]||selectedAssetManifest.bitmap_aliases?.[hexKey])throw new Error(`unrelated optional-status bitmap became addressable through manifest ${graphic}`);
}
const battlePersistentStatusMovieSource=script.slice(script.indexOf("      }else if(marker===\"BM\")"),script.indexOf("      }else if(marker===\"B%\"",script.indexOf("      }else if(marker===\"BM\")")));
if(!/marker==="BM"[\s\S]*?battleScheduleDamage\(state,timing\.startAt[\s\S]*?battleApplyRosterStatus\(participant,"main",status,battleStatusClock\(\)\)/.test(battlePersistentStatusMovieSource)||
   !/marker==="BR"[\s\S]*?battleScheduleDamage\(state,startsAt[\s\S]*?battleApplyRosterStatus\(participant,"reverse",on,battleStatusClock\(\)\)/.test(battlePersistentStatusMovieSource)){
  throw new Error("BM/BR movie records are not wired to their independent persistent slots");
}
const receiveMapMusicStart=script.indexOf("  function receiveMap(values) {");
const receiveMapMusicEnd=script.indexOf("  /* The reference 2.5 setup.cf",receiveMapMusicStart);
if(receiveMapMusicStart<0||receiveMapMusicEnd<=receiveMapMusicStart)throw new Error("receiveMap music boundary missing");
const receiveMapMusicSource=script.slice(receiveMapMusicStart,receiveMapMusicEnd);
if(/app\.music\.mapTone\s*=\s*null|app\.music\.mapBgmNo\s*=\s*-1/.test(receiveMapMusicSource)){
  throw new Error("ordinary room/floor M packets must preserve native map_bgm_no until a new marker appears");
}
/* writeAutoMapColor() stores sizeof(autoMapColorTbl), whose MAX_GRAPHICS is
   selected by the executable build.  The preserved runtime carries 250000
   bytes, so the browser must consume the whole payload rather than an old
   fixed-size prefix. */
const autoMapParseStart=script.indexOf("  function parseAutoMapColorTable(");
const autoMapParseEnd=script.indexOf("  function parseAutoMapPalette(",autoMapParseStart);
if(autoMapParseStart<0||autoMapParseEnd<=autoMapParseStart)throw new Error("auto-map colour table parser boundary missing");
const parseAutoMapColorTable=new Function("AUTO_MAP_COLOR_HEADER_SIZE","AUTO_MAP_COLOR_VERSION","AUTO_MAP_COLOR_TABLE_LIMIT",`${script.slice(autoMapParseStart,autoMapParseEnd)};return parseAutoMapColorTable;`)(10,4,1000000);
const nativeAutoMapBuffer=new ArrayBuffer(250010),nativeAutoMapView=new DataView(nativeAutoMapBuffer);nativeAutoMapView.setUint16(0,4,true);new Uint8Array(nativeAutoMapBuffer)[250009]=207;
const nativeAutoMapTable=parseAutoMapColorTable(nativeAutoMapBuffer);
if(nativeAutoMapTable?.length!==250000||nativeAutoMapTable[249999]!==207)throw new Error("auto-map parser truncated the build-sized colour table");
nativeAutoMapView.setUint16(0,3,true);
if(parseAutoMapColorTable(nativeAutoMapBuffer)!==null)throw new Error("auto-map parser accepted a non-v4 table");
/* DrawAutoMapping paints every tile byte, including palette index zero.
   createAutoMap skips zero only for the optional parts overlay. */
const autoMapColorStart=script.indexOf("  function autoMapTableColor(");
const autoMapColorEnd=script.indexOf("  function autoMapBitmapColor(",autoMapColorStart);
if(autoMapColorStart<0||autoMapColorEnd<=autoMapColorStart)throw new Error("auto-map palette helper boundary missing");
const autoMapTableColor=new Function(`${script.slice(autoMapColorStart,autoMapColorEnd)};return autoMapTableColor;`)();
const autoMapTable=Uint8Array.from([0,0,17]),autoMapPalette=Array.from({length:256},()=>[0,0,0]);autoMapPalette[17]=[11,22,33];
if(autoMapTableColor(autoMapTable,autoMapPalette,1,false)!=="rgb(0,0,0)"||
   autoMapTableColor(autoMapTable,autoMapPalette,1,true)!==null||
   autoMapTableColor(autoMapTable,autoMapPalette,2,false)!=="rgb(11,22,33)"||
   autoMapTableColor(autoMapTable,autoMapPalette,999,false)!=="rgb(0,0,0)"||
   autoMapTableColor(autoMapTable,autoMapPalette,999,true)!==null){
  throw new Error("auto-map tile/parts palette-zero semantics drifted from MAP.CPP");
}
if(/autoMapFallbackColor|hsl\(\$\{hue\}/.test(script))throw new Error("auto-map still invents non-native HSL colours");
if(!/Math\.floor\(Date\.now\(\)\/1000\)&1/.test(script)||
   !/setInterval\(\(\)=>\{ const mapWindow=\$\("map-screen"\);[^}]+\},1000\)/.test(script)){
  throw new Error("auto-map player marker must toggle on the native one-second clock");
}
if (/id="field-right-help"/.test(html)) throw new Error("2.5 field HUD must not expose the 8.5 help button");
/* MakeWindowDisp's nine bitmap tiles are visual DirectDraw records, never a
   browser hit target.  `.legacy-screen>*` enables input for the real window
   controls later in the cascade, so the native frame needs an explicit
   child override or it covers WN CANCEL/OK buttons despite looking correct. */
if (!/\.legacy-screen>\.legacy-frame,\s*\.legacy-screen>\.legacy-native-frame\{z-index:0!important;pointer-events:none!important\}/.test(html)) {
  throw new Error("native window frame must not cover NPC/WN response buttons");
}
if (!/#server-window-screen #server-window-options \.server-window-controls\{[^}]*pointer-events:auto/.test(html)) {
  throw new Error("native WN response controls must remain clickable when the choice list is pointer-transparent");
}
/* BATTLEMENU.CPP::BattleTargetSelect() is used by ordinary H as well as
   capture and actor-targeted magic.  Its MakeWindowDisp(210,356,3,2) pixels
   begin at pActInfoWnd->x/y after the REALBIN (-32,-24) anchor is applied;
   both prompt lines are then stocked at +38,+28 and +38,+52. */
const battleTargetRenderStart = script.indexOf("  function renderBattleTargets(){");
const battleTargetRenderEnd = script.indexOf("  async function sendBattleTarget", battleTargetRenderStart);
const battleTargetRenderSource = script.slice(battleTargetRenderStart, battleTargetRenderEnd);
if (battleTargetRenderStart < 0 || battleTargetRenderEnd <= battleTargetRenderStart ||
    !/if\(battleUsesActorTarget\(action\)\)\{[\s\S]*panel\.classList\.add\("actor-target-prompt"\)[\s\S]*battleTargetPromptPlacement\(panel\)[\s\S]*panel\.classList\.remove\("hidden"\)/.test(battleTargetRenderSource) ||
    /if\(action\.kind==="attack"\)[\s\S]*panel\.classList\.add\("hidden"\)/.test(battleTargetRenderSource)) {
  throw new Error("ordinary attack must retain the native BattleTargetSelect prompt window");
}
/* The selected server/client contract is sa_2903 2.5.  Its PC.H has magic
   targets 0..8 and item/pet targets 0..7; 8.5 adds 9..11 only under
   __ATTACK_MAGIC.  Fail closed instead of emitting synthetic row targets
   that the 2.5 server never defined. */
const nativePCHeader=fs.readFileSync(__dirname+"/../../vendor/upstream/code_sa_client/SYSTEMINC/PC.H","latin1");
if(!/MAGIC_TARGET_WHOLEOTHERSIDE[\s\S]{0,120}\};/.test(nativePCHeader)||/__ATTACK_MAGIC/.test(nativePCHeader)){
  throw new Error("unexpected 2.5 PC.H battle target enum");
}
const battleTargetTypeStart=script.indexOf("  function battleTargetTypeAllowed(");
const battleTargetTypeEnd=script.indexOf("  function battleAggregateTarget(",battleTargetTypeStart);
const battleTargetTypeAllowed=new Function(`${script.slice(battleTargetTypeStart,battleTargetTypeEnd)};return battleTargetTypeAllowed;`)();
if(battleTargetTypeStart<0||battleTargetTypeEnd<=battleTargetTypeStart||
   !battleTargetTypeAllowed({kind:"magic",targetType:8})||battleTargetTypeAllowed({kind:"magic",targetType:9})||
   !battleTargetTypeAllowed({kind:"item",targetType:7})||battleTargetTypeAllowed({kind:"item",targetType:8})||
   !battleTargetTypeAllowed({kind:"pet",targetType:7})||battleTargetTypeAllowed({kind:"pet",targetType:8})||
   /battleRowTargetCode|case 9:return|case 10:return|case 11:return/.test(script.slice(battleTargetTypeStart,script.indexOf("  function battleFieldAllowed(",battleTargetTypeStart)))){
  throw new Error("Web battle target types leaked the 8.5 __ATTACK_MAGIC extension into 2.5");
}
for (const expected of [
  /#battle-target-panel\.actor-target-prompt \{[^}]*width:192px;height:96px;min-height:96px;padding:0/,
  /#battle-target-panel\.actor-target-prompt \.battle-wnd2-frame\{left:0;top:0;width:192px;height:96px\}/,
  /#battle-target-panel\.actor-target-prompt > strong\{left:38px;top:28px;width:132px;font:11px\/24px/,
]) {
  if (!expected.test(html)) throw new Error(`native battle target prompt regression: ${expected}`);
}
/* BattleTargetSelect() uses a one-time y<228 choice, then the native
   hysteresis thresholds y>300 / y<156. Exercise the placement helper so a
   CSS-only regression cannot silently move the hint over the battle actors. */
const targetPromptStart=script.indexOf("  function battleTargetPromptPlacement(panel){");
const targetPromptEnd=script.indexOf("  function renderBattleTargets(){",targetPromptStart);
if(targetPromptStart<0||targetPromptEnd<=targetPromptStart)throw new Error("battle target prompt placement helper missing");
const makeBattleTargetPromptPlacement=new Function("battleCursorY",`const app={battleCursorY};${script.slice(targetPromptStart,targetPromptEnd)};return battleTargetPromptPlacement;`);
function targetPromptPanel(cursorY,placement=""){
  const classes=new Set();
  return {dataset:{promptPlacement:placement},classList:{toggle(name,force){if(force===undefined? !classes.has(name):force)classes.add(name);else classes.delete(name);}},_classes:classes,cursorY};
}
for(const [cursorY,expected] of [[100,"bottom"],[227,"bottom"],[228,"top"],[479,"top"]]){
  const panel=targetPromptPanel(cursorY);makeBattleTargetPromptPlacement(cursorY)(panel);
  if(panel.dataset.promptPlacement!==expected||!panel._classes.has(`target-prompt-${expected}`)){
    throw new Error(`native target prompt initial placement drifted at y=${cursorY}: ${JSON.stringify({placement:panel.dataset.promptPlacement,classes:[...panel._classes]})}`);
  }
}
for(const [startY,followY,expected] of [["bottom",301,"top"],["top",155,"bottom"],["top",200,"top"],["bottom",300,"bottom"]]){
  const panel=targetPromptPanel(followY,startY);
  makeBattleTargetPromptPlacement(followY)(panel);
  if(panel.dataset.promptPlacement!==expected)throw new Error(`native target prompt hysteresis drifted: ${startY} -> ${followY} = ${panel.dataset.promptPlacement}`);
}
/* InitBattleMenu/BattleButtonAttack keep the master's button memory
   independent from the active pet's later W menu.  A real H -> W turn must
   therefore reopen Attack on the next BP, and the opening turn starts with
   that same native button-0 default. */
if (!script.includes('rosterBeforeBp:false,lastPlayerActionKind:"attack",lastPlayerActionCommand:"",lastPetActionKind:"",lastPetActionCommand:"",defaultAttackTurnKey:null')) {
  throw new Error("battle opening menu must default to the native Attack button");
}
/* The field-visible PME/C pet cache is not the player's owned-pet roster.
   Rendering app.pets can repeat one pet across all five menu rows; the native
   menu instead walks each occupied CHAR_getCharPet slot exactly once. */
const occupiedPetsStart = script.indexOf("  function occupiedPetEntries()");
const renderPetsStart = script.indexOf("  function renderPets() {");
const renderPetsEnd = script.indexOf("  function renderStatus()", renderPetsStart);
const renderPetsSource = script.slice(occupiedPetsStart, renderPetsEnd);
if (occupiedPetsStart < 0 || renderPetsStart <= occupiedPetsStart || renderPetsEnd <= renderPetsStart ||
    !/function occupiedPetEntries\(\)\{return app\.petSlots\.map\(\(pet,index\)=>\(\{pet,index\}\)\)\.filter\(entry=>Boolean\(entry\.pet\)\);\}/.test(renderPetsSource) ||
    !/row\.style\.top=`\$\{rowIndex\*51\}px`/.test(renderPetsSource) ||
    /app\.pets\.length\?app\.pets/.test(renderPetsSource)) {
  throw new Error("pet menu must render each occupied owned-pet slot exactly once");
}
for (const expected of [
  /#pets-screen \.legacy-pet-row\{[^}]*height:51px/,
  /#pets-screen \.legacy-pet-row \.pet-name\{[^}]*left:73px;top:35px/,
  /#pets-screen \.legacy-pet-row \.pet-stat\{[^}]*top:59px/,
  /#pets-screen \.legacy-pet-row \.pet-level\{left:123px;width:24px\}/,
  /#pets-screen \.legacy-pet-row \.pet-hp\{left:171px;width:32px\}/,
  /#pets-screen \.legacy-pet-row \.pet-max-hp\{left:211px;width:32px\}/,
  /#pets-screen \.legacy-pet-row \.pet-button\{[^}]*left:15px;top:33px/,
  /#pets-screen #pet-status-open\{[^}]*left:44px;top:295px[^}]*bitmap_9176\.png/,
  /#pets-screen\.pet-list-populated #pets-close\{left:156px\}/,
  /surface=\{list:\{bitmap:9164,height:320\},detail:\{bitmap:9165,height:332\},skills:\{bitmap:9238,height:348\}\}/,
  /const skills=imageButton\("查看宠物技能",9166,[\s\S]{0,180}pet-detail-skills/,
  /#pets-screen \.pet-skill-capacity\{[^}]*width:auto;height:auto;max-width:none;max-height:none/,
  /const barOrigins=\[\[17,61\],\[18,85\],\[18,110\],\[17,135\],\[18,160\],\[17,185\],\[15,210\]\]/,
  /bar\.style\.left=`\$\{barOrigins\[i\]\[0\]\}px`;bar\.style\.top=`\$\{barOrigins\[i\]\[1\]\}px`/,
  /#pets-screen \.pet-skill-row\{[^}]*padding:0 0 0 56px/,
]) {
  if (!expected.test(html)) throw new Error(`native pet-window layout regression: ${expected}`);
}
/* MakeAnimDisp(..., ANIM_DISP_PET) starts at anim_ang=1 and runs the full
   standing row through pattern(..., ANM_LOOP).  It also increments anim_ang
   when the preview is clicked; direction 0 plus frames[0] is a frozen back
   view, not the native pet detail actor. */
for (const expected of [
  /if\(page==="detail"&&app\.petMenuPage!=="detail"\)app\.petDetailDirection=1/,
  /const direction=\(\(Number\(app\.petDetailDirection\?\?1\)%8\)\+8\)%8,actor=\{graphic:pet\.graphic\?\?pet\.graNo,direction,action:3\}/,
  /nativeAnimation=spriteAnimationForAction\(actor,direction,3\),animation=nativeAnimation\|\|entry\?\.sprite\?\.actions\?\.\[0\]/,
  /if\(!nativeAnimation&&!assetState\.spritesReady\)loadSpriteManifest\(\)\.then\(sprites=>\{if\(sprites&&app\.petMenuPage==="detail"\)renderPets\(\);\}\)/,
  /frameDuration=Math\.max\(1,Number\(animation\.frame_ms\)\|\|7\)\*LEGACY_FIELD_ANIMATION_TICK_MS/,
  /paint\(frames\[Math\.floor\(Math\.max\(0,now-started\)\/frameDuration\)%frames\.length\]\)/,
  /app\.petDetailDirection=\(direction\+1\)%8;playSoundEffect\(217\);renderPets\(\)/,
]) {
  if (!expected.test(renderPetsSource)) throw new Error(`native animated pet preview regression: ${expected}`);
}
/* MENU.CPP uses separate 8px-cell numeric fields. A single proportional
   padded string makes the HP columns drift because browser spaces are not
   eight pixels wide; the status %4d fields likewise end after 32px. */
if (!/for\(const \[field,value\] of \[\["level",pet\.level\],\["hp",pet\.hp\],\["max-hp",pet\.maxHp\]\]\)/.test(renderPetsSource) ||
    !/#status-screen \.status-readout \.hp\{left:72px;top:137px;width:32px;text-align:right/.test(html) ||
    !/#status-screen \.status-readout \.max-hp\{left:122px;top:137px;width:32px;text-align:right/.test(html) ||
    !/#status-screen \.status-readout \.level\{left:74px;top:74px;width:24px;text-align:right/.test(html) ||
    !/#status-screen \.status-readout \.exp\{left:81px;top:95px;width:56px;text-align:right/.test(html) ||
    !/#status-screen \.status-readout \.mp\{left:74px;top:158px;width:24px;text-align:right/.test(html) ||
    !/#status-screen \.status-readout \.base-vital\{left:85px;top:292px;width:24px;text-align:right/.test(html) ||
    !/#pets-screen \.pet-detail-level\{left:74px;top:86px;width:24px;text-align:right/.test(html) ||
    !/#pets-screen \.pet-detail-exp\{left:81px;top:110px;width:56px;text-align:right/.test(html) ||
    !/#pets-screen \.pet-detail-hp\{left:70px;top:158px;width:32px;text-align:right/.test(html) ||
    !/#pets-screen \.pet-detail-max-hp\{left:117px;top:158px;width:32px;text-align:right/.test(html) ||
    !/#pets-screen \.pet-detail-atk\{left:74px;top:182px;width:24px;text-align:right/.test(html)) {
  throw new Error("pet/status current and maximum HP fields must keep native fixed columns");
}
/* MENU.CPP::statusWndNo==0 submits four independent SKUP hit sprites only
   while StatusUpPoint is non-zero.  The title/group/close CG records all use
   different anchors, so a flex toolbar or a web-only attribute chooser can
   never reproduce the native status surface. */
const renderStatusStart = script.indexOf("  function renderStatus() {");
const renderStatusEnd = script.indexOf("  function renderParty()", renderStatusStart);
const renderStatusSource = script.slice(renderStatusStart, renderStatusEnd);
for (const expected of [
  /id="status-up"[^>]*data-status-point="0"/,
  /id="status-up-str"[^>]*data-status-point="1"/,
  /id="status-up-tgh"[^>]*data-status-point="2"/,
  /id="status-up-dex"[^>]*data-status-point="3"/,
  /#status-screen #status-up\{left:114px;top:293px\}/,
  /#status-screen #status-up-str\{left:234px;top:293px\}/,
  /#status-screen #status-up-tgh\{left:114px;top:313px\}/,
  /#status-screen #status-up-dex\{left:234px;top:313px\}/,
  /#status-screen #status-title\{left:139px;top:54px;width:68px;height:16px/,
  /#status-screen #status-party\{left:43px;top:341px;width:80px;height:16px[^}]*bitmap_9199\.png/,
  /#status-screen #status-close\{left:157px;top:341px;width:80px;height:16px[^}]*bitmap_9134\.png/,
  /#status-screen #status-level-up-art\{[^}]*left:69px;top:272px;width:112px;height:17px/,
  /#status-screen #status-level-up-points\{[^}]*left:190px;top:272px;width:16px/,
]) {
  if (!expected.test(html)) throw new Error(`native status-window layout regression: ${expected}`);
}
if (renderStatusStart < 0 || renderStatusEnd <= renderStatusStart ||
    !/showSkillUp=skillPoints>0/.test(renderStatusSource) ||
    !/\["status-level-up-art","status-level-up-points","status-up","status-up-str","status-up-tgh","status-up-dex"\]/.test(renderStatusSource) ||
    !/classList\.toggle\("hidden",!showSkillUp\)/.test(renderStatusSource) ||
    !/String\(skillPoints\)\.padStart\(2," "\)/.test(renderStatusSource) ||
    /openLocalDialog\("技能点"/.test(script) ||
    !/const requestStatusSkillUp=point=>\{[\s\S]{0,360}send\("SKUP",\[point\]\)/.test(script) ||
    !/querySelectorAll\("\[data-status-point\]"\)[\s\S]{0,180}requestStatusSkillUp/.test(script) ||
    !/id="status-title-dialog"[^>]*class="hidden"/.test(html) ||
    !/send\("FT",\[value\]\)/.test(script)) {
  throw new Error("status skill/title controls must follow the native inline interaction");
}
/* statusWndNo==1 always starts with the player, compresses occupied pet
   slots into the upper block, then draws at most four non-self party members
   from y=268.  Current/max HP remain separate four-cell fields. */
const partyFixedStart = script.indexOf("  function renderPartyFixed(){");
const partyFixedEnd = script.indexOf("  function parseAutoMapData", partyFixedStart);
const partyFixedSource = script.slice(partyFixedStart, partyFixedEnd);
if (partyFixedStart < 0 || partyFixedEnd <= partyFixedStart ||
    !/appendRow\(\{name:pc\.name\|\|app\.character\|\|"人物",mp:pc\.mp,hp:pc\.hp,maxHp:pc\.maxHp\},25,\{mp:true\}\)/.test(partyFixedSource) ||
    !/app\.petSlots\.filter\(Boolean\)\.slice\(0,5\)/.test(partyFixedSource) ||
    !/64\+index\*40/.test(partyFixedSource) ||
    !/for\(const member of app\.party\.filter\(Boolean\)\)/.test(partyFixedSource) ||
    !/Number\(member\.id\)===ownId/.test(partyFixedSource) ||
    !/if\(members\.length===4\)break/.test(partyFixedSource) ||
    !/268\+index\*40/.test(partyFixedSource) ||
    !/maxHp\.className="party-max-hp"/.test(partyFixedSource)) {
  throw new Error("group status window must render self, pets, then four other party members");
}
for (const expected of [
  /#party-screen \.legacy-party-row \.party-name\{[^}]*left:21px;top:0;width:128px[^}]*text-align:center/,
  /#party-screen \.legacy-party-row \.party-mp\{left:98px\}/,
  /#party-screen \.legacy-party-row \.party-hp\{left:163px\}/,
  /#party-screen \.legacy-party-row \.party-max-hp\{left:203px\}/,
  /#party-screen #party-close\{left:92px;top:433px\}/,
]) {
  if (!expected.test(html)) throw new Error(`native group-window layout regression: ${expected}`);
}
/* IME.CPP::ImeProc() writes the closed/default input mode as
   "       abc" from x=545, so its visible suffix begins at x=601.  The
   legacy client has no web-only "player mode" or ping counter in this bar. */
if (!/#battle-taskbar-ime\s*\{\s*left:601px/.test(html) ||
    !/<span id="battle-taskbar-ime">abc<\/span>/.test(html) ||
    /<span id="battle-taskbar-(?:mode|ping)"|(?:mode|ping)\.textContent|>玩家模式</.test(html)) {
  throw new Error("battle task bar must keep the native 2.5 IME marker only");
}
const sendBattleTargetStart = script.indexOf("  async function sendBattleTarget(target)");
const sendBattleTargetEnd = script.indexOf("  function battleActivePet(", sendBattleTargetStart);
const sendBattleTargetSource = script.slice(sendBattleTargetStart, sendBattleTargetEnd);
const battleSetCommandLockStart = script.indexOf("  function battleSetCommandLock(state,kind,locked)");
const battleSetCommandLockEnd = script.indexOf("  function battleResetCommandLocks", battleSetCommandLockStart);
if (sendBattleTargetStart < 0 || sendBattleTargetEnd <= sendBattleTargetStart ||
    battleSetCommandLockStart < 0 || battleSetCommandLockEnd <= battleSetCommandLockStart ||
    (sendBattleTargetSource.match(/lastPlayerActionKind/g) || []).length !== 1 ||
    !/if\(kind==="player"\)\{[\s\S]{0,320}lastPlayerActionKind[\s\S]{0,220}\}else\{[\s\S]{0,220}lastPetActionKind/.test(sendBattleTargetSource)) {
  throw new Error("pet actor-target W must not overwrite the player's remembered command");
}
/* BattleTargetSelect() emits the row/target tone (217) for J/I/W and then
   the shared command-confirm tone (203).  H/T have only the shared tone;
   TARGET_NONE I uses only 203 because no actor target was selected. */
const makeBattleTargetToneHarness=new Function("action",`
  const owner=action.kind==="pet"?"pet":"player";
  const state={pendingAction:{...action},myNo:0,turnKey:3,commandLocked:false,petCommandLocked:false,commandPending:{player:null,pet:null},choiceDeadline:0,choiceOwner:owner,menuMotion:{owner,phase:"shown",buttonX:515,buttonA:0}};
  const app={battle:true,battleState:state,battleCommands:[],battleTarget:-1,battlePopup:null,character:"Tester"};
  const tones=[],sends=[],menuLeaves=[];
  const window={setTimeout(){return 1;},clearTimeout(){}};
  function playSoundEffect(tone,x,y){tones.push([tone,x,y]);}
  function battleActionAllowed(){return true;}
  function rejectFullBattleCapture(){return false;}
  function rejectUnavailableBattleMagic(){return false;}
  function battleTargetSelectable(){return true;}
  function battleActionCommand(current,target){
    const head={attack:"H",capture:"T",magic:"J",item:"I",pet:"W"}[current.kind];
    return current.kind==="attack"||current.kind==="capture"?head+"|A":head+"|0|A";
  }
  function battleButtonCommandForAction(current){return current?.kind==="attack"?"H|A":current?.kind==="capture"?"C|A":"";}
  function setBattleMenuPressedCommand(current,command){current.menuMotion.pressedCommand=String(command||"").toUpperCase();return current.menuMotion.pressedCommand;}
  function leaveBattleMenuMotion(current,menuOwner){menuLeaves.push(menuOwner);if(current.menuMotion?.owner===menuOwner)current.menuMotion.phase="leaving";return true;}
  function battleChoiceDeadline(current){return Number(current.choiceDeadline)||0;}
  function clearBattlePlayerChoiceTimer(){}
  function clearBattlePetChoiceTimer(){}
  function battlePetChoiceTimeout(){}
  function battlePlayerTimeoutDefaults(){}
  function battleEntryPending(){return false;}
  function openBattlePlayerMenuMotion(){}
  function battleStartCommandPending(current,kind,command){current.commandPending[kind]=command;}
  function renderBattleWorld(){}
  function renderBattle(){}
  function send(name,values){sends.push([name,values]);return new Promise(()=>{});}
  function reportError(error){throw error;}
  ${script.slice(battleSetCommandLockStart,battleSetCommandLockEnd)}
  ${sendBattleTargetSource}
  sendBattleTarget({battleId:10,name:"Target"});
  return {tones,sends,menuLeaves,menuPhase:state.menuMotion.phase,pressedCommand:state.menuMotion.pressedCommand||""};
`);
for(const [action,expected,pressedCommand] of [
  [{kind:"attack",targetType:1},[203],"H|A"],
  [{kind:"capture",targetType:1},[203],"C|A"],
  [{kind:"magic",targetType:1,index:0},[217,203],""],
  [{kind:"item",targetType:1,index:5},[217,203],""],
  [{kind:"pet",targetType:1,index:0},[217,203],""],
  [{kind:"item",targetType:5,index:5},[203],""],
]){
  const result=makeBattleTargetToneHarness(action),actual=result.tones.map(item=>item[0]);
  const expectedOwner=action.kind==="pet"?"pet":"player";
  if(result.sends.length!==1||actual.join(",")!==expected.join(",")||result.menuLeaves.join(",")!==expectedOwner||result.menuPhase!=="leaving"||result.pressedCommand!==pressedCommand){
    throw new Error(`battle target confirmation tone order drifted for ${action.kind}/${action.targetType}: ${JSON.stringify(result)}`);
  }
}
/* Exercise the complete pet ordinary-attack interaction instead of only
   matching its source text: the W0 row closes the native popup, target type
   6 exposes the master and enemy (but not the casting pet), the same
   selectable predicate owns both transparent actor proxies, and clicking
   the enemy writes the original 2.5 hexadecimal command W|0|A. */
const battleTargetCoreStart=script.indexOf("  function battleTargetTypeAllowed(");
const battleTargetCoreEnd=script.indexOf("  function battleTargetHighlightIds(",battleTargetCoreStart);
const battleActionCommandStart=script.indexOf("  function battleActionCommand(");
const battleActionCommandEnd=script.indexOf("  function battlePetSwitchEntry(",battleActionCommandStart);
const battlePopupRowStart=script.indexOf("  function battlePopupRow(");
const battlePopupRowEnd=script.indexOf("  function battleTargetPromptPlacement(",battlePopupRowStart);
const beginBattleActionStart=script.indexOf("  function beginBattleAction(action,options={})");
const beginBattleActionEnd=script.indexOf("  function battleNoHelpType(",beginBattleActionStart);
if(battleTargetCoreStart<0||battleTargetCoreEnd<=battleTargetCoreStart||battleActionCommandStart<0||battleActionCommandEnd<=battleActionCommandStart||
   battlePopupRowStart<0||battlePopupRowEnd<=battlePopupRowStart||beginBattleActionStart<0||beginBattleActionEnd<=beginBattleActionStart){
  throw new Error("pet ordinary-attack production function boundary missing");
}
const petAttackTargetHarness=new Function("targetCore","actionCommand","popupSource","beginSource","sendSource",`
  class FakeClassList{
    constructor(){this.values=new Set();}
    add(...values){for(const value of values)this.values.add(value);}
    remove(...values){for(const value of values)this.values.delete(value);}
    toggle(value,force){const next=force===undefined?!this.values.has(value):Boolean(force);if(next)this.values.add(value);else this.values.delete(value);return next;}
    contains(value){return this.values.has(value);}
  }
  class FakeNode{
    constructor(tag,id=""){this.tagName=String(tag).toUpperCase();this.id=id;this.children=[];this.dataset={};this.style={};this.attributes={};this.listeners={};this.classList=new FakeClassList();this.textContent="";this.disabled=false;}
    append(...nodes){this.children.push(...nodes);}
    replaceChildren(...nodes){this.children=[...nodes];}
    addEventListener(type,handler){(this.listeners[type]||(this.listeners[type]=[])).push(handler);}
    dispatch(type){const event={type,preventDefault(){},stopPropagation(){}};for(const handler of this.listeners[type]||[])handler(event);}
    setAttribute(name,value){this.attributes[name]=String(value);}
    removeAttribute(name){delete this.attributes[name];}
  }
  const nodes={
    "battle-popup":new FakeNode("div","battle-popup"),"battle-popup-bg":new FakeNode("img","battle-popup-bg"),
    "battle-popup-title":new FakeNode("img","battle-popup-title"),"battle-popup-list":new FakeNode("div","battle-popup-list"),
    "battle-target-panel":new FakeNode("div","battle-target-panel"),"battle-target-list":new FakeNode("div","battle-target-list"),
    "battle-log":new FakeNode("div","battle-log")
  };
  const document={createElement(tag){return new FakeNode(tag);}},$=id=>nodes[id]||null;
  const state={myNo:0,bpFlags:0,movieActive:false,commandLocked:true,petCommandLocked:false,pendingAction:null,
    participants:[{battleId:0,name:"Master",hp:100,maxHp:100},{battleId:5,name:"Pet",hp:80,maxHp:80},{battleId:10,name:"Enemy",hp:90,maxHp:90}],
    commandPending:{player:null,pet:null},choiceDeadline:987654321,choiceOwner:"pet",lastPlayerActionKind:"attack",lastPlayerActionCommand:"H|A"};
  const app={battle:true,battlePopup:{kind:"pet-skill"},battleState:state,battleCommands:[],battleTarget:-1,character:"Master",selectedPet:0,
    petSlots:[{name:"Pet",hp:80,maxHp:80}],petSkills:[[{index:0,skillId:1,field:1,target:6,name:"攻击",memo:"普通攻击"}]],magic:[]};
  const sends=[],tones=[];let targetProxies=[];
  const BATTLE_BP_BOOMERANG=1,BATTLE_BC_DEATH=1<<7;
  function battleSide(id){const value=Number(id);return value>=0&&value<10?0:value>=10&&value<20?1:-1;}
  function battleIsEnemy(item){return battleSide(item?.battleId)!==battleSide(state.myNo);}
  function battleAlive(item,allowDead=false){return Boolean(item)&&(allowDead||(!item.dead&&Number(item.hp)>0));}
  function battleParticipant(id){return state.participants.find(item=>Number(item.battleId)===Number(id))||null;}
  function battleSpecialTarget(id,name,title=""){return {battleId:id,name,title,special:true};}
  function battleHex(value){return Math.max(0,Math.trunc(Number(value)||0)).toString(16).toUpperCase();}
  eval(targetCore);eval(actionCommand);
  function battleFieldAllowed(entry){return Number(entry?.field)!==2;}
  function battleLevelAllowed(){return true;}
  function battleUsablePetSkill(entry){return Boolean(entry?.name||entry?.skillId!==undefined)&&battleFieldAllowed(entry);}
  function battleActivePetSlot(){return 0;}
  function resolveBitmapInfo(){return null;}
  function inventoryItem(){return null;}function inventoryTextLines(){return [];}function battleUsableMagic(){return false;}
  function battleItemPopupRow(){return new FakeNode("button");}function battlePetSwitchEntry(){return {listed:false};}
  function battleSwitchPet(){}function sendBattlePetDefault(){}function syncBattleButtonVisualStates(){}
  function closeBattlePopup(){app.battlePopup=null;nodes["battle-popup"].classList.add("hidden");}
  function battleActionAllowed(action){return Boolean(app.battle&&!state.movieActive&&(action?.kind==="pet"?!state.petCommandLocked:!state.commandLocked));}
  function rejectFullBattleCapture(){return false;}function rejectUnavailableBattleMagic(){return false;}function battleExplainUnavailable(){throw new Error("pet attack unexpectedly unavailable");}
  function battleButtonCommandForAction(){return "";}function battleUsesActorTarget(action){return action?.kind==="attack"||action?.kind==="capture"||(battleTargetTypeAllowed(action)&&Number(action?.targetType)!==5);}
  function setBattleMenuPressedCommand(current,command){current.pressedCommand=String(command||"").toUpperCase();return current.pressedCommand;}
  function renderBattleTargets(){}function renderBattle(){}
  function renderBattleWorld(){
    const action=state.pendingAction&&battleActionAllowed(state.pendingAction)?state.pendingAction:null;
    targetProxies=action?state.participants.filter(item=>battleTargetSelectable(action,item,state)).map(item=>{
      const proxy=new FakeNode("button");proxy.dataset.battleTarget=String(item.battleId);proxy.setAttribute("aria-label","选择目标 "+item.name);proxy.addEventListener("click",event=>{event.preventDefault();event.stopPropagation();sendBattleTarget(item);});return proxy;
    }):[];
  }
  function battleSetCommandLock(current,kind,locked){if(kind==="pet")current.petCommandLocked=Boolean(locked);else current.commandLocked=Boolean(locked);}
  function battleStartCommandPending(current,kind,command){current.commandPending[kind]=command;}
  function playSoundEffect(...values){tones.push(values);}
  function send(name,values){sends.push([name,[...values]]);return new Promise(()=>{});}
  function reportError(error){throw error;}
  eval(popupSource);eval(beginSource);eval(sendSource);
  renderBattlePopup();
  const popupRows=[...nodes["battle-popup-list"].children];
  popupRows[0]?.dispatch("click");
  const proxyIds=targetProxies.map(node=>Number(node.dataset.battleTarget));
  const enemyProxy=targetProxies.find(node=>Number(node.dataset.battleTarget)===10);
  enemyProxy?.dispatch("click");
  return {app,state,sends,tones,popupRows,proxyIds};
`)(
  script.slice(battleTargetCoreStart,battleTargetCoreEnd),script.slice(battleActionCommandStart,battleActionCommandEnd),
  script.slice(battlePopupRowStart,battlePopupRowEnd),script.slice(beginBattleActionStart,beginBattleActionEnd),sendBattleTargetSource
);
if(petAttackTargetHarness.popupRows[0]?.title!=="攻击"||petAttackTargetHarness.proxyIds.join(",")!=="0,10"||
   petAttackTargetHarness.sends.length!==1||petAttackTargetHarness.sends[0][0]!=="B"||petAttackTargetHarness.sends[0][1][0]!=="W|0|A"||
   !petAttackTargetHarness.state.petCommandLocked||!petAttackTargetHarness.state.commandLocked||
   petAttackTargetHarness.state.choiceDeadline!==987654321||petAttackTargetHarness.state.lastPlayerActionCommand!=="H|A"){
  throw new Error(`pet ordinary attack popup/actor-target/W path drifted: ${JSON.stringify({rows:petAttackTargetHarness.popupRows.map(row=>row.title),proxyIds:petAttackTargetHarness.proxyIds,sends:petAttackTargetHarness.sends,state:petAttackTargetHarness.state})}`);
}
/* The pre-bitmap Web prototype still has legacy raw command controls because
   a few harnesses use their bindings.  They must never become a second user
   surface: off-screen CSS alone leaves duplicate Attack/Guard/Escape buttons
   in Chromium's accessibility tree and can make QA submit E accidentally. */
if(!/<div id="battle-actions" hidden inert aria-hidden="true">/.test(html)||
   !/<form id="battle-command-form" class="row" hidden inert aria-hidden="true">/.test(script)||
   !/<div id="battle-menu" class="card-grid" hidden inert aria-hidden="true">/.test(script)||
   !/#battle-actions\[hidden\],#battle-command-form\[hidden\],#battle-menu\[hidden\] \{ display:none !important; \}/.test(html)){
  throw new Error("legacy raw battle controls must remain hidden, inert, and outside the accessibility tree");
}
const rawBattleCommandStart = script.indexOf("  async function battleCommand(command)");
const rawBattleCommandEnd = script.indexOf("  function parseFormBytes", rawBattleCommandStart);
const rawBattleCommandSource = script.slice(rawBattleCommandStart, rawBattleCommandEnd);
if (rawBattleCommandStart < 0 || rawBattleCommandEnd <= rawBattleCommandStart ||
    (rawBattleCommandSource.match(/lastPlayerActionKind/g) || []).length !== 1 ||
    !/if\(kind==="player"\)\{[\s\S]{0,480}lastPlayerActionKind[\s\S]{0,220}\}else\{[\s\S]{0,180}lastPetActionKind/.test(rawBattleCommandSource)) {
  throw new Error("raw pet W must keep the player's battleButtonBak memory");
}
/* BattleButtonOff clears all old flags before one target/submenu owner is
   activated. commandPending is only the network/watchdog latch; the much
   shorter menuMotion.pressedCommand latch mirrors battleButtonFlag until
   the original buttonX/buttonA return trajectory has actually completed. */
const pressedStart = script.indexOf("  function battleButtonPressed(command,state=app.battleState)");
const pressedEnd = script.indexOf("  function syncBattleButtonVisualStates", pressedStart);
const pressedSource = script.slice(pressedStart, pressedEnd);
if (pressedStart < 0 || pressedEnd <= pressedStart ||
    !/returningCommand=motion\?\.owner==="player"&&motion\.phase==="leaving"/.test(pressedSource) ||
    !/if\(returningCommand\)return returningCommand===wanted/.test(pressedSource) ||
    !/if\(pendingCommand\)return pendingCommand\.toUpperCase\(\)===wanted/.test(pressedSource) ||
    !/if\(popupCommand\)return popupCommand\.toUpperCase\(\)===wanted/.test(pressedSource) ||
    /state\.commandPending|pendingPlayer|pendingPet/.test(pressedSource)) {
  throw new Error("battle pressed flags must have one native menu owner");
}
const pressedHelpersStart=script.indexOf("  function battleButtonCommandForAction(action)");
const makeBattlePressedHarness=new Function("pendingAction","popupKind","commandPending","menuMotion",`
  const state={pendingAction,commandPending,menuMotion:menuMotion||{owner:"",phase:"hidden",pressedCommand:""}};
  const app={battle:true,battleState:state,battlePopup:popupKind?{kind:popupKind}:null};
  const battleMenuMotionShape=current=>current.menuMotion;
  ${script.slice(pressedHelpersStart,pressedEnd)}
  return command=>battleButtonPressed(command,state);
`);
const selectorPressed=makeBattlePressedHarness({kind:"attack"},null,{player:"I|5|0",pet:"W|0|A"});
if(!selectorPressed("H|A")||selectorPressed("ITEM")||selectorPressed("PET")){
  throw new Error("active actor selector must be the sole DOWN battle button");
}
const rowTargetPressed=makeBattlePressedHarness({kind:"magic",index:0,targetType:1},null,{player:"J|0|A",pet:null});
if(["H|A","J|A","ITEM","PET"].some(rowTargetPressed)){
  throw new Error("Jujutsu/Item/Pet row targeting must follow native ClearBattleButton");
}
const popupPressed=makeBattlePressedHarness(null,"item",{player:"H|A",pet:"W|0|A"});
if(!popupPressed("ITEM")||popupPressed("H|A")||popupPressed("PET")){
  throw new Error("active battle popup must be the sole DOWN battle button");
}
const wireOnlyPressed=makeBattlePressedHarness(null,null,{player:"H|A",pet:"W|0|A"});
if(["H|A","J|A","ITEM","PET","G","E"].some(wireOnlyPressed)){
  throw new Error("in-flight battle command must not drive DOWN artwork");
}
for(const [pressedCommand,expected] of [["H|A","H|A"],["C|A","C|A"],["G","G"],["PET","PET"],["E","E"]]){
  const returningPressed=makeBattlePressedHarness(null,null,{player:pressedCommand,pet:null},{owner:"player",phase:"leaving",pressedCommand});
  for(const command of ["H|A","J|A","C|A","HELP","G","ITEM","PET","E"]){
    if(returningPressed(command)!==(command===expected))throw new Error(`returning player menu lost native DOWN flag ${pressedCommand}/${command}`);
  }
}
const completedReturnPressed=makeBattlePressedHarness(null,null,{player:"H|A",pet:null},{owner:"",phase:"hidden",pressedCommand:"H|A"});
if(["H|A","G","PET","E"].some(completedReturnPressed))throw new Error("ClearBattleButton must release return artwork after the player menu is hidden");
/* BattleMenuProc paints *_UP + battleButtonFlag; HitDispNo hover is never a
   second source of DOWN artwork.  A cancelled button must visibly release
   under the stationary pointer, not only clear aria-pressed/target boxes. */
const syncPressedStart=script.indexOf("  function syncBattleButtonVisualStates(state=app.battleState)");
const syncPressedEnd=script.indexOf("  function battlePendingShape",syncPressedStart);
const bindPressedStart=script.indexOf("  function bindBattleButtonStates()");
const bindPressedEnd=script.indexOf("  function activateBattlePetCommandButton",bindPressedStart);
const syncPressedSource=script.slice(syncPressedStart,syncPressedEnd),bindPressedSource=script.slice(bindPressedStart,bindPressedEnd);
if(syncPressedStart<0||syncPressedEnd<=syncPressedStart||bindPressedStart<0||bindPressedEnd<=bindPressedStart||
   !/image\.setAttribute\("src",active\?down:normal\)/.test(syncPressedSource)||
   /matches\(":hover"\)|matches\(":focus"\)|active\|\|hovered/.test(syncPressedSource)||
   /setHover\(true\)|\(active\|\|pressed\)\?down:normal/.test(bindPressedSource)||
   !/battleButtonPressed\(node\.dataset\.command\)\?down:normal/.test(bindPressedSource)){
  throw new Error("battle UP/DOWN artwork must follow only the native battleButtonFlag");
}
/* BattleMenuProc uses one discrete buttonX/buttonA pair for both command
   surfaces.  Prove the exact 60 Hz integer trajectory rather than accepting
   a visually similar CSS easing, and keep the player -> pet hand-off wired
   to the completed return path. */
const menuAdvanceStart = script.indexOf("  function battleAdvanceMenuMotion(state)");
const menuAdvanceEnd = script.indexOf("  function battleMenuMotionFinished", menuAdvanceStart);
if (menuAdvanceStart < 0 || menuAdvanceEnd <= menuAdvanceStart) throw new Error("battle menu motion helper boundary missing");
const menuAdvance = new Function(`
  const BATTLE_MENU_START_X=815,BATTLE_MENU_START_ACCELERATION=25;
  const battleMenuMotionShape=state=>state.menuMotion;
  ${script.slice(menuAdvanceStart, menuAdvanceEnd)}
  return battleAdvanceMenuMotion;
`)();
const enteringMenu={menuMotion:{owner:"player",phase:"entering",buttonX:815,buttonA:25}};
const enteringFrames=[];
for(let frame=0;frame<25;frame++){menuAdvance(enteringMenu);enteringFrames.push([enteringMenu.menuMotion.buttonX,enteringMenu.menuMotion.buttonA]);}
if (enteringFrames[0].join(",")!=="791,24" || enteringFrames[23].join(",")!=="515,1" || enteringFrames[24].join(",")!=="515,0" || enteringMenu.menuMotion.phase!=="shown") {
  throw new Error(`battle player-menu enter trajectory drifted: ${JSON.stringify(enteringFrames)}`);
}
enteringMenu.menuMotion.phase="leaving";
const leavingFrames=[];let finishedOwner="";
for(let frame=0;frame<27;frame++){finishedOwner=menuAdvance(enteringMenu)||finishedOwner;leavingFrames.push([enteringMenu.menuMotion.buttonX,enteringMenu.menuMotion.buttonA]);}
if (leavingFrames[0].join(",")!=="516,1" || leavingFrames[23].join(",")!=="815,24" || leavingFrames[25].join(",")!=="866,26" || finishedOwner!=="player" || enteringMenu.menuMotion.phase!=="hidden") {
  throw new Error(`battle player-menu return trajectory drifted: ${JSON.stringify(leavingFrames)}`);
}
const menuLifecycleSource=script.slice(script.indexOf("  const BATTLE_MENU_FRAME_MS"),script.indexOf("  const BATTLE_COUNTDOWN_LOGICAL_BASE"));
for(const expected of [
  /const BATTLE_MENU_FRAME_MS=1000\/60/,
  /fallbackTimer/,
  /const translation=`translate3d\(\$\{offset\}px,0,0\)`;\s*player\.style\.transform=translation;pet\.style\.transform=translation/,
  /motion\.phase==="shown"[\s\S]{0,260}motion\.buttonX=BATTLE_MENU_ANCHOR_X/,
  /const watchdog=\(\)=>\{[\s\S]{0,520}stalled=[^;]+now-Number\(motion\.lastAt/,
  /startBattleMenuMotion[\s\S]{0,1200}battleAdvanceMenuMotion\(state\);[\s\S]{0,180}scheduleBattleMenuMotion\(state\)/,
  /battleMenuMotionFinished\(state,owner\)[\s\S]{0,520}motion\.pressedCommand=""[\s\S]{0,240}owner==="player"&&state\.petMenuStagePending\)battleCommitPetMenuStage/,
  /queueBattlePetMenuStage\(state=app\.battleState,command=""\)[\s\S]{0,520}leaveBattleMenuMotion\(state,"player"\)/,
  /rememberedBattlePetAction[\s\S]{0,650}lastPetActionSlot/,
]) if(!expected.test(menuLifecycleSource))throw new Error(`native battle menu lifecycle regression: ${expected}`);
if(!/#battle-player-menu,#battle-pet-command-menu\s*\{[^}]*left:0;[^}]*transform:translate3d\(300px,0,0\);[^}]*will-change:transform/.test(html)||
   /#battle-player-menu,#battle-pet-command-menu\s*\{[^}]*will-change:left/.test(html)){
  throw new Error("battle menu must use one GPU translation; will-change:left exposes black compositor tiles over the arena");
}
const closeBattlePopupSource=script.slice(script.indexOf("  function closeBattlePopup(options={})"),script.indexOf("  function openBattlePopup",script.indexOf("  function closeBattlePopup(options={})")));
if(/sendBattlePetDefault/.test(closeBattlePopupSource)||!/options\.clearChoiceTimer/.test(closeBattlePopupSource)){
  throw new Error("closing the native pet-skill window must keep the pet stage/countdown alive");
}
/* BattleButtonAttack/Jujutsu/Item/Pet all use bak + BattleButtonOff(). A
   second press closes a still-open popup, but selecting a J/I/W row calls
   ClearBattleButton(); pressing that UP main button again must cancel the
   actor hit boxes and reopen its popup. Exercise the production functions
   with an absolute countdown and a send spy. */
const battlePopupToggleStart=script.indexOf("  function openBattlePopup(kind)");
const battlePopupToggleEnd=script.indexOf("  function battlePopupRow",battlePopupToggleStart);
const battleActionToggleStart=script.indexOf("  function beginBattleAction(action,options={})");
const battleActionToggleEnd=script.indexOf("  function battleMenuCommand",battleActionToggleStart);
const battleButtonCommandStart=script.indexOf("  function battleButtonCommandForAction(action)");
const battleButtonCommandEnd=script.indexOf("  function battleButtonPressed",battleButtonCommandStart);
if(battlePopupToggleStart<0||battlePopupToggleEnd<=battlePopupToggleStart||battleActionToggleStart<0||battleActionToggleEnd<=battleActionToggleStart||battleButtonCommandStart<0||battleButtonCommandEnd<=battleButtonCommandStart){
  throw new Error("battle toggle production function boundary missing");
}
const makeBattleToggleHarness=new Function("initialState",`
  const state=initialState;
  const app={battle:true,battleState:state,battlePopup:null};
  const panel={classList:{add(){}},dataset:{}};
  const $=()=>panel;
  let sends=0,worldRenders=0,battleRenders=0,targetRenders=0,popupRenders=0,unavailable=0,menuLeaves=0;
  function send(){sends++;return Promise.resolve();}
  function closeBattlePopup(){app.battlePopup=null;}
  function leaveBattleMenuMotion(){menuLeaves++;state.menuMotion.phase="leaving";return true;}
  function battleCommandAllowed(){return true;}
  function battleActionAllowed(){return true;}
  function rejectFullBattleCapture(){return false;}
  function rejectUnavailableBattleMagic(){return false;}
  function battleExplainUnavailable(){unavailable++;}
  function renderBattleWorld(){worldRenders++;}
  function renderBattle(){battleRenders++;}
  function renderBattlePopup(){popupRenders++;}
  function battleTargetCandidates(){return [{battleId:10,name:"enemy"}];}
  function battleUsesActorTarget(){return true;}
  function renderBattleTargets(){targetRenders++;}
  function battleMenuMotionShape(current){return current.menuMotion;}
  ${script.slice(battleButtonCommandStart,battleButtonCommandEnd)}
  ${script.slice(battlePopupToggleStart,battlePopupToggleEnd)}
  ${script.slice(battleActionToggleStart,battleActionToggleEnd)}
  return {
    state,
    beginBattleAction,
    openBattlePopup,
    popup:()=>app.battlePopup,
    counts:()=>({sends,worldRenders,battleRenders,targetRenders,popupRenders,unavailable,menuLeaves})
  };
`);
const toggleDeadline=9876543210;
const toggleHarness=makeBattleToggleHarness({pendingAction:{kind:"attack",defaulted:true},choiceDeadline:toggleDeadline,menuMotion:{owner:"player",phase:"shown",buttonX:515,buttonA:0}});
if(toggleHarness.beginBattleAction({kind:"attack"})!==false||toggleHarness.state.pendingAction!==null){
  throw new Error("second/default Attack press must cancel its actor selector");
}
if(toggleHarness.counts().sends!==0||toggleHarness.state.choiceDeadline!==toggleDeadline||toggleHarness.counts().menuLeaves!==0||toggleHarness.state.menuMotion.phase!=="shown"){
  throw new Error("cancelling Attack must not send B, restart BattleCntDown, or return the player menu");
}
if(toggleHarness.beginBattleAction({kind:"attack"})!==true||toggleHarness.state.pendingAction?.kind!=="attack"){
  throw new Error("Attack must re-arm after its pressed state was cancelled");
}
if(toggleHarness.counts().menuLeaves!==0||toggleHarness.state.menuMotion.phase!=="shown"){
  throw new Error("Attack target selection must keep the native player menu shown until a real target is submitted");
}
if(!toggleHarness.openBattlePopup("magic")||toggleHarness.state.pendingAction!==null||toggleHarness.popup()?.kind!=="magic"){
  throw new Error("Attack -> Jujutsu must leave only the Jujutsu window active");
}
if(toggleHarness.openBattlePopup("magic")!==false||toggleHarness.popup()!==null){
  throw new Error("second Jujutsu press must close its native window");
}
for(const kind of ["item","pet"]){
  if(!toggleHarness.openBattlePopup(kind)||toggleHarness.popup()?.kind!==kind||toggleHarness.openBattlePopup(kind)!==false||toggleHarness.popup()!==null){
    throw new Error(`second ${kind} press must close its native window`);
  }
}
toggleHarness.openBattlePopup("magic");
if(!toggleHarness.beginBattleAction({kind:"magic",index:2,targetType:0},{closePopup:true})||toggleHarness.popup()!==null||toggleHarness.state.pendingAction?.kind!=="magic"){
  throw new Error("choosing a Jujutsu row must advance from popup to actor targeting");
}
if(toggleHarness.openBattlePopup("magic")!==true||toggleHarness.state.pendingAction!==null||toggleHarness.popup()?.kind!=="magic"){
  throw new Error("UP Jujutsu must cancel actor targeting and reopen its native window");
}
if(toggleHarness.openBattlePopup("magic")!==false||toggleHarness.popup()!==null){
  throw new Error("second Jujutsu press must close the reopened native window");
}
if(toggleHarness.counts().sends!==0||toggleHarness.state.choiceDeadline!==toggleDeadline||toggleHarness.counts().unavailable!==0){
  throw new Error("battle command toggles changed packet count, countdown, or availability");
}
/* NETPROC.CPP sets NoHelpFlag for EN result 2/5. In that state the native
   HELP button keeps its hit area, paints CG_BTL_BUTTON_CROSS at (577,27),
   and a click emits only SE 220: no helpFlag mutation and no HL packet. */
const battleNoHelpStart=script.indexOf("  function battleNoHelpType(type)");
const battleNoHelpEnd=script.indexOf("  function battleActionAllowed",battleNoHelpStart);
if(battleNoHelpStart<0||battleNoHelpEnd<=battleNoHelpStart){
  throw new Error("battle NoHelp production function boundary missing");
}
const battleNoHelpSource=script.slice(battleNoHelpStart,battleNoHelpEnd);
const battleNoHelpVisualSource=script.slice(script.indexOf("  function syncBattleButtonVisualStates"),script.indexOf("  function battlePendingShape"));
if(!/#battle-help-cross\{left:577px;top:27px;width:40px;height:40px/.test(html)||
   !/<img id="battle-help-cross" data-src="\/assets\/bitmaps\/bitmap_8701\.png"/.test(html)||
   !/helpCross\.hidden=!app\.battle\|\|!state\?\.noHelp/.test(battleNoHelpVisualSource)||
   !/battle-command-no-help/.test(battleNoHelpVisualSource)||
   !script.includes("type:type||1,noHelp:battleNoHelpType(type)")){
  throw new Error("battle NoHelp EN state, cross asset, or exact position regressed");
}
const makeBattleNoHelpHarness=new Function("noHelp","initialHelpFlag",`
  const BATTLE_BP_PLAYER_MENU_NON=1<<1,BATTLE_BP_PET_MENU_NON=1<<3;
  const state={noHelp:Boolean(noHelp),bpFlags:0,movieActive:false,commandLocked:false,petCommandLocked:false};
  const app={battle:true,battleState:state,status:{helpFlag:Number(initialHelpFlag)?1:0}};
  const sends=[],tones=[],statusWrites=[],chats=[];
  function send(name,values){sends.push([name,[...values]]);return Promise.resolve();}
  function playSoundEffect(tone,x,y){tones.push([tone,x,y]);}
  function syncBattleButtonVisualStates(current){if(current!==state)throw new Error("NoHelp visual sync used stale state");}
  function setStatus(name,value){statusWrites.push([name,value]);}
  function addChat(channel,message){chats.push([channel,message]);}
  function reportError(error){throw error;}
  function battleExplainUnavailable(){throw new Error("NoHelp click must use the native SE 220 path");}
  function battleChoiceExpired(){return false;}
  function battleLocalDeath(){return false;}
  function battleCommandKind(){return "player";}
  ${battleNoHelpSource}
  return {
    state,app,battleNoHelpType,battleHelpCommand,rejectBattleHelp,battleMenuCommand,battleCommandAllowed,
    result:()=>({sends:[...sends],tones:[...tones],statusWrites:[...statusWrites],chats:[...chats],helpFlag:app.status.helpFlag})
  };
`);
const noHelpHarness=makeBattleNoHelpHarness(true,1);
if(!noHelpHarness.battleNoHelpType(2)||!noHelpHarness.battleNoHelpType("5")||noHelpHarness.battleNoHelpType(1)||noHelpHarness.battleNoHelpType(6)){
  throw new Error("EN result 2/5 must be the exact native NoHelp set");
}
if(noHelpHarness.battleCommandAllowed("HELP")!==false||noHelpHarness.battleMenuCommand("HELP")!==false){
  throw new Error("NoHelp must reject HELP locally while retaining the live click path");
}
const noHelpResult=noHelpHarness.result();
if(noHelpResult.helpFlag!==1||noHelpResult.sends.length!==0||noHelpResult.statusWrites.length!==0||noHelpResult.chats.length!==0||
   noHelpResult.tones.length!==1||noHelpResult.tones[0].join(",")!=="220,320,240"){
  throw new Error(`NoHelp must preserve helpFlag and emit only SE 220: ${JSON.stringify(noHelpResult)}`);
}
const normalHelpHarness=makeBattleNoHelpHarness(false,1);
if(normalHelpHarness.battleCommandAllowed("?")!==true)throw new Error("ordinary battle HELP unexpectedly disabled");
normalHelpHarness.battleMenuCommand("?");
const normalHelpResult=normalHelpHarness.result();
if(normalHelpResult.helpFlag!==0||normalHelpResult.sends.length!==1||normalHelpResult.sends[0][0]!=="HL"||normalHelpResult.sends[0][1][0]!==0||
   normalHelpResult.tones.length!==0||normalHelpResult.statusWrites.length!==1||normalHelpResult.chats.length!==1){
  throw new Error(`ordinary HELP toggle or HL packet regressed: ${JSON.stringify(normalHelpResult)}`);
}
/* CheckPetSuu() runs before BattleButtonCapture's bak/ButtonOff path. With
   five occupied pet slots, Capture paints CG_BTL_BUTTON_CROSS and a click
   emits only SE 220; it must not cancel a remembered Attack selector or let
   2.5 consume a T command which PET_createPetFromCharaIndex later rejects. */
const battleCaptureHelperStart=script.indexOf("  function battlePetCapacityFull()");
const battleCaptureHelperEnd=script.indexOf("  function battleUsableMagic(entry)",battleCaptureHelperStart);
if(battleCaptureHelperStart<0||battleCaptureHelperEnd<=battleCaptureHelperStart){
  throw new Error("battle Capture capacity production function boundary missing");
}
const captureVisualSource=script.slice(script.indexOf("  function syncBattleButtonVisualStates"),script.indexOf("  function battlePendingShape"));
if(!/#battle-capture-cross\{left:523px;top:27px;width:40px;height:40px/.test(html)||
   !/<img id="battle-capture-cross" data-src="\/assets\/bitmaps\/bitmap_8701\.png"/.test(html)||
   !/captureCross\.hidden=!app\.battle\|\|!battlePetCapacityFull\(\)/.test(captureVisualSource)||
   !/async function sendBattleTarget[\s\S]{0,700}rejectFullBattleCapture\(action,app\.battleState\)/.test(script)||
   !script.includes('if(/^(?:T|C)(?:\\||$)/i.test(text)&&rejectFullBattleCapture({kind:"capture"},state))return;')){
  throw new Error("full-stable Capture cross or final/direct packet gate regressed");
}
const makeBattleCaptureHarness=new Function("petSlots",`
  const state={pendingAction:{kind:"attack",defaulted:true},choiceDeadline:888888,commandLocked:false,petCommandLocked:false};
  const app={battle:true,battleState:state,battlePopup:null,petSlots};
  const panel={dataset:{},classList:{add(){},remove(){}}};
  const $=()=>panel;
  let sends=0,targets=0,worldRenders=0,battleRenders=0,syncs=0;
  const tones=[];
  function send(){sends++;return Promise.resolve();}
  function playSoundEffect(tone,x,y){tones.push([tone,x,y]);}
  function syncBattleButtonVisualStates(){syncs++;}
  function battleActionAllowed(){return true;}
  function battleExplainUnavailable(){}
  function battleButtonCommandForAction(action){return action?.kind==="attack"?"H":action?.kind==="capture"?"C":"";}
  function rejectUnavailableBattleMagic(){return false;}
  function closeBattlePopup(){app.battlePopup=null;}
  function renderBattleWorld(){worldRenders++;}
  function renderBattle(){battleRenders++;}
  function renderBattlePopup(){}
  function battleTargetCandidates(){return [{battleId:10,name:"enemy"}];}
  function battleUsesActorTarget(){return true;}
  function renderBattleTargets(){targets++;}
  function setBattleMenuPressedCommand(current,command){current.pressedCommand=String(command||"").toUpperCase();return current.pressedCommand;}
  function addChat(){}
  ${script.slice(battleCaptureHelperStart,battleCaptureHelperEnd)}
  ${script.slice(battleActionToggleStart,battleActionToggleEnd)}
  return {state,beginBattleAction,counts:()=>({sends,targets,worldRenders,battleRenders,syncs,tones:[...tones]})};
`);
const fullPetSlots=Array.from({length:5},(_,index)=>({index,useFlag:1,name:`pet${index}`}));
const fullCaptureHarness=makeBattleCaptureHarness(fullPetSlots),fullCaptureDeadline=fullCaptureHarness.state.choiceDeadline;
if(fullCaptureHarness.beginBattleAction({kind:"capture"})!==false||fullCaptureHarness.state.pendingAction?.kind!=="attack"||
   fullCaptureHarness.state.choiceDeadline!==fullCaptureDeadline||fullCaptureHarness.state.commandLocked||
   fullCaptureHarness.counts().sends!==0||fullCaptureHarness.counts().targets!==0||fullCaptureHarness.counts().tones.length!==1||
   fullCaptureHarness.counts().tones[0][0]!==220||fullCaptureHarness.counts().syncs!==1){
  throw new Error(`full-stable Capture must preserve default Attack/deadline and send only SE 220: ${JSON.stringify(fullCaptureHarness.counts())}`);
}
const openCaptureHarness=makeBattleCaptureHarness(fullPetSlots.slice(0,4));
if(!openCaptureHarness.beginBattleAction({kind:"capture"})||openCaptureHarness.state.pendingAction?.kind!=="capture"||
   openCaptureHarness.counts().targets!==1||openCaptureHarness.counts().tones.length!==0){
  throw new Error("Capture must retain native target selection while fewer than five pet slots are occupied");
}
/* _STANDBYPET BattleButtonPet() iterates only pet.useFlag slots selected in
   pc.selectPetNo.  The 2.5 server turns an out-of-mask S|n into PETIN rather
   than rejecting it, so exercise the production writer after a simulated
   SPET mask change as well as the popup filter. */
const battlePetSwitchStart=script.indexOf("  function battlePetSwitchEntry(index)");
const battlePetSwitchEnd=script.indexOf("  function closeBattlePopup(options={})",battlePetSwitchStart);
const battlePetPopupStart=script.indexOf("  function renderBattlePopup()");
const battlePetPopupEnd=script.indexOf("  function battleTargetPromptPlacement",battlePetPopupStart);
if(battlePetSwitchStart<0||battlePetSwitchEnd<=battlePetSwitchStart||battlePetPopupStart<0||battlePetPopupEnd<=battlePetPopupStart){
  throw new Error("battle standby-pet production function boundary missing");
}
const battlePetPopupSource=script.slice(battlePetPopupStart,battlePetPopupEnd);
for(const expected of [
  /entry:battlePetSwitchEntry\(index\)/,
  /filter\(item=>item\.entry\.listed\)/,
  /classList\.toggle\("pet-current",entry\.current\)/,
  /classList\.toggle\("pet-dead",!entry\.alive\)/,
]) if(!expected.test(battlePetPopupSource))throw new Error(`native standby-pet popup regression: ${expected}`);
const makeBattlePetSwitchHarness=new Function("petSlots","mask","selectedPet",`
  const state={choiceDeadline:666666,commandLocked:false,petCommandLocked:false,pendingAction:{kind:"sentinel"}};
  const app={battle:true,phase:"battle",battleState:state,battlePopup:{kind:"pet"},petSlots,status:{standbyPetMask:mask},selectedPet};
  const sends=[],tones=[];
  function playSoundEffect(tone,x,y){tones.push([tone,x,y]);}
  function battleCommandAllowed(){return true;}
  function battleActivePetSlot(){return Number(app.selectedPet);}
  function setBattleMenuPressedCommand(current,command){current.pressedCommand=String(command||"").toUpperCase();return current.pressedCommand;}
  function battleSetCommandLock(current,kind,locked){if(kind==="player")current.commandLocked=locked;else current.petCommandLocked=locked;}
  function battleStartCommandPending(current,kind,command){current.commandPending={kind,command};}
  function battleClearCommandPending(current,kind){if(current.commandPending?.kind===kind)current.commandPending=null;}
  function send(name,values){sends.push([name,[...values]]);return new Promise(()=>{});}
  function closeBattlePopup(){app.battlePopup=null;}
  function renderPets(){}
  function renderBattle(){}
  function maybeOpenBattlePetSkillMenu(){}
  function reportError(error){throw error;}
  ${script.slice(battlePetSwitchStart,battlePetSwitchEnd)}
  return {
    app,state,battlePetSwitchEntry,battleSwitchPet,
    listed:()=>[0,1,2,3,4].filter(index=>battlePetSwitchEntry(index).listed),
    setMask:value=>{app.status.standbyPetMask=value;},
    setHp:(index,value)=>{app.petSlots[index].hp=value;},
    counts:()=>({sends:[...sends],tones:[...tones]})
  };
`);
const switchPets=Array.from({length:5},(_,index)=>({index,useFlag:1,name:`pet${index}`,hp:100,maxHp:100}));
const invalidSwitchHarness=makeBattlePetSwitchHarness(switchPets.map(pet=>({...pet})),0b00011,0),switchDeadline=invalidSwitchHarness.state.choiceDeadline,sentinel=invalidSwitchHarness.state.pendingAction;
if(invalidSwitchHarness.listed().join(",")!=="0,1")throw new Error(`standby mask must hide rest/mail pets: ${invalidSwitchHarness.listed()}`);
if(invalidSwitchHarness.battleSwitchPet(4)!==false||invalidSwitchHarness.battleSwitchPet(0)!==false){
  throw new Error("out-of-mask and current battle pets must reject locally");
}
invalidSwitchHarness.setHp(1,0);
if(invalidSwitchHarness.battleSwitchPet(1)!==false||invalidSwitchHarness.counts().sends.length!==0||
   invalidSwitchHarness.counts().tones.length!==3||invalidSwitchHarness.counts().tones.some(item=>item.join(",")!=="220,320,240")||
   invalidSwitchHarness.state.commandLocked||invalidSwitchHarness.state.petCommandLocked||invalidSwitchHarness.state.petSwitchPending||
   invalidSwitchHarness.state.commandPending||invalidSwitchHarness.state.choiceDeadline!==switchDeadline||
   invalidSwitchHarness.state.pendingAction!==sentinel||invalidSwitchHarness.app.battlePopup?.kind!=="pet"){
  throw new Error(`invalid/dead/current pet clicks must preserve popup, deadline and turn: ${JSON.stringify(invalidSwitchHarness.counts())}`);
}
const staleMaskHarness=makeBattlePetSwitchHarness(switchPets.map(pet=>({...pet})),0b00011,0);
staleMaskHarness.setMask(0b00001);
if(staleMaskHarness.battleSwitchPet(1)!==false||staleMaskHarness.counts().sends.length!==0||staleMaskHarness.counts().tones[0]?.[0]!==220){
  throw new Error("SPET change after popup open must reject stale S|n before the wire");
}
const legalSwitchHarness=makeBattlePetSwitchHarness(switchPets.map(pet=>({...pet})),0b00011,0),legalDeadline=legalSwitchHarness.state.choiceDeadline;
if(legalSwitchHarness.battleSwitchPet(1)!==true||legalSwitchHarness.counts().sends.length!==1||
   legalSwitchHarness.counts().sends[0][0]!=="B"||legalSwitchHarness.counts().sends[0][1][0]!=="S|1"||
   !legalSwitchHarness.state.commandLocked||legalSwitchHarness.state.petSwitchPending?.index!==1||
   legalSwitchHarness.state.choiceDeadline!==legalDeadline||legalSwitchHarness.state.pressedCommand!=="PET"||
   legalSwitchHarness.app.battlePopup!==null||legalSwitchHarness.counts().tones.length!==1||legalSwitchHarness.counts().tones[0]?.join(",")!=="203,320,240"){
  throw new Error(`legal standby pet must send exactly B(S|1) and lock the master turn: ${JSON.stringify(legalSwitchHarness.counts())}`);
}
/* BattleButtonJujutsu() does not hide or disable MP-starved/map-only
   spells. It keeps the row/window alive, paints the native red/gray state,
   and emits only SE 220. Exercise both the initial row click and the final
   target click because BP can replace the authoritative MP in between. */
const battleMagicHelperStart=script.indexOf("  function battleUsableMagic(entry)");
const battleMagicHelperEnd=script.indexOf("  function battleActionCommand(action,target)",battleMagicHelperStart);
const battleTargetSendStart=script.indexOf("  async function sendBattleTarget(target)");
const battleTargetSendEnd=script.indexOf("  function battleActivePet(",battleTargetSendStart);
const battleMagicPopupStart=script.indexOf("  function renderBattlePopup()");
const battleMagicPopupEnd=script.indexOf("  function battleTargetPromptPlacement",battleMagicPopupStart);
if(battleMagicHelperStart<0||battleMagicHelperEnd<=battleMagicHelperStart||battleTargetSendStart<0||battleTargetSendEnd<=battleTargetSendStart||battleMagicPopupStart<0||battleMagicPopupEnd<=battleMagicPopupStart){
  throw new Error("battle Jujutsu production function boundary missing");
}
const battleMagicPopupSource=script.slice(battleMagicPopupStart,battleMagicPopupEnd);
for(const expected of [
  /app\.magic\.filter\(battleUsableMagic\)\.slice\(0,5\)/,
  /mpCost:mp/,
  /magic-insufficient/,
  /magic-map-only/,
  /aria-disabled","true"/,
]) if(!expected.test(battleMagicPopupSource))throw new Error(`native battle Jujutsu row regression: ${expected}`);
const makeBattleMagicHarness=new Function("magic","myMp","field",`
  const state={pendingAction:null,myMp,choiceDeadline:777777,commandLocked:false,petCommandLocked:false};
  const app={battle:true,battleState:state,battlePopup:{kind:"magic"},magic,pc:{mp:999}};
  const panel={dataset:{},classList:{add(){},remove(){}}};
  const $=()=>panel;
  let sends=0,targets=0,popupRenders=0,worldRenders=0,battleRenders=0,unavailable=0;
  const tones=[];
  function playSoundEffect(tone,x,y){tones.push([tone,x,y]);}
  function battleActionAllowed(){return true;}
  function rejectFullBattleCapture(){return false;}
  function battleExplainUnavailable(){unavailable++;}
  function battleButtonCommandForAction(action){return action?.kind==="magic"?"J":"";}
  function setBattleMenuPressedCommand(current,command){current.pressedCommand=String(command||"").toUpperCase();return current.pressedCommand;}
  function closeBattlePopup(){app.battlePopup=null;}
  function renderBattlePopup(){popupRenders++;}
  function renderBattleWorld(){worldRenders++;}
  function renderBattle(){battleRenders++;}
  function battleTargetCandidates(){return [{battleId:10,name:"enemy"}];}
  function battleUsesActorTarget(){return true;}
  function renderBattleTargets(){targets++;}
  function battleTargetSelectable(){return true;}
  function battleActionCommand(action,target){return \`J|\${Number(action.index).toString(16).toUpperCase()}|\${Number(target.battleId).toString(16).toUpperCase()}\`;}
  function battleSetCommandLock(current,kind,locked){if(kind==="player")current.commandLocked=locked;else current.petCommandLocked=locked;}
  function battleStartCommandPending(current,kind,command){current.commandPending={kind,command};}
  function battleClearCommandPending(){}
  function maybeOpenBattlePetSkillMenu(){}
  function reportError(error){throw error;}
  function send(name,values){sends++;return new Promise(()=>{});}
  function addChat(){}
  ${script.slice(battleMagicHelperStart,battleMagicHelperEnd)}
  ${script.slice(battleActionToggleStart,battleActionToggleEnd)}
  ${script.slice(battleTargetSendStart,battleTargetSendEnd)}
  return {
    app,state,beginBattleAction,sendBattleTarget,
    setMp(value){state.myMp=value;},
    counts(){return {sends,targets,popupRenders,worldRenders,battleRenders,unavailable,tones:[...tones]};}
  };
`);
const insufficientMagic=[{index:0,useFlag:1,mp:10,field:1,target:1,name:"测试咒术"}];
const insufficientHarness=makeBattleMagicHarness(insufficientMagic,0,1),insufficientDeadline=insufficientHarness.state.choiceDeadline;
if(insufficientHarness.beginBattleAction({kind:"magic",index:0,targetType:1,field:1,mpCost:10},{closePopup:true})!==false||
   insufficientHarness.app.battlePopup?.kind!=="magic"||insufficientHarness.state.pendingAction!==null||
   insufficientHarness.counts().targets!==0||insufficientHarness.counts().sends!==0||insufficientHarness.counts().tones.length!==1||
   insufficientHarness.counts().tones[0][0]!==220||insufficientHarness.state.choiceDeadline!==insufficientDeadline||insufficientHarness.state.commandLocked){
  throw new Error(`MP-starved Jujutsu must keep the native window/deadline and send only SE 220: ${JSON.stringify(insufficientHarness.counts())}`);
}
const exactMpHarness=makeBattleMagicHarness(insufficientMagic,10,1);
if(!exactMpHarness.beginBattleAction({kind:"magic",index:0,targetType:1,field:1,mpCost:10},{closePopup:true})||
   exactMpHarness.state.pendingAction?.kind!=="magic"||exactMpHarness.counts().targets!==1||exactMpHarness.counts().tones.length!==0){
  throw new Error("Jujutsu cost equal to authoritative BP MP must remain selectable");
}
exactMpHarness.setMp(0);exactMpHarness.sendBattleTarget({battleId:10,name:"enemy"});
if(exactMpHarness.counts().sends!==0||exactMpHarness.counts().tones.length!==1||exactMpHarness.counts().tones[0][0]!==220||
   exactMpHarness.state.pendingAction!==null||exactMpHarness.app.battlePopup?.kind!=="magic"||exactMpHarness.state.commandLocked){
  throw new Error(`late BP MP drop must reject J before the B write and restore Jujutsu: ${JSON.stringify(exactMpHarness.counts())}`);
}
const mapMagic=[{index:0,useFlag:1,mp:0,field:2,target:1,name:"地图咒术"}],mapMagicHarness=makeBattleMagicHarness(mapMagic,99,2);
if(mapMagicHarness.beginBattleAction({kind:"magic",index:0,targetType:1,field:2,mpCost:0},{closePopup:true})!==false||
   mapMagicHarness.app.battlePopup?.kind!=="magic"||mapMagicHarness.counts().sends!==0||mapMagicHarness.counts().tones[0]?.[0]!==220){
  throw new Error("map-only Jujutsu must stay visible/clickable but reject locally with SE 220");
}
/* InitItem2()/BattleButtonItem() are deliberately slot based: only
   pc.item[5..19] exists in the battle window, holes remain holes, the live
   REALBIN offset owns icon placement, and MOUSE_LEFT_DBL_CRICK is the sole
   confirmation gesture. Keep a DOM-level harness here because filtering or
   binding the ordinary click event both look plausible while breaking the
   native item/command contract. */
const nativeBattleMenuSource=fs.readFileSync(__dirname+"/../../vendor/upstream/code_sa_client/SYSTEM/BATTLEMENU.CPP","latin1");
const nativeBattleMpMeterStart=nativeBattleMenuSource.indexOf("void HpMeterDisp");
const nativeBattleMpMeterEnd=nativeBattleMenuSource.indexOf("void BattleButtonAttack",nativeBattleMpMeterStart);
const nativeBattleMpMeterSource=nativeBattleMenuSource.slice(nativeBattleMpMeterStart,nativeBattleMpMeterEnd);
if(nativeBattleMpMeterStart<0||nativeBattleMpMeterEnd<=nativeBattleMpMeterStart||
   !/p_party\[ BattleMyNo \]->mp\s*\/\s*100\.0[\s\S]{0,40}\*\s*40\.0/.test(nativeBattleMpMeterSource)){
  throw new Error("native 2.5 fixed-100 battle MP meter reference drifted");
}
const battleMpFillStart=script.indexOf("  function battlePlayerMpFillWidth(mp){");
const battleMpFillEnd=script.indexOf("  function scheduleBattleTransientCleanup",battleMpFillStart);
if(battleMpFillStart<0||battleMpFillEnd<=battleMpFillStart)throw new Error("battle MP fill helper boundary missing");
const battlePlayerMpFillWidth=new Function(`${script.slice(battleMpFillStart,battleMpFillEnd)};return battlePlayerMpFillWidth;`)();
for(const [mp,expected] of [[0,0],[50,20],[100,40],[140,40],[-20,0]]){
  const actual=battlePlayerMpFillWidth(mp);
  if(actual!==expected)throw new Error(`2.5 battle MP meter drifted: ${mp} -> ${actual}px, expected ${expected}px`);
}
const battleMpRenderSource=script.slice(script.indexOf("  function renderBattleWorld"),script.indexOf("  function enterBattle"));
if(!/mp\.style\.width=`\$\{battlePlayerMpFillWidth\(state\.myMp\)\}px`/.test(battleMpRenderSource)||
   /battle-mp[\s\S]{0,600}(app\.pc\?\.maxMp|spec\.item\.maxMp)/.test(battleMpRenderSource)){
  throw new Error("battle MP HUD must use the native fixed-100 fill without the status maxMp denominator");
}
/* BattleMenuProc() invokes HpMeterDisp() for only the local ten-slot side,
   never both sides.  Keep this source boundary locked because showing enemy
   HP bars is a visually plausible Web addition that is not present in 2.5. */
const nativeBattleMenuProcStart=nativeBattleMenuSource.indexOf("void BattleMenuProc");
const nativeBattleMenuProcEnd=nativeBattleMenuSource.length;
const nativeBattleMenuProcSource=nativeBattleMenuSource.slice(nativeBattleMenuProcStart,nativeBattleMenuProcEnd);
if(nativeBattleMenuProcStart<0||nativeBattleMenuProcEnd<=nativeBattleMenuProcStart||
   !/BattleMyNo\s*<\s*10[\s\S]{0,180}for\(\s*i\s*=\s*0\s*;\s*i\s*<\s*10[\s\S]{0,100}HpMeterDisp\(\s*i\s*\)/.test(nativeBattleMenuProcSource)||
   !/BattleMyNo\s*>=\s*10[\s\S]{0,180}for\(\s*i\s*=\s*10\s*;\s*i\s*<\s*20[\s\S]{0,100}HpMeterDisp\(\s*i\s*\)/.test(nativeBattleMenuProcSource)){
  throw new Error("native 2.5 local-side-only battle HP meter reference drifted");
}
const nativeBattleReceiveMovieStart=nativeBattleProcSource.indexOf("case BATTLE_SUBPROC_RECEIVE_MOVIE");
const nativeBattleMovieStart=nativeBattleProcSource.indexOf("case BATTLE_SUBPROC_MOVIE",nativeBattleReceiveMovieStart);
const nativeBattleExitStart=nativeBattleProcSource.indexOf("case BATTLE_SUBPROC_OUT_PRODUCE_INIT",nativeBattleMovieStart);
const nativeBattleCharInStart=nativeBattleProcSource.indexOf("case BATTLE_SUBPROC_CHAR_IN");
const nativeBattleCommandStart=nativeBattleProcSource.indexOf("case BATTLE_SUBPROC_CMD_INPUT",nativeBattleCharInStart);
const nativeBattleExecutableText=value=>String(value||"").replace(/\/\*[\s\S]*?\*\//g,"").replace(/\/\/[^\r\n]*/g,"");
if(nativeBattleReceiveMovieStart<0||nativeBattleMovieStart<=nativeBattleReceiveMovieStart||nativeBattleExitStart<=nativeBattleMovieStart||nativeBattleCharInStart<0||nativeBattleCommandStart<=nativeBattleCharInStart||
   !/BattleMyNo\s*>=\s*20[\s\S]{0,220}for\(\s*i\s*=\s*0\s*;\s*i\s*<\s*20[\s\S]{0,100}HpMeterDisp\(\s*i\s*\)/.test(nativeBattleProcSource.slice(nativeBattleReceiveMovieStart,nativeBattleMovieStart))||
   /HpMeterDisp\s*\(/.test(nativeBattleExecutableText(nativeBattleProcSource.slice(nativeBattleMovieStart,nativeBattleExitStart)))||
   /HpMeterDisp\s*\(/.test(nativeBattleExecutableText(nativeBattleProcSource.slice(nativeBattleCharInStart,nativeBattleCommandStart)))){
  throw new Error("native 2.5 battle HP meter stage boundary drifted");
}
const hpMeterSideStart=script.indexOf("  function battleHpMeterVisible(item,state=app.battleState){");
const hpMeterSideEnd=script.indexOf("  function battleIsEnemy",hpMeterSideStart);
if(hpMeterSideStart<0||hpMeterSideEnd<=hpMeterSideStart)throw new Error("battle HP side helper boundary missing");
const battleHpMeterVisible=new Function("battleSide",`${script.slice(hpMeterSideStart,hpMeterSideEnd)};return battleHpMeterVisible;`)(id=>{const value=Number(id);return value>=0&&value<10?0:value>=10&&value<20?1:-1;});
const hpStageBase={myNo:0,myNoKnown:true,bpReceived:true,entryPending:false,awaitingBattleRoster:false,movieActive:false,result:null,exitPending:false,movieGeneration:2,bpMovieGeneration:2,commandPending:{player:null}};
for(const [overrides,battleId,expected] of [
  [{},0,true],[{},5,true],[{},10,false],
  [{myNo:13},10,true],[{myNo:13},18,true],[{myNo:13},3,false],
  [{myNoKnown:false},0,false],[{bpReceived:false},0,false],[{entryPending:true},0,false],
  [{awaitingBattleRoster:true},0,false],[{movieActive:true},0,false],[{movieGeneration:3},0,false],
  [{result:{}},0,false],[{exitPending:true},0,false],
  [{myNo:20,commandPending:{player:null}},0,false],[{myNo:20,commandPending:{player:"N"}},0,true],[{myNo:20,commandPending:{player:"N"}},10,true],
]){
  const state={...hpStageBase,...overrides},actual=battleHpMeterVisible({battleId},state);
  if(actual!==expected)throw new Error(`2.5 battle HP meter stage/side drifted: ${JSON.stringify({state,battleId,actual,expected})}`);
}
if(!/const showHp=Boolean\(spec\.item&&battleHpMeterVisible\(spec\.item,state\)&&!spec\.item\.dead/.test(battleMpRenderSource)){
  throw new Error("battle actor renderer must follow the native HP meter stage and side boundary");
}
const nativeBattleItemStart=nativeBattleMenuSource.indexOf("void InitItem2");
const nativeBattleItemEnd=nativeBattleMenuSource.indexOf("void HpMeterDisp",nativeBattleItemStart);
const nativeBattleItemButtonStart=nativeBattleMenuSource.indexOf("void BattleButtonItem");
const nativeBattleItemButtonEnd=nativeBattleMenuSource.indexOf("void BattleButtonPet",nativeBattleItemButtonStart);
const nativeBattleItemSource=nativeBattleMenuSource.slice(nativeBattleItemStart,nativeBattleItemEnd)+nativeBattleMenuSource.slice(nativeBattleItemButtonStart,nativeBattleItemButtonEnd);
if(nativeBattleItemStart<0||nativeBattleItemEnd<=nativeBattleItemStart||nativeBattleItemButtonStart<0||nativeBattleItemButtonEnd<=nativeBattleItemButtonStart||
   !/for\(\s*i\s*=\s*5\s*;\s*i\s*<\s*MAX_ITEM\s*;\s*i\+\+\s*\)/.test(nativeBattleItemSource)||
   !/MOUSE_LEFT_DBL_CRICK/.test(nativeBattleItemSource)||!/ITEM_FIELD_MAP/.test(nativeBattleItemSource)||
   !/StockDispBuffer\(\s*ItemBuffer\[\s*i\s*\]\.defX\s*,\s*ItemBuffer\[\s*i\s*\]\.defY/.test(nativeBattleItemSource)){
  throw new Error("native 2.5 battle-item slot/double-click contract drifted");
}
const battleItemFieldStart=script.indexOf("  function battleFieldAllowed(entry)");
const battleItemFieldEnd=script.indexOf("  function battlePetCapacityFull()",battleItemFieldStart);
const battleItemHelperStart=script.indexOf("  function battleUsableItem(entry)");
const battleItemHelperEnd=script.indexOf("  function battleUsablePetSkill(entry)",battleItemHelperStart);
const battleItemPopupStart=script.indexOf("  function setBattleItemInspection(entry)");
const battleItemPopupEnd=script.indexOf("  function battleTargetPromptPlacement",battleItemPopupStart);
if(battleItemFieldStart<0||battleItemFieldEnd<=battleItemFieldStart||battleItemHelperStart<0||battleItemHelperEnd<=battleItemHelperStart||battleItemPopupStart<0||battleItemPopupEnd<=battleItemPopupStart){
  throw new Error("battle item production function boundary missing");
}
const makeBattleItemPopupHarness=new Function("inventory","level",`
  class FakeClassList{
    constructor(owner){this.owner=owner;this.values=new Set();}
    add(...values){for(const value of values)this.values.add(value);this.sync();}
    remove(...values){for(const value of values)this.values.delete(value);this.sync();}
    toggle(value,force){const next=force===undefined?!this.values.has(value):Boolean(force);if(next)this.values.add(value);else this.values.delete(value);this.sync();return next;}
    contains(value){return this.values.has(value);}
    sync(){this.owner._className=[...this.values].join(" ");}
  }
  class FakeNode{
    constructor(tag,id=""){this.tagName=String(tag).toUpperCase();this.id=id;this.children=[];this.dataset={};this.style={};this.attributes={};this.listeners={};this.classList=new FakeClassList(this);this._className="";this.textContent="";this.disabled=false;}
    set className(value){this._className=String(value||"");this.classList.values=new Set(this._className.split(/\\s+/).filter(Boolean));}
    get className(){return this._className;}
    append(...nodes){this.children.push(...nodes);}
    replaceChildren(...nodes){this.children=[...nodes];}
    addEventListener(type,handler){(this.listeners[type]||(this.listeners[type]=[])).push(handler);}
    dispatch(type){const event={type,defaultPrevented:false,preventDefault(){this.defaultPrevented=true;}};for(const handler of this.listeners[type]||[])handler(event);return event;}
    setAttribute(name,value){this.attributes[name]=String(value);}
    removeAttribute(name){delete this.attributes[name];}
  }
  const nodes={
    "battle-popup":new FakeNode("div","battle-popup"),"battle-popup-bg":new FakeNode("img","battle-popup-bg"),
    "battle-popup-title":new FakeNode("img","battle-popup-title"),"battle-popup-list":new FakeNode("div","battle-popup-list"),
    "battle-popup-return":new FakeNode("button","battle-popup-return"),"battle-item-name":new FakeNode("strong","battle-item-name"),
    "battle-item-memo":new FakeNode("p","battle-item-memo")
  };
  const document={createElement(tag){return new FakeNode(tag);}},$=id=>nodes[id]||null;
  const app={battle:true,battlePopup:{kind:"item"},battleState:{},inventory,pc:{level},selectedPet:-1,magic:[],petSlots:[],petSkills:[]};
  const actions=[],tones=[];
  function inventoryItem(index){return app.inventory.find(item=>Number(item?.index)===Number(index))||null;}
  function inventoryTextLines(value){return [String(value||"")];}
  function resolveBitmapInfo(value){return Number(value)===101?{file:"bitmaps/item.png",width:30,height:20,xoffset:-11,yoffset:-17}:null;}
  function beginBattleAction(action,options){actions.push({action:{...action},options:{...options}});return true;}
  function playSoundEffect(...values){tones.push(values);}
  function syncBattleButtonVisualStates(){}
  function closeBattlePopup(){throw new Error("valid item render must not close the popup");}
  ${script.slice(battleItemFieldStart,battleItemFieldEnd)}
  ${script.slice(battleItemHelperStart,battleItemHelperEnd)}
  ${script.slice(battleItemPopupStart,battleItemPopupEnd)}
  renderBattlePopup();
  return {app,nodes,actions,tones,rows:nodes["battle-popup-list"].children};
`);
const battleItems=[
  {index:3,name:"装备区物品",graphic:101,field:0,target:1,level:1,memo:"不能出现在战斗栏"},
  {index:5,name:"可用肉",graphic:101,field:1,target:1,level:1,deadTarget:false,memo:"恢复体力"},
  {index:7,name:"地图专用",graphic:101,field:2,target:1,level:1,deadTarget:false,memo:"只能在地图使用"},
  {index:19,name:"等级不足",graphic:101,field:0,target:1,level:99,deadTarget:true,memo:"需要更高等级"},
];
const battleItemHarness=makeBattleItemPopupHarness(battleItems,10),battleItemRows=battleItemHarness.rows;
if(battleItemRows.length!==15||battleItemRows.map(row=>Number(row.dataset.slot)).join(",")!==Array.from({length:15},(_,index)=>index+5).join(",")||
   battleItemRows[1].disabled!==true||battleItemRows[2].disabled||battleItemRows[14].disabled){
  throw new Error(`battle item window must preserve all absolute slots 5..19: ${battleItemRows.map(row=>[row.dataset.slot,row.disabled])}`);
}
const battleItemIcon=battleItemRows[0].children[0];
if(!battleItemIcon||battleItemIcon.style.left!=="14px"||battleItemIcon.style.top!=="7px"||"width" in battleItemIcon.style||"height" in battleItemIcon.style){
  throw new Error(`battle item icon lost intrinsic REALBIN placement: ${JSON.stringify(battleItemIcon?.style)}`);
}
const singleClick=battleItemRows[0].dispatch("click");
if(!singleClick.defaultPrevented||battleItemHarness.actions.length||battleItemHarness.tones.length||
   battleItemHarness.nodes["battle-item-name"].textContent!=="可用肉"||battleItemHarness.nodes["battle-item-memo"].textContent!=="恢复体力"){
  throw new Error("single-click battle item inspection must never commit a command");
}
battleItemRows[2].dispatch("dblclick");battleItemRows[14].dispatch("dblclick");
if(battleItemHarness.actions.length||battleItemHarness.tones.length!==2||battleItemHarness.tones.some(tone=>tone.join(",")!=="220,320,240")||
   battleItemRows[2].disabled||battleItemRows[14].disabled||!battleItemHarness.nodes["battle-item-name"].classList.contains("level-locked")){
  throw new Error(`map-only/level-locked battle items must remain visible and reject only with SE 220: ${JSON.stringify(battleItemHarness.tones)}`);
}
battleItemRows[0].dispatch("dblclick");
if(battleItemHarness.actions.length!==1||battleItemHarness.actions[0].action.kind!=="item"||battleItemHarness.actions[0].action.index!==5||
   battleItemHarness.actions[0].options.closePopup!==true||battleItemHarness.tones.length!==2){
  throw new Error(`legal battle item double-click must preserve its absolute I slot: ${JSON.stringify(battleItemHarness.actions)}`);
}
if(/#battle-popup\[data-kind="item"\][^{]*\.battle-popup-icon\s*\{[^}]*width\s*:\s*48px/.test(html)){
  throw new Error("battle item icons must not be stretched to their hit-box size");
}
if(!/battlePetCommandHit\?\.addEventListener\("click",activateBattlePetCommandButton\)/.test(script)||
   !/activateBattlePetCommandButton[\s\S]{0,700}state\.pendingAction=null[\s\S]{0,420}openBattlePetSkillPopup\(\{surfaceReady:true\}\)/.test(script)){
  throw new Error("pet command skill/cancel bitmap lost its native click lifecycle");
}
/* BattleMenuProc handles MOUSE_RIGHT_CRICK as BattleButtonOff() for the
   player surface.  Its pet branch routes a selected target back through
   BattleButtonWaza(), reopening the skill window without changing the one
   BattleCntDown deadline or sending a command.  Exercise the production
   helper for every actor-target owner and both pet-window toggle states. */
const battleRightCancelStart=script.indexOf("  function cancelBattleSelectionFromContextMenu()");
const battleRightCancelEnd=script.indexOf("  const battlePetCommandHit=",battleRightCancelStart);
if(battleRightCancelStart<0||battleRightCancelEnd<=battleRightCancelStart)throw new Error("battle right-click cancel helper missing");
const makeBattleRightCancelHarness=new Function("pendingKind","popupKind","owner",`
  const deadline=9123456789;
  const state={pendingAction:pendingKind?{kind:pendingKind}:null,choiceDeadline:deadline,choiceOwner:owner,movieActive:false,menuMotion:{owner,phase:"shown"},commandLocked:false,petCommandLocked:false};
  const app={battle:true,phase:"battle",battleState:state,battlePopup:popupKind?{kind:popupKind}:null};
  const classes=new Set(),panel={dataset:{promptPlacement:"top"},classList:{add(...values){for(const value of values)classes.add(value);},remove(...values){for(const value of values)classes.delete(value);}}};
  let opens=0,closes=0,worldRenders=0,battleRenders=0,sends=0,hoverClears=0;
  const tones=[];
  const $=()=>panel;
  function battleChoiceActive(){return true;}
  function battleMenuMotionShape(){return state.menuMotion;}
  function setBattleMenuPressedCommand(current,command){current.menuMotion.pressedCommand=String(command||"").toUpperCase();return current.menuMotion.pressedCommand;}
  function closeBattlePopup(){closes++;app.battlePopup=null;}
  function openBattlePetSkillPopup(){opens++;app.battlePopup={kind:"pet-skill"};return true;}
  function paintBattleHoverTarget(value){if(value===null)hoverClears++;}
  function playSoundEffect(...values){tones.push(values);}
  function renderBattleWorld(){worldRenders++;}
  function renderBattle(){battleRenders++;}
  function send(){sends++;}
  ${script.slice(battleRightCancelStart,battleRightCancelEnd)}
  return {state,app,deadline,cancelBattleSelectionFromContextMenu,counts:()=>({opens,closes,worldRenders,battleRenders,sends,hoverClears,tones:[...tones],hidden:classes.has("hidden")})};
`);
for(const kind of ["attack","capture","magic","item"]){
  const harness=makeBattleRightCancelHarness(kind,null,"player");
  if(!harness.cancelBattleSelectionFromContextMenu()||harness.state.pendingAction!==null||harness.state.choiceDeadline!==harness.deadline||
     harness.counts().opens!==0||harness.counts().sends!==0||harness.counts().tones[0]?.join(",")!=="217,320,240"||
     !harness.counts().hidden||harness.counts().hoverClears!==1){
    throw new Error(`right-click must locally cancel ${kind} targeting without changing BattleCntDown: ${JSON.stringify(harness.counts())}`);
  }
}
const petTargetCancel=makeBattleRightCancelHarness("pet",null,"pet");
if(!petTargetCancel.cancelBattleSelectionFromContextMenu()||petTargetCancel.state.pendingAction!==null||petTargetCancel.app.battlePopup?.kind!=="pet-skill"||
   petTargetCancel.counts().opens!==1||petTargetCancel.counts().sends!==0||petTargetCancel.state.choiceDeadline!==petTargetCancel.deadline){
  throw new Error(`right-click pet target must reopen the native skill window: ${JSON.stringify(petTargetCancel.counts())}`);
}
const petPopupCancel=makeBattleRightCancelHarness(null,"pet-skill","pet");
if(!petPopupCancel.cancelBattleSelectionFromContextMenu()||petPopupCancel.app.battlePopup!==null||petPopupCancel.counts().opens!==0||
   petPopupCancel.counts().sends!==0||petPopupCancel.state.choiceDeadline!==petPopupCancel.deadline){
  throw new Error(`right-click open pet skill window must toggle it off locally: ${JSON.stringify(petPopupCancel.counts())}`);
}
const battleContextMenuStart=script.indexOf('  document.addEventListener("contextmenu"');
const battleContextMenuEnd=script.indexOf('  document.addEventListener("dblclick"',battleContextMenuStart);
const battleContextMenuSource=script.slice(battleContextMenuStart,battleContextMenuEnd);
if(battleContextMenuStart<0||battleContextMenuEnd<=battleContextMenuStart||
   !/isEditableTarget\(event\.target\)/.test(battleContextMenuSource)||!/event\.preventDefault\(\)/.test(battleContextMenuSource)||
   !/app\.phase==="battle"&&app\.battle/.test(battleContextMenuSource)||!/cancelBattleSelectionFromContextMenu\(\)/.test(battleContextMenuSource)||
   /battleScreen\.addEventListener\("contextmenu"/.test(script)){
  throw new Error("top-level battle target overlay must route document contextmenu to native right-click cancel");
}
/* SPRDISP.CPP creates exactly one DISP_INFO/hitDispNo for each actor.  Keep
   the compositor-stable overlay as that sole DOM owner: a second transparent
   button in the depth-sorted actor wrapper is both non-native and covered by
   the overlay during real/semantic clicks. */
const battleWorldRenderStart=script.indexOf("  function renderBattleWorld(){");
const battleWorldRenderEnd=script.indexOf("  function enterBattle(",battleWorldRenderStart);
const battleWorldRenderSource=script.slice(battleWorldRenderStart,battleWorldRenderEnd);
if(battleWorldRenderStart<0||battleWorldRenderEnd<=battleWorldRenderStart||
   (battleWorldRenderSource.match(/document\.createElement\("button"\)/g)||[]).length!==1||
   /wrapper\.querySelector\(":scope > \.battle-target-hit"\)/.test(battleWorldRenderSource)||
   /#battle-actors-layer \.battle-target-hit/.test(html)||
   !/layer\.setAttribute\("aria-hidden","true"\)/.test(battleWorldRenderSource)||
   !/targetOverlay\.setAttribute\("aria-hidden",pendingTarget\?"false":"true"\)/.test(battleWorldRenderSource)||
   !/targetOverlay\.append\(proxy\)/.test(battleWorldRenderSource)||
   !/proxyHit\.style\.left=`\$\{hitLeft\}px`/.test(battleWorldRenderSource)){
  throw new Error("each selectable battle actor must have exactly one ordered overlay hit owner");
}
for(const expected of [
  /#battle-target-overlay \.battle-target-frame \{[^}]*border:0;[^}]*linear-gradient\(currentColor 0 0\) 1px 0\/calc\(100% - 2px\) 2px no-repeat[^}]*animation:battle-target-box-color 1s/,
  /@keyframes battle-target-box-color\{0%,74%\{color:#00ff00\}75%,83%\{color:#28e128\}84%,91%\{color:#008000\}/,
])if(!expected.test(html))throw new Error(`native green battle target rectangle regression: ${expected}`);
if(/battle-actor\.battle-dead \.battle-sprite \{[^}]*grayscale|battle-actor\.battle-dead \.battle-sprite \{[^}]*brightness/.test(html)){
  throw new Error("native VCT252 corpse must retain its unfiltered final DEAD bitmap");
}
if(!/const deathSoundAt=startAt\+deadStartOffset\+deadDuration;[\s\S]{0,280}battleScheduleSound\(state,deathSoundAt,[\s\S]{0,180}playSoundEffect\(6,/.test(script)||
   !/battleScheduleSound\(state,startAt\+bdDuration\+deadDuration,[\s\S]{0,180}playSoundEffect\(6,/.test(script)){
  throw new Error("native death SE 6 must play only when the DEAD row reaches VCT252");
}
/* CheckGroupSelect() outlines all actors that carry the clicked grouped
   hitFlag.  Exercise ordinary, boomerang-row, side, row and all-target
   expansion independently from the DOM renderer. */
const battleHighlightStart=script.indexOf("  function battleTargetHighlightIds(");
const battleHighlightEnd=script.indexOf("  function battleFieldAllowed(",battleHighlightStart);
if(battleHighlightStart<0||battleHighlightEnd<=battleHighlightStart)throw new Error("battle target highlight helper boundary missing");
const battleTargetHighlightIds=new Function("battleTargetSelectable","battleSide","BATTLE_BP_BOOMERANG","app",`${script.slice(battleHighlightStart,battleHighlightEnd)};return battleTargetHighlightIds;`)(
  ()=>true,id=>Number(id)<10?0:1,1,{}
);
const highlightParticipants=[0,1,5,10,11,15].map(battleId=>({battleId}));
const highlightState={participants:highlightParticipants,bpFlags:0};
const highlighted=(id,action,state=highlightState)=>[...battleTargetHighlightIds(id,action,state)].sort((a,b)=>a-b).join(",");
if(highlighted(10,{kind:"attack"})!=="10"||
   highlighted(10,{kind:"attack"},{...highlightState,bpFlags:1})!=="10,11"||
   highlighted(10,{kind:"magic",targetType:8})!=="10,11,15"||
   highlighted(10,{kind:"magic",targetType:4})!=="0,1,5,10,11,15"){
  throw new Error("native grouped battle target outlines drifted");
}
/* MOUSE.CPP only reports a target after the physical pointer enters the
   48x48 foot box; the complete sprite rectangle is an outline painted after
   that hit, never a second hover-only selector. */
const battleHoverStart = script.indexOf("  function updateBattleHoverFromPointer(event)");
const battleHoverEnd = script.indexOf("  battleScreen.addEventListener(\"pointermove\"", battleHoverStart);
const battleHoverSource = script.slice(battleHoverStart, battleHoverEnd);
if (battleHoverStart < 0 || battleHoverEnd <= battleHoverStart ||
    !/querySelectorAll\("#battle-target-overlay \.battle-target-hit"\)/.test(battleHoverSource) ||
    /querySelectorAll\("#battle-target-overlay \[data-battle-target\] > \.battle-target-frame"\)/.test(battleHoverSource)) {
  throw new Error("battle hover and click must share the native 48x48 hit box");
}
/* The smooth local walker is a shallow fractional-position copy.  Persist
   its decoded frame back to the real C actor so the next render cannot lose
   the fallback bitmap and blank the player for one tick. */
const worldActorPaintStart = script.indexOf("  function renderSceneActorsAndParts(");
const worldActorPaintEnd = script.indexOf("  const MAP_LAYER_ASYNC_RASTER_THRESHOLD", worldActorPaintStart);
const worldActorPaintSource = script.slice(worldActorPaintStart, worldActorPaintEnd);
if (!/if\(renderActor!==actor&&renderActor\?\._lastFrame\)actor\._lastFrame=renderActor\._lastFrame/.test(worldActorPaintSource)) {
  throw new Error("walking actor must retain its last decoded frame");
}
if (/requestPointerLock|exitPointerLock/.test(script)) {
  throw new Error("field cursor must never lock or move the browser's real pointer");
}
const worldPointerStart = script.indexOf("  function updateWorldPointer(event)");
const worldPointerEnd = script.indexOf("  function faceTowardTile", worldPointerStart);
if (worldPointerStart < 0 || worldPointerEnd <= worldPointerStart) throw new Error("world pointer handler boundary missing");
if (/worldTransitionActive\(\)\)return/.test(script.slice(worldPointerStart, worldPointerEnd))) {
  throw new Error("scene transitions must not freeze the painted fish");
}
/* serverWindowType1 is the one native WN handler whose ESC path emits a
   response: CANCEL (2) with data "0".  Other WN types only close locally.
   Both the keyboard and bitmap close paths must share that distinction. */
const dismissStart = script.indexOf("  function dismissServerWindow(){");
const dismissEnd = script.indexOf("  function actorIsOwn", dismissStart);
if (dismissStart < 0 || dismissEnd <= dismissStart) throw new Error("native WN dismiss helper boundary missing");
const responseStart = script.indexOf("  function windowResponse(select,data=\"\"){"),
  responseEnd = script.indexOf("  function splitWindowTokens", responseStart);
if (responseStart < 0 || responseEnd <= responseStart) throw new Error("native WN response helper boundary missing");
const responseFactory = new Function("app", "send", "closeServerWindow", `${script.slice(responseStart, responseEnd)};return windowResponse;`);
{
  const calls=[];
  let closes=0;
  const app={position:[17,25],activeWindow:{windowType:0,seqno:271,objindex:1604}};
  const respond=responseFactory(app,(name,fields)=>{calls.push({name,fields});return Promise.resolve();},()=>{closes++;app.activeWindow=null;});
  const result=respond(1,"乌力～～乌力");
  if (closes!==1 || app.activeWindow!==null || calls.length!==1 || calls[0].name!=="WN" ||
      JSON.stringify(calls[0].fields)!==JSON.stringify([17,25,271,1604,1,"乌力～～乌力"]) ||
      !(result&&typeof result.then==="function")) {
    throw new Error(`message WN response must close immediately after queueing: ${JSON.stringify({closes,calls,active:app.activeWindow})}`);
  }
}
{
  const calls=[];
  let closes=0;
  const app={position:[17,25],activeWindow:{windowType:1,seqno:272,objindex:1604}};
  const respond=responseFactory(app,(name,fields)=>{calls.push({name,fields});return Promise.resolve();},()=>{closes++;app.activeWindow=null;});
  respond(32,"next");
  if (closes!==0 || !app.activeWindow || calls.length!==1) {
    throw new Error("paging WN response must retain the current window");
  }
}
const dismissFactory = new Function("app", "send", "closeServerWindow", `${script.slice(dismissStart, dismissEnd)};return dismissServerWindow;`);
{
  const calls=[];
  let closes=0;
  const app={position:[17,25],activeWindow:{windowType:2,seqno:41,objindex:243}};
  const dismiss=dismissFactory(app,(name,fields)=>{calls.push({name,fields});return Promise.resolve();},()=>{closes++;app.activeWindow=null;});
  dismiss();
  if(closes!==1||calls.length!==1||calls[0].name!=="WN"||JSON.stringify(calls[0].fields)!==JSON.stringify([17,25,41,243,2,"0"])){
    throw new Error(`SELECT dismiss must send native CANCEL before closing: ${JSON.stringify({closes,calls})}`);
  }
}
for(const windowType of [0,1,3,4,5,6,10,11]){
  const calls=[];
  let closes=0;
  const app={position:[17,25],activeWindow:{windowType,seqno:42,objindex:244}};
  const dismiss=dismissFactory(app,(name,fields)=>{calls.push({name,fields});return Promise.resolve();},()=>{closes++;app.activeWindow=null;});
  dismiss();
  if(closes!==1||calls.length!==0)throw new Error(`WN type ${windowType} must close locally without synthetic CANCEL`);
}
const escapeHandler = script.indexOf('if(event.key==="Escape"&&!event.isComposing)');
if (escapeHandler < 0 || !/if\(app\.activeWindow\)\{event\.preventDefault\(\);dismissServerWindow\(\)\.catch\(reportError\);return;\}/.test(script.slice(escapeHandler, escapeHandler + 1400))) {
  throw new Error("Escape must use the native type-aware WN dismiss path before other screens");
}
if (!/\$\("server-window-close"\)\.addEventListener\("click",\(\)=>dismissServerWindow\(\)\.catch\(reportError\)\)/.test(script)) {
  throw new Error("WN close button must use the same native type-aware dismiss path");
}
/* MESSAGEANDLINEINPUT and WIDEMESSAGEANDLINEINPUT submit their text through
   WINDOW_BUTTONTYPE_OK (1).  A zero select value is not a form index and is
   ignored by the 2.5 NPC handlers. */
if (!/\$\("server-window-form"\)\.addEventListener\("submit",[\s\S]{0,900}windowResponse\(1,\$\("server-window-input"\)\.value\)/.test(script)) {
  throw new Error("WN line-input form must submit the native OK bit");
}
const battleDepthStart = script.indexOf("  function battleProjectileVerticalPosition");
const battleDepthEnd = script.indexOf("  /* Exact 5×5 positions from oft.cpp::oft_test()", battleDepthStart);
if (battleDepthStart < 0 || battleDepthEnd <= battleDepthStart) throw new Error("battle shared depth helper boundary missing");
const battleDepthFns = new Function(`${script.slice(battleDepthStart, battleDepthEnd)}; return {battleProjectileVerticalPosition,battleDepthPaintOrder};`)();
const depthEntries = [
  {name:"far actor",depth:180,order:-10,kind:0},
  {name:"missile",depth:180,order:1000,kind:1},
  {name:"near actor",depth:220,order:-1,kind:0},
].sort(battleDepthFns.battleDepthPaintOrder);
if (depthEntries.map(entry => entry.name).join(",") !== "far actor,missile,near actor") {
  throw new Error(`shared battle depth order failed: ${depthEntries.map(entry => entry.name).join(",")}`);
}
if (/#map-transition::after\s*\{/.test(html)) {
  throw new Error("map transition regressed: the artificial center seam must not be drawn");
}
/* A floor-fold curtain blocks gameplay routing through worldRouteInputBlocked,
   but must not become a pointer/cursor layer: the painted fish is deliberately
   above it and document-level trusted pointer samples must continue updating
   during the fold. */
const mapTransitionCSS = html.match(/#map-transition\s*\{([^}]*)\}/)?.[1] || "";
if (!/pointer-events\s*:\s*none/.test(mapTransitionCSS) || /cursor\s*:\s*none/.test(mapTransitionCSS)) {
  throw new Error("map transition must stay pointer-transparent without hiding the painted cursor");
}
/* BattleMenuProc(), BattleButtonJujutsu/Item/Pet/Waza and every extracted
   REALBIN bitmap use the executable's one 640x480 back-buffer.  Keep the
   visible rectangles checked as numbers: a later responsive-rule edit must
   neither restore the erroneous 960x720 intermediate surface nor put the
   right-hand edge past x=640. */
function cssBlocks(selector) {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return [...html.matchAll(new RegExp(`${escaped}\\s*\\{([^}]*)\\}`, "g"))].map(match => match[1]);
}
function cssPixel(block, property) {
  const match = String(block || "").match(new RegExp(`(?:^|;)\\s*${property}\\s*:\\s*(-?\\d+(?:\\.\\d+)?)px(?:\\s|;|$)`));
  return match ? Number(match[1]) : null;
}
const battleLayoutRects = [
  ["#battle-ui .battle-base", 406, 1, 220, 128],
  ...[411,464,517,570,411,464,517,570].map((left,index)=>[`#battle-ui .battle-hit:nth-of-type(${index+1})`,left,index===3?23:index<4?22:75,52,50]),
  ["#battle-pet-command-menu .battle-pet-base",405,8,220,80],
  ["#battle-pet-command-menu .battle-pet-button-art",401,11,228,77],
  ["#battle-pet-command-hit",401,11,228,77],
  ["#battle-pet-selected-skill",415,41,192,16],
  ['#battle-popup[data-kind="magic"] #battle-popup-bg',360,142,272,292],
  ['#battle-popup[data-kind="magic"] #battle-popup-list',391,179,230,140],
  ['#battle-popup[data-kind="magic"] #battle-popup-close',456,408,80,16],
  ['#battle-popup[data-kind="item"] #battle-popup-bg',360,159,272,281],
  ['#battle-popup[data-kind="item"] #battle-popup-title',360,138,272,34],
  ['#battle-popup[data-kind="item"] #battle-popup-list',368,176,255,145],
  ['#battle-popup[data-kind="item"] #battle-popup-close',456,414,80,16],
  ['#battle-popup[data-kind="pet"] #battle-popup-bg',376,132,256,320],
  ['#battle-popup[data-kind="pet"] #battle-popup-list',393,165,220,240],
  ["#battle-popup-return",398,423,96,16],
  ['#battle-popup[data-kind="pet"] #battle-popup-close',464,423,80,16],
  ['#battle-popup[data-kind="pet"][data-active-pet="1"] #battle-popup-close',523,423,80,16],
  ['#battle-popup[data-kind="pet-skill"] #battle-popup-bg',364,96,272,348],
  ['#battle-popup[data-kind="pet-skill"] #battle-popup-list',379,154,240,200],
  ['#battle-popup[data-kind="pet-skill"] #battle-popup-close',460,418,80,16],
];
for (const [selector,left,top,width,height] of battleLayoutRects) {
  const block = cssBlocks(selector)[0];
  if (!block) throw new Error(`battle layout rule missing: ${selector}`);
  const actualLeft=cssPixel(block,"left"),actualTop=cssPixel(block,"top");
  const declaredWidth=cssPixel(block,"width"),declaredHeight=cssPixel(block,"height");
  if (actualLeft!==left||actualTop!==top||(declaredWidth!==null&&declaredWidth!==width)||(declaredHeight!==null&&declaredHeight!==height)) {
    throw new Error(`battle layout mismatch: ${selector} got ${actualLeft},${actualTop},${declaredWidth},${declaredHeight}`);
  }
  if (left<0||top<0||left+width>640||top+height>480) throw new Error(`battle layout clipped: ${selector}`);
}
for (const [selector,width,height] of [["#battle-ui",640,480],["#battle-popup",640,480]]) {
  const blocks=cssBlocks(selector);if(!blocks.length)throw new Error(`battle surface missing: ${selector}`);
  const primary=blocks[0];
  if(cssPixel(primary,"width")!==width||cssPixel(primary,"height")!==height)throw new Error(`battle surface is not 640x480: ${selector}`);
  for(const block of blocks){
    const transform=String(block).match(/(?:^|;)\s*transform\s*:\s*([^;}]+)/)?.[1]?.trim();
    if(transform&&!/^none(?:\s*!important)?$/.test(transform))throw new Error(`battle surface has a second scale: ${selector} ${transform}`);
  }
}
/* BattleMap.CPP paints a fixed 640×480 viewport.  The web image must stay on
   that logical surface and must have a deterministic file fallback while the
   179 MB manifest is still loading; hiding it in that interval exposes the
   black #battle-screen background and looks like a frozen encounter. */
const battleWorldSource = script.slice(script.indexOf("  function renderBattleWorld"), script.indexOf("  function enterBattle"));
const nativeBattleMapHeader = fs.readFileSync(__dirname + "/../../vendor/upstream/code_sa_client/SYSTEMINC/BATTLEMAP.H", "latin1");
const nativeBattleNetSource = fs.readFileSync(__dirname + "/../../vendor/upstream/code_sa_client/SYSTEM/NETPROC.CPP", "latin1");
const nativeBattleMapSource = fs.readFileSync(__dirname + "/../../vendor/upstream/code_sa_client/SYSTEM/BATTLEMAP.CPP", "latin1");
if (!/#define BATTLE_MAP_FILES\s+218/.test(nativeBattleMapHeader) ||
    !/field < 0 \|\| BATTLE_MAP_FILES <= field[\s\S]{0,100}BattleMapNo = 0/.test(nativeBattleNetSource) ||
    !/if\( no >= BATTLE_MAP_FILES \) no = 0/.test(nativeBattleMapSource)) {
  throw new Error("native 2.5 battle-map range/fallback reference drifted");
}
const battleMapBoundaryStart = script.indexOf("  const BATTLE_MAP_FILES_25=218;");
const battleMapBoundaryEnd = script.indexOf("  const BATTLE_RASTER_MAP_MIN", battleMapBoundaryStart);
if (battleMapBoundaryStart < 0 || battleMapBoundaryEnd <= battleMapBoundaryStart) throw new Error("2.5 battle-map normalizer boundary missing");
const normalizeBattleMapId = new Function(`${script.slice(battleMapBoundaryStart, battleMapBoundaryEnd)};return normalizeBattleMapId;`)();
for (const [input, expected] of [[0,0],[217,217],[218,0],[219,0],[-1,0],["217",217],[12.5,0],[NaN,0]]) {
  const actual = normalizeBattleMapId(input);
  if (actual !== expected) throw new Error(`2.5 battle-map normalization drifted: ${String(input)} -> ${actual}, expected ${expected}`);
}
const enterBattleStart = script.indexOf("  function enterBattle(");
const enterBattleSource = script.slice(enterBattleStart, script.indexOf("  async function battleCommand", enterBattleStart));
if (!/#battle-map-image\s*\{[^}]*width:640px; height:480px/.test(html) ||
    !/#battle-map-canvas\s*\{[^}]*width:640px; height:480px/.test(html) ||
    !/rawBattleId=Number\(app\.battleState\?\.fieldNo\?\?app\.battleState\?\.field\)/.test(battleWorldSource) ||
    !/battleId=normalizeBattleMapId\(rawBattleId\)/.test(battleWorldSource) ||
    !/battleId>=0&&battleId<BATTLE_MAP_FILES_25/.test(battleWorldSource) ||
    !/const fieldNo=normalizeBattleMapId\(field\)/.test(enterBattleSource) ||
    !/beginBattleMusic\(type,fieldNo\)/.test(enterBattleSource) ||
    !/服务器战斗（场景 \$\{fieldNo\}）/.test(enterBattleSource) ||
    !/BATTLE_RASTER_MAP_MIN=148,BATTLE_RASTER_MAP_MAX=150,BATTLE_RASTER_CLEARANCE=8/.test(script) ||
    !/BATTLE_RASTER_PATTERN=\[0,1,2,3,4,5,6,7,8,9,10,10,11,11,11,12,/.test(script) ||
    !/function drawBattleRasterMap\(canvas,image,now=performance\.now\(\),state=app\.battleState\)/.test(script) ||
    !/sourceY=d7\+BATTLE_RASTER_PATTERN\[\(d6\+phase\)&63\]\+12/.test(script) ||
    !/const rasterKey=`\$\{epoch\}\|\$\{source\}\|\$\{phase\}`/.test(script) ||
    !/canvas\.dataset\.battleRasterKey===rasterKey/.test(script) ||
    !/state\.rasterPaintPhase=phase;state\.rasterPaintEpoch=epoch;state\.rasterPaintSource=source/.test(script) ||
    !/rasterBattleMap\?"none":"block"/.test(battleWorldSource) ||
    !/fallbackBattleFile=battleMapInRange\?`battle\/battle_\$\{String\(battleId\)\.padStart\(2,"0"\)\}\.png`/.test(battleWorldSource) ||
    !/mapNode\.dataset\.battleFallback!=="1"[\s\S]{0,700}battle\/battle_00\.png/.test(battleWorldSource) ||
    !/battleRequestedFile=requestedBattleFile[\s\S]{0,320}battleFallback="1"[\s\S]{0,180}mapNode\.src=fallbackSource/.test(battleWorldSource) ||
    !/const rasterNeedsPaint=rasterActive&&rasterReady&&Number\(state\.rasterPaintPhase\)!==rasterPhase/.test(script) ||
    !/transient\|\|rasterNeedsPaint/.test(script) ||
    /transient\|\|rasterActive\)/.test(script)) {
  throw new Error("battle map fixed viewport/fallback path regressed");
}
/* A title/server-list failure is a native commonMsgWin, not a chat/event
   line.  Keeping the diagnostic in app.events made it reappear underneath
   the list after the next successful login. */
const failureSource = script.slice(script.indexOf("function showConnectionFailure"), script.indexOf("function returnToAccountLogin"));
if (/addEvent\(/.test(failureSource)) throw new Error("connection failure leaked into the event list");
/* SAAC may answer one CharList request with a transient `locked` status
   while releasing an abandoned session.  The native flow retries once after
   the lock is released; other errors must go straight to the common dialog.
   Keep these source contracts beside the title failure checks so a future
   refactor cannot reintroduce a second click or an infinite reconnect loop. */
const charListRetryStart = script.indexOf("const CHAR_LIST_LOCK_RETRY_DELAY_MS");
const charListRetryEnd = script.indexOf("function returnToAccountLogin", charListRetryStart);
if (charListRetryStart < 0 || charListRetryEnd <= charListRetryStart) {
  throw new Error("transient CharList lock retry path missing");
}
const charListRetrySource = script.slice(charListRetryStart, charListRetryEnd);
if (!/CHAR_LIST_LOCK_RETRY_DELAY_MS=350/.test(charListRetrySource) ||
    !/function retryLockedCharacterList\(reason\)/.test(charListRetrySource) ||
    !/toLowerCase\(\)!=="locked"\|\|Number\(app\.charListLockRetryCount\|\|0\)>0\)return false/.test(charListRetrySource) ||
    !/app\.charListLockRetryCount=1/.test(charListRetrySource) ||
    !/closeTransport\(\);[\s\S]{0,180}openServerSelection\("connecting"/.test(charListRetrySource) ||
    !/window\.setTimeout\(\(\)=>\{[\s\S]{0,320}connectLogin\(\)\.catch/.test(charListRetrySource) ||
    !/\},CHAR_LIST_LOCK_RETRY_DELAY_MS\)/.test(charListRetrySource)) {
  throw new Error("CharList lock retry must be delayed, one-shot, and reconnect through connectLogin");
}
const receiveCharacterListSource = script.slice(script.indexOf("function receiveCharacterList"), script.indexOf("function escapeHTML", script.indexOf("function receiveCharacterList")));
if (!/if\(retryLockedCharacterList\(reason\)\)return;/.test(receiveCharacterListSource) ||
    !/showConnectionFailure\(reason,"读取人物列表失败，请稍后重试。"\)/.test(receiveCharacterListSource)) {
  throw new Error("CharList failures must retry only locked responses and otherwise show the common dialog");
}
const serverSelectionSource = script.slice(script.indexOf("function renderServerSelection"), script.indexOf("function openServerSelection"));
if (!/if\(stage==="group"\)app\.charListLockRetryCount=0/.test(serverSelectionSource)) {
  throw new Error("new server-list visits must reset the CharList retry budget");
}
/* The stock 2.5 MENU.CPP has no _NEW_SYSTEM_MENU/SaMenu branch. Keep the
   browser menu honest: only the implemented logout paths and local chat/audio
   settings are visible, and no placeholder is allowed to fall back to a
   server notice after a click. */
const systemMenuStart = script.indexOf("function renderSystem()");
if (systemMenuStart < 0) throw new Error("system menu renderer missing");
const systemMenuSource = script.slice(systemMenuStart);
for (const label of ["官方主页","我的邮箱","原地遇敌","取消原地","支票制作","任务查询","个人信息","在线充值","卡密使用","快捷传送","掉线重连","战力详情","捕鱼达人","成就排行","滑鼠设定"]) {
  if (systemMenuSource.includes(`"${label}"`)) throw new Error(`8.5-only system menu entry leaked: ${label}`);
}
for (const expected of [
  /add\("退出游戏",openLogoutChoice,0\)/,
  /add\("聊天设定",[\s\S]{0,120}systemPage="chat"/,
  /add\("背景音乐",[\s\S]{0,120}systemPage="bgm"/,
  /add\("音效设定",[\s\S]{0,120}systemPage="se"/,
  /const close=systemChoice\("关闭",closeGameplayOverlay,172\);list\.append\(close\)/,
  /if\(page==="logout-choice"\)[\s\S]{0,350}systemChoice\("回记录点",\(\)=>openLogoutConfirm\("record"\)[\s\S]{0,220}systemChoice\("原地登出",\(\)=>openLogoutConfirm\("in-place"\)/,
]) {
  if (!expected.test(systemMenuSource)) throw new Error(`2.5 system menu regression: ${expected}`);
}
for (const expected of [
  /const specs=\{menu:\{x:4,y:4,w:192,h:288,title:9145\}/,
  /"logout-choice":\{x:4,y:4,w:192,h:144,title:9146\}/,
  /chat:\{x:4,y:4,w:256,h:384,title:9148\}/,
  /bgm:\{x:4,y:4,w:256,h:384,title:9149\}/,
  /se:\{x:4,y:4,w:256,h:288,title:9150\}/,
]) {
  if (!expected.test(script)) throw new Error(`2.5 system window geometry regression: ${expected}`);
}
if(!/#system-screen\.system-page-menu\{[^}]*--legacy-h:288px/.test(html)){
  throw new Error("2.5 system root menu must retain the native 3x6 (192x288) frame");
}
if (systemMenuSource.includes("sendLegacySystemMenu") || systemMenuSource.includes("systemPage=\"auto\"")) {
  throw new Error("system menu still contains an unsupported server-extension path");
}
if (/id="system-(?:server|logout|close)"/.test(html) || /\$\("system-(?:server|logout|close)"\)/.test(script)) {
  throw new Error("system menu retains a hidden non-native control");
}
if (html.includes("system-close") || script.includes('"system-close"')) {
  throw new Error("system menu retains a stale close handler");
}
if (!/fetch\(`\/audio\/pal\/Palet_\$\{id\}\.sap`/.test(script)) {
  throw new Error("map palette loader must use the published audio/pal tree");
}
/* CHAR_FS_* is sparse in the 2.5 server.  FIELD.CPP exposes five settings
   rows; the fifth enables incoming trade requests with CHAR_FS_TRADE (bit
   5).  The wheel is the separate TD action that starts a trade. */
for (const expected of [
  /FIELD_SETTING_BITS=Object\.freeze\(\{party:1,duel:4,mail:16,chat:8,trade:32\}\)/,
  /setStatus\("duelAllowed",Boolean\(flags&4\)\)/,
]) {
  if (!expected.test(html)) throw new Error(`field protocol regression: ${expected}`);
}
const protocolEnd = script.indexOf("  class HTTPTransport");
if (protocolEnd < 0) throw new Error("protocol boundary missing");
/* Keep the generated legacy resources in the same regression gate as the
   movie scheduler.  The core manifest is scanned line by line; the optional
   compact sprite resource is parsed separately.  Every SPR frame has one
   bitmap file and must now have exactly one numeric SoundNo; those native
   events are what make contact and combo timing deterministic. */
function scanGeneratedBattleAssets(filename) {
  const filenames = Array.isArray(filename) ? filename : [filename];
  const buffer = Buffer.allocUnsafe(1024 * 1024);
  let carry = "";
  let inSprites = false;
  let inBattles = false;
  let foundSprites = false;
  let foundBattles = false;
  let currentSprite = "";
  let currentDirection = null;
  let currentAction = null;
  let lastFrameFile = "";
  const result = {
    spriteFrames: 0,
    soundRows: 0,
    invalidSoundRows: 0,
    contactFrames: 0,
    comboFrames: 0,
    heroAttackHasContact: false,
    heroDirection3AttackFrames: 0,
    heroDirection3ContactBitmap: "",
    heroDirection3DeadFrames: 0,
    heroDirection3DeadLastBitmap: "",
    heroDirection3ActionUniqueFrames: {},
    playerActionRows: 0,
    playerActionRowsComplete: true,
    battleCount: 0,
  };
  const consumeSpritePayload = payload => {
    const sprites = payload?.sprites && typeof payload.sprites === "object" ? payload.sprites : payload;
    if (!sprites || typeof sprites !== "object") return;
    foundSprites = true;
    for (const spriteNumber of Array.from({length: 12}, (_, index) => String(100000 + index * 20))) {
      const rows = sprites[spriteNumber]?.actions || [];
      for (let action = 0; action <= 12; action++) for (let direction = 0; direction < 8; direction++) {
        const row = rows.find(item => Number(item?.action) === action && Number(item?.direction) === direction);
        if (row && Array.isArray(row.frames) && row.frames.length) result.playerActionRows += 1;
        else result.playerActionRowsComplete = false;
      }
    }
    for (const [spriteNumber, sprite] of Object.entries(sprites)) {
      for (const animation of sprite?.actions || []) {
        const direction = Number(animation?.direction);
        const action = Number(animation?.action);
        for (const frame of animation?.frames || []) {
          const file = String(frame?.file || "");
          if (!/^bitmaps\/bitmap_\d+\.png$/.test(file)) continue;
          let lastFrameFile = file;
          result.spriteFrames += 1;
          if (spriteNumber === "100000" && direction === 3 && action === 0) result.heroDirection3AttackFrames += 1;
          if (spriteNumber === "100000" && direction === 3 && action === 2) {
            result.heroDirection3DeadFrames += 1;
            result.heroDirection3DeadLastBitmap = lastFrameFile;
          }
          const sound = Number(frame?.sound);
          if (!Number.isFinite(sound)) { result.invalidSoundRows += 1; continue; }
          result.soundRows += 1;
          if (sound >= 10000 && sound < 10100) result.contactFrames += 1;
          if (sound >= 10100) result.comboFrames += 1;
          if (spriteNumber === "100000" && action === 0 && sound === 10000) {
            result.heroAttackHasContact = true;
            if (direction === 3) result.heroDirection3ContactBitmap = lastFrameFile;
          }
        }
      }
    }
    const heroRows=(sprites["100000"]?.actions||[]).filter(animation=>Number(animation?.direction)===3);
    for(const animation of heroRows){
      const action=Number(animation?.action),frames=Array.isArray(animation?.frames)?animation.frames:[];
      if(action>=0&&action<=12)result.heroDirection3ActionUniqueFrames[action]=new Set(frames.map(frame=>`${frame?.file||""}|${Number(frame?.xoffset)||0}|${Number(frame?.yoffset)||0}`)).size;
    }
  };
  const visit = line => {
    if (line === '  "sprites": {') {
      inSprites = true;
      foundSprites = true;
      return;
    }
    if (inSprites && line === '  "faces": {') {
      inSprites = false;
      currentSprite = "";
      currentDirection = null;
      currentAction = null;
      lastFrameFile = "";
    }
    if (line === '  "battles": {') {
      inBattles = true;
      foundBattles = true;
      return;
    }
    if (inBattles && line === '  "battle": {') inBattles = false;
    if (inBattles && /^    "\d+": \{$/.test(line)) result.battleCount += 1;
    if (!inSprites) return;
    const sprite = line.match(/^    "(\d+)": \{$/);
    if (sprite) {
      currentSprite = sprite[1];
      currentDirection = null;
      currentAction = null;
      lastFrameFile = "";
      return;
    }
    const direction = line.match(/^\s+"direction":\s*(-?\d+)\s*,?$/);
    if (direction) currentDirection = Number(direction[1]);
    const action = line.match(/^\s+"action":\s*(-?\d+)\s*,?$/);
    if (action) currentAction = Number(action[1]);
    const file = line.match(/^\s+"file":\s*"(bitmaps\/bitmap_\d+\.png)"\s*,?$/);
    if (file) {
      lastFrameFile = file[1];
      result.spriteFrames += 1;
      if (currentSprite === "100000" && currentDirection === 3 && currentAction === 0) result.heroDirection3AttackFrames += 1;
      if (currentSprite === "100000" && currentDirection === 3 && currentAction === 2) {
        result.heroDirection3DeadFrames += 1;
        result.heroDirection3DeadLastBitmap = lastFrameFile;
      }
    }
    if (!line.includes('"sound":')) return;
    const sound = line.match(/^\s+"sound":\s*(-?\d+(?:\.\d+)?)\s*,?\s*$/);
    if (!sound) {
      result.invalidSoundRows += 1;
      return;
    }
    const value = Number(sound[1]);
    result.soundRows += 1;
    if (value >= 10000 && value < 10100) result.contactFrames += 1;
    if (value >= 10100) result.comboFrames += 1;
    if (currentSprite === "100000" && currentAction === 0 && value === 10000) {
      result.heroAttackHasContact = true;
      if (currentDirection === 3) result.heroDirection3ContactBitmap = lastFrameFile;
    }
  };
  for (const currentFilename of filenames) {
    if (/sprites\.json$/.test(currentFilename)) {
      /* The split sprite resource is intentionally compact JSON so it can be
         fetched after the first map.  Validate it directly; the core
         manifest remains scanned line-by-line below to keep the large legacy
         battle index out of the test heap. */
      consumeSpritePayload(JSON.parse(fs.readFileSync(currentFilename, "utf8")));
      continue;
    }
    const fd = fs.openSync(currentFilename, "r");
    /* Split packs put SPR tables in sprites.json and battle/map metadata in
       manifest.json.  Reset section state at each file boundary while
       retaining the accumulated counters. */
    inSprites = false;
    inBattles = false;
    carry = "";
    try {
      for (;;) {
        const count = fs.readSync(fd, buffer, 0, buffer.length, null);
        if (!count) break;
        const lines = (carry + buffer.toString("utf8", 0, count)).split("\n");
        carry = lines.pop();
        for (const line of lines) visit(line);
      }
      if (carry) visit(carry);
    } finally {
      fs.closeSync(fd);
    }
  }
  if (!foundSprites || !foundBattles) throw new Error("generated battle resource sections missing");
  return result;
}
const generatedBattleAssets = scanGeneratedBattleAssets([
  __dirname + "/assets/original/manifest.json",
  __dirname + "/assets/original/sprites.json",
].filter(filename => fs.existsSync(filename)));
if (
  generatedBattleAssets.spriteFrames === 0 ||
  generatedBattleAssets.soundRows !== generatedBattleAssets.spriteFrames ||
  generatedBattleAssets.invalidSoundRows !== 0 ||
  generatedBattleAssets.contactFrames === 0 ||
  generatedBattleAssets.comboFrames === 0 ||
  !generatedBattleAssets.heroAttackHasContact ||
  generatedBattleAssets.heroDirection3AttackFrames !== 12 ||
  generatedBattleAssets.heroDirection3ContactBitmap !== "bitmaps/bitmap_10308.png" ||
  generatedBattleAssets.heroDirection3DeadFrames !== 6 ||
  generatedBattleAssets.heroDirection3DeadLastBitmap !== "bitmaps/bitmap_10321.png" ||
  generatedBattleAssets.playerActionRows !== 12 * 13 * 8 ||
  !generatedBattleAssets.playerActionRowsComplete ||
  [0,1,2,3,4,6,7,8,9,11,12].some(action=>Number(generatedBattleAssets.heroDirection3ActionUniqueFrames[action]||0)<2) ||
  generatedBattleAssets.battleCount !== BATTLE_MAP_FILES_25
) {
  throw new Error(`generated SPR/battle resource regression: ${JSON.stringify(generatedBattleAssets)}`);
}
/* SPR SoundNo is part of the native animation timeline, not optional image
   metadata.  Verify the pure timing helper with one SE frame, one ordinary
   contact and one combo contact so attack scheduling cannot regress to a
   guessed whole-movie percentage. */
const spriteTimingStart = script.indexOf("  function spriteAnimationTiming");
const spriteTimingEnd = script.indexOf("  function battleActorAnimationTiming", spriteTimingStart);
if (spriteTimingStart < 0 || spriteTimingEnd <= spriteTimingStart) throw new Error("sprite timing helper boundary missing");
/* The preserved 2.5 GAMEMAIN.CPP uses a 60Hz ProcTime (16.666667ms).  The
   8ms clock belongs to the later _OPTIMIZATIONFLIP_ build and must not drive
   this 2.5 battle timing. */
const nativeProcTickMs = 1000 / 60;
const nearlyEqual = (actual, expected, epsilon = 1e-9) => Math.abs(Number(actual) - Number(expected)) <= epsilon;
const spriteAnimationTiming = new Function("BATTLE_PROC_TICK_MS", `${script.slice(spriteTimingStart, spriteTimingEnd)}; return spriteAnimationTiming;`)(nativeProcTickMs);
const nativeAttackTiming = spriteAnimationTiming({frame_ms:5,frames:[{sound:51},{sound:0},{sound:10000},{sound:10100}]});
if (!nativeAttackTiming || !nearlyEqual(nativeAttackTiming.duration, 5 * 4 * nativeProcTickMs) || !nearlyEqual(nativeAttackTiming.contactOffset, 5 * 2 * nativeProcTickMs) || nativeAttackTiming.contacts.length !== 2 || nativeAttackTiming.soundEvents.find(event=>event.sound===10000)?.kind !== "contact" || nativeAttackTiming.soundEvents.find(event=>event.sound===10100)?.kind !== "combo") {
  throw new Error(`native SPR timing failed: ${JSON.stringify(nativeAttackTiming)}`);
}
const contactTimingStart = script.indexOf("  const BATTLE_CONTACT_HOLD_NORMAL_TICKS");
const contactTimingEnd = script.indexOf("  function battleAttackMotionSpec", contactTimingStart);
if (contactTimingStart < 0 || contactTimingEnd <= contactTimingStart) throw new Error("contact hold helper boundary missing");
const battleContactTiming = new Function("BATTLE_FLAG","BATTLE_PROC_TICK_MS",`${script.slice(contactTimingStart,contactTimingEnd)}; return {battleLegacyTravelDuration,battleNativeTickProgress,battleNativeSpeedProgress,battleLegacyHitProfile,battleContactHoldDuration,battleTimelineOffsetForFrame,battleAnimationElapsedWithHolds};`)(
  {death:1,critical:4,ultimate1:64,ultimate2:128,reflect:1024},
  nativeProcTickMs,
);
if (!nearlyEqual(battleContactTiming.battleLegacyTravelDuration(192,32,0), 24 * nativeProcTickMs) || !nearlyEqual(battleContactTiming.battleLegacyTravelDuration(192,32,64), 16 * nativeProcTickMs) || !nearlyEqual(battleContactTiming.battleLegacyHitProfile(0).knockbackDuration, 8 * nativeProcTickMs) || !nearlyEqual(battleContactTiming.battleLegacyHitProfile(1).knockbackDuration, 32 * nativeProcTickMs)) {
  throw new Error(`native movement profile failed: ${JSON.stringify({travel:battleContactTiming.battleLegacyTravelDuration(192,32,0),normal:battleContactTiming.battleLegacyHitProfile(0),heavy:battleContactTiming.battleLegacyHitProfile(1)})}`);
}
const normalContactHold = battleContactTiming.battleContactHoldDuration(0);
const heavyContactHold = battleContactTiming.battleContactHoldDuration(1);
const zeroDamageContactHold = battleContactTiming.battleContactHoldDuration(0,0,0);
if (!nearlyEqual(normalContactHold, 8 * nativeProcTickMs) || !nearlyEqual(heavyContactHold, 32 * nativeProcTickMs) || zeroDamageContactHold !== 0 || !nearlyEqual(battleContactTiming.battleTimelineOffsetForFrame(60,[{frameOffset:30,timelineOffset:30,duration:normalContactHold}]), 30 + 30 + normalContactHold) || !nearlyEqual(battleContactTiming.battleAnimationElapsedWithHolds(54,[{frameOffset:30,timelineOffset:30,duration:normalContactHold}]), 30)) {
  throw new Error(`native contact hold timing failed: ${JSON.stringify({normalContactHold,heavyContactHold,zeroDamageContactHold})}`);
}
const terminalHoldStart = script.indexOf("  function battleTerminalHoldUntil");
const terminalHoldEnd = script.indexOf("  function battleMoviePending", terminalHoldStart);
const battleTerminalHoldUntil = new Function(`${script.slice(terminalHoldStart, terminalHoldEnd)}; return battleTerminalHoldUntil;`)();
if (battleTerminalHoldUntil({motionQueueAt:100,pendingDamageUntil:260,motions:[],projectiles:[]}) !== 260 || !/pendingDamageUntil/.test(script.slice(terminalHoldStart, terminalHoldEnd))) {
  throw new Error("terminal hold does not include pending contact damage");
}
const attackSequenceStart = script.indexOf("  function battleAttackSequenceSpec");
const attackSequenceEnd = script.indexOf("  function battleQueueDeath", attackSequenceStart);
if (attackSequenceStart < 0 || attackSequenceEnd <= attackSequenceStart) throw new Error("attack sequence helper boundary missing");
const attackSequenceHelpers = new Function("battleSlotPoint","battleFacingDirection","battleActorAnimationTiming","battleSlotDirection","battleContactHoldDuration","battleTimelineOffsetForFrame","battleLegacyTravelDuration","BATTLE_PROC_TICK_MS","BATTLE_FLAG",`${script.slice(attackSequenceStart,attackSequenceEnd)}; return {battleAttackSequenceSpec,battleCounterAttackMotionSpec,battleCounterExchangeSpec};`)(
  id=>id===0?[0,0]:id===10?[320,160]:[360,200],
  ()=>6,
  ()=>({duration:120,contactOffset:30,nativeContact:true,contacts:[{offset:30},{offset:60}],soundEvents:[{sound:51,offset:0,kind:"sfx"}]}),
  ()=>3,
  battleContactTiming.battleContactHoldDuration,
  battleContactTiming.battleTimelineOffsetForFrame,
  battleContactTiming.battleLegacyTravelDuration,
  nativeProcTickMs,
  {dodge:32},
);
const {battleAttackSequenceSpec,battleCounterExchangeSpec}=attackSequenceHelpers;
const sameTargetSequence = battleAttackSequenceSpec(0,[10,10],0);
if (sameTargetSequence.steps.length !== 1 || sameTargetSequence.hits.length !== 2 || !nearlyEqual(sameTargetSequence.hits[1].contactOffset - sameTargetSequence.hits[0].contactOffset, 30 + normalContactHold) || sameTargetSequence.returnStartOffset <= sameTargetSequence.hits[1].contactOffset) {
  throw new Error(`BH combo sequence returned home between hits: ${JSON.stringify(sameTargetSequence)}`);
}
const zeroDamageSequence = battleAttackSequenceSpec(0,[10,10],0,[0,0],[0,0],[0,0]);
if (zeroDamageSequence.hits.length !== 2 || zeroDamageSequence.hits[1].contactOffset - zeroDamageSequence.hits[0].contactOffset !== 30 || zeroDamageSequence.duration >= sameTargetSequence.duration) {
  throw new Error(`zero-damage BH should not add HIT_STOP: ${JSON.stringify({zeroDamageSequence,sameTargetSequence})}`);
}
/* In oft.cpp the next tuple's ATT_COUNTER is inspected while the original
   VCT2 is still running.  A dodging defender walks out for 20 ticks, waits
   only for VCT2 to end, walks back for 20 ticks and counters the attacker at
   the existing 64-pixel contact point.  The first attacker may return only
   after that reverse hit/reaction has completed. */
const counterExchange=battleCounterExchangeSpec(0,[10],[{wireIndex:1,attacker:10,target:0,flags:16,amount:16,petAmount:0,reactionDuration:900}],0,[32],[0],[0]);
const counterPlan=counterExchange.counters[0],primaryHit=counterExchange.normal.hits[0];
if (!counterPlan ||
    counterExchange.counterStartOffset < primaryHit.contactOffset+40*nativeProcTickMs ||
    counterExchange.counterStartOffset < counterExchange.normalAttackEndOffset+20*nativeProcTickMs ||
    counterPlan.motion.kind !== "counter-attack" || counterPlan.motion.approachDuration !== 0 ||
    !nearlyEqual(counterPlan.motion.contactDistance,64,0.01) ||
    counterPlan.contactOffset >= counterExchange.normal.returnStartOffset ||
    counterExchange.normal.returnStartOffset < counterPlan.reactionEndOffset ||
    counterExchange.normal.duration < counterExchange.normal.returnStartOffset+counterExchange.normal.returnDuration) {
  throw new Error(`dodge/counter chain returned the first attacker too early: ${JSON.stringify(counterExchange)}`);
}
/* A plain f2 hit followed by f10 is the canonical 2.5 counter example.  The
   defender leaves DAMAGE at the end of VCT11, so its reverse attack must begin
   before the first attack's ordinary return boundary and from the short
   HIT_STOP displacement, while the master remains parked at contact. */
const plainCounterExchange=battleCounterExchangeSpec(0,[10],[{wireIndex:1,attacker:10,target:0,flags:16,amount:16,petAmount:0,reactionDuration:900,reactionBeforeReturnDuration:400,reactionOffsetX:-18,reactionOffsetY:-9}],0,[2],[32],[0]);
const plainCounterPlan=plainCounterExchange.counters[0],plainPrimaryHit=plainCounterExchange.normal.hits[0];
if (!plainCounterPlan || !plainPrimaryHit.counterTakeover ||
    !nearlyEqual(plainCounterExchange.counterStartOffset,plainPrimaryHit.contactOffset+nativeProcTickMs+normalContactHold) ||
    plainCounterExchange.counterStartOffset >= plainCounterExchange.normalAttackEndOffset ||
    !nearlyEqual(Math.hypot(plainCounterPlan.actorOrigin.x,plainCounterPlan.actorOrigin.y),32,0.01) ||
    !nearlyEqual(plainCounterPlan.motion.contactDistance,96,0.02) ||
    plainCounterPlan.motion.approachDuration !== 0 || !plainCounterPlan.masterReturn ||
    plainCounterExchange.normal.returnStartOffset < plainCounterPlan.contactOffset+400 ||
    nearlyEqual(plainCounterExchange.normal.returnFromX,plainCounterPlan.targetOrigin.x) ||
    plainCounterExchange.normal.duration < plainCounterExchange.normal.returnStartOffset+plainCounterExchange.normal.returnDuration) {
  throw new Error(`plain-hit counter did not exchange at VCT11: ${JSON.stringify(plainCounterExchange)}`);
}
const battleMovieSource = script.slice(script.indexOf("  function battleMovieEffects"), script.indexOf("  const pendingBattlePacketTTL"));
if (/\.52/.test(battleMovieSource) || !/contactAt:start\+Math\.max\(0,Number\(motion\?\.contactOffset\)\|\|0\)/.test(battleMovieSource) || !/normalSequence=scheduleAttackSequence\(attacker,normalTargets,0,normalFlags,normalDamageValues,normalPetValues\)/.test(battleMovieSource)) {
  throw new Error("battle attacks still guess the impact frame instead of using SPR SoundNo");
}
if (!/amount>=hp&&amount>0\)value\|=BATTLE_FLAG\.death/.test(battleMovieSource) || !/projectedHp=new Map\(\)/.test(battleMovieSource)) {
  throw new Error("lethal BH damage is not threaded into the contact timeline");
}
if (!/battleCounterExchangeSpec\(attacker,normalTargets,counterEntries/.test(battleMovieSource) ||
    !/const exchange=counterEntries\.length\?battleCounterExchangeSpec/.test(battleMovieSource) ||
    !/scheduleAttackSequenceSpec\(exchange\.normal,exchange\.counterStartOffset\)/.test(battleMovieSource) ||
    !/counterPending=true;hit\.counterWaitUntil=counterWaitUntil/.test(battleMovieSource) ||
    !/counterTakeover:Boolean\(timing\?\.counterTakeover\)/.test(battleMovieSource) ||
    !/counterMasterReturn:Boolean\(planned\?\.masterReturn\)/.test(battleMovieSource) ||
    !/planned\?\.timing\|\|scheduleAttackPair/.test(battleMovieSource)) {
  throw new Error("BH ATT_COUNTER tuples are not using the native VCT11/dodge contact chain");
}
const battleMotionSource=script.slice(script.indexOf("  function battleMotionValue"),script.indexOf("  function battleNamesVisible"));
if (!/if\(elapsed<returnStart\)\{[\s\S]{0,650}value\.action=3/.test(battleMotionSource) ||
    !/motion\.kind===\"attack\"\|\|motion\.kind===\"counter-attack\"/.test(battleMotionSource) ||
    !/const outDuration=Math\.max\(BATTLE_PROC_TICK_MS[\s\S]{0,500}backStart=outDuration\+waitDuration/.test(battleMotionSource) ||
    !/toX:vector\[0\]\*80,toY:vector\[1\]\*80/.test(battleMovieSource) ||
    /Math\.sin\(Math\.min\(1,progress\)\*Math\.PI\)/.test(battleMotionSource)) {
  throw new Error("battle renderer lost the parked attacker or discrete VCT16/VCT18 dodge phases");
}
const timingFallbackSource = script.slice(script.indexOf("  function battleActorAnimationTiming"), script.indexOf("  function actorFrame", script.indexOf("  function battleActorAnimationTiming")));
if (!/if\(!animation\)animation=spriteAnimationForAction\(actor,direction,3\)/.test(timingFallbackSource) || !/entry\?\.sprite\?\.actions/.test(timingFallbackSource)) {
  throw new Error("battle timing does not follow actorFrame action fallback");
}
const hitMotionStart = script.indexOf("  function battleHitMotionSpec");
const hitMotionEnd = script.indexOf("  function battleQueueDeath", hitMotionStart);
if (hitMotionStart < 0 || hitMotionEnd <= hitMotionStart) throw new Error("hit motion helper boundary missing");
const battleHitMotionSpec = new Function("battleSlotDirection","battleActorAnimationTiming","battleLegacyHitProfile","battleLegacyTravelDuration","BATTLE_FLAG","BATTLE_PROC_TICK_MS","BATTLE_RADAR_DIRECTIONS",`${script.slice(hitMotionStart,hitMotionEnd)}; return battleHitMotionSpec;`)(
  ()=>3,
  (_id,_direction,action)=>({duration:action===10?210:240}),
  battleContactTiming.battleLegacyHitProfile,
  battleContactTiming.battleLegacyTravelDuration,
  {guard:8},
  nativeProcTickMs,
  [6,7,0,1,2,3,4,5],
);
const guardMotion = battleHitMotionSpec(10,3,8),hurtMotion = battleHitMotionSpec(10,3,0);
if (guardMotion.kind !== "guard" || hurtMotion.kind !== "hit" || hurtMotion.knockbackDuration <= 0 || hurtMotion.returnDuration <= 0) {
  throw new Error(`native guard/hurt motion failed: ${JSON.stringify({guardMotion,hurtMotion})}`);
}
/* Exercise the compositor's runtime positions, not only its planning
   offsets.  A counter has two positions for the original attacker: the
   first contact point where it waits for the reverse hit, and the displaced
   point from which VCT4 returns after that hit.  They must never be
   conflated, especially during the longer VCT16..18 dodge branch. */
const counterMotionRuntimeStart = script.indexOf("  function battleMotionRenderPriority");
const counterMotionRuntimeEnd = script.indexOf("  function battleNamesVisible", counterMotionRuntimeStart);
if (counterMotionRuntimeStart < 0 || counterMotionRuntimeEnd <= counterMotionRuntimeStart) throw new Error("battle counter runtime helper boundary missing");
const counterMotionRuntime = new Function("BATTLE_PROC_TICK_MS","battleSlotDirection","battleNativeTickProgress","battleAnimationElapsedWithHolds","battleNativeSpeedProgress",`${script.slice(counterMotionRuntimeStart,counterMotionRuntimeEnd)}; return {battleMotionRenderPriority,battleMotionValue};`)(
  nativeProcTickMs,
  id=>Number(id)<10?3:7,
  battleContactTiming.battleNativeTickProgress,
  battleContactTiming.battleAnimationElapsedWithHolds,
  battleContactTiming.battleNativeSpeedProgress,
);
const counterRuntimeBase = 1_000_000;
function counterRuntimeMotion(motion,startOffset,duration=motion?.duration){
  const length=Math.max(1,Number(duration)||1),start=Math.max(0,Number(startOffset)||0);
  return {...motion,startedAt:counterRuntimeBase,delay:start,duration:length,until:counterRuntimeBase+start+length};
}
function counterMasterReaction(plan){
  const reaction=battleHitMotionSpec(plan.target,plan.motion.direction,plan.flags);
  reaction.originX=Number(plan.targetOrigin?.x)||0;reaction.originY=Number(plan.targetOrigin?.y)||0;
  reaction.returnDuration=0;
  reaction.duration=Math.max(nativeProcTickMs,Number(reaction.vct10Duration||0)+Number(reaction.knockbackDuration||0)+Number(reaction.decelDuration||0)+Number(reaction.pauseDuration||0));
  reaction.nativeTiming=true;reaction.counterMasterReturn=true;
  return reaction;
}
function counterTakeoverReaction(exchange,flags){
  const hit=exchange.normal.hits[0],reaction=battleHitMotionSpec(hit.target,hit.direction,flags);
  reaction.decelDuration=0;reaction.decelSpeeds=[];reaction.pauseDuration=0;reaction.returnDuration=0;reaction.decelX=0;reaction.decelY=0;
  reaction.duration=Math.max(nativeProcTickMs,Number(reaction.vct10Duration||0)+Number(reaction.knockbackDuration||0));reaction.nativeTiming=true;reaction.counterTakeover=true;
  return reaction;
}
function sampleCounterExchange(exchange,{dodge=false,primaryFlags=0}={}){
  const plan=exchange.counters[0],hit=exchange.normal.hits[0],motions=[counterRuntimeMotion(exchange.normal,0,exchange.normal.duration),counterRuntimeMotion(plan.motion,plan.startOffset,plan.motion.duration)];
  if(dodge){
    const out=20*nativeProcTickMs,back=20*nativeProcTickMs,wait=Math.max(0,exchange.counterStartOffset-hit.contactOffset-out-back);
    motions.push(counterRuntimeMotion({kind:"dodge",target:hit.target,direction:hit.direction,toX:-80,toY:0,originX:0,originY:0,outDuration:out,waitDuration:wait,backDuration:back},hit.contactOffset,out+wait+back));
  }else motions.push(counterRuntimeMotion(counterTakeoverReaction(exchange,primaryFlags),hit.contactOffset,exchange.counterStartOffset-hit.contactOffset));
  const reaction=counterMasterReaction(plan);motions.push(counterRuntimeMotion(reaction,plan.contactOffset,reaction.duration));
  return {state:{motions},plan,hit,reaction,value:(id,offset)=>counterMotionRuntime.battleMotionValue({motions},id,counterRuntimeBase+offset)};
}
const runtimeCounterReaction=battleHitMotionSpec(0,6,16),runtimeReactionBeforeReturn=Number(runtimeCounterReaction.vct10Duration||0)+Number(runtimeCounterReaction.knockbackDuration||0)+Number(runtimeCounterReaction.decelDuration||0)+Number(runtimeCounterReaction.pauseDuration||0),runtimeCounterEntry={wireIndex:1,attacker:10,target:0,flags:16,amount:16,petAmount:0,reactionDuration:runtimeCounterReaction.duration,reactionBeforeReturnDuration:runtimeReactionBeforeReturn,reactionOffsetX:Number(runtimeCounterReaction.knockbackX||0)+Number(runtimeCounterReaction.decelX||0),reactionOffsetY:Number(runtimeCounterReaction.knockbackY||0)+Number(runtimeCounterReaction.decelY||0)},runtimePlainCounterExchange=battleCounterExchangeSpec(0,[10],[runtimeCounterEntry],0,[2],[32],[0]),runtimeDodgeCounterExchange=battleCounterExchangeSpec(0,[10],[runtimeCounterEntry],0,[32],[0],[0]);
for(const [label,exchange,options] of [["plain",runtimePlainCounterExchange,{primaryFlags:2}],["dodge",runtimeDodgeCounterExchange,{dodge:true,primaryFlags:32}]]){
  const runtime=sampleCounterExchange(exchange,options),park=runtime.plan.targetOrigin;
  const beforeImpactOffset=Math.max(runtime.plan.startOffset,runtime.plan.contactOffset-nativeProcTickMs/2),beforeImpact=runtime.value(0,beforeImpactOffset),counterActor=runtime.value(runtime.plan.attacker,beforeImpactOffset);
  if(!nearlyEqual(beforeImpact.dx,park.x,0.02)||!nearlyEqual(beforeImpact.dy,park.y,0.02)||!nearlyEqual(counterActor.dx,runtime.plan.actorOrigin.x,0.02)||!nearlyEqual(counterActor.dy,runtime.plan.actorOrigin.y,0.02)){
    throw new Error(`${label} counter moved an actor before reverse contact: ${JSON.stringify({beforeImpact,counterActor,park,plan:runtime.plan})}`);
  }
  const beforeReturn=runtime.value(0,exchange.returnStartOffset-nativeProcTickMs/2),expectedReturnX=Number(park.x)+Number(runtime.reaction.knockbackX||0)+Number(runtime.reaction.decelX||0),expectedReturnY=Number(park.y)+Number(runtime.reaction.knockbackY||0)+Number(runtime.reaction.decelY||0);
  if(!nearlyEqual(beforeReturn.dx,expectedReturnX,0.02)||!nearlyEqual(beforeReturn.dy,expectedReturnY,0.02)||!beforeReturn.hit){
    throw new Error(`${label} counter did not finish its reverse reaction before VCT4: ${JSON.stringify({beforeReturn,expectedReturnX,expectedReturnY})}`);
  }
  const duringReturn=runtime.value(0,exchange.returnStartOffset+exchange.normal.returnDuration/2),returned=runtime.value(0,exchange.normal.duration+nativeProcTickMs);
  if(duringReturn.action!==4||Math.hypot(duringReturn.dx,duringReturn.dy)>=Math.hypot(expectedReturnX,expectedReturnY)||Math.hypot(duringReturn.dx,duringReturn.dy)<=0||!nearlyEqual(returned.dx,0,0.02)||!nearlyEqual(returned.dy,0,0.02)){
    throw new Error(`${label} counter VCT4 runtime return is not serialized: ${JSON.stringify({duringReturn,returned,expectedReturnX,expectedReturnY})}`);
  }
}
const deathSource = script.slice(script.indexOf("  function battleQueueDeath"), script.indexOf("  function battleNamesVisible"));
if (!/deathStartOffset=vct10Duration\+knockbackDuration\+decelDuration\+pauseDuration/.test(deathSource) || !/returnDuration:0/.test(deathSource) || !/decelX/.test(deathSource) || !/value\.action=1/.test(script.slice(script.indexOf("  function battleMotionValue"), script.indexOf("  function battleNamesVisible"))) || !/value\.action=2/.test(script.slice(script.indexOf("  function battleMotionValue"), script.indexOf("  function battleNamesVisible"))) || !/value\.action=10/.test(script.slice(script.indexOf("  function battleMotionValue"), script.indexOf("  function battleNamesVisible"))) || !/blocking:false,presentationTail:true/.test(deathSource) || /value\.opacity=Math\.max\(\.18/.test(deathSource)) {
  throw new Error("native hit/knockback/return/dead chain is incomplete");
}
const directDamageSource = script.slice(script.indexOf("    const addDirectDamage="), script.indexOf("    const queueDodge=", script.indexOf("    const addDirectDamage=")));
if (!/const damageAt=impactAt;/.test(directDamageSource) || !/else if\(hp\|\|pet\|\|flags&BATTLE_FLAG\.guard\)/.test(directDamageSource) || /else if\([^\n]*BATTLE_FLAG\.normal/.test(directDamageSource)) {
  throw new Error("zero-damage living targets must not manufacture a hurt motion; guard must use its native pose");
}
const receiveBattleStatusForFreshDeath = script.slice(script.indexOf("  function receiveBattleStatus"), script.indexOf("  function receiveBattlePacket"));
if (!/const freshDead=Boolean\(\(item\.flags&BATTLE_BC_FRESH\)&&!old\)/.test(receiveBattleStatusForFreshDeath) ||
    !/if\(freshDead\)[\s\S]{0,1300}state\.deathStartedAt\.set\(Number\(item\.battleId\),Date\.now\(\)-deadDuration-BATTLE_PROC_TICK_MS\)/.test(receiveBattleStatusForFreshDeath) ||
    !/\}\s*else\{\s*battleQueueDeath\(/.test(receiveBattleStatusForFreshDeath)) {
  throw new Error("BC fresh+dead must enter the held final corpse frame without replaying the death chain");
}
const battleDamageSource = script.slice(script.indexOf('      }else if(marker==="BD")'), script.indexOf('      }else if(marker==="B+")'));
const battleMovieScopeSource = script.slice(script.indexOf("  function battleMovieEffects(command){"), script.indexOf("  function receiveBattlePacket", script.indexOf("  function battleMovieEffects(command){")));
const fieldSendScopeSource = script.slice(script.indexOf("  function fieldSend(functionName,values){"), script.indexOf("  function toggleFieldSetting", script.indexOf("  function fieldSend(functionName,values){")));
if (!/const timedMotion=[\s\S]{0,1600}const scheduleBDMotion=/.test(battleMovieScopeSource) ||
    !/const resetBDBatch=/.test(battleMovieScopeSource) ||
    !/const bdProjectedHp=new Map\(\)/.test(battleMovieScopeSource) ||
    /resetBDBatch|scheduleBDMotion|bdProjectedHp/.test(fieldSendScopeSource) ||
    !/duration=60\*BATTLE_PROC_TICK_MS/.test(script) ||
    !/scheduleBDMotion\(target,bdKind,sign,amount,petAmount,fatalHint\)/.test(battleDamageSource) ||
    !/if\(fatal\)\{[\s\S]{0,260}battleQueueDirectDeath\(state,target,start\)/.test(script) ||
    /const wasDead=Boolean\(battleFindParticipant\(target\)\?\.dead\),hitTiming=timedMotion\(\{kind:"hit"/.test(battleDamageSource)) {
  throw new Error("BD must use native 60-tick grouped VCT78/79 timing and direct fatal death");
}
for (const expected of [
  /marker==="BP"[\s\S]{0,500}scheduleAttack\(segment,target,"attack",0,flags\)/,
  /String\(segment\.rawMarker\|\|""\)==="Bb"[\s\S]{0,1700}scheduleBattleModelProjectile\(attacker,target,modelGraphic,flags,objectIndex\)/,
  /String\(segment\.rawMarker\|\|""\)==="Bd"[\s\S]{0,900}scheduleAttack\(segment,target,"attack",0,flags\|BATTLE_FLAG\.death\)/,
  /marker==="B\+"[\s\S]{0,500}scheduleAttack\(segment,target,"attack",0,flags\)/,
  /marker==="BY"[\s\S]{0,1800}scheduleAttackPair\(attacker,target,"attack",0,flags\)/,
  /* EarthRound is two native records: BC_FLG_HIDE/BF followed by BI.  The
     ATT_IN parser clears the visual hide state and walks from the edge before
     falling through to the normal attack tuple; an actor-filter regression
     can otherwise make BI look like an empty/stuck movie. */
  /marker==="BI"&&attacker>=0[\s\S]{0,900}entryX=side===1\?640\+106:-106[\s\S]{0,500}kind:"appear"/,
  /const enteringOrAttacking=state\.motions\.some\(motion=>[\s\S]{0,420}motion\.kind==="appear"\|\|motion\.kind==="attack"/,
  /exitX=side===1\?-106:640\+106[\s\S]{0,500}earthRoundHide:true/,
]) {
  if (!expected.test(script)) throw new Error(`critical/death contact flags are not threaded through an attack path: ${expected}`);
}
const localDefeatStart = script.indexOf("  function battleLocalParticipant");
const localDefeatEnd = script.indexOf("  function finishLocalBattleDeath", localDefeatStart);
if (localDefeatStart < 0 || localDefeatEnd <= localDefeatStart) throw new Error("local-side defeat helper boundary missing");
const localDefeatApp = {character:"Hero",battleState:null};
const localDefeat = new Function("app","BATTLE_BC_DEATH","battleSide",`${script.slice(localDefeatStart,localDefeatEnd)}; return {battleLocalDeath,battleLocalSideDefeated,battleServerSideDefeated};`)(
  localDefeatApp,
  2,
  id=>Number(id)>=0&&Number(id)<10?0:Number(id)>=10&&Number(id)<20?1:-1,
);
const deadHero = {battleId:0,name:"Hero",player:true,hp:0,flags:2,dead:true};
const livingPet = {battleId:5,name:"Pet",player:false,hp:30,flags:0,dead:false};
const deadPet = {...livingPet,hp:0,flags:2,dead:true};
const livingEnemy = {battleId:10,name:"Enemy",player:true,hp:50,flags:0,dead:false};
const masterDownPetAlive = {myNoKnown:true,myNo:0,participants:[deadHero,livingPet,livingEnemy]};
const wholeSideDown = {myNoKnown:true,myNo:0,participants:[deadHero,deadPet,livingEnemy]};
if (!localDefeat.battleLocalDeath(masterDownPetAlive) || localDefeat.battleLocalSideDefeated(masterDownPetAlive) || !localDefeat.battleLocalSideDefeated(wholeSideDown)) {
  throw new Error("local player death incorrectly exits while a same-side pet/teammate is alive");
}
/* BATTLE_CountAlive() checks character type, but the browser only has the
   fixed 2.5 battle slots after a gateway has stripped BC_FLG_PLAYER.  Players
   occupy 0..4/10..14 and their pets 5..9/15..19.  Exercise the production
   predicate, including the exact false value written by receiveBattleStatus
   for a missing flag, on both sides. */
const deadHeroWithoutPlayerFlag = {...deadHero,player:false};
const livingTeammate = {battleId:1,name:"Ally",player:true,hp:42,flags:0,dead:false};
const deadTeammate = {...livingTeammate,hp:0,flags:2,dead:true};
const missingFlagMasterDownPetAlive = {myNoKnown:true,myNo:0,participants:[deadHeroWithoutPlayerFlag,livingPet,livingEnemy]};
const missingFlagMasterDownTeammateAlive = {myNoKnown:true,myNo:0,participants:[deadHeroWithoutPlayerFlag,livingTeammate,livingPet,livingEnemy]};
const missingFlagPlayersDownPetAlive = {myNoKnown:true,myNo:0,participants:[deadHeroWithoutPlayerFlag,deadTeammate,livingPet,livingEnemy]};
const sideOneDeadHero = {...deadHeroWithoutPlayerFlag,battleId:10};
const sideOneLivingPet = {...livingPet,battleId:15};
const sideOneLivingTeammate = {...livingTeammate,battleId:11};
const sideOneEnemy = {...livingEnemy,battleId:0};
if (!localDefeat.battleServerSideDefeated(missingFlagMasterDownPetAlive) ||
    localDefeat.battleServerSideDefeated(missingFlagMasterDownTeammateAlive) ||
    !localDefeat.battleServerSideDefeated(missingFlagPlayersDownPetAlive) ||
    !localDefeat.battleServerSideDefeated({myNoKnown:true,myNo:10,participants:[sideOneDeadHero,sideOneLivingPet,sideOneEnemy]}) ||
    localDefeat.battleServerSideDefeated({myNoKnown:true,myNo:10,participants:[sideOneDeadHero,sideOneLivingTeammate,sideOneLivingPet,sideOneEnemy]})) {
  throw new Error("server-side defeat must use the 2.5 player slots when terminal BC omits BC_FLG_PLAYER");
}
const localExitSource = script.slice(script.indexOf("  function finishLocalBattleDeath"), script.indexOf("  function battleStartCommandPending"));
const executableLocalExitSource = localExitSource.replace(/\/\*[\s\S]*?\*\//g,"").replace(/\/\/.*$/gm,"");
if (/send\("EO"/.test(executableLocalExitSource) || !/battleServerSideDefeated\(state\)/.test(executableLocalExitSource) || !/battleTerminalHoldUntil\(state\)/.test(executableLocalExitSource) || !/sendBattleEndOnce\(state,"local-side-defeat"\)/.test(executableLocalExitSource)) {
  throw new Error("all-side defeat exit must wait for corpse terminal frames and send EO through the idempotent boundary");
}
const battleEndStart = script.indexOf("  function sendBattleEndOnce");
const battleEndEnd = script.indexOf("  function battleAbortConnection", battleEndStart);
if (battleEndStart < 0 || battleEndEnd <= battleEndStart) throw new Error("battle EO helper boundary missing");
let eoCalls = 0;
const battleEndApp = {transport:{}};
const sendBattleEndOnce = new Function("app","send",`${script.slice(battleEndStart,battleEndEnd)}; return sendBattleEndOnce;`)(
  battleEndApp,
  (name,values)=>{ if(name !== "EO" || values[0] !== 0) throw new Error(`unexpected EO vector ${name}:${values}`); eoCalls++; return Promise.resolve(true); },
);
const eoState = {};
if (!sendBattleEndOnce(eoState,"result") || sendBattleEndOnce(eoState,"duplicate") || eoCalls !== 1 || eoState.eoReason !== "result" || !eoState.eoSentAt) {
  throw new Error(`battle EO was not exactly-once: ${JSON.stringify({eoCalls,eoState})}`);
}
battleEndApp.transport=null;
if (sendBattleEndOnce({eoSent:false,eoReason:"",eoSentAt:0},"no-transport") !== false) {
  throw new Error("battle EO helper must not claim a send without an active transport");
}
for (const expected of [
  /function finishBattleResult[\s\S]{0,900}sendBattleEndOnce\(state,state\.result\.duel\?"duel-result":"battle-result"\)/,
  /function finishBattleWorldExit[\s\S]{0,700}sendBattleEndOnce\(state,state\.escapeLocalSuccess\|\|state\.escape\?"escape-or-bu":"battle-exit"\)/,
  /escapeExitTimer=window\.setTimeout\(\(\)=>\{[\s\S]{0,180}sendBattleEndOnce\(state,"local-escape"\)/,
]) {
  if (!expected.test(script)) throw new Error(`terminal battle path is missing exactly-once EO boundary: ${expected}`);
}
const resultCloseSource = script.slice(script.indexOf('$("battle-result-close")'), script.indexOf('$("battle-result-close")') + 1800);
if (/send\("EO"/.test(resultCloseSource) || /sendBattleEndOnce/.test(resultCloseSource)) {
  throw new Error("battle result close must not send a second EO");
}
const receiveBattleStatusSource = script.slice(script.indexOf("  function receiveBattleStatus"), script.indexOf("  function receiveBattlePacket"));
const executableReceiveBattleStatusSource = receiveBattleStatusSource.replace(/\/\*[\s\S]*?\*\//g,"").replace(/\/\/.*$/gm,"");
const executablePacketSource = script.slice(script.indexOf("  function handlePacket"),script.indexOf("  function unescapeCharacterOption",script.indexOf("  function handlePacket"))).replace(/\/\*[\s\S]*?\*\//g,"").replace(/\/\/.*$/gm,"");
if (!/if\(battleServerSideDefeated\(state\)\)scheduleBattleDeathExit\(state,false\)/.test(executableReceiveBattleStatusSource) || !/case "XYD"[\s\S]{0,900}battleServerSideDefeated\(app\.battleState\)/.test(executablePacketSource)) {
  throw new Error("BC/XYD still treats the local character alone as the whole defeated side");
}
/* WN dialogue text uses the same legacy character-file escape layer as the
   modal window.  Keep the chat copy newline-normalized too; otherwise an NPC
   reply containing `\\n` is shown literally in the field chat buffer. */
if (!/case "WN"[\s\S]{0,900}addChat\("系统",unescapeCharacterOption\(values\[4\]\|\|""\)\)/.test(executablePacketSource)) {
  throw new Error("WN dialogue chat copy must decode legacy newline escapes");
}
const unescapeStart = script.indexOf("  function unescapeCharacterOption");
const unescapeEnd = script.indexOf("  function parseMapWindowHeader", unescapeStart);
if (unescapeStart < 0 || unescapeEnd <= unescapeStart) throw new Error("legacy text decoder boundary missing");
const decodeLegacyCharacterOption = new Function(`${script.slice(unescapeStart, unescapeEnd)}; return unescapeCharacterOption;`)();
if (decodeLegacyCharacterOption("甲\\n乙") !== "甲\n乙") {
  throw new Error("legacy WN newline escape must decode to an actual line break");
}
/* RS/RD can race the final BC that is drained behind the last B movie.  Once
   a result owns the back-buffer, that late roster must not re-arm the local
   death watchdog or leave its timer behind the result screen. */
if (!/function scheduleBattleDeathExit\(state,fromPosition=false\)\{[\s\S]{0,360}state\.result\|\|app\.battleState!==state/.test(script) ||
    !/function finishBattleResult\(state\)[\s\S]{0,760}clearTimeout\(state\.deathExitTimer\)[\s\S]{0,180}state\.deathExitPending=false/.test(script)) {
  throw new Error("result-vs-final-BC race can re-arm a stale death watchdog");
}
const resultQueueSource = script.slice(script.indexOf("  function queueBattleResult"), script.indexOf("  function openBattleResult"));
if (/Math\.min\(1400/.test(resultQueueSource) || !/battleTerminalHoldUntil\(state\)/.test(resultQueueSource)) {
  throw new Error("battle result still truncates the active movie");
}
/* NETPROC stores RS/RD until BATTLEPROC has completed OUT_PRODUCE; a
   terminal BP/BC/BA batch must therefore never reopen CMD_INPUT between the
   death movie and the result window.  The real 2.5 server sends exactly this
   ordering (terminal BP/BC/BA immediately followed by RS), so keep both the
   queue purge and every late-control guard under regression coverage. */
const openResultStart = script.indexOf("  function openBattleResult");
const openResultEnd = script.indexOf("  const BATTLE_FLAG", openResultStart);
const openResultSource = script.slice(openResultStart,openResultEnd);
if (openResultStart < 0 || openResultEnd <= openResultStart ||
    !/clearTimeout\(Number\(state\.turnApplyTimer\)\|\|0\);state\.turnApplyTimer=0/.test(openResultSource) ||
    !/state\.pendingTurnState=null/.test(openResultSource) ||
    !/state\.pendingBattleControls\)\)state\.pendingBattleControls\.length=0/.test(openResultSource) ||
    !/state\.pendingBattleStatuses\)\)state\.pendingBattleStatuses\.length=0/.test(openResultSource) ||
    !/resetBattleMenuMotion\(state,\{keepGeneration:true\}\)/.test(openResultSource) ||
    !/closeBattlePopup\(\{skipDefault:true,clearChoiceTimer:true\}\)/.test(openResultSource) ||
    !/function applyBattleTurnState\(state,snapshot\)\{\s*if\(!state\|\|app\.battleState!==state\|\|!app\.battle\|\|state\.result\)return;/.test(script) ||
    !/function queueBattleControl\(state,packet\)\{\s*if\(!state\|\|app\.battleState!==state\|\|!packet\|\|state\.result\)return;/.test(script) ||
    !/function receiveBattleStatus\(text\)[\s\S]{0,1900}const state=app\.battleState;[\s\S]{0,520}if\(state\.result\)return;/.test(script) ||
    !/function maybeOpenBattlePetSkillMenu\(state,command=""\)\{\s*if\(!state\|\|app\.battleState!==state\|\|!app\.battle\|\|state\.result\)return;/.test(script)) {
  throw new Error("terminal RS/RD can still drain a queued turn and flash a false command menu");
}
if (!/case "BU"[\s\S]{0,4200}queueBattleWorldExit\(state\)/.test(script)) {
  throw new Error("BU still clears the battle surface before the final movie drains");
}
/* Exercise the battle movie tokenizer independently from the DOM-heavy
   client bootstrap.  This vector mirrors BATTLE_MultiAttMagic(): BJ header,
   target-list FF, two unkeyed result tuples (including a BE damage token),
   the 0x12345678 sentinel, then a BM marker. */
const battleParserStart = script.indexOf("  function isBattleCommandMarker");
const battleParserEnd = script.indexOf("  function battleSignedNumber");
if (battleParserStart < 0 || battleParserEnd <= battleParserStart) throw new Error("battle parser boundary missing");
const parseBattleCommandSegments = new Function(`${script.slice(battleParserStart, battleParserEnd)}; return parseBattleCommandSegments;`)();
const attackMagicSegments = parseBattleCommandSegments("BJ|a0|iBC614E|m1|100|200|300|s0|t0|l0|0|0|0|0|0|0|o0|o0|o0|s0|0|0|r1|r2|FF|14|2|BE|3|14|3|0|4|12345678|BM|2|0|");
if (attackMagicSegments.length !== 2 || attackMagicSegments[0].attackMagicResults?.length !== 2 || attackMagicSegments[0].attackMagicResults[0].damage !== "BE" || attackMagicSegments[1].marker !== "BM") {
  throw new Error(`attack-magic orphan tuple parse failed: ${JSON.stringify(attackMagicSegments)}`);
}
const callDragonSegments = parseBattleCommandSegments("B$|a0|i57264E|m1|100|200|300|0|220|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|r1|r2|FF|14|2|BE|3|14|3|0|4|5711438|BM|2|0|");
if (callDragonSegments.length !== 2 || callDragonSegments[0].callDragonResults?.length !== 2 || callDragonSegments[0].callDragonResults[0].damage !== "BE" || callDragonSegments[1].marker !== "BM") {
  throw new Error(`call-dragon orphan tuple parse failed: ${JSON.stringify(callDragonSegments)}`);
}
/* Bare hexadecimal values are legal positional fields.  In particular,
   sprite/damage ids such as BE and ABC must not become b=E/a=BC merely
   because they begin with a letter; the marker-specific whitelist keeps
   them in `values` for BJ and BM alike. */
const positionalHexSegments = parseBattleCommandSegments("BJ|a0|iBC614E|m1|BE|ABC|0|s0|t0|l0|0|0|0|0|0|0|o0|o0|o0|s0|0|0|r1|FF|0|1|2|3|12345678|BM|ABC|");
if (positionalHexSegments.length !== 2 || positionalHexSegments[0].values.slice(0,2).join(",") !== "BE,ABC" || positionalHexSegments[0].values.length !== 11 || positionalHexSegments[0].fields.b !== undefined || positionalHexSegments[0].fields.r !== "1" || positionalHexSegments[1].values.join(",") !== "ABC") {
  throw new Error(`bare hexadecimal positional parse failed: ${JSON.stringify(positionalHexSegments)}`);
}
/* A normal multi-hit BH repeats r/f/d/p without `counter`; only the later
   BATTLE_Counter() tuples carry counter<damage>.  Keep this wire vector next
   to the parser checks so a future tokenizer change cannot make the renderer
   classify every repeated hit as a reverse counter attack. */
const repeatedBhSegments = parseBattleCommandSegments("BH|a0|r5|f2|dA|p0|r6|f2|dB|p0|r7|f2|dC|p0|r0|f2|counter3|p0|FF|");
const repeatedBh = repeatedBhSegments[0];
if (repeatedBhSegments.length !== 1 || repeatedBh.fields.a !== "0" || repeatedBh.repeated.r?.length !== 3 || repeatedBh.fields.r !== "0" || repeatedBh.fields.counter !== "3") {
  throw new Error(`repeated BH tuple parse failed: ${JSON.stringify(repeatedBhSegments)}`);
}
/* BATTLE_COM_S_FIREKILL writes one ordinary BATTLE_Attack tuple, then
   BATTLE_MultiAttMagic_Fire() writes n<count> followed by its range tuples.
   `n` must stay keyed; otherwise the renderer cannot tell a magic 0/0 dodge
   from the ordinary attack's unflagged miss. */
const fireKillSegments = parseBattleCommandSegments("Bf|a0|rA|f2|dA|p0|n2|rA|f0|d0|p0|rB|f0|d5|p0|FF|");
const fireKill = fireKillSegments[0];
if (fireKillSegments.length !== 1 || fireKill.fields.n !== "2" || fireKill.values.includes("2") || fireKill.repeated.r?.length !== 2 || fireKill.fields.r !== "B") {
  throw new Error(`fire-kill range tuple parse failed: ${JSON.stringify(fireKillSegments)}`);
}
/* The ranged/earth/boomerang headers use the same appended attack tuple.
   `f` is not optional: losing it turns a native dodge/critical/guardian
   result into an ordinary hit even though the d/p values still parse. */
for (const [marker, command] of [
  ["BI", "BI|a0|rA|f20|d1|p0|FF|"],
  ["BB", "BB|a0|w0|rA|f20|d1|p0|FF|"],
  ["BO", "BO|a0|rA|f20|d1|p0|FF|"],
]) {
  const parsed = parseBattleCommandSegments(command)[0];
  if (!parsed || parsed.marker !== marker || parsed.fields.f !== "20") {
    throw new Error(`${marker} attack flag field was lost: ${JSON.stringify(parsed)}`);
  }
}
/* Deep poison uses the lower-case Bd marker but still carries the complete
   a/r/f/d/p attack tuple.  Its `a` field must remain keyed; accepting only
   the upper-case BD status-delta keys turns a bid such as A into positional
   hex 0xAA and drops the attacker animation. */
const deepPoisonSegments = parseBattleCommandSegments("Bd|aA|r0|f2|d1|p0|FF|");
const deepPoison = deepPoisonSegments[0];
if (deepPoisonSegments.length !== 1 || deepPoison.fields.a !== "A" || deepPoison.fields.r !== "0" || deepPoison.fields.f !== "2" || deepPoison.fields.d !== "1" || deepPoison.fields.p !== "0") {
  throw new Error(`deep-poison tuple parse failed: ${JSON.stringify(deepPoison)}`);
}
/* BD status ticks keep the HP amount positional.  BE is a legal hexadecimal
   damage value, not an escape marker, and must survive until the following
   BM segment. */
const statusDeltaSegments = parseBattleCommandSegments("BD|rA|0|0|BE|p0|BM|A|0|");
if (statusDeltaSegments.length !== 2 || statusDeltaSegments[0].values.join(",") !== "0,0,BE" || statusDeltaSegments[0].fields.p !== "0" || statusDeltaSegments[1].marker !== "BM") {
  throw new Error(`status-delta positional amount parse failed: ${JSON.stringify(statusDeltaSegments)}`);
}
/* BATTLE_BadStatusString() emits BM as `BM|<bid>|<status>|` (both values
   positional).  ATT_MALFUNCTION changes the persistent ACTION without a
   floating label or a fabricated hurt pose; status 2 alone owns VCT105's
   exact 60-tick pause. */
const battleStatusMovieSource = script.slice(script.indexOf('      }else if(marker==="BM")'), script.indexOf('      }else if(marker==="BL")'));
if (!/battleSegmentNumber\(segment,"",0,-1\)/.test(battleStatusMovieSource) || !/battleSegmentNumber\(segment,"",1,0\)/.test(battleStatusMovieSource) ||
    !/status===2\?60\*BATTLE_PROC_TICK_MS:BATTLE_PROC_TICK_MS/.test(battleStatusMovieSource) || !/kind:"status-wait"/.test(battleStatusMovieSource) ||
    /kind:"hit"|battlePushEffect/.test(battleStatusMovieSource)) {
  throw new Error("BM positional/timing/persistent-status path diverged from ATT_MALFUNCTION");
}
const battleReverseMovieSource=script.slice(script.indexOf('      }else if(marker==="BR")'),script.indexOf('      }else if(marker==="B%"'));
if(!/kind:"status-wait"[\s\S]{0,220}BATTLE_PROC_TICK_MS/.test(battleReverseMovieSource)||/battlePushEffect/.test(battleReverseMovieSource)){
  throw new Error("BR must mutate only ATR_ATTRIB_WORK on one native tick without a text effect");
}
/* ATT_LIFE is executed when EntrySort reaches BL, not while the complete B
   string is being parsed.  A queued death earlier in the same movie must
   remain a corpse until that exact process tick, and the stock client then
   switches directly to ANIM_STAND without a hit pose or +HP label. */
const battleReviveMovieSource = script.slice(script.indexOf('      }else if(marker==="BL")'), script.indexOf('      }else if(marker==="BY")'));
const battleReviveHelperStart = script.indexOf("  function battleCommitRevive");
const battleReviveHelperEnd = script.indexOf("  function battleQueueDeath", battleReviveHelperStart);
if (battleReviveHelperStart < 0 || battleReviveHelperEnd <= battleReviveHelperStart) throw new Error("battle revive helper boundary missing");
const battleReviveHelperSource = script.slice(battleReviveHelperStart, battleReviveHelperEnd);
if (!/battleQueueRevive\(state,target,hp,Number\(state\.motionQueueAt\)\|\|Date\.now\(\)\)/.test(battleReviveMovieSource) ||
    /item\.hp\s*=|kind:"hit"|battlePushEffect/.test(battleReviveMovieSource) ||
    !/battlePushMotion\(list,\{kind:"revive"[\s\S]{0,180}nativeTick:true\}\)/.test(battleReviveHelperSource) ||
    !/battleScheduleDamage\(state,startAt,\(\)=>battleCommitRevive\(state,id,hp\)\)/.test(battleReviveHelperSource) ||
    !/motion\?\.kind==="death"\|\|motion\?\.kind==="death-direct"/.test(battleReviveHelperSource) ||
    !/state\.deathOffsets\.delete\(id\)/.test(battleReviveHelperSource)) {
  throw new Error("BL must defer the native stand/revive transition and clear both terminal death paths");
}
const battleDeathFlagMatch = script.match(/const BATTLE_BC_DEATH=1<<(\d+)/);
if (!battleDeathFlagMatch) throw new Error("BATTLE_BC_DEATH definition missing for revive regression");
const battleDeathFlag = 1 << Number(battleDeathFlagMatch[1]);
const battleCommitRevive = new Function("BATTLE_BC_DEATH", `${script.slice(battleReviveHelperStart, script.indexOf("  function battleQueueRevive", battleReviveHelperStart))}; return battleCommitRevive;`)(battleDeathFlag);
const revivedParticipant = {battleId:3,hp:0,maxHp:80,dead:true,flags:battleDeathFlag|8};
const unrelatedCorpse = {kind:"death",target:4};
const reviveState = {
  participants:[revivedParticipant],
  deathStartedAt:new Map([[3,100],[4,200]]),
  deathOffsets:new Map([[3,{dx:1,dy:2}],[4,{dx:3,dy:4}]]),
  motions:[{kind:"death",target:3},{kind:"death-direct",target:3},unrelatedCorpse,{kind:"revive",target:3}],
};
battleCommitRevive(reviveState,3,120);
if (revivedParticipant.hp !== 80 || revivedParticipant.dead || revivedParticipant.flags !== 8 || reviveState.deathStartedAt.has(3) || reviveState.deathOffsets.has(3) || !reviveState.deathStartedAt.has(4) || !reviveState.deathOffsets.has(4) || reviveState.motions.length !== 2 || !reviveState.motions.includes(unrelatedCorpse) || !reviveState.motions.some(motion=>motion.kind === "revive" && motion.target === 3)) {
  throw new Error(`ATT_LIFE revive state cleanup failed: ${JSON.stringify({participant:revivedParticipant,motions:reviveState.motions})}`);
}
const battleMotionPrioritySource = script.slice(script.indexOf("  function battleMotionRenderPriority"), script.indexOf("  function battleMotionValue"));
const battleMotionValueSource = script.slice(script.indexOf("  function battleMotionValue"), script.indexOf("  function battleNamesVisible"));
if (!/kind==="revive"&&target===value\)return 101/.test(battleMotionPrioritySource) || !/motion\.kind==="revive"&&target===Number\(id\)[\s\S]{0,260}value\.action=3[\s\S]{0,180}value\.animationLoop=true/.test(battleMotionValueSource)) {
  throw new Error("revive must own the terminal corpse on its scheduled tick and restore looping ANIM_STAND");
}
/* BATTLE_CommandWait() can prepend `t` in the escape field (`et<bid>`).
   The one-letter movie tokenizer stores that spelling as fields.e =
   `t<bid>`; it must still resolve to the actual battle slot instead of
   defaulting to bid 0 and animating the wrong character. */
const escapeTargetStart = script.indexOf("  function battleNumber");
const escapeTargetEnd = script.indexOf("  function battleEscapeSignature", escapeTargetStart);
if (escapeTargetStart < 0 || escapeTargetEnd <= escapeTargetStart) throw new Error("battle escape target helper boundary missing");
const battleEscapeTarget = new Function(`${script.slice(escapeTargetStart, escapeTargetEnd)}; return battleEscapeTarget;`)();
if (battleEscapeTarget({fields:{e:"A"},values:[]}) !== 10 || battleEscapeTarget({fields:{e:"tA"},values:[]}) !== 10 || battleEscapeTarget({fields:{},values:["etA"]}) !== 10) {
  throw new Error("et<bid> escape target decode failed");
}
for (const expected of [
  /const normalHitCount=Math\.max\(1,targets\.length-counterValues\.length\)/,
  /for\(let index=1;index<normalHitCount;index\+\+\)/,
  /for\(let counterIndex=0;counterIndex<counterValues\.length;counterIndex\+\+\)/,
  /counterAmount=counterValues\[counterIndex\]\?\?0/,
  /* Repeated keyed fields retain native tuple order.  Reading fields.g
     directly returns the last guardian/skill id in a multi-hit movie. */
  /function battleSegmentValues\(segment,key\)[\s\S]{0,900}segment\?\.repeated\?\.\[key\][\s\S]{0,500}function battleSegmentNumber\(segment,key,index=0,fallback=0\)/
]) {
  if (!expected.test(html)) throw new Error(`BH repeated-hit regression missing: ${expected}`);
}
/* The projectile helpers are independent of the DOM.  Exercise their native
   launch/return geometry so a future CSS refactor cannot silently leave BB/BO
   packets with an empty visual timeline. */
const projectilePushStart = script.indexOf("  function battlePushProjectile");
const projectileValueStart = script.indexOf("  function battleProjectileValue");
const projectileEnd = script.indexOf("  function battleQueueDeath", projectileValueStart);
if (projectilePushStart < 0 || projectileValueStart <= projectilePushStart || projectileEnd <= projectileValueStart) throw new Error("battle projectile helper boundary missing");
const motionPushStart = script.indexOf("  function battlePushMotion");
if (motionPushStart < 0 || motionPushStart >= projectilePushStart) throw new Error("battle motion queue helper boundary missing");
const battlePushMotion = new Function(`${script.slice(motionPushStart, projectilePushStart)}; return battlePushMotion;`)();
const futureMotionNow = Date.now(), futureMotions = [];
for (let index = 0; index < 140; index++) {
  battlePushMotion(futureMotions,{kind:"attack",actor:index,startedAt:futureMotionNow+60000+index*10,duration:300});
}
if (futureMotions.length !== 140 || futureMotions[0]?.actor !== 0 || futureMotions[139]?.actor !== 139) {
  throw new Error(`future speed-sorted motions were truncated at the cleanup threshold: ${JSON.stringify({length:futureMotions.length,first:futureMotions[0]?.actor,last:futureMotions.at(-1)?.actor})}`);
}
futureMotions.unshift({kind:"expired",actor:-1,until:futureMotionNow-1});
battlePushMotion(futureMotions,{kind:"attack",actor:140,startedAt:futureMotionNow+62000,duration:300});
if (futureMotions.length !== 141 || futureMotions.some(item=>item.actor===-1) || futureMotions.at(-1)?.actor !== 140) {
  throw new Error("battle motion cleanup must remove only completed records and preserve all future records");
}
const projectileFns = new Function("BATTLE_PROC_TICK_MS", `${script.slice(projectilePushStart, projectileEnd)}; return {battlePushProjectile,battleProjectileValue};`)(nativeProcTickMs);
const projectileState = {}, projectileNow = Date.now();
projectileFns.battlePushProjectile(projectileState, {kind:"arrow", from:[0, 0], to:[100, 0], startAt:projectileNow + 120, endAt:projectileNow + 420});
const arrow = projectileState.projectiles?.[0], arrowMid = projectileFns.battleProjectileValue(arrow, Number(arrow?.startedAt) + Number(arrow?.duration) / 2);
if (!arrow || Math.abs(arrowMid.x - 50) > 2 || Math.abs(arrowMid.y) > 2) throw new Error(`arrow projectile geometry failed: ${JSON.stringify(arrowMid)}`);
projectileFns.battlePushProjectile(projectileState, {kind:"boomerang", from:[0, 0], to:[100, 0], waypoints:[[0, 0], [100, 0], [0, 0]], waypointOffsets:[0, 120, 240], startAt:projectileNow + 120, endAt:projectileNow + 360});
const boomerang = projectileState.projectiles?.[1], boomerangAtTarget = projectileFns.battleProjectileValue(boomerang, Number(boomerang?.startedAt) + 120), boomerangAtReturn = projectileFns.battleProjectileValue(boomerang, Number(boomerang?.startedAt) + 240);
if (!boomerang || Math.abs(boomerangAtTarget.x - 100) > 2 || Math.abs(boomerangAtReturn.x) > 2) throw new Error(`boomerang waypoint geometry failed: ${JSON.stringify({boomerangAtTarget,boomerangAtReturn})}`);
const courseFactory = new Function(`${script.slice(projectileValueStart,script.indexOf("  function battleProjectileBitmapFrame",projectileValueStart))}; return {battleProjectileCourse,battleProjectileDirection};`)();
if(courseFactory.battleProjectileCourse({angle:-90})!==0||courseFactory.battleProjectileCourse({angle:0})!==8||courseFactory.battleProjectileCourse({angle:90})!==16||courseFactory.battleProjectileCourse({angle:180})!==24||
   courseFactory.battleProjectileDirection(0)!==4||courseFactory.battleProjectileDirection(2)!==5||courseFactory.battleProjectileDirection(14)!==0||courseFactory.battleProjectileDirection(30)!==4){
  throw new Error("native 32-way projectile course/direction mapping drifted");
}
const futureProjectileState = {}, futureProjectileNow = Date.now();
for (let index = 0; index < 140; index++) {
  projectileFns.battlePushProjectile(futureProjectileState,{kind:"model",target:index,from:[0,0],to:[10,0],startAt:futureProjectileNow+60000+index*10,endAt:futureProjectileNow+60400+index*10});
}
if (futureProjectileState.projectiles?.length !== 140 || futureProjectileState.projectiles[0]?.target !== 0 || futureProjectileState.projectiles.at(-1)?.target !== 139) {
  throw new Error("future battle projectiles were truncated at the cleanup threshold");
}
futureProjectileState.projectiles.unshift({kind:"expired",target:-1,until:futureProjectileNow-1});
projectileFns.battlePushProjectile(futureProjectileState,{kind:"model",target:140,from:[0,0],to:[10,0],startAt:futureProjectileNow+62000,endAt:futureProjectileNow+62400});
if (futureProjectileState.projectiles.length !== 141 || futureProjectileState.projectiles.some(item=>item.target===-1) || futureProjectileState.projectiles.at(-1)?.target !== 140) {
  throw new Error("battle projectile cleanup must remove only completed records and preserve all future records");
}
const context = {window: {}, TextEncoder, TextDecoder, console};
vm.runInNewContext(script.slice(0, protocolEnd) + "\n})();", context, {filename: "index.html"});
const P = context.window.StoneAgeProtocol;

function equal(actual, expected, label) {
  const left = Buffer.from(actual);
  const right = Buffer.from(expected);
  if (!left.equals(right)) throw new Error(`${label}: got ${left.toString("hex")}, want ${right.toString("hex")}`);
}
function packet(text) { return P.encodePacket(new TextEncoder().encode(text)); }

const vectors = [
  ["1 ClientLogin probe local ", "HBZovLTemtm8s7fgadloSK8dIILiINuPLPGzJ-8\n"],
  ["1 ClientLogin ok ", "EqP1vEFrmmZJs0RtaWbV9eQ+iVQ\n"],
];
for (const [raw, encoded] of vectors) {
  equal(packet(raw), new TextEncoder().encode(encoded), `encode ${raw}`);
  equal(P.decodePacket(new TextEncoder().encode(encoded)), new TextEncoder().encode(raw), `decode ${encoded}`);
}

const long = "StoneAge named protocol map payload | ".repeat(64);
const longPacket = packet(long);
equal(P.decodePacket(longPacket), new TextEncoder().encode(long), "Ringo round trip");
const arbitrary = Uint8Array.from(Array.from({length: 512}, (_, index) => (index * 73 + 19) & 255));
equal(P.decodePacket(P.encodePacket(arbitrary)), arbitrary, "Ringo arbitrary-byte round trip");
for (const value of [0, 1, -1, 61, 62, 100000, 2147483647, -2147483648]) {
  if (P.decodeInt(P.encodeInt(value)) !== value) throw new Error(`base62 round trip failed for ${value}`);
}
const escaped = P.encodeString(new Uint8Array([0xce, 0xde, 0x20, 0x0a, 0x5c]));
equal(P.decodeString(escaped), new Uint8Array([0xce, 0xde, 0x20, 0x0a, 0x5c]), "string escape round trip");
equal(P.decodeString(P.encodeString("你好")), new Uint8Array([0xc4, 0xe3, 0xba, 0xc3]), "CP936 common text");
equal(P.decodeString(P.encodeString("石器时代")), new Uint8Array([0xca, 0xaf, 0xc6, 0xf7, 0xca, 0xb1, 0xb4, 0xfa]), "CP936 reverse table");
const big5MammothName=Uint8Array.from([0xaa,0xf8,0xa4,0xf2,0xb6,0x48,0xa4,0xbd,0xa8,0xae]);
if(P.decodeText(big5MammothName)!=="長毛象公車")throw new Error(`mixed Big5 NPC name decode failed: ${JSON.stringify(P.decodeText(big5MammothName))}`);
const cp936MammothName=Uint8Array.from([0xb3,0xa4,0xc3,0xab,0xcf,0xf3,0xbf,0xcd,0xd4,0xcb]);
if(P.decodeText(cp936MammothName)!=="长毛象客运")throw new Error(`CP936 NPC name decode regressed: ${JSON.stringify(P.decodeText(cp936MammothName))}`);
/* Generated npcgen_man replies are CP936.  Their punctuation bytes decode
   to Big5 compatibility brackets/bopomofo, which used to outrank the
   correct sentence and display NPC text as mojibake. */
const cp936NpcReply=Uint8Array.from([0xb4,0xe5,0xb3,0xa4,0xbc,0xd2,0xb5,0xc4,0xb3,0xf8,0xca,0xa6,0xa3,0xba,0xb5,0xe4,0xc0,0xf1,0xbe,0xd9,0xd0,0xd0,0xb5,0xc4,0xb5,0xd8,0xb5,0xe3,0xd4,0xda,0xa1,0xa4,0xa1,0xa4,0xa1,0xa4,0xcd,0xfc,0xc1,0xcb,0xa1,0xa4,0xa1,0xa4,0xa1,0xa4]);
if(P.decodeText(cp936NpcReply)!=="村长家的厨师：典礼举行的地点在···忘了···")throw new Error(`CP936 NPC reply decode failed: ${JSON.stringify(P.decodeText(cp936NpcReply))}`);
/* All server string fields must use the same mixed-code-page decoder as the
   standalone helper.  A dynamic MC/TD reply bypassing it would reintroduce
   mojibake for legacy Big5 transport/NPC names even though static metadata
   still decoded correctly. */
const big5MCFields=[1,10,20,47,57,10,11,12].map(P.encodeInt);
big5MCFields.push(P.encodeString(big5MammothName));
const big5MC=P.decodeMessage(P.encodePacket(P.rawMessage(10,"MC",big5MCFields)));
if(big5MC.textValues[8]!=="長毛象公車")throw new Error(`MC mixed Big5 string decode failed: ${JSON.stringify(big5MC.textValues[8])}`);
const message = P.parseMessage(new TextEncoder().encode("7 CharList  "));
if (message.id !== 7 || message.function !== "CharList" || message.fields.length !== 1 || message.fields[0].length !== 0) {
  throw new Error(`empty field parse failed: ${JSON.stringify(message)}`);
}
const mcRaw = P.rawMessage(9, "MC", [1, 10, 20, 47, 57, 10, 11, 12].map(P.encodeInt).concat(P.encodeString("map")));
const mc = P.decodeMessage(P.encodePacket(mcRaw));
if (mc.schema.length !== 9 || mc.schema[8] !== "string" || mc.values.slice(0, 8).join(",") !== "1,10,20,47,57,10,11,12" || mc.textValues[8] !== "map") {
  throw new Error(`MC schema decode failed: ${JSON.stringify(mc)}`);
}
const tradeResponseText = "C|77|TradeB29|1";
const tradeResponseRaw = P.rawMessage(13, "TD", [P.encodeString(tradeResponseText)]);
const tradeResponse = P.decodeMessage(P.encodePacket(tradeResponseRaw));
if (tradeResponse.schema.length !== 1 || tradeResponse.schema[0] !== "string" || tradeResponse.textValues[0] !== tradeResponseText) {
  throw new Error(`single-field 2.5 TD decode failed: ${JSON.stringify(tradeResponse)}`);
}
if (!P.CLIENT_FIELDS.w || P.CLIENT_FIELDS.w.join(",") !== P.CLIENT_FIELDS.W.join(",")) throw new Error("w schema mismatch");
if (P.CLIENT_FIELDS.CharLogout.length !== 0) throw new Error("2.5 CharLogout must use the no-argument schema");
for (const name of ["SaMenu", "RideQuery", "SignDay", "STREET_VENDOR"]) {
  if (Object.prototype.hasOwnProperty.call(P.CLIENT_FIELDS, name)) {
    throw new Error(`optional 8.5-only client entry leaked into 2.5 schema: ${name}`);
  }
}
const logoutRaw = new TextDecoder().decode(P.decodePacket(P.packetMessage(12, "CharLogout", [])));
if (logoutRaw !== "12 CharLogout ") throw new Error(`CharLogout wire shape mismatch: ${JSON.stringify(logoutRaw)}`);
const inPlaceLogoutSupportStart = script.indexOf("const IN_PLACE_LOGOUT_IDLE_TIMEOUT_MS");
const inPlaceLogoutStart = script.indexOf("async function performInPlaceLogout()");
const inPlaceLogoutEnd = script.indexOf("function requestInPlaceLogout", inPlaceLogoutStart);
const recordLogoutStart = script.indexOf("function performNormalLogout()", inPlaceLogoutEnd);
const recordLogoutEnd = script.indexOf("function renderSystem()", recordLogoutStart);
if (inPlaceLogoutSupportStart < 0 || inPlaceLogoutStart <= inPlaceLogoutSupportStart || inPlaceLogoutEnd <= inPlaceLogoutStart || recordLogoutStart < 0 || recordLogoutEnd <= recordLogoutStart) {
  throw new Error("logout action boundaries missing");
}
const inPlaceLogoutSupportSource = script.slice(inPlaceLogoutSupportStart, inPlaceLogoutEnd);
const inPlaceLogoutSource = script.slice(inPlaceLogoutStart, inPlaceLogoutEnd);
const recordLogoutSource = script.slice(recordLogoutStart, recordLogoutEnd);
if (!inPlaceLogoutSource.includes("await synchronizeInPlaceLogoutPosition(transport)") ||
    !inPlaceLogoutSource.includes("closeTransport(transport,{waitForPeer:true})") ||
    !inPlaceLogoutSupportSource.includes('send("S",["c"])') ||
    !inPlaceLogoutSupportSource.includes('send("W",[sample.point[0],sample.point[1],serverDirectionChar(step.direction)])') ||
    !inPlaceLogoutSupportSource.includes("serverPositionSampleVersion") ||
    inPlaceLogoutSupportSource.includes('send("CharLogout"')) {
  throw new Error("原地登出 must close the 2.5 socket without sending record-point CharLogout");
}
if (!script.includes('const waitQuery=options?.waitForPeer?"?wait=1":""')) {
  throw new Error("原地登出 must wait for the 2.5 GMSV peer-close acknowledgement");
}
if (!recordLogoutSource.includes('send("CharLogout",[])')) {
  throw new Error("回记录点 must use the no-argument 2.5 CharLogout packet");
}
if (!script.includes("const preserveSameFloor=Boolean(app.map&&sameFloor)")) {
  throw new Error("same-floor S:c refresh must preserve the incremental NPC scene");
}
const systemStateStart = script.indexOf("  function receiveSystemState(data)");
const systemMapBranchStart = script.indexOf('      case "C": {', systemStateStart);
const systemMapBranchEnd = script.indexOf('      case "D": {', systemMapBranchStart);
const systemMapBranchSource = script.slice(systemMapBranchStart, systemMapBranchEnd);
if (systemStateStart < 0 || systemMapBranchStart < 0 || systemMapBranchEnd < 0 || systemMapBranchSource.includes('send("M"')) {
  throw new Error("S:C must rebuild native map state and leave the sole M decision to MC");
}
const mapRequestStart = script.indexOf("  const MAP_WINDOW_REQUEST_TIMEOUT_MS");
const mapRequestEnd = script.indexOf("  function receiveMapChecksum(values)", mapRequestStart);
if (mapRequestStart < 0 || mapRequestEnd <= mapRequestStart) throw new Error("map-window request lifecycle missing");
const mapChecksumStart = script.indexOf("  const LEGACY_MAP_CHECKSUM_CELLS");
if (mapChecksumStart < 0 || mapChecksumStart >= mapRequestStart) throw new Error("native DAT checksum implementation missing");
const checksumMap = {
  floor:1006,width:3,height:2,
  tile:Uint16Array.from([1,2,3,4,5,6]),
  parts:Uint16Array.from([0,5000,0,2,12804,0]),
  event:Uint16Array.from([0xc003,0x4000,2,0,0xffff,1])
};
const mapChecksumHarness = new Function("app", "Protocol", `${script.slice(mapChecksumStart, mapRequestStart)};return {legacyMapLayerCRC,localMapChecksum,makeLocalMapWindowValues,legacyMapWindowBounds,makeLocalMapLiveWindowValues,mapWindowNeedsLocalRefresh};`)({autoMapData:checksumMap}, P);
const checksumVector=[1006,0,0,3,2,55263,32800,49963];
const checksumResult=mapChecksumHarness.localMapChecksum(checksumVector);
if (!checksumResult?.matches || checksumResult.sums.join(",") !== "55263,32800,49963" ||
    mapChecksumHarness.localMapChecksum([...checksumVector.slice(0,5),55263,32800,49962])?.matches) {
  throw new Error(`2.5 DAT CRC vector mismatch: ${JSON.stringify(checksumResult)}`);
}
/* readMap() zero-pads an MC rectangle outside the DAT bounds before hashing.
   A synthetic in-bounds layer with the same four logical cells must therefore
   produce the identical CRC instead of forcing an unnecessary M fallback. */
const edgeCRCMap={width:1,height:1},paddedCRCMap={width:2,height:2};
const edgeCRC=mapChecksumHarness.legacyMapLayerCRC(edgeCRCMap,Uint16Array.from([9]),-1,-1,1,1);
const paddedCRC=mapChecksumHarness.legacyMapLayerCRC(paddedCRCMap,Uint16Array.from([0,0,0,9]),0,0,2,2);
if (edgeCRC===null || edgeCRC!==paddedCRC) throw new Error(`native edge zero-padding CRC mismatch: ${edgeCRC} != ${paddedCRC}`);
const localWindowMap={
  floor:77,width:2,height:2,
  tile:Uint16Array.from([1,2,3,4]),parts:Uint16Array.from([5,6,7,8]),event:Uint16Array.from([9,10,11,12])
};
const localWindow=mapChecksumHarness.makeLocalMapWindowValues(localWindowMap,77,0,0,"edge\\z0");
const localSections=String(localWindow?.[5]||"").split("|");
const localTiles=String(localSections[1]||"").split(",").map(P.decodeInt);
const localParts=String(localSections[2]||"").split(",").map(P.decodeInt);
const localEvents=String(localSections[3]||"").split(",").map(P.decodeInt);
const localIndex=(x,y)=>(y+16)*37+(x+20);
if (!localWindow || localWindow.slice(0,5).join(",")!=="77,-20,-16,17,21" || localSections[0]!=="edge\\z0" ||
    localTiles.length!==37*37 || localParts.length!==37*37 || localEvents.length!==37*37 ||
    localTiles[0]!==0 || localTiles[localIndex(0,0)]!==1 || localTiles[localIndex(1,1)]!==4 ||
    localParts[localIndex(1,0)]!==6 || localEvents[localIndex(0,1)]!==11) {
  throw new Error("local DAT bootstrap must reproduce the native zero-padded 37x37 readMap window");
}
/* MAP.CPP clips the live M rectangle before painting it. Keep the internal
   zero-padded readMap vector above, but expose only the in-floor rectangle to
   receiveMap() at an edge (a 160x160 floor, owner at 1,2 -> 0,0..18,23). */
const edgeLiveBounds=mapChecksumHarness.legacyMapWindowBounds(77,1,2,{width:160,height:160,source:"C"});
if(edgeLiveBounds.join(",")!=="77,0,0,18,23"){
  throw new Error(`live M bounds must match MAP.CPP edge clipping: ${edgeLiveBounds.join(",")}`);
}
const edgeLiveWindow=mapChecksumHarness.makeLocalMapLiveWindowValues(localWindowMap,77,1,1,"edge\\z0");
if(!edgeLiveWindow||edgeLiveWindow.slice(0,5).join(",")!=="77,0,0,2,2"||
   String(edgeLiveWindow[5]).split("|").slice(1).some(section=>section.split(",").length!==4)){
  throw new Error("local DAT live window must be clipped instead of exposing zero-padded negative cells");
}
/* MAP.CPP starts filling the next strip at SEARCH_AREA=11. A centred native
   window has 20/16/16/20 cells around the owner; reaching an 11-cell edge
   must slide the validated DAT window before its diamond becomes visible. */
const centredMapWindow={floor:77,x1:-20,y1:-16,width:37,height:37,tiles:Array(37*37).fill(100)};
if(mapChecksumHarness.mapWindowNeedsLocalRefresh(centredMapWindow,0,0)||
   !mapChecksumHarness.mapWindowNeedsLocalRefresh(centredMapWindow,5,0)||
   !mapChecksumHarness.mapWindowNeedsLocalRefresh(centredMapWindow,0,-5)){
  throw new Error("validated local M window must slide at the native 11-cell search edge");
}
const localSlideStart=script.indexOf("  const MAP_LOCAL_WINDOW_SEARCH_AREA=11");
const localSlideEnd=script.indexOf("  const MAP_WINDOW_REQUEST_TIMEOUT_MS",localSlideStart);
if(localSlideStart<0||localSlideEnd<=localSlideStart)throw new Error("validated local map-window slide helper missing");
const installedSlidingWindows=[];
const localSlideMap={floor:77,width:40,height:40,tile:new Uint16Array(40*40),parts:new Uint16Array(40*40),event:new Uint16Array(40*40)};
const localSlideApp={floor:77,localMapValidatedFloor:77,autoMapData:localSlideMap,map:centredMapWindow,mapWindowHeaderWire:"edge\\z0"};
const localSlideHarness=new Function("app","makeLocalMapLiveWindowValues","receiveMap",`${script.slice(localSlideStart,localSlideEnd)};return {mapWindowNeedsLocalRefresh,installValidatedLocalMapWindow};`)(
  localSlideApp,(...args)=>mapChecksumHarness.makeLocalMapLiveWindowValues(...args),values=>installedSlidingWindows.push(values)
);
if(localSlideHarness.installValidatedLocalMapWindow(0,0)||!localSlideHarness.installValidatedLocalMapWindow(5,0)||
   installedSlidingWindows.length!==1||installedSlidingWindows[0].slice(0,5).join(",")!=="77,0,0,22,21"||
   String(installedSlidingWindows[0][5]).split("|")[0]!=="edge\\z0"){
  throw new Error(`validated DAT must install one clipped live window without losing its palette header: ${JSON.stringify(installedSlidingWindows)}`);
}
const sentMapWindows = [], mapTimers = new Map();let nextMapTimer = 1;
const mapRequestApp = {
  transport:{},phase:"world",floor:1006,connectionToken:7,pendingMapWindowRequest:null,
  mapWindowRequestTimer:0,mapWindowRevisionByKey:new Map(),pendingInitialMapChecksum:null,initialMapChecksumTimer:0
};
const installedLocalWindows=[];
const mapRequestHarness = new Function("app", "window", "send", "reportError", "localMapChecksum", "makeLocalMapLiveWindowValues", "legacyMapWindowBounds", "receiveMap", "requestAutoMapData", `${script.slice(mapRequestStart, mapRequestEnd)};return {requestMapWindow,completeMapWindowRequest,clearMapWindowRequest,clearInitialMapChecksum,fallbackInitialMapChecksum,finishInitialMapChecksum};`)(
  mapRequestApp,
  {
    setTimeout(callback, delay){const id=nextMapTimer++;mapTimers.set(id,{callback,delay});return id;},
    clearTimeout(id){mapTimers.delete(id);}
  },
  (name, values)=>{sentMapWindows.push([name,...values]);return Promise.resolve();},
  error=>{throw error;},
  (values,data)=>mapChecksumHarness.localMapChecksum(values,data),
  (...args)=>mapChecksumHarness.makeLocalMapLiveWindowValues(...args),
  (...args)=>mapChecksumHarness.legacyMapWindowBounds(...args),
  values=>{installedLocalWindows.push(values);},
  ()=>Promise.resolve(null)
);
const initialMapRevision="1006|10|20|37|47|1|2|3";
if (!mapRequestHarness.requestMapWindow(1006,-5,-4,32,33,{revision:initialMapRevision}) ||
    mapRequestHarness.requestMapWindow(1006,-5,-4,32,33,{revision:initialMapRevision}) ||
    sentMapWindows.length !== 1) {
  throw new Error(`identical pending M windows must coalesce: ${JSON.stringify(sentMapWindows)}`);
}
if (!mapRequestHarness.completeMapWindowRequest(1006,0,0,32,33) || mapRequestApp.pendingMapWindowRequest ||
    mapRequestHarness.requestMapWindow(1006,-5,-4,32,33,{revision:initialMapRevision}) || sentMapWindows.length !== 1) {
  throw new Error("a clipped M reply must complete and memoize its MC revision");
}
if (!mapRequestHarness.requestMapWindow(1006,-5,-4,32,33,{revision:`${initialMapRevision}|changed`}) || sentMapWindows.length !== 2) {
  throw new Error("a changed MC checksum must be allowed to refresh the same M rectangle");
}
mapRequestHarness.clearMapWindowRequest();
const localInstallRequest={
  key:"local",floor:1006,position:[1,1],values:checksumVector.slice(),header:"local\\z0",
  revision:"local-match",connectionToken:mapRequestApp.connectionToken
};
mapRequestApp.pendingInitialMapChecksum=localInstallRequest;
mapRequestApp.initialMapChecksumTimer=nextMapTimer++;
if (!mapRequestHarness.finishInitialMapChecksum(localInstallRequest,checksumMap) || installedLocalWindows.length!==1 ||
    sentMapWindows.length!==2 || mapRequestApp.pendingInitialMapChecksum ||
    installedLocalWindows[0].slice(0,5).join(",")!=="1006,0,0,3,2"||mapRequestApp.localMapValidatedFloor!==1006) {
  throw new Error("a matching first MC must install a clipped local DAT window without sending M");
}
const mismatchRequest={...localInstallRequest,key:"mismatch",values:[...checksumVector.slice(0,7),checksumVector[7]^1],revision:"local-mismatch"};
mapRequestApp.pendingInitialMapChecksum=mismatchRequest;
mapRequestApp.initialMapChecksumTimer=nextMapTimer++;
if (!mapRequestHarness.finishInitialMapChecksum(mismatchRequest,checksumMap) || sentMapWindows.length!==3 ||
    mapRequestHarness.finishInitialMapChecksum(mismatchRequest,checksumMap) || sentMapWindows.length!==3||mapRequestApp.localMapValidatedFloor!==-1) {
  throw new Error("a mismatching first MC must fall back to exactly one M request");
}
mapRequestHarness.clearMapWindowRequest();
const supersededRequest={...localInstallRequest,key:"old-promise",revision:"old-promise"};
const authoritativeRequest={...localInstallRequest,key:"new-promise",revision:"new-promise"};
mapRequestApp.pendingInitialMapChecksum=authoritativeRequest;
mapRequestApp.initialMapChecksumTimer=nextMapTimer++;
if (mapRequestHarness.finishInitialMapChecksum(supersededRequest,checksumMap) || installedLocalWindows.length!==1 ||
    sentMapWindows.length!==3 || mapRequestApp.pendingInitialMapChecksum!==authoritativeRequest) {
  throw new Error("a superseded DAT promise must not install or request an old map window");
}
mapRequestHarness.clearInitialMapChecksum(authoritativeRequest);
const receiveMapChecksumSource=script.slice(mapRequestEnd,script.indexOf("  function parseLayer",mapRequestEnd));
if (!receiveMapChecksumSource.includes("const localChecksum=localMapChecksum(values)") ||
    !receiveMapChecksumSource.includes("if(localChecksum?.matches)") ||
    !receiveMapChecksumSource.includes("installValidatedLocalMapWindow(x,y)") ||
    !receiveMapChecksumSource.includes("awaitInitialMapChecksum(values,app.floor,x,y,revision)")) {
  throw new Error("initial and walking MC must use the installed DAT checksum before requesting M");
}
if (!script.includes("const INITIAL_MAP_CHECKSUM_WAIT_MS=350") ||
    !script.includes("if(Number(app.pendingInitialMapChecksum?.floor)===floor)clearInitialMapChecksum()") ||
    !script.includes("clearInitialMapChecksum();clearMapWindowRequest();app.mapWindowRevisionByKey.clear()")) {
  throw new Error("initial DAT bootstrap must have a bounded wait and reject stale world/map lifecycles");
}
if (!script.includes("if(cached&&(!sameFloor||!hasWindow))")) {
  throw new Error("same-floor MC checksum must keep the live map back-buffer");
}
const finalizeMoveStart=script.indexOf("  function finalizePendingMove()");
const finalizeMoveEnd=script.indexOf("  function confirmOwnMove",finalizeMoveStart);
if(finalizeMoveStart<0||finalizeMoveEnd<=finalizeMoveStart||
   !/app\.walkAnimation=null;[\s\S]{0,420}installValidatedLocalMapWindow\(point\[0\],point\[1\]\)/.test(script.slice(finalizeMoveStart,finalizeMoveEnd))){
  throw new Error("each completed local step must slide a checksum-validated DAT window before exposing its edge");
}
/* A failed common tile must never turn a partially painted cache into a
   playable surface. It may retry twice, then it keeps the old completed
   fallback/loading curtain and evicts a stale localStorage data URL. */
const settleMapAssetStart=script.indexOf("  function settleMapAsset(file,failed=false)");
const settleMapAssetEnd=script.indexOf("  const MAP_RENDER_OVERRIDES",settleMapAssetStart);
if(settleMapAssetStart<0||settleMapAssetEnd<=settleMapAssetStart)throw new Error("map-asset settlement helper missing");
const failedCache={pending:new Set(["ground.png"]),failed:new Set(),unavailable:new Set(),retryCount:new Map(),dirty:false,ready:false};
const failedAssetApp={mapLayerCache:failedCache},failedAssetState={images:new Map([["ground.png",{}]])},failedAssetTimers=[],failedAssetEvents={forgot:0,loading:0,refresh:0};
const settleMapAssetHarness=new Function("app","assetState","window","forgetMapAssetCache","loadAsset","renderWorld","renderMapLoadingProgress","scheduleAssetRefresh","setMapLoading",`${script.slice(settleMapAssetStart,settleMapAssetEnd)};return settleMapAsset;`)(
  failedAssetApp,failedAssetState,{setTimeout(callback,delay){failedAssetTimers.push({callback,delay});return failedAssetTimers.length;}},
  ()=>{failedAssetEvents.forgot++;},()=>{},()=>{},()=>{},()=>{failedAssetEvents.refresh++;},()=>{failedAssetEvents.loading++;}
);
settleMapAssetHarness("ground.png",true);settleMapAssetHarness("ground.png",true);settleMapAssetHarness("ground.png",true);
if(failedCache.ready||!failedCache.dirty||!failedCache.failed.has("ground.png")||failedCache.unavailable.has("ground.png")||
   failedCache.retryCount.get("ground.png")!==2||failedAssetEvents.forgot!==3||failedAssetEvents.loading!==1||failedAssetEvents.refresh!==1){
  throw new Error(`failed map tile must retain the complete fallback instead of publishing black cells: ${JSON.stringify({ready:failedCache.ready,dirty:failedCache.dirty,failed:[...failedCache.failed],unavailable:[...failedCache.unavailable],retry:failedCache.retryCount.get("ground.png"),events:failedAssetEvents})}`);
}
/* A completed terrain cache is still not publishable until animated actors
   resolve to decoded SPR frames.  This is the incomplete-SPR/direct-REALBIN
   race that produced tall black blocks immediately after NOW LOADING. */
const finishMapLoadingStart=script.indexOf("  function fieldBootstrapFrameReadiness()");
const finishMapLoadingEnd=script.indexOf("  function send(functionName,values)",finishMapLoadingStart);
const finishMapLoadingSource=script.slice(finishMapLoadingStart,finishMapLoadingEnd);
if(finishMapLoadingStart<0||finishMapLoadingEnd<=finishMapLoadingStart||
   !/expectsSprite&&frame\?\.direct/.test(finishMapLoadingSource)||
   !/const bootstrapReady=assetState\.fieldBootstrapSpritesReady\|\|assetState\.spritesReady/.test(finishMapLoadingSource)||
   !/if\(dynamicReady&&!actorFrames\.ready\)[\s\S]{0,260}setMapLoading\(true,`正在解码首屏人物/.test(finishMapLoadingSource)||
   finishMapLoadingSource.indexOf("if(dynamicReady&&!actorFrames.ready)")>finishMapLoadingSource.indexOf("setMapLoading(false)")) {
  throw new Error("map loading must retain its opaque curtain until first actor SPR frames decode");
}
if (!script.includes("function bindMapFloorTransitionTarget(floor)") ||
    !script.includes("if(!bindMapFloorTransitionTarget(floor)&&!sameFloor&&app.floor>=0&&app.map)startMapFloorTransition(floor)")) {
  throw new Error("same-floor EV warp must bind the destination before revealing the map");
}
const renderWorldGuardStart=script.indexOf("  function renderWorld(force=false)");
const renderWorldGuardEnd=script.indexOf("  function scheduleWorldAnimation()",renderWorldGuardStart);
if(renderWorldGuardStart<0||renderWorldGuardEnd<=renderWorldGuardStart){
  throw new Error("renderWorld source boundary missing");
}
const renderWorldGuardSource=script.slice(renderWorldGuardStart,renderWorldGuardEnd);
const mapLayerUsableStart=script.indexOf("  function mapLayerCacheUsable(cache){");
const mapLayerUsableEnd=script.indexOf("  /* A same-floor M refresh",mapLayerUsableStart);
if(mapLayerUsableStart<0||mapLayerUsableEnd<=mapLayerUsableStart){
  throw new Error("map layer readiness predicate boundary missing");
}
const mapLayerCacheUsable=new Function(`${script.slice(mapLayerUsableStart,mapLayerUsableEnd)};return mapLayerCacheUsable;`)();
if(mapLayerCacheUsable({ready:true,rasterComplete:false})||
   mapLayerCacheUsable({ready:true,rasterFailed:true})||
   !mapLayerCacheUsable({ready:true,rasterComplete:true})||
   !mapLayerCacheUsable({ready:true})||
   mapLayerCacheUsable({ready:false,rasterComplete:true})){
  throw new Error("map layer must be publishable only after a complete raster");
}
const mapLayerRepairStart=script.indexOf("  function mapLayerCacheNeedsRepair(cache){");
const mapLayerRepairEnd=script.indexOf("  /* A same-floor M refresh",mapLayerRepairStart);
if(mapLayerRepairStart<0||mapLayerRepairEnd<=mapLayerRepairStart){
  throw new Error("map layer stale-cache repair predicate missing");
}
const mapLayerCacheNeedsRepair=new Function(`${script.slice(mapLayerUsableStart,mapLayerRepairEnd)};return mapLayerCacheNeedsRepair;`)();
if(!mapLayerCacheNeedsRepair({ready:true,rasterComplete:false,rasterScheduled:false,groundCells:null})||
   mapLayerCacheNeedsRepair({ready:true,rasterComplete:false,rasterScheduled:true,groundCells:null})||
   mapLayerCacheNeedsRepair({ready:true,rasterComplete:false,rasterScheduled:false,groundCells:[]} )||
   mapLayerCacheNeedsRepair({ready:true,rasterComplete:true,rasterScheduled:false,groundCells:null})||
   mapLayerCacheNeedsRepair({ready:false,rasterComplete:false,rasterScheduled:false,groundCells:null})){
  throw new Error("an interrupted map raster must be detected and rebuilt");
}
if(!/current&&current\.key===key&&mapLayerCacheNeedsRepair\(current\)[\s\S]{0,180}current\.ready=false;current\.dirty=true;current\.rasterFailed=true/.test(script)){
  throw new Error("ensureMapLayerCache must invalidate stale incomplete caches");
}
if(!/function mapLayerCacheUsable\(cache\)\{[\s\S]{0,260}cache\.rasterComplete!==false[\s\S]{0,120}cache\.rasterFailed/.test(script)||
   !/const waitingForDynamicCache=Boolean\(dynamicReady&&center&&\(!mapLayerCacheUsable\(layerCache\)&&!mapLayerCacheUsable\(app\.mapLayerFallback\)\)\)/.test(renderWorldGuardSource)||
   !/const groundCache=mapLayerCacheUsable\(layerCache\)\?layerCache:\(mapLayerCacheUsable\(app\.mapLayerFallback\)\?app\.mapLayerFallback:null\)/.test(renderWorldGuardSource)||
   !/cache\.rasterComplete=true;/.test(script)||
   !/waitingForDynamicCache&&app\.mapLoading&&!mapTransitionState\.active\)[\s\S]{0,260}maybeFinishMapLoading\(\);[\s\S]{0,100}renderWorldOverlay\(\);[\s\S]{0,40}return;/.test(renderWorldGuardSource)||
   !/app\.worldBackBufferHasFrame=true/.test(script)||
   !/app\.worldBackBufferHasFrame=false;const result=enterWorldWithoutBattleTimers/.test(script)){
  throw new Error("map refresh must retain the last complete back-buffer until the replacement cache is ready");
}
if (!script.includes("function parseMapWindowHeader(value)") ||
    !script.includes("drawTimeAnime:true") ||
    !script.includes("app.mapDrawTimeAnime=header.drawTimeAnime!==false")) {
  throw new Error("M/MC palette must stay independent from the day/night field strip");
}
if (!script.includes("fetch(`/audio/pal/Palet_${id}.sap`") ||
    !script.includes("function mapPaletteImage(file,image)") ||
    !script.includes("正在应用地图调色板")) {
  throw new Error("fixed map palettes must be applied to live map bitmaps");
}
if (!script.includes("const MAP_PALETTE_FILE_IDS=Object.freeze([1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,0])") ||
    !script.includes("function mapPaletteFileId(value=app.mapPaletteNo)") ||
    !script.includes("function mapPaletteUsesBase(value=app.mapPaletteNo)")) {
  throw new Error("2.5 map palette indexes must follow the 8.5 palname.h file order");
}
if (!script.includes("async function refreshTalkServerPosition()") ||
    !script.includes('if(!moving&&(!sameMovePoint(app.serverPosition,app.position)||sampleStale))')) {
  throw new Error("NPC talk must refresh the authoritative 2.5 position before TK");
}
if (!script.includes("function scheduleMoveWireDrain(delay=320)") ||
    !script.includes("if(app.moveWirePending)scheduleMoveWireDrain();") ||
    !script.includes("route!==String(app.moveWireRoute||\"\")")) {
  throw new Error("normal owner walks must drain the wire latch without waiting for the long watchdog");
}
if (!script.includes("const cursorTarget=cursorCandidate&&!cursorCandidate.staticNPC?cursorCandidate:null")) {
  throw new Error("stale static NPC cursor metadata must not outrank live NPCs");
}
if (!script.includes("const implicitFacing=facingTarget&&(!facingTarget.staticNPC||!liveTalkNearby)?facingTarget:null")) {
  throw new Error("synthetic facing NPC metadata must not hijack live talk NPCs");
}
/* A live C record can evict a floor-wide metadata twin while the NPC is in
   the server viewport.  When the C object later leaves, the twin must be
   rebuilt with its original REALBIN graphic and direction; dropping either
   field makes the NPC render as an invisible graphic-0 actor. */
const npcMetadataStart = script.indexOf("function mergeStaticNPCMetadata(records,floor)");
const npcMetadataEnd = script.indexOf("function removeStaticNPCAt", npcMetadataStart);
const npcMetadataSource = script.slice(npcMetadataStart, npcMetadataEnd);
if (npcMetadataStart < 0 || npcMetadataEnd <= npcMetadataStart ||
    !/const metadata=\{[^}]*graphic,direction:stateNumber\(record\.direction\)/.test(npcMetadataSource) ||
    !/graphicKey:String\(metadata\.graphic\|\|\"\"\)/.test(script)) {
  throw new Error("static NPC metadata must preserve graphic/direction for live-object restoration");
}
const cliHeader = fs.readFileSync(__dirname + "/../../vendor/upstream/code_sa_client/SYSTEMINC/LSSPROTO_CLI.H", "utf8");
const expectedClientFunctions = [...cliHeader.matchAll(/void lssproto_(\w+)_send\s*\(/g)].map(match => match[1]);
const expectedServerFunctions = [...cliHeader.matchAll(/void lssproto_(\w+)_recv\s*\(/g)].map(match => match[1]);
for (const name of expectedClientFunctions) if (!P.CLIENT_FIELDS[name]) throw new Error(`missing client protocol schema ${name}`);
for (const name of expectedServerFunctions) if (!P.SERVER_FIELDS[name]) throw new Error(`missing server protocol schema ${name}`);
if (expectedClientFunctions.length !== 50 || expectedServerFunctions.length !== 36) throw new Error("unexpected upstream protocol header shape");

/* The archived Windows header contains optional entry points that are not
   implemented by the local 2.5 gateway.  Check the actual bridge schema as
   well as the upstream header: this catches both accidental 8.5 additions
   and field-count drift that would otherwise desynchronise the numeric GMSV
   parser while the visual client is being extended. */
const bridgeSource = fs.readFileSync(__dirname + "/../../server/go/bridge/translator.go", "utf8");
const clientSchemaStart = bridgeSource.indexOf("var clientToServerSchemas");
const serverSchemaStart = bridgeSource.indexOf("var serverToClientSchemas");
if (clientSchemaStart < 0 || serverSchemaStart <= clientSchemaStart) throw new Error("2.5 bridge schema boundary missing");
const bridgeClientSource = bridgeSource.slice(clientSchemaStart, serverSchemaStart);
const bridgeServerSource = bridgeSource.slice(serverSchemaStart);
const bridgeSchemas = new Map();
const bridgeServerSchemas = new Map();
/* These are browser-side decode aliases, not wire functions.  BC is the
   marker inside the legacy B command, and PETS is the spelling used by the
   optional _PETS_SELECTCON callback while the numeric bridge keeps its
   historical PETST name. */
const serverSchemaAliases = new Set(["BC", "PETS"]);
const bridgeSchemaPattern = /schema\("([^"]+)",\s*\d+((?:,\s*field(?:Int|String))*)\)/g;
for (const match of bridgeClientSource.matchAll(bridgeSchemaPattern)) {
  const kinds = [...match[2].matchAll(/field(Int|String)/g)].map(kind => kind[1] === "Int" ? "int" : "string");
  bridgeSchemas.set(match[1], kinds);
}
for (const match of bridgeServerSource.matchAll(bridgeSchemaPattern)) {
  const kinds = [...match[2].matchAll(/field(Int|String)/g)].map(kind => kind[1] === "Int" ? "int" : "string");
  bridgeServerSchemas.set(match[1], kinds);
}
if (!bridgeSchemas.size) throw new Error("2.5 bridge client schema is empty");
if (!bridgeServerSchemas.size) throw new Error("2.5 bridge server schema is empty");
const pageSchemas = new Map(Object.entries(P.CLIENT_FIELDS));
const pageServerSchemas = new Map(Object.entries(P.SERVER_FIELDS));
for (const [name, kinds] of bridgeSchemas) {
  const pageKinds = pageSchemas.get(name);
  if (!pageKinds || pageKinds.join(",") !== kinds.join(",")) {
    throw new Error(`web schema differs from 2.5 bridge for ${name}: ${JSON.stringify(pageKinds)} != ${JSON.stringify(kinds)}`);
  }
}
for (const [name, kinds] of bridgeServerSchemas) {
  const pageKinds = pageServerSchemas.get(name);
  if (!pageKinds || pageKinds.join(",") !== kinds.join(",")) {
    throw new Error(`web server schema differs from 2.5 bridge for ${name}: ${JSON.stringify(pageKinds)} != ${JSON.stringify(kinds)}`);
  }
}
for (const name of pageSchemas.keys()) {
  if (!bridgeSchemas.has(name)) throw new Error(`web schema is not supported by the 2.5 bridge: ${name}`);
}
for (const name of pageServerSchemas.keys()) {
  if (!bridgeServerSchemas.has(name) && !serverSchemaAliases.has(name)) {
    throw new Error(`web server schema is not supported by the 2.5 bridge: ${name}`);
  }
}
if (pageSchemas.size !== bridgeSchemas.size) throw new Error("web/bridge client schema count mismatch");
if (pageServerSchemas.size !== bridgeServerSchemas.size + serverSchemaAliases.size) throw new Error("web/bridge server schema count mismatch");

/* Scan the actual browser call sites, not only the exported schema table.
   This is the guard that catches a future 8.5 visual/menu port adding a
   literal send("SaMenu", ...) while someone forgets to update the table.
   Dynamic protocol-console calls are still constrained by packetMessage(),
   while every literal call used by gameplay must be present in the 2.5
   bridge table. */
const runtimeSource = script.slice(protocolEnd);
/* The two native MENU.CPP paths that do not have a generic field button must
   remain reachable from the dedicated web surfaces: PET MAIL uses PMSG,
   while a non-battle pet skill uses PS.  Keep these checks beside the
   protocol boundary so a visual refactor cannot silently turn either row
   back into a no-op. */
if (!/packetName="PMSG"[\s\S]{0,500}packetValues=index=>\[index,petIndex[\s\S]{0,180}send\(packetName,packetValues\(index\)\)/.test(runtimeSource)) {
  throw new Error("pet mail composer lost its PMSG send path");
}
if (!/function useFieldPetSkill\(petIndex,skill\)[\s\S]{0,900}fieldSend\("PS",\[Number\(petIndex\),skillIndex,0,""\]\)/.test(runtimeSource)) {
  throw new Error("pet skill page lost its field PS send path");
}
const literalSendNames = new Set();
for (const match of runtimeSource.matchAll(/\b(?:send|fieldSend)\(\s*["'`]([^"'`]+)["'`]\s*,/g)) {
  literalSendNames.add(match[1]);
}
for (const match of runtimeSource.matchAll(/\bpacketMessage\(\s*[^,]+,\s*["'`]([^"'`]+)["'`]\s*,/g)) {
  literalSendNames.add(match[1]);
}
for (const name of literalSendNames) {
  if (!bridgeSchemas.has(name)) throw new Error(`browser call site is outside the 2.5 bridge: ${name}`);
}
for (const name of ["SaMenu", "RideQuery", "SignDay", "STREET_VENDOR"]) {
  let rejected = false;
  try { P.packetMessage(1, name, []); } catch (_) { rejected = true; }
  if (!rejected) throw new Error(`8.5-only function unexpectedly encodable: ${name}`);
}

/* The generated Windows header is useful for codec vectors, but it is not
   the compatibility authority: the running service is the local 2.5 GMSV.
   Compare every numeric id in that server header with the bridge tables so a
   later 8.5 entry cannot sneak in under a reused function name or number.
   The legacy header is GBK, while these preprocessor lines are ASCII. */
const gmsvHeader = fs.readFileSync(__dirname + "/../../server/legacy/source/2.5/gmsv/include/lssproto_serv.h", "latin1");
const localWire = {client: new Map(), server: new Map()};
for (const match of gmsvHeader.matchAll(/^#define\s+LSSPROTO_(\w+)_(RECV|SEND)\s+(\d+)/gm)) {
  const rawName = match[1];
  const side = match[2] === "RECV" ? "client" : "server";
  /* W2 is the lower-case named `w` entry in the preserved client protocol;
     all other generated identifiers are case-insensitive spellings of the
     bridge name (CLIENTLOGIN -> ClientLogin, etc.). */
  const name = rawName === "W2" ? "w" : [...(side === "client" ? bridgeSchemas : bridgeServerSchemas).keys()]
    .find(candidate => candidate.toUpperCase() === rawName);
  if (!name) throw new Error(`2.5 header function has no bridge schema: ${rawName}_${match[2]}`);
  localWire[side].set(name, Number(match[3]));
}
if (localWire.client.size !== bridgeSchemas.size || localWire.server.size !== bridgeServerSchemas.size) {
  throw new Error(`2.5 header/bridge function count mismatch: ${localWire.client.size}/${bridgeSchemas.size} client, ${localWire.server.size}/${bridgeServerSchemas.size} server`);
}
const bridgeClientIds = new Map();
for (const match of bridgeClientSource.matchAll(/schema\("([^"]+)",\s*(\d+)/g)) bridgeClientIds.set(match[1], Number(match[2]));
const bridgeServerIds = new Map();
for (const match of bridgeServerSource.matchAll(/schema\("([^"]+)",\s*(\d+)/g)) bridgeServerIds.set(match[1], Number(match[2]));
for (const [name, number] of localWire.client) {
  if (bridgeClientIds.get(name) !== number) throw new Error(`2.5 client id drift for ${name}: ${number} != ${bridgeClientIds.get(name)}`);
  if (!pageSchemas.has(name)) throw new Error(`2.5 client header function missing from web schema: ${name}`);
}
for (const [name, number] of localWire.server) {
  if (bridgeServerIds.get(name) !== number) throw new Error(`2.5 server id drift for ${name}: ${number} != ${bridgeServerIds.get(name)}`);
  if (!pageServerSchemas.has(name)) throw new Error(`2.5 server header function missing from web schema: ${name}`);
}
for (const name of pageServerSchemas.keys()) {
  if (!bridgeServerSchemas.has(name) && !serverSchemaAliases.has(name)) throw new Error(`web server schema is not supported by the 2.5 bridge: ${name}`);
}
if (pageServerSchemas.size - serverSchemaAliases.size !== bridgeServerSchemas.size) throw new Error("web/bridge server schema count mismatch");

/* FIELD.CPP treats the settings and Action toolbar entries as toggles.  A
   second click on the already-visible window must close it; otherwise the
   extracted button's down state can never be cleared and the field becomes
   trapped behind an Action window.  Keep this source invariant beside the
   protocol checks so a future UI refactor cannot silently restore the old
   unconditional openFieldWindow() call. */
const fieldActionToggle = /function fieldAction\(name\)\{[\s\S]{0,1200}if\(name==="settings"\|\|name==="action"\)\{[\s\S]{0,700}if\(target&&!target\.classList\.contains\("hidden"\)\)\{closeFieldWindow\(\);return;\}[\s\S]{0,240}openFieldWindow\(name==="settings"\?"settings":"actions"\);return;\s*\}/;
if (!fieldActionToggle.test(script)) throw new Error("field settings/action windows are not toggleable");
if (!/function setActivePanelButton\(name=""\)\{[\s\S]{0,500}node\.dataset\.fieldAction===name/.test(script) ||
    !/function openFieldWindow\(name\)\{[\s\S]{0,700}setActivePanelButton\(name==="actions"\?"action":"settings"\)/.test(script)) {
  throw new Error("field MENU/Action pressed artwork is not synchronized");
}

/* BATTLE_ActSettingSend() has two broadcast loops: the primary battle and
   any linked/parent battle.  The latter must send BA to its local
   `charaindex`; accidentally reusing the primary loop's `pindex` leaves the
   linked client's BattleProc in RECEIVE_MOVIE forever.  Keep this tiny source
   invariant in the protocol gate because the legacy file is GBK/byte-oriented
   and cannot be covered by the browser's JS unit fixtures. */
const battleCommandSource = fs.readFileSync(__dirname + "/../../server/legacy/source/2.5/gmsv/battle/battle_command.c", "latin1");
const actSettingStart = battleCommandSource.indexOf("void BATTLE_ActSettingSend");
const actSettingEnd = battleCommandSource.indexOf("BOOL BATTLE_IsHide", actSettingStart);
if (actSettingStart < 0 || actSettingEnd <= actSettingStart) throw new Error("BATTLE_ActSettingSend source missing");
const actSettingSource = battleCommandSource.slice(actSettingStart, actSettingEnd);
const primaryBroadcast = /if\( CHAR_getInt\( pindex, CHAR_WHICHTYPE \) == CHAR_TYPEPLAYER[\s\S]{0,260}BATTLE_CommandSend\( pindex, szBA \);/.test(actSettingSource);
const linkedBroadcast = /charaindex = pBattle->Side\[0\]\.Entry\[i\]\.charaindex[\s\S]{0,300}BATTLE_CommandSend\( charaindex, szBA \);/.test(actSettingSource);
if (!primaryBroadcast || !linkedBroadcast) throw new Error("BATTLE_ActSettingSend BA recipient regression");
/* CHAR_DropMoney() owns the actual landing cell in the 2.5 GMSV.  Preserve
   the order facing cell -> other seven neighbours -> own cell fallback. */
const charItemSource = fs.readFileSync(__dirname + "/../../server/legacy/source/2.5/gmsv/char/char_item.c", "latin1");
const dropMoneyStart = charItemSource.indexOf("void CHAR_DropMoney(");
const dropMoneyEnd = charItemSource.indexOf("END:", dropMoneyStart);
if (dropMoneyStart < 0 || dropMoneyEnd <= dropMoneyStart) throw new Error("CHAR_DropMoney source missing");
const dropMoneySource = charItemSource.slice(dropMoneyStart, dropMoneyEnd);
if (!/dirx\[i\+1\] = CHAR_getDX\([\s\S]{0,260}dirx\[0\] = CHAR_getDX[\s\S]{0,180}dirx\[8\] = 0;/.test(dropMoneySource) ||
    !/for\( i = 0 ; i < 9 ; i \+\+ \)[\s\S]{0,3000}CHAR_getInt\(charaindex,CHAR_X\) \+ dirx\[8\]/.test(dropMoneySource)) {
  throw new Error("CHAR_DropMoney must exhaust surrounding cells before the player cell");
}
console.log("web protocol vectors OK");
