"use strict";

const assert = require("node:assert/strict");
const players = require("./static/players.js");

class ClassList {
  constructor(element) { this.element = element; this.values = new Set(); }
  add(...names) { names.forEach(name => this.values.add(name)); }
  remove(...names) { names.forEach(name => this.values.delete(name)); }
  contains(name) { return this.values.has(name); }
  toggle(name, force) {
    const next = force === undefined ? !this.values.has(name) : Boolean(force);
    if (next) this.values.add(name); else this.values.delete(name);
    return next;
  }
}

function kebabToDataset(name) {
  return name.replace(/^data-/, "").replace(/-([a-z])/g, (_, letter) => letter.toUpperCase());
}

class Element {
  constructor(document, tagName) {
    this.ownerDocument = document;
    this.tagName = tagName.toUpperCase();
    this.children = [];
    this.parentNode = null;
    this.attributes = new Map();
    this.dataset = {};
    this.classList = new ClassList(this);
    this._className = "";
    this.listeners = new Map();
    this.style = {};
    this.hidden = false;
    this.disabled = false;
    this.value = "";
    this.textContent = "";
    this.type = "";
  }
  get className() { return this._className; }
  set className(value) {
    this._className = String(value || "");
    this.classList.values = new Set(this._className.split(/\s+/).filter(Boolean));
  }
  get firstChild() { return this.children[0] || null; }
  get options() { return this.children.filter(child => child.tagName === "OPTION"); }
  appendChild(child) {
    if (child.parentNode) child.parentNode.removeChild(child);
    child.parentNode = this;
    this.children.push(child);
    if (this.tagName === "SELECT" && child.tagName === "OPTION" && this.value === "") this.value = child.value;
    return child;
  }
  insertBefore(child, before) {
    if (child.parentNode) child.parentNode.removeChild(child);
    const index = before ? this.children.indexOf(before) : -1;
    child.parentNode = this;
    if (index < 0) this.children.push(child); else this.children.splice(index, 0, child);
    return child;
  }
  removeChild(child) {
    const index = this.children.indexOf(child);
    if (index >= 0) {
      this.children.splice(index, 1);
      child.parentNode = null;
    }
    return child;
  }
  setAttribute(name, value) {
    this.attributes.set(name, String(value));
    if (name.startsWith("data-")) this.dataset[kebabToDataset(name)] = String(value);
  }
  addEventListener(type, listener) {
    if (!this.listeners.has(type)) this.listeners.set(type, []);
    this.listeners.get(type).push(listener);
  }
  dispatchEvent(event) {
    const next = event || {};
    next.type = next.type || "event";
    next.target = next.target || this;
    next.currentTarget = this;
    next.defaultPrevented = Boolean(next.defaultPrevented);
    next.preventDefault = next.preventDefault || function () { next.defaultPrevented = true; };
    for (const listener of this.listeners.get(next.type) || []) listener(next);
    return !next.defaultPrevented;
  }
  focus() { this.ownerDocument.activeElement = this; }
  matches(selector) { return matchSelector(this, selector); }
  querySelector(selector) { return this.querySelectorAll(selector)[0] || null; }
  querySelectorAll(selector) {
    const parts = selector.trim().split(/\s+/);
    let current = [this];
    for (const part of parts) {
      const next = [];
      for (const parent of current) {
        walk(parent, child => { if (matchSelector(child, part)) next.push(child); });
      }
      current = next;
    }
    return current;
  }
}

function walk(element, visit) {
  for (const child of element.children) {
    visit(child);
    walk(child, visit);
  }
}

function matchSelector(element, selector) {
  if (selector.startsWith("#")) return element.attributes.get("id") === selector.slice(1);
  if (selector.startsWith(".")) return element.classList.contains(selector.slice(1));
  const dataAttribute = selector.match(/^\[([^=\]]+)\]$/);
  if (dataAttribute) return element.attributes.has(dataAttribute[1]) || element.dataset[kebabToDataset(dataAttribute[1])] !== undefined;
  return element.tagName.toLowerCase() === selector.toLowerCase();
}

