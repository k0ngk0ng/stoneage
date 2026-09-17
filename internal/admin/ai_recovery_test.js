"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");

const template = fs.readFileSync(__dirname + "/templates/ai_profiles.html", "utf8");
const source = fs.readFileSync(__dirname + "/static/ai.js", "utf8");

// Recovery is now read-only detail data. The profile page must not expose the
// former acknowledgement panel or retain a write path which could be mistaken
// for a way to certify an unknown model result.
assert.doesNotMatch(template, /ai-profile-recovery|ai-recovery-panel|ai-recovery-ack|ai-recovery-submit/);
assert.doesNotMatch(source, /ai-profile-recovery|ai-recovery-panel|ai-recovery-ack|ai-recovery-submit/);
assert.doesNotMatch(source, /\/recovery\"\s*,\s*\"POST\"/);
assert.match(source, /查看面板不会提交操作/);

// Exercise the paused-state mapping directly without duplicating the modal
// request and timer race coverage in ai_profile_view_race_test.js.
const messageStart = source.indexOf("  function aiRuntimeMessageText");
const messageEnd = source.indexOf("\n  function aiRecoveryDetailText", messageStart);
assert(messageStart >= 0 && messageEnd > messageStart, "runtime message helper not found");
const runtimeMessage = new Function(
  "aiAuditText",
  source.slice(messageStart, messageEnd) + "\nreturn aiRuntimeMessageText;"
)(value => value === undefined || value === null ? "" : String(value));

const paused = runtimeMessage("checkpoint_recovery_required");
assert.equal(paused, "上一轮执行异常，已暂停；点击启动可重新运行");
assert.match(paused, /点击启动/);
assert.doesNotMatch(paused, /人工恢复确认|勾选|恢复确认/);
assert.equal(runtimeMessage("unrelated status"), "unrelated status");

console.log("AI recovery confirmation UI is removed and paused text points to manual start");
