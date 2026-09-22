"use strict";
const $ = (id) => document.getElementById(id);
const el = (tag, text, cls) => {
  const n = document.createElement(tag);
  if (text != null) n.textContent = text;
  if (cls) n.className = cls;
  return n;
};
let sequence = 0,
  detailSequence = 0,
  controller,
  labels = {};
function state() {
  const p = new URLSearchParams(location.hash.slice(1));
  return {
    kind: p.get("kind") || "",
    q: p.get("q") || "",
    offset: Math.max(0, Number(p.get("offset")) || 0),
    entry: p.get("entry") || "",
  };
}
function navigate(changes) {
  const s = { ...state(), ...changes };
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(s)) if (v) p.set(k, String(v));
  location.hash = p.toString();
}
// Search runs in a worker against one prebuilt static index. No query text is
// sent to the server. Only the most recently viewed detail shard is retained.
let requestID = 0,
  shardCache;
const pending = new Map();
let searchWorker;
function ensureWorker() {
  if (searchWorker) return;
  searchWorker = new Worker("/wiki/search-worker.js");

  searchWorker.onmessage = ({ data }) => {
    const task = pending.get(data.id);
    if (!task) return;
    pending.delete(data.id);
    task.cleanup();
    if (data.error) task.reject(new Error(data.error));
    else task.resolve(data.data);
  };
  searchWorker.onerror = () => {
    for (const task of pending.values()) {
      task.cleanup();
      task.reject(new Error("本地搜索无法启动，请刷新页面"));
    }
    pending.clear();
  };
}
function localLookup(params, signal) {
  ensureWorker();
  return new Promise((resolve, reject) => {
    const id = ++requestID;
    const abort = () => {
      pending.delete(id);
      reject(Object.assign(new Error("已取消"), { name: "AbortError" }));
    };
    if (signal?.aborted) {
      abort();
      return;
    }
    const cleanup = () => signal?.removeEventListener("abort", abort);
    pending.set(id, { resolve, reject, cleanup });
    signal?.addEventListener("abort", abort, { once: true });
    searchWorker.postMessage({ id, params });
  });
}
async function request(params, signal) {
  const result = await localLookup(params, signal);
  if (!params.entry) return result;
  if (!shardCache || shardCache.url !== result.url) {
    const url = result.url;
    const promise = fetch(url).then((r) => {
      if (!r.ok) throw new Error("详情读取失败，请刷新页面后重试");
      return r.json();
    });
    shardCache = { url, promise };
    promise.catch(() => {
      if (shardCache?.promise === promise) shardCache = null;
    });
  }
  const shard = await shardCache.promise;
  if (!shard[params.entry]) throw new Error("没有找到该条目");
  return shard[params.entry];
}

