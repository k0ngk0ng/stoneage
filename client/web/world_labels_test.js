"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");

const html = fs.readFileSync(__dirname + "/runtimeassets/index.html", "utf8");
const surfaceSource = html.slice(
  html.indexOf("  const worldLabelState="),
  html.indexOf("  function worldBackBuffer(")
);
const presentSource = html.slice(
  html.indexOf("  function presentWorldBackBuffer("),
  html.indexOf("  function getMap2DContext(")
);
const labelSource = html.slice(
  html.indexOf("  function drawActorLabels("),
  html.indexOf("  function drawActor(")
);

function harness(rect, dpr = 2) {
  const calls = [];
  const canvas = {
    width: 640,
    height: 480,
    getBoundingClientRect: () => rect,
  };
  const context = {
    setTransform: (...args) => calls.push(["transform", ...args]),
    clearRect: (...args) => calls.push(["clear", ...args]),
    save: () => calls.push(["save"]),
    restore: () => calls.push(["restore"]),
    strokeText: (...args) => calls.push(["stroke", ...args]),
    fillText: (...args) => calls.push(["fill", ...args]),
  };
  const runtime = {
    app: {mapBackBufferHold: false, phase: "world", battle: false, _lastWorldRender: 10},
    window: {
      devicePixelRatio: dpr,
      addEventListener() {},
      visualViewport: {addEventListener() {}},
    },
    $: id => id === "world-labels" ? canvas : {getBoundingClientRect: () => rect},
    getCanvas2DContext: () => context,
    actorHasCharacterLabels: actor => Boolean(actor && (actor.kind === "character" || actor.objectType === 1 || actor.charType !== undefined)),
    actorIsOwn: actor => Boolean(actor?.own),
    renderWorld() {},
  };
  vm.createContext(runtime);
  vm.runInContext(surfaceSource + labelSource, runtime);
  return {runtime, canvas, calls};
}

function presentationHarness() {
  const labelCanvas = {width: 640, height: 480};
  const visibleCanvas = {width: 640, height: 480};
  const buffer = {};
  const labelCalls = [];
  const visibleCalls = [];
  const labelContext = {
    globalCompositeOperation: "source-over",
    setTransform: (...args) => labelCalls.push(["transform", ...args]),
    clearRect: (...args) => labelCalls.push(["clear", ...args]),
    save: () => labelCalls.push(["save"]),
    restore: () => labelCalls.push(["restore"]),
    strokeText: (...args) => labelCalls.push(["stroke", ...args]),
    fillText: (...args) => labelCalls.push(["fill", ...args]),
  };
  const visibleContext = {
    globalCompositeOperation: "source-over",
    drawImage: (...args) => visibleCalls.push(["draw", ...args]),
  };
  const runtime = {
    app: {mapBackBufferHold: false, phase: "world", battle: false},
    window: {devicePixelRatio: 2},
    $: id => id === "world-labels" ? labelCanvas : id === "world" ? visibleCanvas : {getBoundingClientRect: () => ({width: 960, height: 720})},
    getCanvas2DContext: () => labelContext,
    getWorld2DContext: () => visibleContext,
    actorHasCharacterLabels: actor => Boolean(actor && actor.kind === "character"),
    actorIsOwn: actor => Boolean(actor?.own),
  };
  vm.createContext(runtime);
  vm.runInContext(surfaceSource + presentSource + labelSource, runtime);
  return {runtime, labelCalls, visibleCalls, buffer};
}

{
  const h = harness({width: 960, height: 720}, 2);
  h.runtime.beginWorldLabelFrame();
  h.runtime.drawActorLabels(null, {kind: "character", name: "阿明", fmName: "石器部落"}, [320, 240]);
  h.runtime.paintWorldLabels();
  assert.equal(h.canvas.width, 1920, "label canvas follows displayed scene width × DPR");
  assert.equal(h.canvas.height, 1440, "label canvas follows displayed scene height × DPR");
  assert.deepEqual(h.calls.find(call => call[0] === "transform"), ["transform", 3, 0, 0, 3, 0, 0]);
  assert.deepEqual(h.calls.filter(call => call[0] === "stroke"), [
    ["stroke", "石器部落", 320, 217],
    ["stroke", "阿明", 320, 230],
  ], "guild then character order remains native");
}

{
  const h = harness({width: 1280, height: 960}, 1.5);
  h.runtime.beginWorldLabelFrame();
  h.runtime.drawActorLabels(null, {kind: "character", name: "高清名字"}, [100, 100]);
  h.runtime.paintWorldLabels();
  assert.equal(h.canvas.width, 1920);
  assert.equal(h.canvas.height, 1440);
  assert.deepEqual(h.calls.find(call => call[0] === "transform"), ["transform", 3, 0, 0, 3, 0, 0]);
}

{
  const h = harness({width: 960, height: 720}, 2);
  h.runtime.beginWorldLabelFrame();
  h.runtime.drawActorLabels(null, {kind: "character", name: "旧文字"}, [10, 20]);
  h.runtime.paintWorldLabels();
  const before = h.calls.length;
  h.runtime.app.mapBackBufferHold = true;
  h.runtime.beginWorldLabelFrame();
  h.runtime.drawActorLabels(null, {kind: "character", name: "新文字"}, [10, 20]);
  assert.equal(h.runtime.paintWorldLabels(), false, "held map keeps the previous label frame");
  assert.equal(h.calls.length, before, "held map does not clear or repaint labels");
}

{
  const h = harness({width: 960, height: 720}, 2);
  h.runtime.beginWorldLabelFrame();
  h.runtime.drawActorLabels(null, {kind: "character", name: "自己", own: true}, [10, 20]);
  h.runtime.drawActorLabels(null, {kind: "item", name: "石头"}, [10, 20]);
  h.runtime.drawActorLabels(null, {charType: 3, name: "内部名", freeName: "宠物名"}, [20, 30]);
  h.runtime.paintWorldLabels();
  assert.deepEqual(h.calls.filter(call => call[0] === "stroke"), [["stroke", "宠物名", 20, 20]], "own/item filtering and pet display name remain unchanged");
}

/* Transition teardown presents the held world buffer directly, without
   passing through renderWorld(). The text layer must stay held with it and
   paint the newly queued actor labels only after the copy is released. */
{
  const h = presentationHarness();
  h.runtime.beginWorldLabelFrame();
  h.runtime.drawActorLabels(null, {kind: "character", name: "旧地图"}, [10, 20]);
  h.runtime.paintWorldLabels();
  h.runtime.app.mapBackBufferHold = true;
  h.runtime.beginWorldLabelFrame();
  h.runtime.drawActorLabels(null, {kind: "character", name: "新地图"}, [10, 20]);
  const heldLabels = h.labelCalls.length;
  h.runtime.presentWorldBackBuffer(h.buffer);
  assert.equal(h.visibleCalls.length, 0, "held back-buffer must not present pixels");
  assert.equal(h.labelCalls.length, heldLabels, "held back-buffer must not present labels");
  h.runtime.app.mapBackBufferHold = false;
  h.runtime.presentWorldBackBuffer(h.buffer);
  assert.equal(h.visibleCalls[0][0], "draw", "released back-buffer must present pixels");
  assert.deepEqual(h.labelCalls.filter(call => call[0] === "stroke").at(-1), ["stroke", "新地图", 10, 10], "released back-buffer must present the queued labels");
}

console.log("world labels: displayed-size DPR rasterisation, order, filtering and held-frame semantics passed");
