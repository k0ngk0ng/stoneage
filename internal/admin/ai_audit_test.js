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

function renderedText(root) {
  const nodes = [];
  const visit = node => { nodes.push(node); node.children.forEach(visit); };
  visit(root);
  return nodes.map(node => node.textContent).join("\n");
}

let recoveryRoot = new Element("div");
audit.renderRecovery(recoveryRoot, {
  runtime: {state: "paused", message: "checkpoint_recovery_required"}
}, {
  recovery: {
    attempt_id: "attempt-unknown",
    attempt_updated_at: "2026-09-17T12:00:00Z",
    reserved_tokens: 17,
    ready: false,
    execution: {container_stopped: false}
  }
});
let recoveryText = renderedText(recoveryRoot);
assert.match(recoveryText, /系统因上一轮模型回合结果未知而自动暂停/);
assert.match(recoveryText, /遗留容器[\s\S]*尚未退出/);
assert.match(recoveryText, /保持玩家停止，等待遗留模型容器退出后重新打开恢复确认/);

recoveryRoot = new Element("div");
audit.renderRecovery(recoveryRoot, {
  runtime: {state: "paused", message: "unresolved model turn requires recovery"}
}, {
  recovery: {
    attempt_id: "attempt-ready",
    attempt_updated_at: "2026-09-17T12:01:00Z",
    reserved_tokens: 19,
    ready: true,
    execution: {container_stopped: true}
  }
});
recoveryText = renderedText(recoveryRoot);
assert.match(recoveryText, /遗留容器[\s\S]*已退出/);
assert.match(recoveryText, /可以在核对后确认/);
assert.match(recoveryText, /玩家仍保持停止，需要手动启动新轮次/);

recoveryRoot = new Element("div");
audit.renderRecovery(recoveryRoot, {runtime: {state: "running"}}, {recovery: null});
assert.match(renderedText(recoveryRoot), /没有待确认的异常回合/);

const observationRoot = new Element("div");
audit.renderObservation(observationRoot, [
  {
    kind: "game.observation",
    created_at: "2026-09-17T12:04:00Z",
    detail: {kind: "character.level", subject: "character-1", content: {level: true}}
  },
  {
    kind: "memory.confirmed",
    created_at: "2026-09-17T12:03:30Z",
    detail: {kind: "character.level", subject: "character-1", content: {level: 99}}
  },
  {
    kind: "game.observation",
    created_at: "2026-09-17T12:03:00Z",
    detail: {kind: "party.snapshot", subject: "party", content: {members: [
      {id: "party-1", name: "队友", level: 10, hp: 20, max_hp: 30},
      {id: "party-2", name: "未知 HP", level: true, hp: null, max_hp: false}
    ]}}
  },
  {
    kind: "game.observation",
    created_at: "2026-09-17T12:02:00Z",
    detail: {kind: "pet.level", subject: "pet-1", content: {level: 8}}
  },
  {
    kind: "game.observation",
    created_at: "2026-09-17T12:01:00Z",
    detail: {kind: "pet.level", subject: "pet-1", content: {level: 7}}
  },
  {
    kind: "game.observation",
    created_at: "2026-09-17T12:00:00Z",
    detail: {kind: "character.level", subject: "character-1", content: {level: 11}}
  }
]);
const observationText = renderedText(observationRoot);
assert.match(observationText, /角色等级\s+11 级 · 最近观测/);
assert.match(observationText, /宠物等级[\s\S]*8 级/);
assert.doesNotMatch(observationText, /7 级/);
assert.match(observationText, /队伍 HP[\s\S]*角色自身 HP 没有单独记录/);
assert.match(observationText, /角色位置[\s\S]*无记录/);
assert.match(observationText, /战斗状态[\s\S]*无记录/);
assert.match(observationText, /不是实时状态/);
assert.match(observationText, /2026/);
assert.doesNotMatch(observationText, /99 级/);
assert.doesNotMatch(observationText, /(?:^|[^\d])0 级/);
assert.doesNotMatch(observationText, /HP 0\/0/);

console.log("admin AI audit timeline tests passed");
