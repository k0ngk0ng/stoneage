const { test } = require("node:test");
const assert = require("node:assert/strict");
const vm = require("node:vm");
const fs = require("node:fs");
class Element {
  constructor(tag) {
    this.tag = tag;
    this.children = [];
    this.textContent = "";
    this.attributes = {};
    this.open = false;
    this.value = "";
  }
  append(...nodes) {
    this.children.push(...nodes);
  }
  replaceChildren(...nodes) {
    this.children = nodes;
  }
  setAttribute(k, v) {
    this.attributes[k] = v;
  }
  addEventListener() {}
  showModal() {
    this.open = true;
  }
  close() {
    this.open = false;
  }
}
function setup() {
  const nodes = new Map();
  const requests = [];
  const ctx = vm.createContext({
    document: {
      getElementById: (id) => {
        if (!nodes.has(id)) nodes.set(id, new Element("div"));
        return nodes.get(id);
      },
      createElement: (tag) => new Element(tag),
    },
    location: { hash: "" },
    window: { addEventListener() {} },
    URL,
    URLSearchParams,
    AbortController,
    fetch: (url, options) =>
      new Promise((resolve) => requests.push({ url, options, resolve })),
    console,
  });
  vm.runInContext(fs.readFileSync(__dirname + "/site/app.js", "utf8"), ctx);
  return { ctx, nodes, requests };
}
const flush = () => new Promise((resolve) => setImmediate(resolve));
const reply = (request, data) =>
  request.resolve({ ok: true, json: async () => data });
const listing = (name) => ({
  categories: [{ id: "quest", name: "历史任务", count: 1 }],
  entries: [{ kind: "quest", key: "quest:q", name, description: "简介" }],
  total: 1,
  notes: [],
});
test("late list response cannot replace latest search and names remain text", async () => {
  const { ctx, nodes, requests } = setup();
  vm.runInContext("location.hash='#q=new';render()", ctx);
  reply(requests[1], listing("<img src=x onerror=alert(1)>"));
  await flush();
  reply(requests[0], listing("old"));
  await flush();
  assert.equal(
    nodes.get("results").children[0].children[1].textContent,
    "<img src=x onerror=alert(1)>",
  );
  assert.match(nodes.get("status").textContent, /new/);
  assert.equal(requests[0].options.signal.aborted, true);
});
test("late detail response cannot reopen or replace a closed detail", async () => {
  const { ctx, nodes, requests } = setup();
  reply(requests[0], listing("one"));
  await flush();
  vm.runInContext("location.hash='#entry=quest:q';render()", ctx);
  assert.equal(nodes.get("detail").open, true);
  vm.runInContext("location.hash='';render()", ctx);
  assert.equal(nodes.get("detail").open, false);
  reply(requests[1], {
    name: "stale",
    fields: [],
    tables: [],
    links: [],
    sources: [],
  });
  await flush();
  assert.equal(nodes.get("detail").open, false);
  assert.notEqual(nodes.get("article").children[0].textContent, "stale");
});
test("detail source links reject executable URL schemes", async () => {
  const { ctx, nodes, requests } = setup();
  vm.runInContext("openDetail('quest:q')", ctx);
  reply(requests[1], {
    name: "one",
    fields: [],
    links: [
      { name: "bad", url: "javascript:alert(1)" },
      { name: "source", url: "https://news.17173.com/z/stoneage/renwu/n1.htm" },
    ],
    sources: [],
  });
  await flush();
  const links = nodes
    .get("article")
    .children.find((n) => n.className === "links");
  assert.equal(links.children.length, 1);
  assert.match(links.children[0].href, /^https:/);
});
