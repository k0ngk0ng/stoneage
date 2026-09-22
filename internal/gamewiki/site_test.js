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
  const messages = [];
  let worker;
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
    Worker: class {
      constructor() {
        worker = this;
      }
      postMessage(data) {
        messages.push(data);
      }
    },
    console,
  });
  vm.runInContext(fs.readFileSync(__dirname + "/site/app.js", "utf8"), ctx);
  return {
    ctx,
    nodes,
    requests,
    messages,
    response: (message, data) =>
      worker.onmessage({ data: { id: message.id, data } }),
  };
}
const flush = () => new Promise((resolve) => setImmediate(resolve));
const reply = (request, data) =>
  request.resolve({ ok: true, json: async () => data });
const listing = (name) => ({
  categories: [{ id: "quest", name: "任务", count: 1 }],
  entries: [{ kind: "quest", key: "quest:q", name, description: "简介" }],
  total: 1,
  notes: [],
});

test("landing only loads small directory and never starts search", async () => {
  const { nodes, requests, messages } = setup();
  assert.equal(requests.length, 1);
  assert.equal(requests[0].url, "/wiki/data/catalog.json");
  reply(requests[0], listing("one"));
  await flush();
  assert.equal(messages.length, 0);
  assert.equal(nodes.get("results").children.length, 1);
  assert.match(nodes.get("status").textContent, /目录/);
});
test("stale local search cannot replace latest search; values remain text", async () => {
  const { ctx, nodes, requests, messages, response } = setup();
  reply(requests[0], listing("one"));
  await flush();
  vm.runInContext("location.hash='#q=old';render()", ctx);
  vm.runInContext("location.hash='#q=new';render()", ctx);
  response(messages[1], listing("<img src=x onerror=alert(1)>"));
  await flush();
  response(messages[0], listing("old"));
  await flush();
  assert.equal(
    nodes.get("results").children[0].children[1].textContent,
    "<img src=x onerror=alert(1)>",
  );
  assert.match(nodes.get("status").textContent, /new/);
  assert.equal(requests.length, 1);
});
test("closing detail ignores pending shard; links reject executable schemes", async () => {
  const { ctx, nodes, requests, messages, response } = setup();
  reply(requests[0], listing("one"));
  await flush();
  vm.runInContext("location.hash='#entry=quest:q';render()", ctx);
  response(messages[0], { url: "/wiki/data/revision/00.json" });
  await flush();
  vm.runInContext("location.hash='';render()", ctx);
  reply(requests[1], { "quest:q": { name: "late", fields: [], links: [] } });
  await flush();
  assert.equal(nodes.get("detail").open, false);
  vm.runInContext("openDetail('quest:other')", ctx);
  response(messages[1], { url: "/wiki/data/revision/01.json" });
  await flush();
  reply(requests[2], {
    "quest:other": {
      name: "one",
      fields: [],
      links: [
        { name: "bad", url: "javascript:alert(1)" },
        {
          name: "source",
          url: "https://news.17173.com/z/stoneage/renwu/n1.htm",
        },
      ],
    },
  });
  await flush();
  const links = nodes
    .get("article")
    .children.find((n) => n.className === "links");
  assert.equal(links.children.length, 1);
});

test("media only uses configured CDN paths, never local media or arbitrary URLs", () => {
  const {ctx,nodes}=setup();
  vm.runInContext('$("media-config")',ctx);
  nodes.get('media-config').getAttribute=()=> 'https://cdn.example.com/stoneage';
  assert.equal(vm.runInContext('mediaURL("assets/bitmaps/bitmap_9136.png")',ctx),'https://cdn.example.com/stoneage/assets/bitmaps/bitmap_9136.png');
  for(const path of ['https://evil.example/a.png','../secret','assets/../x','wiki/maps/1.png']) {
    ctx.badPath=path; assert.equal(vm.runInContext('mediaURL(badPath)',ctx),'');
  }
});
