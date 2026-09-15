"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const gifts = require("./static/gifts.js");

const template = fs.readFileSync(path.join(__dirname, "templates/gifts.html"), "utf8");
assert.match(template, /id="gift-preview-warning"/);
assert.match(template, /id="gift-runs-more"/);
assert.match(template, /id="gift-deliveries-more"/);
assert.match(template, /id="gift-character-slot"[^>]*disabled/);
assert.doesNotMatch(template, /gift-preview-token/);

class ClassList {
  constructor() { this.values = new Set(); }
  toggle(name, force) { const next = force === undefined ? !this.values.has(name) : Boolean(force); if (next) this.values.add(name); else this.values.delete(name); return next; }
  add(name) { this.values.add(name); }
  remove(name) { this.values.delete(name); }
  contains(name) { return this.values.has(name); }
}

class Element {
  constructor(document, tag) {
    this.ownerDocument = document; this.tagName = String(tag).toUpperCase(); this.children = [];
    this.parentNode = null; this.dataset = {}; this.attributes = new Map(); this.listeners = new Map();
    this.classList = new ClassList(); this.hidden = false; this.disabled = false; this.value = ""; this.textContent = ""; this.type = "";
  }
  get firstChild() { return this.children[0] || null; }
  get options() { return this.children.filter(child => child.tagName === "OPTION"); }
  appendChild(child) { if (child.parentNode) child.parentNode.removeChild(child); child.parentNode = this; this.children.push(child); return child; }
  removeChild(child) { const index = this.children.indexOf(child); if (index >= 0) this.children.splice(index, 1); child.parentNode = null; return child; }
  setAttribute(name, value) { this.attributes.set(name, String(value)); if (name.startsWith("data-")) this.dataset[name.slice(5).replace(/-([a-z])/g, (_, letter) => letter.toUpperCase())] = String(value); }
  addEventListener(type, handler) { if (!this.listeners.has(type)) this.listeners.set(type, []); this.listeners.get(type).push(handler); }
  dispatchEvent(event) { const current = event || {}; current.type = current.type || "event"; current.target = current.target || this; current.currentTarget = this; current.preventDefault = current.preventDefault || function () { current.defaultPrevented = true; }; for (const handler of this.listeners.get(current.type) || []) handler(current); return !current.defaultPrevented; }
  focus() { this.ownerDocument.activeElement = this; }
  querySelectorAll(selector) {
    const result = [];
    const visit = node => { for (const child of node.children) { if (selector === "button" && child.tagName === "BUTTON") result.push(child); else if (selector === "option" && child.tagName === "OPTION") result.push(child); else if (selector === 'input[name="gift-scope"]' && child.tagName === "INPUT" && child.name === "gift-scope") result.push(child); visit(child); } };
    visit(this); return result;
  }
}

class Document {
  constructor() { this.byID = new Map(); this.activeElement = null; this.body = new Element(this, "body"); }
  createElement(tag) { return new Element(this, tag); }
  register(id, tag) { const element = new Element(this, tag); element.setAttribute("id", id); this.byID.set(id, element); return element; }
  getElementById(id) { return this.byID.get(id) || null; }
}

function makeHarness(fetcher, confirmationMessages) {
  const document = new Document();
  const root = document.register("gift-admin", "div"); root.dataset.csrf = "csrf-token";
  const ids = [
    "gift-status", "gift-refresh", "gift-package-list", "gift-new-package", "gift-editor-title", "gift-editor-mode",
    "gift-package-form", "gift-package-id", "gift-package-name", "gift-item-search", "gift-item-query", "gift-item-results",
    "gift-item-selected", "gift-pet-search", "gift-pet-query", "gift-pet-results", "gift-pet-selected", "gift-definition-hint",
    "gift-package-save", "gift-run-form", "gift-run-package", "gift-single-target", "gift-account-id", "gift-account-username",
    "gift-character-slot", "gift-load-characters", "gift-character-hint", "gift-preview-panel", "gift-preview-stats", "gift-preview-warning",
    "gift-preview-skipped", "gift-preview-reset", "gift-confirm", "gift-run-result", "gift-runs-refresh", "gift-runs-list", "gift-runs-more",
    "gift-run-detail", "gift-run-detail-title", "gift-run-detail-close", "gift-run-deliveries", "gift-deliveries-more"
  ];
  const refs = Object.fromEntries(ids.map(id => [id, document.register(id, id.includes("form") || id.includes("search") ? "form" : id.includes("select") || id === "gift-run-package" || id === "gift-character-slot" ? "select" : id.includes("button") ? "button" : "div")]));
  refs["gift-package-name"].tagName = "INPUT"; refs["gift-package-name"].value = "";
  refs["gift-item-query"].tagName = "INPUT"; refs["gift-pet-query"].tagName = "INPUT"; refs["gift-account-id"].tagName = "INPUT"; refs["gift-account-username"].tagName = "INPUT";
  const single = document.register("gift-single-target", "div"); refs["gift-single-target"] = single;
  const scopeSingle = document.register("scope-single", "input"); scopeSingle.name = "gift-scope"; scopeSingle.value = "single"; scopeSingle.checked = true;
  const scopeAll = document.register("scope-all", "input"); scopeAll.name = "gift-scope"; scopeAll.value = "all"; scopeAll.checked = false;
  root.querySelectorAll = selector => selector === 'input[name="gift-scope"]' ? [scopeSingle, scopeAll] : [];
  root.appendChild(scopeSingle); root.appendChild(scopeAll);
  const windowObject = {confirm: message => { if (confirmationMessages) confirmationMessages.push(message); return true; }};
  return {document, root, refs, scopeSingle, scopeAll, state: gifts.init(document, windowObject, fetcher)};
}

