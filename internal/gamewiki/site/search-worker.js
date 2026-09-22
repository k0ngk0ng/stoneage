"use strict";
let indexPromise, indexScope, catalogPromise;
function catalog() {
  if (!catalogPromise)
    catalogPromise = fetch("/wiki/data/catalog.json", { cache: "no-cache" })
      .then((r) => {
        if (!r.ok) throw new Error("目录读取失败");
        return r.json();
      })
      .catch((e) => {
        catalogPromise = null;
        throw e;
      });
  return catalogPromise;
}
function index(kind) {
  const scope = kind || "all";
  if (!indexPromise || indexScope !== scope) {
    indexScope = scope;
    const promise = catalog()
      .then((c) => {
        if (kind && !c.categories.some((x) => x.id === kind))
          throw new Error("没有找到该分类");
        return fetch(
          "/wiki/data/" +
            c.revision +
            "/" +
            (kind ? "category-" + kind : "index") +
            ".json",
          { cache: "default" },
        );
      })
      .then((r) => {
        if (!r.ok) throw new Error("搜索索引读取失败，请刷新后重试");
        return r.json();
      });
    indexPromise = promise;
    promise.catch(() => {
      if (indexPromise === promise) indexPromise = null;
    });
  }
  return indexPromise;
}
function summary(r) {
  return {
    key: r[0],
    kind: r[1],
    name: r[2],
    description: r[3],
    location: r[4],
    level: r[5],
    hp: r[6],
    skills: r[7],
    group: r[8],
  };
}
self.onmessage = async ({ data: { id, params } }) => {
  try {
    if (params.entry) {
      const catalogData = await catalog();
      const digest = new Uint8Array(
        await crypto.subtle.digest(
          "SHA-256",
          new TextEncoder().encode(params.entry),
        ),
      );
      const shard = digest[0].toString(16).padStart(2, "0");
      self.postMessage({
        id,
        data: { url: `/wiki/data/${catalogData.revision}/${shard}.json` },
      });
      return;
    }
    const d = await index(params.kind || "");
    const q = (params.q || "").trim().toLowerCase(),
      kind = params.kind || "",
      group = params.group || "";
    if (q.length > 256) throw new Error("搜索内容过长");
    const offset = Math.max(0, Number(params.offset) || 0),
      limit = Math.min(100, Math.max(1, Number(params.limit) || 40));
    const entries = [];
    let total = 0;
    for (const row of d.rows) {
      if (
        (kind && row[1] !== kind) ||
        (group && row[8] !== group) ||
        (q && !row[9].includes(q))
      )
        continue;
      if (total >= offset && entries.length < limit) entries.push(summary(row));
      total++;
    }
    self.postMessage({
      id,
      data: {
        entries,
        total,
        offset,
        limit,
        categories: d.categories,
        notes: d.notes,
        revision: d.revision,
      },
    });
  } catch (e) {
    self.postMessage({ id, error: e.message });
  }
};
