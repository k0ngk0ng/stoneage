"use strict";

// This test extracts only the profile-view controller from ai.js.  It keeps
// the worker's production file untouched while exercising the browser-level
// request and timer lifecycle with deterministic deferred responses.
const assert = require("node:assert/strict");
const fs = require("node:fs");

const source = fs.readFileSync(__dirname + "/static/ai.js", "utf8");
const start = source.indexOf('    let profileViewID = "";');
const end = source.indexOf("    if (form && canWrite)", start);
assert(start >= 0 && end > start, "profile view controller block not found");
const profileViewSource = source.slice(start, end) +
  "\nreturn {openProfileView, closeProfileView, refreshProfileView, copyProfileLocalCommand};";
const recoveryTextStart = source.indexOf("  function aiRuntimeMessageText");
const recoveryTextEnd = source.indexOf("  function aiObservationContent", recoveryTextStart);
assert(recoveryTextStart >= 0 && recoveryTextEnd > recoveryTextStart, "runtime recovery text block not found");
const recoveryText = source.slice(recoveryTextStart, recoveryTextEnd);
assert.match(recoveryText, /保持暂停|已暂停/, "paused state must be explicit");
assert.match(recoveryText, /点击启动|可以启动/, "paused state must tell the operator how to continue");
assert.doesNotMatch(recoveryText, /自动恢复|自动重试/, "paused state must not promise automatic recovery");

class ClassList {
  constructor() { this.values = new Set(); }
  add(name) { this.values.add(name); }
  remove(name) { this.values.delete(name); }
  toggle(name, force) {
    const next = force === undefined ? !this.values.has(name) : Boolean(force);
    if (next) this.values.add(name); else this.values.delete(name);
    return next;
  }
  contains(name) { return this.values.has(name); }
}

class Element {
  constructor(document, id) {
    this.ownerDocument = document;
    this.id = id || "";
    this.dataset = {};
    this.hidden = false;
    this.disabled = false;
    this.title = "";
    this.value = "";
    this.textContent = "";
    this.className = "";
    this.style = {};
    this.children = [];
    this.parentNode = null;
    this.listeners = new Map();
    this.classList = new ClassList();
    this.scrollTop = 0;
    this.open = false;
  }
  addEventListener(type, listener) {
    if (!this.listeners.has(type)) this.listeners.set(type, []);
    this.listeners.get(type).push(listener);
  }
  async dispatch(type, event) {
    const current = event || {};
    current.type = type;
    current.target = current.target || this;
    let result;
    for (const listener of this.listeners.get(type) || []) result = await listener(current);
    return result;
  }
  appendChild(child) { child.parentNode = this; this.children.push(child); return child; }
  removeChild(child) { const index = this.children.indexOf(child); if (index >= 0) this.children.splice(index, 1); child.parentNode = null; return child; }
  setAttribute(name, value) { this[name] = String(value); }
  select() { this.selected = true; }
  replaceChildren(...children) { this.children = children; }
  querySelector(selector) {
    if (this.id === "ai-profile-view-modal" && selector === ".ai-profile-view-dialog") return this.dialog;
    if (this.id === "ai-profile-view-modal" && selector === "button[data-ai-profile-view-cancel]") return this.closeButton;
    return null;
  }
  querySelectorAll(selector) {
    if (this.id === "ai-profile-view-modal" && selector === "[data-ai-profile-view-cancel]") {
      return [this.backdrop, this.closeButton].filter(Boolean);
    }
    if (selector === "details") return [];
    return [];
  }
  focus() { this.ownerDocument.activeElement = this; }
}