function response(payload, status = 200) { return {ok: status >= 200 && status < 300, status, json: async () => payload}; }
const packages = [{id: 7, name: "新手包", definition: {items: [{template_id: 11, quantity: 10}], pets: []}}];

(async () => {
  assert.deepEqual(gifts.definitionPayload([{template_id: "101", id: "11", quantity: "2"}], [{id: 22, quantity: 1}]), {items: [{id: 101, quantity: 2}], pets: [{id: 22, quantity: 1}]});
  assert.equal(gifts.normalizeDefinition({items: [{template_id: "101", quantity: "2"}], pets: []}).items[0].id, 101);
  assert.deepEqual(gifts.buildGiftRunPayload({package_id: "7", scope: "single", account_id: "42", character_slot: "1", account_username: "alice"}), {package_id: 7, scope: "single", account_id: 42, character_slot: 1, account_username: "alice"});
  assert.equal(gifts.previewSummary({targets: [{id: 1}, {id: 2}], eligible: 1, skipped: [{reason: "容量不足"}], preview_token: "preview-1"}).targets, 2);
  assert.equal(gifts.previewSummary({total: 3, eligible: 2, skipped: [], preview_token: "preview-2"}).targets, 3);

  const calls = [];
  let runPage = 0; let noEligiblePreview = false; let deferPreview = false; let releasePreview = null; const confirmationMessages = [];
  const fetcher = async (url, options = {}) => {
    calls.push({url, options});
    if (url === "/api/gift-packages" && options.method !== "POST" && options.method !== "PUT") return response({packages});
    if (url === "/api/gift-runs") {
      if (options.method === "POST") return response({run: {id: 99, status: "queued"}});
      runPage += 1; return response({runs: [{id: runPage, target_scope: "all", status: "completed"}], next_cursor: runPage === 1 ? "2" : ""});
    }
    if (url === "/api/gift-runs/99" || url === "/api/gift-runs/99?cursor=1&limit=100") {
      return url.includes("cursor=") ? response({run: {id: 99, target_scope: "all"}, deliveries: [{account_id: 2, character_name: "阿木", character_slot: 1, status: "skip_capacity", error: "容量不足"}]}) : response({run: {id: 99, target_scope: "all"}, deliveries: [{account_id: 1, character_name: "阿石", character_slot: 0, status: "applied"}], next_cursor: "1"});
    }
    if (url === "/api/gift-preview") {
      const previewPayload = noEligiblePreview
        ? response({targets: 2, eligible: 0, skipped: [{account: "bob", character: "#1", reason: "容量不足"}, {account: "carol", character: "#0", reason: "容量不足"}]})
        : response({targets: 2, eligible: 1, skipped: [{account: "bob", character: "#1", reason: "容量不足"}], preview_token: "preview-1"});
      if (deferPreview) return new Promise(resolve => { releasePreview = () => { deferPreview = false; resolve(previewPayload); }; });
      return previewPayload;
    }
    if (url.startsWith("/api/player-catalog?kind=item")) return response({entries: [{id: 11, template_id: 111, name: "石斧"}], total: 1});
    if (url.startsWith("/api/gift-characters?account_id=42")) return response({account_id: 42, account_username: "alice", characters: [{slot: 0, name: "阿石"}, {slot: 1, name: "阿木"}]});
    if (url.startsWith("/api/gift-characters?account_username=alice")) return response({account_id: 42, account_username: "alice", characters: [{slot: 0, name: "阿石"}]});
    if (options.method === "POST" && url === "/api/gift-packages") return response({package: {id: 8, name: "测试包", definition: {items: [{template_id: 111, quantity: 1}], pets: []}}});
    return response({});
  };
  const harness = makeHarness(fetcher, confirmationMessages); for (let index = 0; index < 8; index++) await Promise.resolve();
  assert.equal(harness.state.packages.length, 1, "initial package list loads");
  const packageOption = harness.refs["gift-run-package"].options.find(option => option.value === "7");
  assert.match(packageOption.textContent, /10 件物品/, "package summary uses total quantity");
  harness.refs["gift-item-query"].value = "斧"; harness.refs["gift-item-search"].dispatchEvent({type: "submit"});
  for (let index = 0; index < 8; index++) await Promise.resolve();
  const addButton = harness.refs["gift-item-results"].querySelectorAll("button")[0]; assert(addButton, "catalog result exposes add action"); addButton.dispatchEvent({type: "click"});
  assert.equal(harness.state.selected.item[0].id, 111, "catalog selection uses the template ID");
  harness.refs["gift-package-name"].value = "测试包"; harness.refs["gift-package-save"].dispatchEvent({type: "click"});
  for (let index = 0; index < 8; index++) await Promise.resolve();
  const saved = calls.find(call => call.options.method === "POST" && call.url === "/api/gift-packages");
  assert(saved, "saving a package calls the package API");
  assert.equal(JSON.parse(saved.options.body).definition.items[0].id, 111);
  const savedPackage = harness.state.packages.find(packageValue => packageValue.id === 8);
  assert(savedPackage, "saved package remains available for editing");
  harness.state.editPackage(savedPackage);
  assert.equal(harness.state.selected.item[0].id, 111, "saved template ID remains when editing");
  assert.equal(harness.state.selected.item[0].quantity, 1, "saved quantity remains when editing");
  assert.equal(harness.state.selected.item[0].name, "石斧", "cached catalog name is restored when editing");
  harness.state.catalog.item = [];
  harness.state.editPackage(savedPackage);
  for (let index = 0; index < 8; index++) await Promise.resolve();
  assert(calls.some(call => call.url.includes("/api/player-catalog?kind=item&q=111")), "editing queries the catalog by ID when the cache is empty");
  assert.equal(harness.state.selected.item[0].name, "石斧", "catalog lookup restores the saved item name");
  harness.refs["gift-account-id"].value = "42"; harness.state.loadCharacters(); for (let index = 0; index < 8; index++) await Promise.resolve();
  assert(calls.some(call => call.url === "/api/gift-characters?account_id=42"), "role picker uses the finalized gift character endpoint");
  assert.equal(harness.refs["gift-character-slot"].options.length, 2, "role picker renders returned characters");
  harness.refs["gift-account-id"].value = ""; harness.refs["gift-account-username"].value = "alice"; harness.state.loadCharacters(); for (let index = 0; index < 8; index++) await Promise.resolve();
  assert(calls.some(call => call.url === "/api/gift-characters?account_username=alice"), "role picker resolves a username when no account ID is supplied");
  harness.refs["gift-run-package"].value = "7"; harness.refs["gift-account-id"].value = "42"; harness.refs["gift-character-slot"].value = "0";
  deferPreview = true;
  const stalePreview = harness.state.previewRun();
  harness.refs["gift-account-id"].value = "43";
  harness.refs["gift-account-id"].dispatchEvent({type: "input"});
  assert.equal(harness.state.preview, null, "changing a target invalidates the current preview");
  assert.equal(harness.refs["gift-preview-panel"].hidden, true, "changing a target hides the stale preview");
  assert(releasePreview, "preview request is held for the stale response test");
  releasePreview();
  await stalePreview;
  assert.equal(harness.state.preview, null, "stale preview response is discarded after target changes");
  assert.equal(harness.refs["gift-preview-panel"].hidden, true, "stale preview response does not restore the panel");
  harness.refs["gift-account-id"].value = "42";
  noEligiblePreview = true;
  await harness.state.previewRun();
  assert.equal(harness.refs["gift-preview-panel"].hidden, false, "all-capacity-skipped previews remain visible");
  assert.equal(harness.refs["gift-confirm"].disabled, true, "all-capacity-skipped previews cannot be confirmed");
  assert.equal(harness.refs["gift-preview-skipped"].children.length > 0, true, "all capacity skips remain visible");
  noEligiblePreview = false;
  harness.refs["gift-run-form"].dispatchEvent({type: "submit"}); for (let index = 0; index < 8; index++) await Promise.resolve();
  assert.equal(harness.refs["gift-confirm"].disabled, false, "preview token enables confirmation when an eligible target exists");
  assert(harness.refs["gift-preview-skipped"].children.length > 0, "capacity skips are rendered in the preview");
  assert.match(harness.refs["gift-preview-warning"].textContent, /2 个/);
  harness.refs["gift-confirm"].dispatchEvent({type: "click"}); for (let index = 0; index < 8; index++) await Promise.resolve();
  const run = calls.find(call => call.options.method === "POST" && call.url === "/api/gift-runs");
  assert(run, "confirmation submits the gift run"); assert.equal(JSON.parse(run.options.body).preview_token, "preview-1");
  assert.match(confirmationMessages[0], /2 个/);
  harness.state.runsNextCursor = "2"; await harness.state.loadRuns({append: true});
  assert(calls.some(call => call.url === "/api/gift-runs?cursor=2&limit=100"), "run history loads the next cursor page");
  await harness.state.loadRunDetail(99);
  assert.equal(harness.state.deliveries[0].status, "applied");
  await harness.state.loadRunDetail(99, {append: true});
  assert.equal(harness.state.deliveries[1].status, "skip_capacity", "delivery detail appends capacity skips");
  assert.equal(gifts.deliveryStatusLabel("applied"), "已发放");
  assert.equal(gifts.deliveryStatusLabel("skip_capacity"), "容量不足，整角色跳过");
  console.log("admin gift package/search/preview/confirm UI tests passed");
})().catch(error => { console.error(error); process.exitCode = 1; });