class Document {
  constructor() {
    this.activeElement = null;
    this.listeners = new Map();
    this.byID = new Map();
    this.body = new Element(this, "body");
    this.documentElement = new Element(this, "html");
    this.readyState = "complete";
  }
  createElement(tagName) { return new Element(this, tagName); }
  register(id, element) { element.setAttribute("id", id); this.byID.set(id, element); return element; }
  getElementById(id) { return this.byID.get(id) || null; }
  addEventListener(type, listener) {
    if (!this.listeners.has(type)) this.listeners.set(type, []);
    this.listeners.get(type).push(listener);
  }
  dispatchEvent(event) { for (const listener of this.listeners.get(event.type) || []) listener(event); }
}

function makeHarness(fetcher) {
  const document = new Document();
  const root = document.register("player-admin", document.createElement("main"));
  root.dataset.accountId = "42";
  root.dataset.csrf = "csrf-token";
  const characterSelect = document.register("player-character", document.createElement("select"));
  const refresh = document.register("players-refresh", document.createElement("button"));
  const loadState = document.register("player-load-state", document.createElement("div"));
  const online = document.register("player-online-status", document.createElement("span"));
  const snapshot = document.register("player-snapshot", document.createElement("div"));
  const catalogForm = document.register("player-catalog-search", document.createElement("form"));
  const catalogKind = document.register("catalog-kind", document.createElement("select"));
  catalogKind.value = "item";
  const catalogQuery = document.register("catalog-query", document.createElement("input"));
  const catalogHint = document.register("catalog-search-hint", document.createElement("p"));
  const catalogResults = document.register("catalog-results", document.createElement("div"));
  const catalogSelection = document.register("catalog-selection", document.createElement("div"));
  const dialog = document.register("player-dialog", document.createElement("div"));
  dialog.hidden = true;
  const backdrop = document.createElement("div");
  backdrop.setAttribute("data-player-dialog-cancel", "");
  const panel = document.createElement("section");
  panel.className = "player-dialog-panel";
  const title = document.register("player-dialog-title", document.createElement("h2"));
  const message = document.register("player-dialog-message", document.createElement("p"));
  const cancel = document.createElement("button");
  cancel.setAttribute("data-player-dialog-cancel", "");
  const accept = document.createElement("button");
  accept.setAttribute("data-player-dialog-accept", "");
  panel.appendChild(title);
  panel.appendChild(message);
  panel.appendChild(cancel);
  panel.appendChild(accept);
  dialog.appendChild(backdrop);
  dialog.appendChild(panel);

  const editorDialog = document.register("player-editor-dialog", document.createElement("div"));
  editorDialog.hidden = true;
  const editorBackdrop = document.createElement("div");
  editorBackdrop.setAttribute("data-player-editor-cancel", "");
  const editorPanel = document.createElement("section");
  editorPanel.className = "player-dialog-panel";
  const editorTitle = document.register("player-editor-dialog-title", document.createElement("h2"));
  const editorClose = document.createElement("button");
  editorClose.setAttribute("data-player-editor-cancel", "");
  const editorContent = document.register("player-editor-dialog-content", document.createElement("div"));
  editorPanel.appendChild(editorTitle);
  editorPanel.appendChild(editorClose);
  editorPanel.appendChild(editorContent);
  editorDialog.appendChild(editorBackdrop);
  editorDialog.appendChild(editorPanel);

  [characterSelect, refresh, loadState, online, snapshot, catalogForm, catalogKind,
    catalogQuery, catalogHint, catalogResults, catalogSelection].forEach(element => root.appendChild(element));
  document.body.appendChild(root);
  document.body.appendChild(dialog);
  document.body.appendChild(editorDialog);
  const state = players.init(document, {fetch: fetcher}, fetcher);
  return {document, root, characterSelect, catalogForm, catalogKind, catalogQuery, catalogResults, catalogSelection, dialog, accept, editorDialog, editorContent, state};
}

function jsonResponse(payload, status = 200) {
  return {ok: status >= 200 && status < 300, status, json: async () => payload};
}

