"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");

const template = fs.readFileSync(__dirname + "/templates/ai_profiles.html", "utf8");
const source = fs.readFileSync(__dirname + "/static/ai.js", "utf8");

// Creation recovery remains a server-side idempotency facility. It is not a
// second draft workflow in the profile page and must not be presented as an
// unfinished player or expose a recovery button in the browser.
assert.doesNotMatch(template, /ai-initializations-card|ai-initializations-list|ai-initializations-refresh/);
assert.doesNotMatch(source, /\/api\/ai\/initializations/);
assert.doesNotMatch(source, /ai-initialization-recover|恢复发布/);
console.log("AI initial-recovery journal remains backend-only");