function makeDocument() {
  const document = {
    activeElement: null,
    body: {classList: new ClassList(), children: [], appendChild(child) { child.parentNode = this; this.children.push(child); return child; }, removeChild(child) { const index = this.children.indexOf(child); if (index >= 0) this.children.splice(index, 1); child.parentNode = null; return child; }},
    listeners: new Map(),
    nodes: new Map(),
    addEventListener(type, listener) {
      if (!this.listeners.has(type)) this.listeners.set(type, []);
      this.listeners.get(type).push(listener);
    },
    async dispatch(type, event) {
      const current = event || {};
      current.type = type;
      for (const listener of this.listeners.get(type) || []) await listener(current);
    },
    createElement(tag) { return new Element(document, tag); },
    getElementById(id) { return this.nodes.get(id) || null; },
    querySelectorAll(selector) {
      if (selector === "[data-ai-profile-view-cancel]") {
        const modal = this.nodes.get("ai-profile-view-modal");
        return modal ? [modal.backdrop, modal.closeButton] : [];
      }
      return [];
    },
    contains() { return true; }
  };

  const modal = new Element(document, "ai-profile-view-modal");
  modal.hidden = true;
  modal.dialog = new Element(document, "ai-profile-view-dialog");
  modal.backdrop = new Element(document, "ai-profile-view-backdrop");
  modal.closeButton = new Element(document, "ai-profile-view-close");
  document.nodes.set(modal.id, modal);
  for (const id of [
    "ai-profile-view-content", "ai-profile-view-title", "ai-profile-view-subtitle",
    "ai-profile-view-refresh", "ai-profile-view-refresh-interval",
    "ai-profile-view-refresh-status", "ai-profile-copy-command",
    "ai-profile-local-location", "ai-profile-local-connection",
    "ai-profile-local-command-status"
  ]) document.nodes.set(id, new Element(document, id));
  const commandHost = new Element(document, "ai-profile-local-command-host");
  commandHost.appendChild(document.nodes.get("ai-profile-local-command-status"));
  document.commandHost = commandHost;
  document.nodes.get("ai-profile-view-refresh-interval").value = "30";
  document.activeElement = document.nodes.get("ai-profile-view-refresh");
  return document;
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return {promise, resolve, reject};
}

function flush() {
  return new Promise(resolve => setImmediate(() => setImmediate(resolve)));
}

function fixture({canWrite = true, clipboardWrite} = {}) {
  const document = makeDocument();
  const timers = new Map();
  const clearedTimers = [];
  let nextTimerID = 1;
  const windowObject = {
    setTimeout(callback) {
      const id = nextTimerID++;
      timers.set(id, callback);
      return id;
    },
    clearTimeout(id) {
      clearedTimers.push(id);
      timers.delete(id);
    },
    addEventListener() {}
  };
  const calls = [];
  const api = (_root, path, method, body, signal) => {
    const response = deferred();
    calls.push({path, method: method || "GET", body, signal, response});
    return response.promise;
  };
  const aiAuditElement = (tag, className, text) => {
    const element = document.createElement(tag);
    element.className = className || "";
    if (text !== undefined) element.textContent = text;
    return element;
  };
  const render = (parent, name) => parent.appendChild(aiAuditElement("p", "marker", name));
  const writeClipboard = clipboardWrite || (async value => { fixture.clipboard.push(value); });
  const controls = new Function(
    "document", "window", "navigator", "profileRoot", "canWrite", "api",
    "initialText", "renderAIRecoverySummary", "renderAIObservationEvidence",
    "renderAIAuditState", "renderAIAuditEvents", "aiAuditTime", "aiAuditElement",
    "show", "hide", "AbortController", profileViewSource
  )(
    document, windowObject, {clipboard: {writeText: writeClipboard}},
    {dataset: {csrf: "fixture-csrf"}}, canWrite, api,
    profile => "initial:" + profile.id, parent => render(parent, "recovery"), parent => render(parent, "observation"),
    parent => render(parent, "state"), parent => render(parent, "events"), value => String(value || "time"), aiAuditElement,
    element => { element.hidden = false; }, element => { element.hidden = true; }, AbortController
  );
  return {document, windowObject, timers, clearedTimers, calls, controls, commandHost: document.commandHost};
}

fixture.clipboard = [];

function responseFor(id, executor) {
  return {
    profile: {id, updated_at: "2026-09-17T10:00:00Z", character: {name: id === "profile-a" ? "Alpha" : "Beta"}, runtime: {state: "paused", message: "paused"}},
    events: {events: []},
    "life-state": {available: false},
    recovery: {},
    executor
  };
}

