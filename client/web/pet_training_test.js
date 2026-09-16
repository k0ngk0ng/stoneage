"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");

const html = fs.readFileSync(__dirname + "/index.html", "utf8");
const start = html.indexOf("  function parsePetSkillShop(");
const end = html.indexOf("  function windowAssetButton", start);
assert(start >= 0 && end > start, "pet skill shop function boundary missing");

class FakeStyle {
  setProperty(name, value) {
    this[name] = String(value);
  }

  removeProperty(name) {
    delete this[name];
  }
}

class FakeClassList {
  constructor(owner) {
    this.owner = owner;
  }

  names() {
    return new Set(String(this.owner._className || "").split(/\s+/).filter(Boolean));
  }

  write(names) {
    this.owner._className = [...names].join(" ");
  }

  add(...names) {
    const current = this.names();
    names.forEach(name => current.add(name));
    this.write(current);
  }

  remove(...names) {
    const current = this.names();
    names.forEach(name => current.delete(name));
    this.write(current);
  }

  toggle(name, force) {
    const current = this.names();
    const enabled = force === undefined ? !current.has(name) : Boolean(force);
    if (enabled) current.add(name); else current.delete(name);
    this.write(current);
    return enabled;
  }

  contains(name) {
    return this.names().has(name);
  }
}

class FakeText {
  constructor(value) {
    this.textContent = String(value);
    this.children = [];
  }
}

class FakeNode {
  constructor(tagName = "div") {
    this.tagName = String(tagName).toUpperCase();
    this.children = [];
    this.listeners = Object.create(null);
    this.attributes = Object.create(null);
    this.dataset = Object.create(null);
    this.style = new FakeStyle();
    this._className = "";
    this.classList = new FakeClassList(this);
    this._textContent = "";
    this.disabled = false;
  }

  get className() {
    return this._className;
  }

  set className(value) {
    this._className = String(value || "");
  }

  get textContent() {
    if (this.children.length) return this.children.map(child => child?.textContent || "").join("");
    return this._textContent;
  }

  set textContent(value) {
    this._textContent = String(value ?? "");
    this.children = [];
  }

  append(...items) {
    this._textContent = "";
    for (const item of items) {
      const child = typeof item === "string" ? new FakeText(item) : item;
      if (child === null || child === undefined) continue;
      child.parentNode = this;
      this.children.push(child);
    }
  }

  replaceChildren(...items) {
    this.children = [];
    this._textContent = "";
    if (items.length) this.append(...items);
  }

  setAttribute(name, value) {
    this.attributes[name] = String(value);
  }

  getAttribute(name) {
    return this.attributes[name] ?? null;
  }

  addEventListener(name, handler) {
    this.listeners[name] = handler;
  }

  click() {
    if (this.disabled) return;
    this.listeners.click?.({target: this, currentTarget: this});
  }
}

function descendants(root) {
  const result = [];
  const visit = node => {
    if (!(node instanceof FakeNode)) return;
    result.push(node);
    node.children.forEach(visit);
  };
  visit(root);
  return result;
}

function byLabel(root, label) {
  return descendants(root).find(node => node.getAttribute("aria-label") === label);
}

function byClass(root, className) {
  return descendants(root).filter(node => node.classList.contains(className));
}

const nodes = new Map([
  ["server-window-screen", new FakeNode("section")],
  ["server-window-body", new FakeNode("div")],
  ["server-window-options", new FakeNode("div")],
]);
const packets = [];
const app = {
  activeWindow: null,
  petSlots: [],
  petSkills: Array.from({length: 5}, () => []),
  pc: {gold: 10000},
};

function button(label, handler, kind = "") {
  const item = new FakeNode("button");
  item.type = "button";
  item.textContent = label;
  if (kind) item.className = kind;
  item.addEventListener("click", handler);
  return item;
}

function windowAssetButton(label, bitmap, handler, width = 80, height = 16) {
  const item = button(label, handler);
  item.bitmap = bitmap;
  item.style.width = `${width}px`;
  item.style.height = `${height}px`;
  item.setAttribute("aria-label", label);
  return item;
}

function shopAppendText(parent, className, text) {
  const node = new FakeNode("span");
  node.className = className;
  node.textContent = String(text ?? "");
  parent.append(node);
  return node;
}

const context = vm.createContext({
  app,
  document: {createElement: tagName => new FakeNode(tagName)},
  $: id => nodes.get(id) || (() => {
    const node = new FakeNode("div");
    nodes.set(id, node);
    return node;
  })(),
  button,
  windowAssetButton,
  shopAppendText,
  clearItemShopFrame() {},
  shopLegacyLines: value => String(value || "").split("\n"),
  shopDisplayGold: () => Number(app.pc.gold),
  windowResponse: (select, data = "") => {
    packets.push({select, data});
    return Promise.resolve(true);
  },
  reportError: error => { throw error; },
  closeServerWindow: () => { app.activeWindow = null; },
  decimal: (value, fallback = 0) => {
    const number = Number.parseInt(String(value), 10);
    return Number.isFinite(number) ? number : fallback;
  },
  stateNumber: (value, fallback = 0) => {
    const number = Number(value);
    return Number.isFinite(number) ? number : fallback;
  },
  splitWindowTokens: data => String(data || "").split("|").map(value => String(value).replace(/\\y/g, "\\").replace(/\\z/g, "|").replace(/\\n/g, "\n").replace(/\\c/g, ",")),
});
vm.runInContext(html.slice(start, end), context);

