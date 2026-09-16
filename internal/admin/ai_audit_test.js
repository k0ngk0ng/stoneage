"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");

class Element {
  constructor(tag) {
    this.tagName = tag;
    this.children = [];
    this.textContent = "";
    this.className = "";
  }
  appendChild(child) { this.children.push(child); return child; }
}

const document = {
  getElementById() { return null; },
  createElement(tag) { return new Element(tag); }
};
const context = {window: {}, document, Date, Intl, Number};
vm.runInNewContext(fs.readFileSync(__dirname + "/static/ai.js", "utf8"), context, {
  filename: "internal/admin/static/ai.js"
});

const audit = context.window.StoneAgeAIAudit;
assert(audit && typeof audit.describe === "function");

let view = audit.describe({
  kind: "decision.failed",
  detail: {charged_tokens: 1200, error: "模型请求超时"}
});
assert.equal(view.name, "模型决策失败");
assert.match(view.action, /失败的模型决策/);
assert.match(view.result, /1,200 tokens/);
assert.equal(view.reason, "模型请求超时");

view = audit.describe({
  kind: "supervisor.turn_failed",
  detail: {failure_count: 2, error: "Responses API 返回错误"}
});
assert.equal(view.name, "模型回合失败");
assert.equal(view.reason, "Responses API 返回错误");
assert.match(view.result, /第 2 次失败/);

view = audit.describe({
  kind: "schedule.created",
  detail: {schedule_id: "schedule-1", kind: "pet-care", run_at: "2026-09-17T12:00:00Z", repeat_interval_ns: 300000000000}
});
assert.match(view.action, /pet-care/);
assert.match(view.result, /已创建/);
assert.match(view.result, /每 5 分钟重复/);

view = audit.describe({kind: "checkpoint.saved", detail: {version: 12}});
assert.equal(view.name, "保存运行检查点");
assert.equal(view.action, "保存运行检查点");
assert.equal(view.result, "检查点版本 12 已保存");
assert.equal(view.compact, true);

view = audit.describe({kind: "game.friend_confirmed", detail: {ok: true}});
assert.equal(view.name, "游戏事件（game.friend_confirmed）");
assert.equal(view.action, "系统记录事件类型「game.friend_confirmed」");
assert.equal(view.result, "事件已记录");

view = audit.describe({kind: "schedule.cancelled", detail: {reason: "玩家已停用"}});
assert.equal(view.reasonLabel, "取消原因");
assert.equal(view.reason, "玩家已停用");

view = audit.describe({kind: "supervisor.turn_completed", detail: {next_step: "继续观察游戏"}});
assert.equal(view.next, "继续观察游戏");

const parent = new Element("div");
audit.render(parent, [{
  id: 9,
  kind: "checkpoint.saved",
  actor: "supervisor",
  created_at: "2026-09-17T11:59:00Z",
  detail: {version: 12}
}, {
  id: 10,
  kind: "decision.failed",
  actor: "runtime",
  created_at: "2026-09-17T12:00:00Z",
  detail: {error: "模型请求超时", charged_tokens: 12}
}]);
const all = [];
const visit = node => { all.push(node); node.children.forEach(visit); };
visit(parent);
assert(all.some(node => node.tagName === "details"));
assert(all.some(node => node.textContent === "查看原始 JSON"));
assert(all.some(node => node.tagName === "pre" && node.textContent.includes("decision.failed")));
assert(all.some(node => node.textContent.includes("模型请求超时")));
assert(all.some(node => node.className.includes("ai-audit-event-compact")));

console.log("admin AI audit timeline tests passed");