function snapshot(slot, revision = "rev-" + slot) {
  return {
    online: slot === 0,
    graphic_id: 100020,
    name: slot === 0 ? "阿石" : "阿木",
    revision,
    capacities: {item_inventory: 15, item_warehouse: 30, pet_inventory: 5, pet_warehouse: 5},
    attributes: [
      {key: "gld", label: "随身石币", value: 100, min: 0, max: 10000000},
      {key: "vi", label: "体力", value: 12345, min: 0, max: 2147483647},
      {key: "str", label: "腕力", value: 1800, min: 0, max: 2147483647},
      {key: "tou", label: "耐力", value: 2001, min: 0, max: 2147483647},
      {key: "dx", label: "速度", value: 999, min: 0, max: 2147483647},
      {key: "lv", label: "等级", value: 25, min: 1, max: 140}
    ],
    possessions: [
      {kind: "item", location: "inventory", slot: 5, name: "石斧", id: 100, graphic_id: 200, attributes: [
        {key: "str", label: "物品数值", value: 18, min: -100000000, max: 100000000}
      ]},
      {kind: "pet", location: "inventory", slot: 0, name: "小石", id: 300, graphic_id: 400, attributes: [
        {key: "vi", label: "体力", value: 12345, min: 0, max: 2147483647},
        {key: "str", label: "腕力", value: 1800, min: 0, max: 2147483647},
        {key: "tou", label: "耐力", value: 2001, min: 0, max: 2147483647},
        {key: "dx", label: "速度", value: 999, min: 0, max: 2147483647},
        {key: "growth_vi", label: "体力成长基础", value: 123, min: 0, max: 255},
        {key: "lv", label: "等级", value: 7, min: 1, max: 140}
      ], skills: [
        {slot: 0, id: -1}, {slot: 1, id: 72, name: "火焰"}
      ]},
      {kind: "pet", location: "warehouse", slot: 0, name: "仓库小石", id: 301, graphic_id: 401, attributes: [], skills: []},
      {kind: "pet", location: "warehouse", slot: 5, name: "超出容量的小石", id: 302, graphic_id: 402, attributes: [], skills: []}
    ]
  };
}

function makeFetcher() {
  const calls = [];
  let mutationStatus = 200;
  let postPayload = null;
  const fetcher = async (url, options = {}) => {
    calls.push({url, options});
    if (options.method === "POST") {
      postPayload = JSON.parse(options.body);
      if (mutationStatus !== 200) return jsonResponse({error: "版本冲突"}, mutationStatus);
      return jsonResponse(snapshot(Number(url.split("/").pop())));
    }
    if (url.includes("/players") && !url.includes("/players/")) {
      return jsonResponse({characters: [{slot: 0, name: "阿石", online: true}, {slot: 1, name: "阿木", online: false}]});
    }
    if (url.includes("/players/1")) return jsonResponse(snapshot(1));
    if (url.includes("/players/0")) return jsonResponse(snapshot(0));
    if (url.startsWith("/api/player-catalog")) {
      const kind = new URL("http://stoneage.test" + url).searchParams.get("kind");
      const entry = kind === "item"
        ? {kind: "item", id: 11, template_id: 11, name: "铁斧", description: "斧头", graphic_id: 12}
        : kind === "pet"
          ? {kind: "pet", id: 22, template_id: 22, name: "小龟", description: "宠物", graphic_id: 23}
          : {kind: "pet_skill", id: 88, name: "冰箭", description: "冰属性技能", graphic_id: 0};
      return jsonResponse({entries: [entry], total: 1});
    }
    throw new Error("unexpected GET " + url);
  };
  return {fetcher, calls, get postPayload() { return postPayload; }, set mutationStatus(value) { mutationStatus = value; }};
}

async function settle() {
  for (let index = 0; index < 20; index++) await Promise.resolve();
}

function submit(form) { form.dispatchEvent({type: "submit"}); }
function click(element) { element.dispatchEvent({type: "click"}); }

