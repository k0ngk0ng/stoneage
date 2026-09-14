"use strict";
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");
const html = fs.readFileSync(__dirname + "/index.html", "utf8");
const extension = JSON.parse(fs.readFileSync(__dirname + "/album-extensions.json", "utf8"))[0];
const source = html.slice(html.indexOf("  function albumStorageKey("), html.indexOf("  function attachMapImage("));
const store = new Map();
const base = Array.from({length: 224}, (_, index) => ({index, name: "", graphic: 0, valid: false}));
base[0] = {index: 0, name: "乌力", graphic: 28001, valid: true};
const manifest = {album: [...base, {...extension, index: 224, valid: true}]};
const context = vm.createContext({
  app: {account: "album-test", phase: "world", album: [], petSlots: [], pets: [], albumPage: 0},
  assetState: {manifest},
  localStorage: {getItem: key => store.get(key), setItem: (key, value) => store.set(key, value)},
});
vm.runInContext(source, context);
const {app} = context;
store.set(context.albumStorageKey(), JSON.stringify({0: {flag: 2, freeName: "旧宠物", level: 12}}));
context.hydrateAlbumCatalog();
assert.equal(app.album.length, 225);
assert.equal(app.album[0].freeName, "旧宠物", "extending the album must preserve existing saved slots");
assert.equal(app.album[224].flag, 0, "new species must remain undiscovered until received from the game");
assert.equal(context.markAlbumPet({graphic: 100873}), false, "another tiger must not unlock 佩露夏");
// K owned-pet packets carry the animation id, never the album portrait id.
app.petSlots = [{graphic: 100872, faceGraNo: 100872, level: 1, maxHp: 57, atk: 12, def: 8, quick: 9, water: 100}];
context.syncAlbumFromPets();
assert.equal(app.album[224].flag, 2);
assert.equal(app.album[224].name, "佩露夏");
assert.equal(app.album[224].faceGraNo, 28196, "SPR id must not overwrite the album portrait");
assert.equal(app.album[224].maxHp, 57);
assert.equal(app.album[224].str, 12, "K status attack (atk) must populate the album's str field");
// Later updates must persist new stats, not just the first discovery.
app.petSlots[0].level = 2; app.petSlots[0].maxHp = 65;
context.syncAlbumFromPets();
const saved = JSON.parse(store.get(context.albumStorageKey()));
assert.equal(saved[224].level, 2);
assert.equal(saved[224].maxHp, 65);
assert.equal(saved[224].faceGraNo, 28196);
app.album = []; app.petSlots = [];
context.hydrateAlbumCatalog();
assert.equal(app.album[224].flag, 2, "discovery survives a new session without the pet in inventory");
assert.equal(app.album[224].level, 2);
assert.equal(app.album[0].freeName, "旧宠物");
// Old or damaged saved portrait metadata cannot change the species mapping.
saved[224].faceGraNo = 100872;
saved[224].name = "错误的物种";saved[224].templateId = 619;saved[224].spriteGraphic = 100873;
store.set(context.albumStorageKey(), JSON.stringify(saved));
context.hydrateAlbumCatalog();
assert.equal(app.album[224].faceGraNo, 28196);
assert.equal(app.album[224].name, "佩露夏");
assert.equal(app.album[224].templateId, 777);
assert.equal(app.album[224].spriteGraphic, 100872);
assert.equal(context.markAlbumPet({graphic: 28196, level: 3}), true, "portrait-based discoveries remain compatible");
assert.equal(context.markAlbumPet({graphic: 28196, level: 3}), false, "unchanged updates must not cause repeated saves");
function element() {
  return {style: {}, children: [], listeners: {}, classList: {add() {}},
    append(...nodes) {this.children.push(...nodes);}, replaceChildren() {this.children = [];},
    setAttribute() {}, addEventListener(event, handler) {this.listeners[event] = handler;}};
}
const list = element(), status = element();
context.document = {createElement: element};
context.$ = id => id === "album-list" ? list : status;
context.imageButton = (label, bitmap, onClick) => {const node = element();node.label = label;node.listeners.click = onClick;return node;};
context.resolveBitmapInfo = id => {
  assert.equal(id, 28196, "rendering must resolve the logical portrait id");
  return {file: "bitmaps/bitmap_126325.png", width: 96, height: 104, xoffset: -48, yoffset: -52};
};
vm.runInContext(html.slice(html.indexOf("  function renderAlbumFixed("), html.indexOf("  function systemChoice(")), context);
app.selectedAlbum = 224;app.albumPage = 0;
context.renderAlbumFixed();
list.children.find(node => node.className === "album-prev").listeners.click();
assert.equal(app.albumPage, 28, "previous from the first page must reach the appended pet");
assert.equal(status.textContent, "29/29");
const row = list.children.find(node => node.className === "legacy-album-row");
assert.equal(row.disabled, false);
assert.equal(row.children[1].textContent, "佩露夏");
const face = list.children[0].children.find(node => node.className === "album-detail-face");
assert.equal(face.src, "/assets/bitmaps/bitmap_126325.png", "portrait must use a fixed asset URL");
assert.deepEqual(face.style, {left: "151px", top: "65px", width: "96px", height: "104px"});
const navCSS = html.match(/#album-screen \.album-prev,#album-screen \.album-next\{([^}]+)\}/)[1];
assert.match(navCSS, /pointer-events:auto/, "page buttons must override the non-interactive album layer");
app.phase = "login";app.petSlots = [{graphic: 100872, level: 2}];
app.account = "another-account";
context.saveAlbumState();
assert.equal(store.has(context.albumStorageKey()), false, "switching accounts must not save the old account's album under the new key");
context.hydrateAlbumCatalog();
assert.equal(app.album[224].flag, 0, "another account must not inherit discoveries from memory");
assert.equal(app.album[224].level, undefined);
app.account = "album-test";context.hydrateAlbumCatalog();
assert.equal(app.album[224].flag, 2, "the original account retains its own discoveries");
console.log("pet album extension, sprite/portrait mapping, existing saves and status persistence passed");