function resolveBatch(fixtureValue, startIndex, id, executor) {
  const batch = fixtureValue.calls.slice(startIndex, startIndex + 5);
  assert.equal(batch.length, 5, "a profile refresh must issue five requests");
  for (const call of batch) {
    assert.equal(call.method, "GET", "profile refresh must be read-only");
    if (call.path.endsWith("/events")) call.response.resolve(responseFor(id, executor).events);
    else if (call.path.endsWith("/life-state")) call.response.resolve(responseFor(id, executor)["life-state"]);
    else if (call.path.endsWith("/recovery")) call.response.resolve(responseFor(id, executor).recovery);
    else if (call.path.endsWith("/executor")) call.response.resolve(responseFor(id, executor).executor);
    else call.response.resolve({profile: responseFor(id, executor).profile});
  }
  return batch;
}

(async () => {
  fixture.clipboard.length = 0;
  const f = fixture();
  const content = f.document.getElementById("ai-profile-view-content");
  const title = f.document.getElementById("ai-profile-view-title");
  const refresh = f.document.getElementById("ai-profile-view-refresh");
  const interval = f.document.getElementById("ai-profile-view-refresh-interval");
  const copy = f.document.getElementById("ai-profile-copy-command");
  const close = f.document.getElementById("ai-profile-view-modal").closeButton;

  f.controls.openProfileView("profile-a", refresh);
  assert.equal(f.calls.length, 5);
  assert(f.calls.every(call => call.method === "GET"), "opening the modal must not POST");
  const firstBatch = f.calls.slice(0, 5);

  // Switching profiles aborts/invalidate the first request set.  A late Alpha
  // response must never replace the currently visible Beta profile.
  f.controls.openProfileView("profile-b", refresh);
  assert.equal(f.calls.length, 10);
  assert(firstBatch.every(call => call.signal && call.signal.aborted), "switching profiles did not abort the old requests");
  assert(f.calls.slice(5).every(call => call.method === "GET"));
  resolveBatch(f, 5, "profile-b", {location: "local", connected: true});
  await flush();
  assert.equal(title.textContent, "Beta · AI 玩家详情");
  assert(content.children.some(child => child.textContent === "initial:profile-b"));
  resolveBatch(f, 0, "profile-a", {location: "local", connected: true});
  await flush();
  assert.equal(title.textContent, "Beta · AI 玩家详情", "late profile-A response overwrote the active profile");
  assert(content.children.some(child => child.textContent === "initial:profile-b"), "late profile-A response replaced content");

  // Automatic refresh is also read-only.  Trigger the scheduled callback by
  // hand so the test never sleeps and can prove its method for every request.
  assert.equal(f.timers.size, 1, "successful refresh must schedule auto refresh");
  const autoTimerID = Array.from(f.timers.keys())[0];
  const autoRefresh = f.timers.get(autoTimerID);
  f.timers.delete(autoTimerID);
  autoRefresh();
  const autoBatchStart = f.calls.length - 5;
  assert.equal(f.calls.length, 15);
  assert(f.calls.slice(autoBatchStart).every(call => call.method === "GET"), "auto refresh issued a write");
  resolveBatch(f, autoBatchStart, "profile-b", {location: "local", connected: true});
  await flush();

  // Closing the modal clears the scheduled callback and invalidates any late
  // response.  A callback retained by a browser timer must be harmless too.
  const callsBeforeClose = f.calls.length;
  assert.equal(f.timers.size, 1);
  await close.dispatch("click");
  assert.equal(f.timers.size, 0, "closing the modal left an auto-refresh timer armed");
  assert(f.clearedTimers.length > 0, "closing the modal did not clear its timer");
  assert.equal(f.document.getElementById("ai-profile-view-modal").hidden, true);
  for (const callback of f.timers.values()) callback();
  await flush();
  assert.equal(f.calls.length, callsBeforeClose, "closed modal scheduled another refresh");

  // Reopen and verify command generation is the only write, and happens only
  // after the explicit copy button click.
  f.controls.openProfileView("profile-b", refresh);
  const reopenStart = f.calls.length - 5;
  resolveBatch(f, reopenStart, "profile-b", {location: "local", connected: true});
  await flush();
  assert.equal(copy.disabled, false, "connected local executor should enable copy for an admin");
  assert(f.calls.every(call => call.method === "GET"), "refreshing executor state issued a POST");
  copy.dispatch("click");
  await flush();
  const pendingCopy = f.calls.at(-1);
  assert.equal(pendingCopy.method, "POST");
  assert.match(pendingCopy.path, /\/local-command$/);
  assert.equal(copy.disabled, true, "copy button must be disabled while command generation is pending");
  copy.dispatch("click");
  await flush();
  assert.equal(f.calls.at(-1), pendingCopy, "a second click issued a duplicate invitation");
  pendingCopy.response.resolve({command: "stoneage local --profile profile-b", expires_at: "2026-09-17T11:00:00Z"});
  await flush();
  assert.deepEqual(fixture.clipboard, ["stoneage local --profile profile-b"]);

  // A pending invitation belongs to the profile that requested it. Switching
  // profiles invalidates the POST response before it can copy or update the
  // newly visible profile.
  const copiedBeforeSwitch = fixture.clipboard.length;
  copy.dispatch("click");
  await flush();
  const pendingSwitchCopy = f.calls.at(-1);
  assert.equal(pendingSwitchCopy.method, "POST");
  f.controls.openProfileView("profile-a", refresh);
  const switchStart = f.calls.length - 5;
  resolveBatch(f, switchStart, "profile-a", {location: "local", connected: true});
  await flush();
  pendingSwitchCopy.response.resolve({command: "stoneage local --profile stale-profile"});
  await flush();
  assert.equal(fixture.clipboard.length, copiedBeforeSwitch, "late profile command was copied after switching profiles");

  // Closing the modal invalidates the same request even when the response
  // arrives after the dialog is gone.
  const closeCopy = f.document.getElementById("ai-profile-copy-command");
  closeCopy.dispatch("click");
  await flush();
  const pendingCloseCopy = f.calls.at(-1);
  assert.equal(pendingCloseCopy.method, "POST");
  await f.document.getElementById("ai-profile-view-modal").closeButton.dispatch("click");
  pendingCloseCopy.response.resolve({command: "stoneage local --profile closed-profile"});
  await flush();
  assert.equal(fixture.clipboard.length, copiedBeforeSwitch, "late closed-dialog command was copied");

  // A rejected Clipboard API falls back to a visible, selectable textarea so
  // an operator can still copy the exact command manually.
  const fallback = fixture({clipboardWrite: async () => { throw new Error("clipboard denied"); }});
  const fallbackRefresh = fallback.document.getElementById("ai-profile-view-refresh");
  fallback.controls.openProfileView("profile-fallback", fallbackRefresh);
  const fallbackStart = fallback.calls.length - 5;
  resolveBatch(fallback, fallbackStart, "profile-fallback", {location: "server", connected: false});
  await flush();
  const fallbackCopy = fallback.document.getElementById("ai-profile-copy-command");
  fallbackCopy.dispatch("click");
  await flush();
  const fallbackPost = fallback.calls.at(-1);
  fallbackPost.response.resolve({command: "stoneage local --profile profile-fallback"});
  await flush();
  const manual = fallback.commandHost.children.find(child => child.className === "ai-profile-local-command-fallback");
  assert(manual, "clipboard rejection did not expose a manual command textarea");
  assert.equal(manual.value, "stoneage local --profile profile-fallback");

  // A server-side player or a disconnected local executor is exactly when an
  // invitation is useful: clicking it requests a fresh local-run command.
  f.controls.openProfileView("profile-a", refresh);
  const unavailableStart = f.calls.length - 5;
  resolveBatch(f, unavailableStart, "profile-a", {location: "server", connected: false});
  await flush();
  assert.equal(copy.disabled, false, "server/disconnected executor must allow local-run invitation");
  assert(f.calls.slice(unavailableStart).every(call => call.method === "GET"));

  // Read-only operators never receive a command-generation control, even if
  // the executor is otherwise eligible for a local invitation.
  const readOnly = fixture({canWrite: false});
  const readOnlyRefresh = readOnly.document.getElementById("ai-profile-view-refresh");
  readOnly.controls.openProfileView("profile-ro", readOnlyRefresh);
  const readOnlyStart = readOnly.calls.length - 5;
  resolveBatch(readOnly, readOnlyStart, "profile-ro", {location: "server", connected: false});
  await flush();
  assert.equal(readOnly.document.getElementById("ai-profile-copy-command").disabled, true, "read-only operator can generate a local command");
  assert(readOnly.calls.every(call => call.method === "GET"));

  console.log("AI profile view race, read-only refresh, close cleanup and click-only command tests completed");
})().catch(error => {
  console.error(error);
  process.exitCode = 1;
});

