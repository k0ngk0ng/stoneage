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
    !battleExtractorSource.includes("bitmap_cache = {}")) {
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
if (battleFiles.length !== 220) throw new Error(`generated battle viewport count drifted: ${battleFiles.length} != 220`);
const transparentBattleFiles = [];
for (let battle = 0; battle < 220; battle++) {
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
if (Object.keys(battleManifest.battles || {}).length !== 220) throw new Error("battle manifest must describe all 220 native SAB files");
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
for (let battle = 0; battle < 220; battle++) {
  const entry = battleManifest.battles?.[String(battle)], name = `battle_${String(battle).padStart(2, "0")}.png`;
  if (entry?.image !== `battle/${name}` || entry?.render?.width !== 640 || entry?.render?.height !== 480) {
    throw new Error(`battle manifest viewport drift for ${battle}: ${JSON.stringify(entry)}`);
  }
}

const html = fs.readFileSync(__dirname + "/index.html", "utf8");
const script = html.match(/<script>([\s\S]*?)<\/script>/)[1];
for (const expected of [
  'fetch(ASSET_MANIFEST_URL,{cache:"no-cache"',
  'fetch(CREATION_SPRITE_MANIFEST_URL,{cache:"no-cache"',
  'fetch(FIELD_SPRITE_MANIFEST_URL,{cache:"no-cache"',
  'fetch(`${SPRITE_MANIFEST_URL}`,{cache:"no-cache"',
]) {
  if (!script.includes(expected)) {
    throw new Error(`asset manifest must revalidate across deployments: ${expected}`);
  }
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
    !script.includes("return assetState.sprites||assetState.fieldSprites||assetState.creationSprites||assetState.manifest?.sprites||null") ||
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
/* A held pointer can complete one route, receive its one-shot S:c sample,
   then continue moving after the native one-second move-mode delay.  That
   newer movement must get its own final sample; otherwise serverPosition
   remains at the intermediate tile and later NPC/logout checks roll back to
   stale state.  Re-arm only after a real authoritative version increment so
   a silent 2.5 connection still cannot generate a probe loop. */
const beginMovePredictionStart = script.indexOf("  function beginMovePrediction(from,target){");
const beginMovePredictionEnd = script.indexOf("  function maybeReleaseMovePrediction", beginMovePredictionStart);
if (beginMovePredictionStart < 0 || beginMovePredictionEnd <= beginMovePredictionStart) {
  throw new Error("move prediction boundary not found");
}
const acknowledgedPrediction = {
  movePredictionActive: true,
  movePredictionStartedAt: 1,
  movePredictionServerVersion: 4,
  serverPositionVersion: 5,
  movePredictionSyncRequested: true,
  movePredictionSyncAttempts: 1,
  movePredictionTrail: [[10, 10]],
};
const makeBeginMovePrediction = state => new Function("app", "sameMovePoint", "scheduleMovePredictionRelease",
  `${script.slice(beginMovePredictionStart, beginMovePredictionEnd)};return beginMovePrediction;`)(
    state,
    (left, right) => Array.isArray(left) && Array.isArray(right) && left[0] === right[0] && left[1] === right[1],
    () => {},
  );
makeBeginMovePrediction(acknowledgedPrediction)([10, 10], [10, 11]);
if (acknowledgedPrediction.movePredictionSyncRequested || acknowledgedPrediction.movePredictionServerVersion !== 5 ||
    acknowledgedPrediction.movePredictionSyncAttempts !== 0) {
  throw new Error("acknowledged intermediate move sample must re-arm the final S:c probe");
}
const silentPrediction = {
  movePredictionActive: true,
  movePredictionStartedAt: 1,
  movePredictionServerVersion: 4,
  serverPositionVersion: 4,
  movePredictionSyncRequested: true,
  movePredictionSyncAttempts: 1,
  movePredictionTrail: [[10, 10]],
};
makeBeginMovePrediction(silentPrediction)([10, 10], [10, 11]);
if (!silentPrediction.movePredictionSyncRequested || silentPrediction.movePredictionServerVersion !== 4 ||
    silentPrediction.movePredictionSyncAttempts !== 1) {
  throw new Error("silent move sample must retain the one-shot probe latch");
}
/* The server executes W's first step immediately and its second on a walk
   tick, so the first S:c can legitimately be one tile behind the completed
   local route.  The client gets exactly one delayed resample; a second
   differing authoritative reply must correct locally instead of probing in
   an unbounded loop. */
const maybeReleaseStart = script.indexOf("  function maybeReleaseMovePrediction(point){");
const maybeReleaseEnd = script.indexOf("  function noteServerMove", maybeReleaseStart);
if (maybeReleaseStart < 0 || maybeReleaseEnd <= maybeReleaseStart) {
  throw new Error("move prediction release boundary not found");
}
const makeMaybeRelease = (state, hooks) => new Function(
  "app", "sameMovePoint", "clearMovePrediction", "scheduleMovePredictionRelease",
  "requestMovePredictionSync", "refreshSettledMoveState", "cancelPendingMove", "window",
  "MOVE_PREDICTION_MAX_SYNC_ATTEMPTS", "MOVE_PREDICTION_RESAMPLE_DELAY_MS",
  `${script.slice(maybeReleaseStart, maybeReleaseEnd)};return maybeReleaseMovePrediction;`,
)(state, hooks.same, hooks.clear, hooks.schedule, hooks.request, hooks.refresh,
  hooks.cancel, hooks.window, 2, 320);
const retryTimers = [];
const tailRace = {
  movePredictionActive: true, pendingMove: false, moveQueue: [], moveSentSteps: 0,
  position: [10, 11], serverPositionVersion: 5, movePredictionServerVersion: 4,
  movePredictionSyncRequested: true, movePredictionSyncAttempts: 1, _movePredictionTimer: 0,
};
let tailRaceCancelled = 0;
const tailRaceHooks = {
  same: (left, right) => left[0] === right[0] && left[1] === right[1],
  clear: () => { throw new Error("a one-tile intermediate sample must not release prediction"); },
  schedule: () => {},
  request: () => { tailRace.movePredictionSyncRequested = true; tailRace.movePredictionSyncAttempts++; },
  refresh: () => {},
  cancel: () => { tailRaceCancelled++; },
  window: {clearTimeout: () => {}, setTimeout: callback => { retryTimers.push(callback); return 7; }},
};
const maybeReleaseTailRace = makeMaybeRelease(tailRace, tailRaceHooks);
maybeReleaseTailRace([10, 10]);
if (tailRace.movePredictionSyncRequested || tailRace.movePredictionServerVersion !== 5 ||
    retryTimers.length !== 1 || tailRaceCancelled) {
  throw new Error("first acknowledged tail-step race must schedule exactly one delayed S:c resample");
}
retryTimers[0]();
if (!tailRace.movePredictionSyncRequested || tailRace.movePredictionSyncAttempts !== 2 || retryTimers.length !== 1) {
  throw new Error("delayed tail-step resample must remain bounded while awaiting its reply");
}
tailRace.serverPositionVersion = 6;
maybeReleaseTailRace([10, 10]);
if (tailRaceCancelled !== 1 || retryTimers.length !== 1) {
  throw new Error("second differing authoritative sample must correct locally without another probe");
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
if (!/function canonicalAccount\(value\)[\s\S]{0,420}replace\(\/\[A-Z\]\/g/.test(script)) {
  throw new Error("web login must canonicalise ASCII account names before sending them to 2.5");
}
if (!/id="world-loading-progress"[^>]*role="progressbar"/.test(html) ||
    !/id="world-loading-detail"/.test(html) ||
    !/id="world-loading-retry"/.test(html) ||
    !/main\.field-loading-active #field-ui[\s\S]{0,260}visibility:hidden/.test(html) ||
    !/function mapLoadingProgress\([\s\S]{0,1800}assetNetworkBytes/.test(script) ||
    !/function assetCachedBytes\([\s\S]{0,900}base64/.test(script) ||
    !/function renderMapLoadingProgress\([\s\S]{0,1800}world-loading-detail/.test(script) ||
    !/if\(indeterminate\)bar\.style\.removeProperty\("width"\)/.test(script) ||
    !/function retryMapLoading\([\s\S]{0,1200}app\.mapLayerCache=null/.test(script) ||
    !/manifestAttempts/.test(script) ||
    !/preferredStable=stable&&stable\.width\*stable\.height>current\.width\*current\.height/.test(script)) {
  throw new Error("map loading must show progress/received bytes and hide field controls while blocked");
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
   PutBmp().  The browser world surface must not regress to a transparent
   canvas: the alpha corners of an isometric map tile would reveal the DOM
   backdrop and create a triangular flash while walking or changing floors. */
if (!/function getWorld2DContext\([\s\S]{0,1200}getCanvas2DContext\(canvas,\{alpha:false/.test(script) ||
    !/ctx\.globalCompositeOperation="copy";ctx\.fillStyle="#000";ctx\.fillRect\(0,0,canvas\.width,canvas\.height\);ctx\.globalCompositeOperation="source-over"/.test(script)) {
  throw new Error("world back-buffer must use an opaque native-style clear before presenting");
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
const tradeWindowPng = fs.readFileSync(path.join(__dirname, "assets", "original", tradeWindow.file));
if (tradeWindowPng.readUInt32BE(16) !== 620 || tradeWindowPng.readUInt32BE(20) !== 456 ||
    !battleExtractorSource.includes('"trade_window_25": 40000') || !battleExtractorSource.includes('parser.add_argument("--ui-only"')) {
  throw new Error("classic 2.5 trade window was not extracted by the UI-only asset path");
}
const tradeMarkup = html.match(/<section id="trade-screen"[\s\S]*?<\/section>/)?.[0] || "";
if (!/id="trade-window-art" src="\/assets\/bitmaps\/bitmap_126231\.png"/.test(tradeMarkup) ||
    (tradeMarkup.match(/data-trade-offer=/g) || []).length !== 2 ||
    !/id="trade-inventory-grid"/.test(tradeMarkup) || !/id="trade-confirm"/.test(tradeMarkup) ||
    /26328|bitmap_126230/.test(tradeMarkup)) {
  throw new Error("classic 2.5 trade surface markup is incomplete or uses the 8.5 plate");
}
for (const expected of [
  /#trade-window-art\s*\{[^}]*left:10px; top:0; width:620px; height:456px/,
  /#trade-confirm\s*\{[^}]*left:369px; background-image:url\('\/assets\/bitmaps\/bitmap_9211\.png'\)/,
  /#trade-cancel\s*\{[^}]*left:501px; background-image:url\('\/assets\/bitmaps\/bitmap_9170\.png'\)/,
  /const column=\(index-5\)%5,row=Math\.floor\(\(index-5\)\/5\)/,
  /slot\.style\.left=`\$\{332\+column\*51\}px`;slot\.style\.top=`\$\{248\+row\*48\}px`/,
  /case "TD": handleTradeMessage\(values\[0\]\|\|""\);break;/,
]) {
  if (!expected.test(html)) throw new Error(`classic 2.5 trade layout/dispatch regression: ${expected}`);
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
    !/id="field-left-trade" class="click" data-field-action="trade" src="\/assets\/bitmaps\/bitmap_126233\.png"/.test(fieldUiMarkup) ||
    !/id="field-left-mail" src="\/assets\/bitmaps\/bitmap_9225\.png"/.test(fieldUiMarkup) ||
    !/id="field-right-join" class="click"/.test(fieldUiMarkup) ||
    !/id="field-right-duel" class="click"/.test(fieldUiMarkup) ||
    !/id="field-right-action" class="click"/.test(fieldUiMarkup)) {
  throw new Error("2.5 field HUD must contain four left and three right controls");
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
   !/function ensureFieldActionSprites\(actor\)[\s\S]{0,900}loadFieldSpriteManifest\(\)[\s\S]{0,900}loadSpriteManifest\(\)/.test(localActionAnimationSource) ||
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
/* FIELD.CPP::actionShortCutKeyProc() exposes the same 13 actions through
   Ctrl+keys.  Keep the exact mapping in the web keyboard boundary and route
   it through the DOM row so the local animation/AC path stays authoritative. */
const actionShortcutStart = script.indexOf('if(event.ctrlKey&&app.phase==="world"&&!app.battle)');
const actionShortcutSource = script.slice(actionShortcutStart, script.indexOf('if(app.phase==="world"&&!app.battle){const directions=', actionShortcutStart));
if (!/const actionShortcuts=\{"0":0,"\^":1,"9":2,"7":3,"8":4,"1":5,"2":6,"4":7,"5":8,"6":9,"-":10,"3":11,"\\\\":12\}/.test(actionShortcutSource) ||
    !/field-actions-list \[data-action-no=/.test(actionShortcutSource) ||
    !/!mapMovementBlocked\(\)&&!app\.pointerMoveHeld/.test(actionShortcutSource)) {
  throw new Error("field Action Ctrl shortcuts must match the native 2.5 mapping");
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
  /id="field-left-bg" src="\/assets\/bitmaps\/bitmap_126232\.png"/,
  /"field-left-trade":\[126233,126234\]/,
  /if\(name==="trade"\)[\s\S]{0,900}fieldSend\("TD",\["D\|D"\]\)/,
  /id="field-right-bg" src="\/assets\/bitmaps\/bitmap_9226\.png"/,
  /#field-settings-screen \.field-window-frame\{left:16px;top:16px;height:240px\}/,
  /#field-actions-screen \.field-window-frame\{left:440px;top:16px;height:288px\}/,
  /#field-settings-screen #field-settings-close\{left:72px;top:208px\}/,
  /#field-actions-screen #field-actions-close\{left:496px;top:266px\}/,
  /FIELD_SETTING_LABELS=Object\.freeze\(\{[\s\S]{0,520}chat:\["聊    天："," 全  员"," 队  伍"\],[\s\S]{0,100}trade:\["交    易："," Ｎ  Ｏ"," ＹＥＳ"\]/,
  /id="help-frame" src="\/assets\/bitmaps\/bitmap_234545\.png"/,
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
  /function battleButtonPressed\(command,state=app\.battleState\)[\s\S]{0,520}pendingCommand=battleButtonCommandForAction\(state\.pendingAction\)/,
  /function armDefaultBattleAttack\(state=app\.battleState\)[\s\S]{0,700}lastPlayerActionKind[\s\S]{0,120}attack[\s\S]{0,520}pendingAction=\{kind:"attack",defaulted:true\}[\s\S]{0,220}renderBattleWorld\(\);renderBattle\(\)/,
  /* MAP.CPP derives environmental levels from nearby object parts 80..89;
     DIRECTDRAW.CPP then paints the exact indexed rain/snow pixel patterns
     into the same field back-buffer. */
  /function mapEffectWeatherLevels\(map=app\.map\)[\s\S]{0,900}value>=80&&value<=84[\s\S]{0,220}value>=85&&value<=89/,
  /const MAP_EFFECT_RAIN_COLOR="#e3f8ff"/,
  /function drawMapEffects\(ctx\)[\s\S]{0,1800}fillRect\(x,y-1,1,1\)[\s\S]{0,700}MAP_EFFECT_SNOW_BRIGHT/,
  /function ensureMapEffectStars\(now\)[\s\S]{0,1000}MAP_EFFECT_STAR_PATTERNS/,
  /function renderWorld\(force=false\)[\s\S]{0,260}updateMapEffects\(now\)[\s\S]{0,4200}renderSceneActorsAndParts\(domActors,parts,canvas\);[\s\S]{0,160}presentWorldBackBuffer\(canvas\)/,
  /function renderSceneActorsAndParts\([\s\S]{0,2600}drawMapEffects\(ctx\)[\s\S]{0,420}StockFontBuffer|DISP_PRIO_RESERVE is emitted[\s\S]{0,260}drawMapEffects\(ctx\)/,
  /* map.cpp's held-left-button mode samples a new moveStack point every
     250 ms; the browser must keep the gesture separate from ordinary UI
     clicks and hide the fish-bone until the physical button is released. */
  /const LEGACY_MOVE_SPEED=4;[\s\S]{0,420}const LEGACY_PROC_TICK_MS=8;[\s\S]{0,260}const MOVE_CARDINAL_DURATION=LEGACY_GRID_SIZE\/LEGACY_MOVE_SPEED\*LEGACY_PROC_TICK_MS;/,
  /const BATTLE_PROC_TICK_MS=1000\/60;/,
  /function moveStepDuration\(from,target\)[\s\S]{0,360}const distance=Math\.hypot\(dx,dy\)[\s\S]{0,120}distance\|\|1/,
  /* A normal 2.5 owner walk has no self C/XYD echo.  The prediction
     watchdog may issue an initial S:c plus one acknowledged tail-step
     resample, but must stop when the server does not answer. */
  /movePredictionSyncRequested/,
  /if\(!app\.movePredictionSyncRequested&&Date\.now\(\)-started>=grace\)\{[\s\S]{0,300}requestMovePredictionSync\(\)[\s\S]{0,260}return;/,
  /function requestMovePredictionSync\(\)[\s\S]{0,800}send\("S",\["c"\]\)/,
  /const MOVE_PREDICTION_MAX_SYNC_ATTEMPTS=2;/,
  /function maybeReleaseMovePrediction\(point\)[\s\S]{0,1800}MOVE_PREDICTION_RESAMPLE_DELAY_MS[\s\S]{0,360}cancelPendingMove\("服务器已校正位置。",point\)/,
  /function scheduleMoveWireDrain\(delay=320\)[\s\S]{0,900}requestMovePredictionSync\(\)/,
  /if\(app\.moveWirePending\|\|app\.movePredictionActive&&!app\.movePredictionSyncRequested\)\{/,
  /function actorFrame\(actor\)[\s\S]{0,1200}if\(!key\|\|key==="0"\)return previousFrame\|\|null;[\s\S]{0,1900}if\(!frames\.length\)return previousFrame\|\|null;/,
  /const POINTER_MOVE_ROUTE_INTERVAL_MS=250;/,
  /const POINTER_MOVE_MODE_DELAY_MS=1000;/,
  /function sampleHeldWorldPointer\(now=Date\.now\(\)\)[\s\S]{0,900}worldTileFromPointerPosition\(clientX,clientY\)[\s\S]{0,260}setHeldMoveDestination\(tile,now,false\)/,
  /function scheduleHeldWorldPointerSample\(delay=POINTER_MOVE_MODE_DELAY_MS\)[\s\S]{0,900}sampleHeldWorldPointer\(Date\.now\(\)\)[\s\S]{0,260}scheduleHeldWorldPointerSample\(POINTER_MOVE_ROUTE_INTERVAL_MS\)/,
  /function updateWorldPointer\(event\)[\s\S]{0,2400}if\(!app\.pointerMoveHeld\)app\.cursor\.visible=true/,
  /function beginHeldWorldPointer\(event\)[\s\S]{0,1200}app\.cursor\.visible=false/,
  /function beginHeldWorldPointer\(event\)[\s\S]{0,1800}setHeldMoveDestination\(tile,Date\.now\(\),true\)[\s\S]{0,240}scheduleHeldWorldPointerSample\(POINTER_MOVE_MODE_DELAY_MS\)/,
  /function updateHeldWorldPointer\(event\)[\s\S]{0,700}app\.cursor\.visible=false/,
  /function updateHeldWorldPointer\(event\)[\s\S]{0,1100}moveModeReady[\s\S]{0,320}setHeldMoveDestination\(tile,Date\.now\(\),false\)/,
  /function endHeldWorldPointer\(event=null,commit=true\)[\s\S]{0,1500}clearHeldWorldPointerSample\(\)[\s\S]{0,700}app\.cursor\.visible=true[\s\S]{0,500}app\.cursor\.updatedAt=Date\.now\(\)/,
  /worldScreen\.addEventListener\("pointerleave",\(\)=>\{if\(app\.phase==="world"\)\{app\.cursor\.visible=!app\.pointerMoveHeld;/,
  /function moveTargetIsSolid\(target\)[\s\S]{0,900}isMapWarpEvent\(event\)\|\|isMapEnemyEvent\(event\)[\s\S]{0,260}localCellWalkable\(target\[0\],target\[1\],false\)===false/,
  /function installMoveRoute\(route,requested\)[\s\S]{0,900}moveTargetIsSolid\(requested\)[\s\S]{0,180}app\.moveTarget=\[Number\(last\[0\]\),Number\(last\[1\]\)\]/,
  /* An in-floor wall/scene-rim click must use the bounded A* nearest-cell
     fallback; returning the straight prefix strands the pointer several
     tiles away from the edge. */
  /Do not return the straight prefix[\s\S]{0,1000}const points=routeFromCells\(origin,to,moveTargetIsSolid\(to\)\);/,
  /* pc.cpp::TalkToNPC() uses a two-tile mouse radius.  Clicking a live NPC
     must talk/approach the object, never install its occupied cell as a W
     destination (which made the web client overlap the NPC). */
  /const targetDistance=Math\.max\(Math\.abs\(Number\(current\.x\)-Number\(app\.position\[0\]\)\),Math\.abs\(Number\(current\.y\)-Number\(app\.position\[1\]\)\)\);[\s\S]{0,620}if\(targetDistance>2\)[\s\S]{0,180}return approachNPC\(current\)/,
  /* A map actor may be painted underneath one of the fixed field controls.
     Native display priority gives the control the click, so keep the UI hit
     guard before actorAtTile() instead of letting the covered NPC consume
     pointerdown and make the toolbar look intermittently unresponsive. */
  /function handleWorldPointerDown\(event\)\{[\s\S]{0,900}if\(event\.button===0\)\{[\s\S]{0,900}if\(worldPointerIsUiTarget\(event\)\)\{[\s\S]{0,240}return;[\s\S]{0,900}const tile=worldTileFromPointer\(event\),actor=tile\?actorAtTile\(tile,isTalkableActor\):null;/,
  /const tile=worldTileFromPointer\(event\),actor=tile\?actorAtTile\(tile,isTalkableActor\):null;\s*if\(actor\)\{[\s\S]{0,900}app\.pointerLookTargetId=Number\(actor\.id\)[\s\S]{0,500}event\.preventDefault\(\);renderWorldOverlay\(\);return;/,
  /if\(distance>0&&distance<=2\)\{[\s\S]{0,220}talkToTarget\(clickedActor\)\.catch\(reportError\);[\s\S]{0,180}return;/,
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
  /if\(targetPoint&&!advancedOpen&&!app\.pointerMoveHeld&&!mapTransitionState\.active\)/,
  /cursor\.style\.display=app\.cursor\.visible!==false\?"block":"none"/,
  /* The painted fish is the final field layer, including over the black
     centre-fold curtain and task-bar hit regions; it must remain pointer
     transparent so the browser never turns the fish into a click shield. */
  /#world-overlay\s*\{[^}]*z-index:1100;[^}]*pointer-events:none/,
  /#world-tools button\s*\{[^}]*pointer-events:auto/,
  /document\.addEventListener\("pointerdown",handleWorldPointerDown,true\)/,
  /document\.addEventListener\("pointercancel",event=>\{if\(app\.phase==="world"\)endHeldWorldPointer\(event,false\);\},true\)/,
  /* The preserved 2.5 T_MUSIC.CPP switch maps only 40..46; 47..59 are
     candidates while drawing but play_map_bgm() leaves the current track. */
  /const MAP_BGM_NO=Object\.freeze\(\{40:4,41:3,42:7,43:8,44:9,45:10,46:11\}\);/,
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
  /battleStartCommandPending\(state,kind,command\);[\s\S]{0,800}renderBattleWorld\(\);/,
  /* EntrySort() already orders B segments by dex; the browser must append
     every segment to one timeline instead of assigning all of them now. */
  /state\.motionQueueAt=start\+length;/,
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
  /const BATTLE_BC_STATUS_DEFS=Object\.freeze\(\[[\s\S]*?graphic:100555[\s\S]*?graphic:101419[\s\S]*?\]\);/,
  /function battleRosterStatuses\(flags\)\{[\s\S]*?1<<10[\s\S]*?graphic:100556/,
  /const BATTLE_FLAG_GRAPHICS=Object\.freeze\(\{[\s\S]*?graphic:26514[\s\S]*?graphic:25869[\s\S]*?graphic:101416[\s\S]*?\}\);/,
  /const BATTLE_ATTRIBUTE_GRAPHICS=Object\.freeze\(\{[\s\S]*?70:101403[\s\S]*?77:101410/,
  /function battleApplyFlagEffects\(effects,target,flags,startsAt=Date\.now\(\),options=\{\}\)/,
  /item\.rideFlag=0;item\.petHp=0;item\.petMaxHp=0;item\.rideFallen=true;/,
  /item\.flags=Number\(item\.flags\|\|0\)&~BATTLE_BC_DEATH[\s\S]{0,260}state\.motions=state\.motions\.filter\(motion=>motion\.kind!=="death"/,
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
  /function submitBattleUnavailableDefaults\(state,turnKey=state\?\.turn\)[\s\S]{0,1800}battleSetCommandLock\(state,"player",true\)[\s\S]{0,1800}state\.implicitPlayerTurn=null[\s\S]{0,500}battleSetCommandLock\(state,"player",false\)/,
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
  /BATTLE_COM_BOOMERANG starts one BO record[\s\S]{0,5600}addDirectDamage\(damageTarget,amount,petAmount,flags,0,impactTiming\)/,
  /* ATT_BOW/ATT_BOOMERANG allocate a separate native missile action; keep a
     visible projectile timeline in the battle back-buffer. */
  /#battle-actors-layer \.battle-projectile\.arrow/,
  /function battlePushProjectile\(state,projectile\)/,
  /function battleProjectileValue\(projectile,now=Date\.now\(\)\)/,
  /waypointOffsets=\[0,\.\.\.boomerangTargets\.map/,
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
  /battlePushProjectile\(state,\{kind,from:\[Number\(from\[0\]\),Number\(from\[1\]\)-34\]/,
  /* BM|bid|0| is the native status-clear record and must never render a
     literal “状态 0” label. */
  /BATTLE_DamageWakeUp\(\)\/BATTLE_BadStatusString\(\)[\s\S]{0,500}if\(status>0\)battlePushEffect/,
  /* ITEM_recv rejects ID while a character is in battle; self/no-target
     items must still go through the B|I command path. */
  /BattleCommandDispach\(\) accepts battle items only through the B[\s\S]{0,700}sendBattleTarget\(self\);return true;/,
  /id="auto-map"/,
  /AUTO_MAP_WIDTH=54/,
  /AUTO_MAP_SEE_FLAG=0x4000/,
  /function drawAutoMap\(/,
  /function requestAutoMapData\(/,
  /fetch\(`?\/maps\//,
  /* MENU.CPP stocks the coordinate fields at independent x/x+73 anchors
     and CG_CLOSE_BTN at (mx,my+102), whose bitmap offset is (-40,-8). */
  /#map-screen #map-coordinates\{left:0;top:0;width:640px;height:480px;/,
  /#map-screen #map-x\{left:449px\}/,
  /#map-screen #map-y\{left:522px\}/,
  /#map-screen #map-close\{left:472px;top:218px;width:80px;height:16px;/,
  /xNode\.textContent=`X \$\{String\(Number\(app\.position\[0\]\)\|\|0\)\.padStart\(3," "\)\}`;/,
  /yNode\.textContent=`Y \$\{String\(Number\(app\.position\[1\]\)\|\|0\)\.padStart\(3," "\)\}`;/,
  /* M's event layer is commonly empty in 2.5; warp/door checks must merge
     the static DAT event table without replacing live tile/object collision. */
  /const liveEvent=Number\(map\.events\?\.\[index\]\?\?0\);[\s\S]{0,900}event=Number\(full\.event\?\.\[fullIndex\]\?\?0\);[\s\S]{0,180}return \{tile:Number\(map\.tiles\?\.\[index\]\?\?0\),object:Number\(map\.objects\?\.\[index\]\?\?0\),event\};/,
  /* ProduceHagare() cuts the 640x480 back-buffer into 64 80x60 shutters;
     keep the scene transition from regressing to the old 8x6 viewport grid. */
  /#scene-transition\s*\{[^}]*width:640px; height:480px;[^}]*grid-template-columns:repeat\(8,[^}]*grid-template-rows:repeat\(8,/s,
  /for\(let index=0;index<64;index\+\+\)/,
  /* The title/login flow redraws directly.  The first shutter is allowed
     only after character selection enters the field; later in-game scene
     changes can keep their native transitions. */
  /function sceneTransitionAllowed\(previous,screen\)[\s\S]{0,900}previous===characterScreen&&screen===worldScreen[\s\S]{0,420}return previousInGame&&nextInGame/,
  /if\(!sceneTransitionAllowed\(previous,screen\)\)\{cancelSceneTransition\(\);return;\}/,
  /function cancelSceneTransition\(\)[\s\S]{0,520}sceneTransition\.classList\.remove\("reveal","active","battle-enter","battle-leave","generic"\)/,
  /const battleEnter=previous===worldScreen&&screen===battleScreen;/,
  /sceneTransition\.classList\.add\("active",battleEnter\?"battle-enter":battleLeave\?"battle-leave":"generic"\)/,
  /* ProduceCenterPress() clears one black back-buffer and vertically
     compresses the complete field surface into the y=240 fold.  Animate the
     complete world/chat surfaces over one black rectangle; two independently
     scaled black halves create a non-native centre seam. */
  /#map-transition\s*\{[^}]*width:640px; height:480px;[^}]*z-index:1000;[^}]*background:#000/,
  /main\.map-transition-press-in #world-screen,[\s\S]{0,140}#chat-screen\s*\{[^}]*map-transition-field-press-in/,
  /main\.map-transition-press-out #world-screen,[\s\S]{0,140}#chat-screen\s*\{[^}]*map-transition-field-press-out/,
  /@keyframes map-transition-field-press-in\s*\{[\s\S]{0,320}scaleY\(1\)[\s\S]{0,180}scaleY\(0\)/,
  /@keyframes map-transition-field-press-out\s*\{[\s\S]{0,320}scaleY\(0\)[\s\S]{0,180}scaleY\(1\)/,
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
  /* shopWindow3 owns a local count picker.  Its OK path confirms and sends
     the 2.5 payload as one-based catalog row plus quantity. */
  /configureItemShopFrame\("quantity"\)[\s\S]{0,1800}windowAssetButton\("减少数量",9280,[\s\S]{0,700}windowAssetButton\("增加数量",9278/,
  /submitShopTransaction\(wnd,session,`\$\{item\.index\+1\}\|\$\{count\}`/,
  /bitmap_\$\{quantity\?9248:9246\}\.png/,
  /* A successful 2.5 shop operation returns only 0|0 or 1|0.  Retain and
     update the client-owned catalog instead of replacing it with an empty
     modal. */
  /const acknowledgement=shop\.valid===0&&shop\.items\.length===0[\s\S]{0,260}applyShopAcknowledgement\(previous\)/,
  /* A fast replacement WN may arrive before the preceding POST resolves;
     the old completion must not close the new shop page. */
  /if\(select!==16&&select!==32&&app\.activeWindow===wnd\)closeServerWindow\(\)/,
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
  /await send\("TK",\[x,y,`P\|\$\{text\}`,0,3\]\);input\.value="";/,
]) {
  if (!expected.test(html)) throw new Error(`field HUD regression: ${expected}`);
}
if (/id="field-right-help"/.test(html)) throw new Error("2.5 field HUD must not expose the 8.5 help button");
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
]) {
  if (!expected.test(html)) throw new Error(`native pet-window layout regression: ${expected}`);
}
/* MENU.CPP uses separate 8px-cell numeric fields. A single proportional
   padded string makes the HP columns drift because browser spaces are not
   eight pixels wide; the status %4d fields likewise end after 32px. */
if (!/for\(const \[field,value\] of \[\["level",pet\.level\],\["hp",pet\.hp\],\["max-hp",pet\.maxHp\]\]\)/.test(renderPetsSource) ||
    !/#status-screen \.status-readout \.hp\{left:72px;top:137px;width:32px;text-align:right/.test(html) ||
    !/#status-screen \.status-readout \.max-hp\{left:122px;top:137px;width:32px;text-align:right/.test(html)) {
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
if (sendBattleTargetStart < 0 || sendBattleTargetEnd <= sendBattleTargetStart ||
    (sendBattleTargetSource.match(/lastPlayerActionKind/g) || []).length !== 1 ||
    !/if\(kind==="player"\)\{[\s\S]{0,320}lastPlayerActionKind[\s\S]{0,220}\}else\{[\s\S]{0,220}lastPetActionKind/.test(sendBattleTargetSource)) {
  throw new Error("pet actor-target W must not overwrite the player's remembered command");
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
   activated.  Selector, popup and pet-W stages must short-circuit stale
   player commandPending state instead of painting two buttons down. */
const pressedStart = script.indexOf("  function battleButtonPressed(command,state=app.battleState)");
const pressedEnd = script.indexOf("  function syncBattleButtonVisualStates", pressedStart);
const pressedSource = script.slice(pressedStart, pressedEnd);
if (pressedStart < 0 || pressedEnd <= pressedStart ||
    !/if\(pendingCommand\)return pendingCommand\.toUpperCase\(\)===wanted/.test(pressedSource) ||
    !/if\(popupCommand\)return popupCommand\.toUpperCase\(\)===wanted/.test(pressedSource) ||
    !/if\(pendingPet\.startsWith\("W\|"\)\)return wanted==="PET"/.test(pressedSource)) {
  throw new Error("battle pressed flags must have one native menu owner");
}
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
  /motion\.phase==="shown"[\s\S]{0,260}motion\.buttonX=BATTLE_MENU_ANCHOR_X/,
  /const watchdog=\(\)=>\{[\s\S]{0,520}stalled=[^;]+now-Number\(motion\.lastAt/,
  /startBattleMenuMotion[\s\S]{0,1200}battleAdvanceMenuMotion\(state\);[\s\S]{0,180}scheduleBattleMenuMotion\(state\)/,
  /battleMenuMotionFinished\(state,owner\)[\s\S]{0,260}owner==="player"&&state\.petMenuStagePending\)battleCommitPetMenuStage/,
  /queueBattlePetMenuStage\(state=app\.battleState,command=""\)[\s\S]{0,520}leaveBattleMenuMotion\(state,"player"\)/,
  /rememberedBattlePetAction[\s\S]{0,650}lastPetActionSlot/,
]) if(!expected.test(menuLifecycleSource))throw new Error(`native battle menu lifecycle regression: ${expected}`);
const closeBattlePopupSource=script.slice(script.indexOf("  function closeBattlePopup(options={})"),script.indexOf("  function openBattlePopup",script.indexOf("  function closeBattlePopup(options={})")));
if(/sendBattlePetDefault/.test(closeBattlePopupSource)||!/options\.clearChoiceTimer/.test(closeBattlePopupSource)){
  throw new Error("closing the native pet-skill window must keep the pet stage/countdown alive");
}
/* BattleButtonAttack/Jujutsu/Item/Pet all use bak + BattleButtonOff(): the
   second press is a local cancel, not another selector/window activation.
   Exercise the extracted production functions with an absolute countdown
   and a send spy so this cannot regress into a CSS-only pressed-state fix. */
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
  let sends=0,worldRenders=0,battleRenders=0,targetRenders=0,popupRenders=0,unavailable=0;
  function send(){sends++;return Promise.resolve();}
  function closeBattlePopup(){app.battlePopup=null;}
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
  ${script.slice(battleButtonCommandStart,battleButtonCommandEnd)}
  ${script.slice(battlePopupToggleStart,battlePopupToggleEnd)}
  ${script.slice(battleActionToggleStart,battleActionToggleEnd)}
  return {
    state,
    beginBattleAction,
    openBattlePopup,
    popup:()=>app.battlePopup,
    counts:()=>({sends,worldRenders,battleRenders,targetRenders,popupRenders,unavailable})
  };
`);
const toggleDeadline=9876543210;
const toggleHarness=makeBattleToggleHarness({pendingAction:{kind:"attack",defaulted:true},choiceDeadline:toggleDeadline});
if(toggleHarness.beginBattleAction({kind:"attack"})!==false||toggleHarness.state.pendingAction!==null){
  throw new Error("second/default Attack press must cancel its actor selector");
}
if(toggleHarness.counts().sends!==0||toggleHarness.state.choiceDeadline!==toggleDeadline){
  throw new Error("cancelling Attack must not send B or restart BattleCntDown");
}
if(toggleHarness.beginBattleAction({kind:"attack"})!==true||toggleHarness.state.pendingAction?.kind!=="attack"){
  throw new Error("Attack must re-arm after its pressed state was cancelled");
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
if(toggleHarness.openBattlePopup("magic")!==false||toggleHarness.state.pendingAction!==null){
  throw new Error("pressed Jujutsu must also cancel its actor-target phase");
}
if(toggleHarness.counts().sends!==0||toggleHarness.state.choiceDeadline!==toggleDeadline||toggleHarness.counts().unavailable!==0){
  throw new Error("battle command toggles changed packet count, countdown, or availability");
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
   !/<img id="battle-capture-cross" src="\/assets\/bitmaps\/bitmap_8701\.png"/.test(html)||
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
   legalSwitchHarness.state.choiceDeadline!==legalDeadline){
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
  /#battle-target-overlay \.battle-target-frame \{[^}]*border:1px solid #00ff00[^}]*background:transparent[^}]*animation:battle-target-box-color/,
  /@keyframes battle-target-box-color\{0%,74%\{border-color:#00ff00\}75%,83%\{border-color:#28e128\}84%,91%\{border-color:#008000\}/,
])if(!expected.test(html))throw new Error(`native green battle target rectangle regression: ${expected}`);
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
/* Escape must release the WN owner before hiding any advanced screen.  If
   show(worldScreen) runs first, the brown NPC dialog disappears visually but
   app.activeWindow keeps the old sequence/object and the next interaction is
   routed to a stale server window. */
const escapeHandler = script.indexOf('if(event.key==="Escape")');
if (escapeHandler < 0 || !/if\(app\.activeWindow\)\{event\.preventDefault\(\);closeServerWindow\(\);return;\}/.test(script.slice(escapeHandler, escapeHandler + 900))) {
  throw new Error("Escape must close the active WN window before other screens");
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
if (!/#battle-map-image\s*\{[^}]*width:640px; height:480px/.test(html) ||
    !/#battle-map-canvas\s*\{[^}]*width:640px; height:480px/.test(html) ||
    !/rawBattleId=Number\(app\.battleState\?\.fieldNo\?\?app\.battleState\?\.field\)/.test(battleWorldSource) ||
    !/battleId>=0&&battleId<220/.test(battleWorldSource) ||
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
  generatedBattleAssets.battleCount !== 220
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
const battleContactTiming = new Function("BATTLE_FLAG","BATTLE_PROC_TICK_MS",`${script.slice(contactTimingStart,contactTimingEnd)}; return {battleLegacyTravelDuration,battleLegacyHitProfile,battleContactHoldDuration,battleTimelineOffsetForFrame,battleAnimationElapsedWithHolds};`)(
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
const battleAttackSequenceSpec = new Function("battleSlotPoint","battleFacingDirection","battleActorAnimationTiming","battleSlotDirection","battleContactHoldDuration","battleTimelineOffsetForFrame","battleLegacyTravelDuration","BATTLE_PROC_TICK_MS",`${script.slice(attackSequenceStart,attackSequenceEnd)}; return battleAttackSequenceSpec;`)(
  id=>id===0?[0,0]:id===10?[320,160]:[360,200],
  ()=>6,
  ()=>({duration:120,contactOffset:30,nativeContact:true,contacts:[{offset:30},{offset:60}],soundEvents:[{sound:51,offset:0,kind:"sfx"}]}),
  ()=>3,
  battleContactTiming.battleContactHoldDuration,
  battleContactTiming.battleTimelineOffsetForFrame,
  battleContactTiming.battleLegacyTravelDuration,
  nativeProcTickMs,
);
const sameTargetSequence = battleAttackSequenceSpec(0,[10,10],0);
if (sameTargetSequence.steps.length !== 1 || sameTargetSequence.hits.length !== 2 || !nearlyEqual(sameTargetSequence.hits[1].contactOffset - sameTargetSequence.hits[0].contactOffset, 30 + normalContactHold) || sameTargetSequence.returnStartOffset <= sameTargetSequence.hits[1].contactOffset) {
  throw new Error(`BH combo sequence returned home between hits: ${JSON.stringify(sameTargetSequence)}`);
}
const zeroDamageSequence = battleAttackSequenceSpec(0,[10,10],0,[0,0],[0,0],[0,0]);
if (zeroDamageSequence.hits.length !== 2 || zeroDamageSequence.hits[1].contactOffset - zeroDamageSequence.hits[0].contactOffset !== 30 || zeroDamageSequence.duration >= sameTargetSequence.duration) {
  throw new Error(`zero-damage BH should not add HIT_STOP: ${JSON.stringify({zeroDamageSequence,sameTargetSequence})}`);
}
const battleMovieSource = script.slice(script.indexOf("  function battleMovieEffects"), script.indexOf("  const pendingBattlePacketTTL"));
if (/\.52/.test(battleMovieSource) || !/contactAt:start\+Math\.max\(0,Number\(motion\?\.contactOffset\)\|\|0\)/.test(battleMovieSource) || !/normalSequence=scheduleAttackSequence\(attacker,normalTargets,0,normalFlags,normalDamageValues,normalPetValues\)/.test(battleMovieSource)) {
  throw new Error("battle attacks still guess the impact frame instead of using SPR SoundNo");
}
if (!/amount>=hp&&amount>0\)value\|=BATTLE_FLAG\.death/.test(battleMovieSource) || !/projectedHp=new Map\(\)/.test(battleMovieSource)) {
  throw new Error("lethal BH damage is not threaded into the contact timeline");
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
if (!/const scheduleBDMotion=/.test(script.slice(script.indexOf("    const now=Date.now();"), script.indexOf("    const queueMotion="))) ||
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
   positional).  Keep the renderer's fallback wired to that native spelling;
   otherwise status ticks parse correctly but never paint an effect. */
const battleStatusMovieSource = script.slice(script.indexOf('      }else if(marker==="BM")'), script.indexOf('      }else if(marker==="BL")'));
if (!/battleSegmentNumber\(segment,"",0,-1\)/.test(battleStatusMovieSource) || !/battleSegmentNumber\(segment,"",1,0\)/.test(battleStatusMovieSource)) {
  throw new Error("BM positional status movie fallback is missing");
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
const projectileFns = new Function(`${script.slice(projectilePushStart, projectileEnd)}; return {battlePushProjectile,battleProjectileValue};`)();
const projectileState = {}, projectileNow = Date.now();
projectileFns.battlePushProjectile(projectileState, {kind:"arrow", from:[0, 0], to:[100, 0], startAt:projectileNow + 120, endAt:projectileNow + 420});
const arrow = projectileState.projectiles?.[0], arrowMid = projectileFns.battleProjectileValue(arrow, Number(arrow?.startedAt) + Number(arrow?.duration) / 2);
if (!arrow || Math.abs(arrowMid.x - 50) > 2 || Math.abs(arrowMid.y) > 2) throw new Error(`arrow projectile geometry failed: ${JSON.stringify(arrowMid)}`);
projectileFns.battlePushProjectile(projectileState, {kind:"boomerang", from:[0, 0], to:[100, 0], waypoints:[[0, 0], [100, 0], [0, 0]], waypointOffsets:[0, 120, 240], startAt:projectileNow + 120, endAt:projectileNow + 360});
const boomerang = projectileState.projectiles?.[1], boomerangAtTarget = projectileFns.battleProjectileValue(boomerang, Number(boomerang?.startedAt) + 120), boomerangAtReturn = projectileFns.battleProjectileValue(boomerang, Number(boomerang?.startedAt) + 240);
if (!boomerang || Math.abs(boomerangAtTarget.x - 100) > 2 || Math.abs(boomerangAtReturn.x) > 2) throw new Error(`boomerang waypoint geometry failed: ${JSON.stringify({boomerangAtTarget,boomerangAtReturn})}`);
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
if (!script.includes("if(cached&&(!sameFloor||!hasWindow))")) {
  throw new Error("same-floor MC checksum must keep the live map back-buffer");
}
if (!script.includes("function bindMapFloorTransitionTarget(floor)") ||
    !script.includes("if(!bindMapFloorTransitionTarget(floor)&&!sameFloor&&app.floor>=0&&app.map)startMapFloorTransition(floor)")) {
  throw new Error("same-floor EV warp must bind the destination before revealing the map");
}
if (!script.includes("function parseMapWindowHeader(value)") ||
    !script.includes("drawTimeAnime:true") ||
    !script.includes("app.mapDrawTimeAnime=header.drawTimeAnime!==false")) {
  throw new Error("M/MC palette must stay independent from the day/night field strip");
}
if (!script.includes("fetch(`/assets/pal/Palet_${id}.sap`") ||
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
console.log("web protocol vectors OK");