const skillFields = [
  ["技能1", 100, "第一项说明", 0],
  ["技能2", 200, "封印技能说明", 1],
  ["技能3", 300, "第三项说明", 0],
  ["技能4", 400, "第四项说明", 0],
  ["技能5", 500, "第五项说明", 0],
  ["技能6", 600, "第六项说明", 0],
  ["技能7", 700, "第七项说明", 0],
  ["技能8", 800, "第八项说明", 0],
  ["技能9", 900, "第九项\\z特殊说明", 0],
];
const shopData = ["1", "宠物技能", "请选择技能", ...skillFields.flat()].join("|");
const shop = context.parsePetSkillShop(shopData);
assert.equal(shop.skills.length, 9, "four-field records must parse across both pages");
assert.equal(shop.skills[0].index, 0);
assert.equal(shop.skills[0].name, "技能1");
assert.equal(shop.skills[0].price, 100);
assert.equal(shop.skills[0].info, "第一项说明");
assert.equal(shop.skills[0].sealFlag, 0);
assert.equal(shop.skills[1].sealFlag, 1, "sealFlag must be retained as a numeric field");
assert.equal(shop.skills[8].info, "第九项|特殊说明", "escaped comment delimiters must be decoded");

app.petSlots = [null, {name: "无槽宠物", maxSkill: 0}, null, {name: "第四只", slot: 3, level: 42, maxHp: 777}];
assert.equal(context.petTrainingSlots(app.petSlots[1]), 0, "maxSkill=0 must expose no slots");
assert.equal(context.petTrainingSlots(app.petSlots[3]), 7, "2.5 storage position must not determine skill capacity");
assert.equal(context.petTrainingSlots({slot: 0}), 7, "first pet must have seven skill slots without maxSkill");
assert.equal(context.petTrainingSlots({maxSkill: 9}), 7, "explicit capacity must be capped at seven");
assert.equal(context.petTrainingSlots(null), 0, "missing pets have no slots");
app.petSkills[3] = [{index: 6, name: "防御"}];
assert.equal(context.petTrainingSlots({maxSkill: -2}), 0, "negative slot counts must be safe");

const wnd = {windowType: 9};
app.activeWindow = wnd;
context.renderPetSkillTraining(wnd, shop);
const options = nodes.get("server-window-options");
const initialPackets = packets.length;
assert.equal(byLabel(options, "技能2，200 石币").disabled, true, "sealed skills must be unavailable");
byLabel(options, "下一页").click();
assert.equal(packets.length, initialPackets, "local catalog pagination must not send WN");
assert.equal(byLabel(options, "技能9，900 石币").disabled, false);
byLabel(options, "上一页").click();
assert.equal(packets.length, initialPackets, "returning to the previous skill page must not send WN");
byLabel(options, "下一页").click();
byLabel(options, "技能9，900 石币").click();
assert.equal(packets.length, initialPackets, "choosing a skill must not send WN");

const petRows = byClass(options, "pet-training-pet-row");
assert.equal(petRows.length, 2, "sparse pet slots must render only present pets");
assert.equal(byLabel(options, "无槽宠物，Lv.-，HP -").disabled, true, "maxSkill=0 pets must be disabled");
const petReturn = byLabel(options, "返回");
petReturn.click();
assert.equal(packets.length, initialPackets, "returning from the pet list must not send WN");
byLabel(options, "技能9，900 石币").click();
byLabel(options, "第四只，Lv.42，HP 777").click();
assert.equal(packets.length, initialPackets, "choosing a sparse pet must not send WN");

let slotRows = byClass(options, "pet-training-row");
assert.equal(slotRows.length, 7, "maxSkill above seven must render exactly seven slots");
assert.equal(slotRows[6].textContent, "技 7：防御", "sparse existing skill must appear in its actual slot");
byLabel(options, "返回").click();
assert.equal(packets.length, initialPackets, "returning from the slot list must not send WN");
byLabel(options, "第四只，Lv.42，HP 777").click();
slotRows = byClass(options, "pet-training-row");
slotRows[6].click();
assert.equal(packets.length, initialPackets, "choosing a slot must not send WN");
assert(nodes.get("server-window-body").textContent.includes("替换技 7「防御」"), "confirmation must name the replaced skill");
byLabel(options, "否").click();
assert.equal(packets.length, initialPackets, "confirmation 'no' must not send WN");
slotRows = byClass(options, "pet-training-row");
assert.equal(slotRows.length, 7, "confirmation 'no' must return to the slot list");
slotRows[6].click();
byLabel(options, "是").click();
assert.equal(packets.length, initialPackets + 1, "only confirmation 'yes' may send one WN");
assert.equal(packets.at(-1).select, 0, "WN select must be zero");
assert.equal(packets.at(-1).data, "9|4|7|900", "WN must use one-based skill, pet and slot numbers");

app.petSlots = [];
context.renderPetSkillTraining(wnd, shop, {page: 0, step: "pet", skill: shop.skills[0]});
assert.equal(byClass(options, "pet-training-pet-row").length, 0, "empty pet lists must render safely");
byLabel(options, "返回").click();
assert.equal(packets.length, initialPackets + 1, "returning from an empty pet list must not send WN");

byLabel(options, "离开").click();
assert.equal(app.activeWindow, null, "leaving must close the catalog locally");
assert.equal(packets.length, initialPackets + 1, "leaving must not send WN");

console.log("pet skill shop parsing, four-step navigation, sparse indices and WN payload tests passed");
