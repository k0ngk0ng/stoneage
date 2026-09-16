"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");

const source = fs.readFileSync(__dirname + "/static/ai.js", "utf8");
assert.match(source, /\/api\/ai\/initializations/);
assert.match(source, /item\.recoverable === true/);
assert.match(source, /textContent = text == null \? "" : String\(text\)/);

const start = source.indexOf("    const initializationsList =");
const end = source.indexOf("    function selectedSkillNames()", start);
assert(start >= 0 && end > start);

class Element {
  constructor(tag) {
    this.tag = tag;
    this.children = [];
    this.handlers = {};
    this.dataset = {};
    this.disabled = false;
    this.hidden = false;
    this.textContent = "";
    this.classList = {toggle() {}};
  }
  appendChild(child) { this.children.push(child); return child; }
  replaceChildren(...children) { this.children = children; }
  addEventListener(name, handler) { this.handlers[name] = handler; }
  fire(name = "click") { return this.handlers[name]?.(); }
}

const ids = ["ai-initializations-list", "ai-initializations-refresh", "ai-initializations-message"];
const nodes = Object.fromEntries(ids.map(id => [id, new Element(id)]));
const requests = [];
let reloads = 0;
const api = (_root, url, method, body) => new Promise((resolve, reject) => requests.push({url, method, body, resolve, reject}));
const document = {
  getElementById(id) { return nodes[id] || null; },
  createElement(tag) { return new Element(tag); }
};
const profileRoot = {dataset: {csrf: "csrf"}};

new Function("document", "api", "profileRoot", "canWrite", "window", source.slice(start, end))(
  document, api, profileRoot, true, {location: {reload() { reloads++; }}}
);

(async function () {
  assert.equal(requests.length, 1);
  requests[0].resolve({initializations: [
    {profile_id: "<script>alert(1)</script>", character_name: "<b>角色</b>", status: "publication_pending", recoverable: true, updated_at: "2026-09-16T08:09:10Z"},
    {profile_id: "pending-2", character_name: "待核验", status: "applying", recoverable: false, updated_at: "2026-09-16T08:10:10Z"}
  ]});
  await Promise.resolve();
  await Promise.resolve();
  assert.equal(nodes["ai-initializations-list"].children.length, 2);
  const first = nodes["ai-initializations-list"].children[0];
  assert.equal(first.children[0].textContent, "<script>alert(1)</script>");
  assert.equal(first.children[1].textContent, "<b>角色</b>");
  assert.equal(first.children[2].textContent, "待发布");
  assert.equal(first.children[4].children.length, 1);
  assert.equal(first.children[4].children[0].textContent, "恢复发布");
  const second = nodes["ai-initializations-list"].children[1];
  assert.equal(second.children[4].children.length, 1);
  assert.match(second.children[4].children[0].textContent, /待核验/);

  const recovering = first.children[4].children[0].fire();
  assert.equal(requests.length, 2);
  assert.equal(requests[1].url, "/api/ai/initializations/%3Cscript%3Ealert(1)%3C%2Fscript%3E/recover");
  assert.equal(requests[1].method, "POST");
  assert.deepEqual(requests[1].body, {});
  requests[1].resolve({profile: {id: "<script>alert(1)</script>", profile_status: "stopped"}});
  await Promise.resolve();
  await Promise.resolve();
  assert.equal(requests.length, 3);
  requests[2].resolve({initializations: []});
  await recovering;
  assert.equal(reloads, 1);
  assert.match(nodes["ai-initializations-message"].textContent, /不会自动启动/);
  console.log("AI initial-recovery list, XSS and recoverable-button tests passed");
})().catch(error => { console.error(error); process.exitCode = 1; });