// The create form is tested separately from the view controller so a failed
// POST can be resolved without constructing an entire browser DOM.  The
// assertion is behavioral: the entered role name and prompt survive the
// rejected request and the page is not reloaded.
(async () => {
  const submitStart = source.indexOf('    if (form && canWrite) form.addEventListener("submit"', start);
  const submitEnd = source.indexOf('\n    });\n\n    document.querySelectorAll(".ai-delete-profile")', submitStart);
  assert(submitStart > start && submitEnd > submitStart, "profile create handler not found");
  const submitSource = source.slice(submitStart, submitEnd + "\n    });".length);

  class Control {
    constructor(value) { this.value = value || ""; this.checked = false; }
    focus() {}
  }
  const fields = new Map();
  for (const name of [
    "id", "goal_kind", "goal_description", "target_level", "target_character_id", "stop_when_completed",
    "target_kind", "life_decision_interval", "character_build_enabled", "build_vital", "build_strength",
    "build_toughness", "build_dexterity", "build_reserve_points", "model_config_id", "personality_name",
    "personality_prompt", "unlimited_funds", "daily_token_budget", "external_spend_limit", "character_name",
    "character_slot", "account_id", "account_username", "character_id", "expected_version", "status"
  ]) fields.set(name, new Control());
  fields.get("goal_kind").value = "life";
  fields.get("life_decision_interval").value = "300";
  fields.get("daily_token_budget").value = "100000";
  fields.get("character_slot").value = "0";
  fields.get("stop_when_completed").checked = false;
  fields.get("unlimited_funds").checked = true;
  fields.get("status").value = "stopped";
  const form = {
    listeners: new Map(),
    elements: {namedItem(name) { return fields.get(name) || null; }},
    querySelectorAll() { return []; },
    addEventListener(type, listener) { this.listeners.set(type, listener); }
  };
  const notice = {textContent: "", hidden: true, classList: {toggle() {}}};
  const profileRoot = {querySelector() { return notice; }};
  const calls = [];
  const fakeWindow = {location: {reload() { calls.push({reloaded: true}); }}};
  const fakeAPI = async (_root, path, method, body) => {
    calls.push({path, method, body});
    throw new Error("创建失败，请稍后重试");
  };
  const fieldValue = (target, name) => {
    const control = target.elements.namedItem(name);
    return control ? String(control.value || "").trim() : "";
  };
  const fieldChecked = (target, name) => {
    const control = target.elements.namedItem(name);
    return !!control && control.checked;
  };
  const fieldInteger = (target, name) => {
    const number = Number.parseInt(fieldValue(target, name), 10);
    return Number.isFinite(number) ? number : 0;
  };
  new Function(
    "form", "canWrite", "provisioningAvailable", "profileRoot", "api", "window", "message",
    "value", "checked", "integer", "selectedSkillNames", "editedSkills", "initialConfig", "initialPets",
    "editingGoal", "editingPersonality",
    submitSource
  )(
    form, true, true, profileRoot, fakeAPI, fakeWindow,
    (_root, text) => { notice.hidden = !text; notice.textContent = text || ""; },
    fieldValue, fieldChecked, fieldInteger, () => [], () => [], () => ({mode: "birth", mount: false}), [], {}, {}
  );
  fields.get("character_name").value = "保留的角色名";
  fields.get("personality_prompt").value = "失败后仍保留这段人格提示词";
  const submit = form.listeners.get("submit");
  assert.equal(typeof submit, "function");
  await submit({preventDefault() {}});
  assert.equal(fields.get("character_name").value, "保留的角色名", "创建失败清空了角色名称");
  assert.equal(fields.get("personality_prompt").value, "失败后仍保留这段人格提示词", "创建失败清空了人格提示词");
  assert.match(notice.textContent, /创建失败/);
  assert(!calls.some(call => call.reloaded), "创建失败仍然触发了页面刷新");
  console.log("AI profile create failure keeps form input and error message");
})().catch(error => {
  console.error(error);
  process.exitCode = 1;
});
