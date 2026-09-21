"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");
const baselineRef = process.argv[process.argv.indexOf("--baseline") + 1] || "HEAD";
const html = process.argv.includes("--baseline")
  ? require("node:child_process").execFileSync("git", ["show", `${baselineRef}:client/web/runtimeassets/index.html`], {cwd: __dirname, encoding: "utf8", maxBuffer: 8 * 1024 * 1024})
  : fs.readFileSync(__dirname + "/runtimeassets/index.html", "utf8");
function section(start, end) {
  const a = html.indexOf(start), b = html.indexOf(end, a);
  assert(a >= 0 && b > a, `missing source boundary: ${start}`);
  return html.slice(a, b);
}
const source = [
  section("  function walkProgress(", "  function drawBitmapAt("),
  section("  function renderSceneActorsAndParts(", "  /* A large M window"),
  section("  function drawWorldGroundToBuffer(", "  /* A sliding M window"),
  section("  function drawMapStar(", "  function mapEffectAnimationActive("),
  section("  function renderWorld(force=false)", "  function scheduleWorldAnimation("),
].join("\n");
function samePoint(actual, expected, message) {
  assert(actual.length === expected.length && expected.every((value, i) => Math.abs(actual[i] - value) < 1e-8), `${message}: ${actual} != ${expected}`);
}

function harness(delta, following = false) {
  let clock = 0;
  const canvas = {width: 640, height: 480}, groundImage = {}, calls = [];
  const project = (x, y) => [1000 + (x + y) * 32, 2000 + (y - x) * 24];
  const animation = delta ? {from: [10, 10], to: [10 + delta[0], 10 + delta[1]], startedAt: 0, duration: 1000, direction: 5} : null;
  const ctx = {canvas, fillRect: (...args) => calls.push({kind: "pixel", args}), save() {}, restore() {}, drawImage: (image, x, y) => calls.push({kind: "ground", image, point: [x, y]})};
  const app = {
    _worldPaintTick: true, phase: "world", battle: false, position: [10, 10], character: "self", playerActorId: 1,
    walkAnimation: following ? null : animation, partyFollowAnimation: following ? animation : null,
    map: {tiles: [100]}, actors: new Map([[1, {id: 1, name: "self", kind: "character", x: 10, y: 10}]]),
    mapEffectParticles: [{kind: "star", gx: 11, gy: 10, mode: 5}, {kind: "rain", x: 40, y: 50}],
  };
  const part = {x: 11, y: 10, image: {complete: true, naturalWidth: 80, naturalHeight: 160}, info: {}, anchor: project(11, 10)};
  const cache = {canvas: groundImage, minX: project(11, 10)[0], minY: project(11, 10)[1], parts: [part]};
  const context = {
    app, performance: {now() {clock += 5; return clock;}},
    $: id => id === "world" ? canvas : {style: {}},
    worldBackBuffer: () => canvas, getWorld2DContext: () => ctx,
    currentWorldWalkAnimation: () => app.walkAnimation || app.partyFollowAnimation,
    mapPixel: project, updateMapEffects() {},
    // Simulate expensive asset/cache work within one synchronous paint.
    ensureMapLayerCache() {clock += 130; return cache;}, mapLayerCacheUsable: value => Boolean(value),
    mapTransitionState: {active: false}, hideWorldGround() {}, maybeFinishMapLoading() {},
    renderWorldOverlay() {}, scheduleWorldAnimation() {}, presentWorldBackBuffer() {},
    beginWorldLabelFrame() {}, presentWorldLabels() {},
    actorFrame() {clock += 75; return null;}, fieldActorFrameVisualKey: () => "",
    mapPartDepth: p => p.anchor[1], mapPartBeforeActor: () => true,
    drawActor: (_, actor, point) => {calls.push({kind: "actor", actor, point}); clock += 35;},
    drawBitmapAt: (_, image, info, point) => {calls.push({kind: "part", point}); clock += 35;},
    drawActorLabels: (_, actor, point) => calls.push({kind: "label", point}),
    drawActorSpeech: (_, actor, point) => calls.push({kind: "speech", point}),
    MAP_EFFECT_STAR_WHITE: "white", MAP_EFFECT_STAR_YELLOW: "yellow", MAP_EFFECT_STAR_SOFT: "soft",
    MAP_EFFECT_STAR_GOLD: "gold", MAP_EFFECT_STAR_PALE: "pale", MAP_EFFECT_RAIN_COLOR: "rain",
  };
  vm.createContext(context); vm.runInContext(source, context);
  return {context, app, calls, project, part, advance: ms => {clock += ms;}, animation};
}

for (const delta of [[1,0], [1,1], [0,1], [-1,1], [-1,0], [-1,-1], [0,-1], [1,-1], null]) {
  for (const following of [false, true]) {
    const h = harness(delta, following);
    h.context.renderWorld(true);
    const actor = h.calls.find(c => c.kind === "actor"), part = h.calls.find(c => c.kind === "part");
    samePoint(actor.point, [320, 240], "local walker must stay at frame camera center despite paint cost");
    const view = delta ? [10 + delta[0] * .005, 10 + delta[1] * .005] : [10, 10];
    const center = h.project(...view), anchor = h.project(11, 10);
    const expected = [320 + anchor[0] - center[0], 240 + anchor[1] - center[1]];
    assert.deepEqual(Array.from(part.point), expected, "tree clip/paint must use ground frame's view");
    for (const kind of ["label", "speech"]) samePoint(h.calls.find(c => c.kind === kind).point, [320, 240], `${kind} must follow the same actor frame`);
    assert.deepEqual(h.calls.find(c => c.kind === "ground").point, expected.map(Math.round), "ground may round pixels but must not resample movement");
    assert.deepEqual(Array.from(h.calls.find(c => c.kind === "pixel" && c.args.length === 4 && c.args[2] === 1).args), [...expected.map(Math.round), 1, 1], "map star must share tree projection");
    assert(h.calls.some(c => c.kind === "pixel" && c.args.join() === "40,49,1,1"), "screen-space rain must not follow map projection");
    assert.equal(h.app.position.join(), "10,10", "rendering must not mutate authoritative position");
    if (delta) assert.equal(actor.actor.action, 4);
    h.context.renderWorld(true);
    if (delta) assert.notDeepEqual(Array.from(h.calls.filter(c => c.kind === "part").at(-1).point), expected, "next frame must continue walking");
  }
}

// The same camera must own culling too: this tree is just inside the right
// edge at frame start, but would be culled by a later walking sample.
{
  const h = harness([-1, 0]);
  h.part.x = 19.96; h.part.anchor = h.project(h.part.x, h.part.y);
  h.context.renderWorld(true);
  const part = h.calls.find(c => c.kind === "part");
  assert(part && part.point[0] < 640 && part.point[0] > 638, "edge tree must not disappear between frame camera capture and culling");
}

// Explicit render view is stable; pointer callers without it remain live.
{
  const h = harness([1, 0]), frame = h.context.worldRenderFrame();
  const fixed = Array.from(h.context.tilePoint(11, 10, frame.view));
  const live = Array.from(h.context.tilePoint(11, 10));
  h.advance(200);
  assert.deepEqual(Array.from(h.context.tilePoint(11, 10, frame.view)), fixed);
  assert.notDeepEqual(Array.from(h.context.tilePoint(11, 10)), live);
  const idle = harness(null), idleFrame = idle.context.worldRenderFrame();
  idle.app.position[0] = 99;
  assert.equal(idleFrame.view[0], 10, "idle snapshots must not alias app.position");
}
console.log("world frame coordinate vectors OK");