let directoryPromise;
function directory() {
  if (!directoryPromise)
    directoryPromise = fetch("/wiki/data/catalog.json", { cache: "no-cache" })
      .then((r) => {
        if (!r.ok) throw new Error("目录读取失败");
        return r.json();
      })
      .then((d) => ({ ...d, directory: true }))
      .catch((e) => {
        directoryPromise = null;
        throw e;
      });
  return directoryPromise;
}
function btn(text, fn, cls) {
  const b = el("button", text, cls);
  b.type = "button";
  b.onclick = fn;
  return b;
}
async function render() {
  const s = state(),
    seq = ++sequence;
  controller?.abort();
  controller = new AbortController();
  $("query").value = s.q;
  $("status").textContent = "正在读取资料…";
  $("results").replaceChildren();
  $("pagination").replaceChildren();
  if (s.entry) openDetail(s.entry);
  else {
    detailSequence++;
    $("detail").close();
  }
  try {
    const d =
      s.kind || s.q
        ? await request(
            { kind: s.kind, q: s.q, offset: s.offset, limit: 40 },
            controller.signal,
          )
        : await directory();
    if (seq !== sequence) return;
    labels = Object.fromEntries(d.categories.map((c) => [c.id, c.name]));
    $("categories").replaceChildren(
      ...[
        {
          id: "",
          name: "百科目录",
          count: d.categories.reduce((n, c) => n + c.count, 0),
        },
        ...d.categories,
      ].map((c) => {
        const b = btn(
          c.name,
          () =>
            navigate({ kind: c.id, q: c.id ? s.q : "", offset: 0, entry: "" }),
          c.id === s.kind ? "active" : "",
        );
        b.append(el("span", c.count.toLocaleString()));
        if (c.id === s.kind) b.setAttribute("aria-current", "page");
        return b;
      }),
    );
    if (d.directory) {
      $("status").textContent = "百科目录 · 选择分类开始查阅";
      $("results").replaceChildren(
        ...d.categories.map((c) => {
          const b = btn(
            "",
            () =>
              navigate({
                kind: c.id,
                q: c.id ? s.q : "",
                offset: 0,
                entry: "",
              }),
            "card directory-card",
          );
          b.append(
            el("h2", c.name),
            el("p", c.count.toLocaleString() + " 条资料"),
            el("span", "进入分类 →", "metrics"),
          );
          return b;
        }),
      );
      $("pagination").replaceChildren();
      $("notes").replaceChildren(...d.notes.map((n) => el("p", n)));
      return;
    }
    $("status").textContent =
      `${labels[s.kind] || "全部资料"} · ${d.total.toLocaleString()} 条${s.q ? " · 搜索「" + s.q + "」" : ""}`;
    $("results").replaceChildren(
      ...d.entries.map((e) => {
        const b = btn("", () => navigate({ entry: e.key }), "card");
        b.append(
          el("span", e.group || labels[e.kind], "badge"),
          el("h2", e.name),
          el("p", e.description),
        );
        if (e.location) b.append(el("p", "位置 · " + e.location));
        if (e.hp || e.level)
          b.append(
            el(
              "p",
              [e.level && "等级 " + e.level, e.hp && "HP " + e.hp]
                .filter(Boolean)
                .join("　"),
              "metrics",
            ),
          );
        if (e.skills) b.append(el("p", e.skills, "skills"));
        return b;
      }),
    );
    if (!d.entries.length)
      $("results").append(el("p", "没有匹配的资料，请尝试其他名称或编号。"));
    const prev = btn("← 上一页", () =>
      navigate({ offset: Math.max(0, s.offset - 40) }),
    );
    prev.disabled = s.offset === 0;
    const next = btn("下一页 →", () => navigate({ offset: s.offset + 40 }));
    next.disabled = s.offset + 40 >= d.total;
    $("pagination").replaceChildren(
      prev,
      el(
        "span",
        `${Math.floor(s.offset / 40) + 1} / ${Math.max(1, Math.ceil(d.total / 40))}`,
      ),
      next,
    );
    $("notes").replaceChildren(...d.notes.map((n) => el("p", n)));
  } catch (e) {
    if (e.name !== "AbortError" && seq === sequence)
      $("status").textContent = e.message;
  }
}
async function openDetail(id) {
  const seq = ++detailSequence;
  $("article").replaceChildren(el("p", "正在读取详情…"));
  if (!$("detail").open) $("detail").showModal();
  try {
    const e = await request({ entry: id });
    if (seq !== detailSequence) return;
    const a = $("article");
    a.replaceChildren(
      el("p", e.group || labels[e.kind] || e.kind, "eyebrow"),
      el("h2", e.name),
      el("p", e.description),
    );
    const dl = el("dl");
    for (const f of e.fields || [])
      dl.append(el("dt", f.label), el("dd", f.value));
    a.append(dl);
    if (e.notes?.length) {
      const n = el("div", null, "notice");
      for (const note of e.notes) n.append(el("p", note));
      a.append(n);
    }
    for (const t of e.tables || []) {
      a.append(el("h3", t.title));
      const wrap = el("div", null, "table-wrap"),
        table = el("table"),
        head = el("thead"),
        tr = el("tr"),
        body = el("tbody");
      for (const c of t.columns) tr.append(el("th", c));
      head.append(tr);
      for (const row of t.rows || []) {
        const tr = el("tr");
        for (const cell of row) tr.append(el("td", cell));
        body.append(tr);
      }
      table.append(head, body);
      wrap.append(table);
      a.append(wrap);
    }
    if (e.links?.length) {
      a.append(el("h3", "相关资料与参考来源"));
      const links = el("div", null, "links");
      for (const l of e.links) {
        const link = el("a", l.name);
        if (l.key) {
          const p = new URLSearchParams(location.hash.slice(1));
          p.set("entry", l.key);
          link.href = "#" + p;
        } else {
          try {
            const u = new URL(l.url);
            if (u.protocol !== "https:") continue;
            link.href = u.href;
            link.target = "_blank";
            link.rel = "noopener noreferrer";
          } catch {
            continue;
          }
        }
        links.append(link);
      }
      a.append(links);
    }
    a.append(el("h3", "资料出处"), el("pre", (e.sources || []).join("\n")));
    $("detail").scrollTop = 0;
  } catch (e) {
    if (seq === detailSequence)
      $("article").replaceChildren(el("p", e.message));
  }
}
$("search").onsubmit = (e) => {
  e.preventDefault();
  navigate({ q: $("query").value.trim(), offset: 0, entry: "" });
};
$("close").onclick = () => navigate({ entry: "" });
$("detail").addEventListener("cancel", (e) => {
  e.preventDefault();
  navigate({ entry: "" });
});
window.addEventListener("hashchange", render);
render();