async function main() {
  const query = new URL("http://stoneage.test" + players.buildCatalogURL("pet_skill", "火 12/3", 40, 10)).searchParams;
  assert.equal(query.get("kind"), "pet_skill");
  assert.equal(query.get("q"), "火 12/3");
  assert.equal(query.get("offset"), "40");
  assert.equal(query.get("limit"), "10");
  assert.equal(players.graphicURL("a/b", "item"), "/api/player-assets/graphic/a%2Fb?kind=item");
  assert.equal(players.graphicURL(null, "pet"), "");
  assert.equal(players.formatFixedPoint(12345), "123.45");
  assert.equal(players.formatFixedPoint(-1200), "-12");
  assert.deepEqual(players.parseFixedPoint("12.34"), {value: 1234});
  assert.deepEqual(players.parseFixedPoint("-0.5"), {value: -50});
  assert(players.parseFixedPoint("12.345").error, "more than two decimal places are rejected");
  assert.equal(players.formatPetCharm(300), "30.0");
  assert.equal(players.formatPetLuck(10000), "100.00");
  assert.deepEqual(players.parsePetCharm("31.5"), {value: 315});
  assert.deepEqual(players.parsePetLuck("-100.00"), {value: -10000});
  assert.deepEqual(players.parsePetLuck("1e2"), {value: 10000});
  assert(players.numberOrString({value: "1.5", dataset: {numeric: "true"}}).error,
    "ordinary integer fields reject fractional values");
  assert.deepEqual(players.numberOrString({value: "1e1", dataset: {numeric: "true"}}), {value: 10});

  const fixture = makeFetcher();
  const page = makeHarness(fixture.fetcher);
  await settle();
  assert.equal(page.state.characterSlot, 0, "initial character is selected");
  assert.equal(page.state.snapshot.name, "阿石", "initial snapshot is loaded");
  assert.equal(page.root.querySelectorAll(".possession-card").length, 4, "existing possessions beyond the current capacity remain visible");

  const initialCard = page.root.querySelector(".possession-card");
  assert(initialCard, "existing possession has a summary card");
  assert.equal(initialCard.querySelector(".possession-name-form"), null, "list cards do not contain edit forms");
  assert.equal(initialCard.querySelector(".attribute-editor"), null, "list cards do not contain attribute editors");
  click(initialCard);
  assert.equal(page.editorDialog.hidden, false, "clicking a possession opens its editor");
  assert.equal(page.editorContent.querySelector(".possession-name-form") !== null, true, "editor contains a name form");
  assert.equal(page.editorContent.querySelectorAll(".skill-row").length, 0, "item editor has no pet skill rows");
  click(page.editorDialog.querySelectorAll("[data-player-editor-cancel]")[1]);
  assert.equal(page.editorDialog.hidden, true, "editor close button closes the editor");

  const characterCard = page.root.querySelector(".character-card");
  assert(characterCard, "character is a clickable summary");
  assert.equal(characterCard.querySelector("img").src, "/api/player-assets/graphic/100020?kind=character");
  assert.equal(characterCard.querySelector(".asset-level").textContent, "等级 25");
  assert.equal(page.root.querySelector(".attribute-row"), null, "character attributes are not on the list");
  assert.equal(characterCard.querySelector("input"), null, "level on the card is read-only");
  assert.equal(page.root.querySelectorAll(".possession-card")[1].querySelector(".asset-level").textContent, "等级 7");
  characterCard.focus();
  characterCard.dispatchEvent({type: "keydown", key: "Enter"});
  assert.equal(page.editorDialog.hidden, false, "keyboard opens character editor");
  const characterRows = page.editorContent.querySelectorAll(".attribute-row");
  const vitalInput = characterRows[1].querySelector(".attribute-editor input");
  assert.equal(vitalInput.value, "123.45", "character vital is shown in game units");
  vitalInput.value = "12.34";
  submit(characterRows[1].querySelector(".attribute-editor"));
  click(page.accept);
  await settle();
  assert.equal(fixture.postPayload.action, "set_character");
  assert.equal(fixture.postPayload.value, 1234, "fixed point input is sent as the exact raw integer");

  let itemCard = page.root.querySelectorAll(".possession-card")[0];
  click(itemCard);
  let itemInputs = page.editorContent.querySelectorAll(".attribute-editor input");
  assert.equal(itemInputs[0].value, "18", "item attributes keep their native units");
  itemInputs[0].value = "19";
  submit(page.editorContent.querySelector(".attribute-editor"));
  click(page.accept);
  await settle();
  assert.equal(fixture.postPayload.action, "set_item");
  assert.equal(fixture.postPayload.value, 19, "item str-like attributes are not scaled");

  itemCard = page.root.querySelectorAll(".possession-card")[0];
  click(itemCard);
  const nameInput = page.editorContent.querySelector(".possession-name-form input");
  assert(nameInput, "editor has a name editor");
  nameInput.value = "新石斧";
  submit(page.editorContent.querySelector(".possession-name-form"));
  assert.equal(page.dialog.hidden, false, "name changes use the custom confirmation dialog");
  click(page.accept);
  await settle();
  assert.equal(fixture.postPayload.action, "set_item");
  assert.equal(fixture.postPayload.name, "新石斧");
  assert.equal(Object.prototype.hasOwnProperty.call(fixture.postPayload, "value"), false, "name changes do not send the int64 value field");
  assert.equal(fixture.postPayload.revision, "rev-0");
  assert.equal(page.editorDialog.hidden, true, "successful edits close the stale editor");

  const postCountBeforeSwitch = fixture.calls.filter(call => call.options.method === "POST").length;
  itemCard = page.root.querySelectorAll(".possession-card")[0];
  click(itemCard);
  const secondNameInput = page.editorContent.querySelector(".possession-name-form input");
  secondNameInput.value = "不应写入旧角色";
  submit(page.editorContent.querySelector(".possession-name-form"));
  page.characterSelect.value = "1";
  page.characterSelect.dispatchEvent({type: "change"});
  click(page.accept);
  await settle();
  assert.equal(fixture.calls.filter(call => call.options.method === "POST").length, postCountBeforeSwitch, "switching characters during confirmation cancels the old mutation");
  assert.equal(page.state.characterSlot, 1);
  assert.equal(page.state.snapshot.name, "阿木");

  fixture.mutationStatus = 409;
  itemCard = page.root.querySelectorAll(".possession-card")[0];
  click(itemCard);
  const conflictInput = page.editorContent.querySelector(".possession-name-form input");
  conflictInput.value = "冲突修改";
  submit(page.editorContent.querySelector(".possession-name-form"));
  click(page.accept);
  await settle();
  assert.equal(page.dialog.hidden, false, "a 409 opens the reload confirmation dialog");
  assert.match(page.document.getElementById("player-dialog-message").textContent, /重新加载/);
  const getCountBeforeReload = fixture.calls.filter(call => !call.options.method).length;
  click(page.accept);
  await settle();
  assert(fixture.calls.filter(call => !call.options.method).length > getCountBeforeReload, "accepting conflict reloads the current snapshot");
  fixture.mutationStatus = 200;

  page.catalogKind.value = "item";
  page.catalogQuery.value = "斧";
  submit(page.catalogForm);
  await settle();
  click(page.catalogResults.querySelector(".catalog-entry"));
  const quantity = page.catalogSelection.querySelector("input");
  const location = page.catalogSelection.querySelector("select");
  assert.equal(quantity.type, "number", "grant quantity is a native number input");
  assert.equal(quantity.min, "1", "grant quantity has a lower bound");
  assert.equal(quantity.step, "1", "grant quantity only steps by whole numbers");
  assert.equal(page.catalogSelection.querySelectorAll(".quantity-step").length, 2,
    "grant quantity has visible increment and decrement controls");
  quantity.value = "1.5";
  submit(page.catalogSelection.querySelector("form"));
  assert.match(page.document.getElementById("player-load-state").textContent, /整数数量/,
    "fractional grant quantities show an integer error");
  quantity.value = "1";
  assert.equal(quantity.max, "14", "item quantity accounts for occupied backpack slots");
  location.value = "warehouse";
  location.dispatchEvent({type: "change"});
  assert.equal(quantity.max, "30", "warehouse item quantity uses the 30-slot capacity");

  page.state.snapshot.capacities.item_warehouse = undefined;
  location.dispatchEvent({type: "change"});
  assert.equal(quantity.max, "30", "warehouse item quantity falls back when the capacity field is missing");

  page.catalogKind.value = "pet";
  page.catalogQuery.value = "龟";
  submit(page.catalogForm);
  await settle();
  click(page.catalogResults.querySelector(".catalog-entry"));
  const petQuantity = page.catalogSelection.querySelector("input");
  const petLocation = page.catalogSelection.querySelector("select");
  petLocation.value = "warehouse";
  petLocation.dispatchEvent({type: "change"});
  assert.equal(petQuantity.max, "4", "warehouse pet quantity uses the current five-slot capacity and ignores slot five");
  page.state.snapshot.capacities.pet_warehouse = 8;
  petLocation.dispatchEvent({type: "change"});
  assert.equal(petQuantity.max, "6", "warehouse pet quantity follows a dynamic capacity and counts newly available slots");

  page.catalogKind.value = "pet_skill";
  page.catalogKind.dispatchEvent({type: "change"});
  assert.equal(page.catalogResults.querySelector(".catalog-entry"), null, "changing catalog type clears old results");
  assert.equal(page.catalogSelection.hidden, true, "changing catalog type clears the old selection");

  page.state.snapshot.possessions = page.state.snapshot.possessions.filter(possession => possession.kind !== "item");
  for (let slot = 0; slot < 5; slot++) {
    page.state.snapshot.possessions.push({kind: "item", location: "inventory", slot, name: "装备" + slot, attributes: []});
  }
  page.catalogKind.value = "item";
  submit(page.catalogForm);
  await settle();
  click(page.catalogResults.querySelector(".catalog-entry"));
  assert.equal(page.catalogSelection.querySelector("input").max, "15", "full equipment does not consume backpack grant capacity");
  for (let slot = 5; slot < 20; slot++) {
    page.state.snapshot.possessions.push({kind: "item", location: "inventory", slot, name: "背包" + slot, attributes: []});
  }
  page.catalogKind.value = "item";
  submit(page.catalogForm);
  await settle();
  click(page.catalogResults.querySelector(".catalog-entry"));
  assert.equal(page.catalogSelection.querySelector("input").max, "0", "a full backpack has no item grant capacity");
  assert.equal(page.catalogSelection.querySelector("button").disabled, true, "item grant is disabled when the backpack is full");

  page.catalogKind.value = "pet_skill";
  page.catalogQuery.value = "88";
  submit(page.catalogForm);
  await settle();
  const catalogEntry = page.catalogResults.querySelector(".catalog-entry");
  assert(catalogEntry, "catalog search renders a selectable result");
  click(catalogEntry);
  const skillInput = page.catalogSelection.querySelector("input");
  assert(skillInput, "pet skill selection renders a slot editor");
  skillInput.value = "6";
  submit(page.catalogSelection.querySelector("form"));
  click(page.accept);
  await settle();
  assert.equal(fixture.postPayload.action, "set_pet_skill");
  assert.equal(fixture.postPayload.skill_slot, 6, "skill slot six is accepted");

  const petCard = page.root.querySelectorAll(".possession-card")[1];
  click(petCard);
  const petInputs = page.editorContent.querySelectorAll(".attribute-editor input");
  assert.equal(petInputs[0].value, "123.45", "pet vital is shown in game units");
  assert.equal(petInputs[4].value, "123", "pet growth bytes remain integers");
  petInputs[1].value = "12.34";
  submit(page.editorContent.querySelectorAll(".attribute-editor")[1]);
  click(page.accept);
  await settle();
  assert.equal(fixture.postPayload.action, "set_pet");
  assert.equal(fixture.postPayload.value, 1234, "pet fixed point attributes are sent as raw hundredths");

  const refreshedPetCard = page.root.querySelectorAll(".possession-card")[1];
  click(refreshedPetCard);
  const petSkills = page.editorContent.querySelectorAll(".skill-row");
  assert.equal(petSkills.length, 2, "empty and occupied skill slots are both visible in the editor");
  assert.equal(petSkills[0].querySelectorAll("button").length, 0, "empty skill slots have no delete action");
  assert.equal(petSkills[1].querySelectorAll("button").length, 1, "occupied skill slots have a delete action");

  console.log("admin player asset UI behavior tests passed");
}

main().catch(error => {
  console.error(error);
  process.exitCode = 1;
});
