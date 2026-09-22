const { test } = require("node:test");
const assert = require("node:assert/strict");
const vm = require("node:vm");
const fs = require("node:fs");
const zlib = require("node:zlib");
const { webcrypto } = require("node:crypto");
const revision = JSON.parse(
  zlib.gunzipSync(fs.readFileSync(__dirname + "/snapshot/catalog.json.gz")),
).revision;
function setup() {
  const calls = [],
    messages = [];
  const ctx = vm.createContext({
    self: { postMessage: (d) => messages.push(d) },
    crypto: webcrypto,
    TextEncoder,
    Uint8Array,
    fetch: async (url) => {
      calls.push(url);
      assert(!url.includes("?"));
      const file =
        __dirname + "/snapshot/" + url.replace("/wiki/data/", "") + ".gz";
      return {
        ok: true,
        json: async () => JSON.parse(zlib.gunzipSync(fs.readFileSync(file))),
      };
    },
  });
  vm.runInContext(
    fs.readFileSync(__dirname + "/site/search-worker.js", "utf8"),
    ctx,
  );
  return {
    calls,
    messages,
    query: async (params) => {
      await ctx.self.onmessage({ data: { id: messages.length + 1, params } });
      return messages.at(-1);
    },
  };
}
test("category search loads only that category; repeated searches have no network", async () => {
  const { calls, query } = setup();
  let r = await query({ kind: "quest", q: "黑暗精灵王" });
  assert(!r.error, r.error);
  assert(r.data.total > 0);
  assert.deepEqual(calls, [
    "/wiki/data/catalog.json",
    `/wiki/data/${revision}/category-quest.json`,
  ]);
  await query({ kind: "quest", q: "贝壳" });
  assert.equal(calls.length, 2);
  r = await query({ kind: "quest", q: "不存在的任务名称" });
  assert.equal(r.data.total, 0);
  await query({ kind: "quest", offset: 90 });
  assert.equal(calls.length, 2);
});
test("global index is only loaded on explicit all-category search", async () => {
  const { calls, query } = setup();
  const r = await query({ q: "1690" });
  assert(!r.error, r.error);
  assert(r.data.total > 0);
  assert.equal(calls[1], `/wiki/data/${revision}/index.json`);
});
test("detail deep link requires only catalog metadata, never any search index", async () => {
  const { calls, query } = setup();
  const r = await query({ entry: "enemy:1690" });
  assert(!r.error, r.error);
  assert.deepEqual(calls, ["/wiki/data/catalog.json"]);
  const shard = JSON.parse(
    zlib.gunzipSync(
      fs.readFileSync(
        __dirname +
          "/snapshot/" +
          r.data.url.replace("/wiki/data/", "") +
          ".gz",
      ),
    ),
  );
  assert.equal(shard["enemy:1690"].hp, "6535–7446");
});
test("category pagination and query validation", async () => {
  const { query } = setup();
  const r = await query({
    kind: "battle_npc",
    group: "道场弟子",
    offset: 190,
    limit: 100,
  });
  assert.equal(r.data.total, 200);
  assert.equal(r.data.entries.length, 10);
  assert((await query({ kind: "../../quests" })).error);
  assert((await query({ q: "x".repeat(257) })).error);
});
