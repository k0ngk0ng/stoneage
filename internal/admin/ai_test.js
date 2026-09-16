"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");

const template = fs.readFileSync(__dirname + "/templates/ai_profiles.html", "utf8");
const source = fs.readFileSync(__dirname + "/static/ai.js", "utf8");

// New provisioning is deliberately a names-only request. The server maps
// those names to the hash-pinned native catalog, so this page must not expose
// the former hand-written JSON/function schema editor.
assert.match(template, /name="skill_names"/);
assert.match(template, /value="\{\{\.Name\}\}"/);
assert.match(template, /eq \.Name "stoneage-play"\}\} checked/);
assert.match(template, /\{\{\.Usage\}\}/);
assert.match(template, /v\{\{\.Version\}\}/);
assert.match(template, /name="initial_mount"[^>]*checked/);
assert.match(template, /name="initial_pet_max"[^>]*value="10"/);
assert.doesNotMatch(template, /skills_json|function schema|parameters/);
assert.match(source, /selectedSkillNames\(\)/);
assert.match(source, /skill_names: selectedSkillNames\(\)/);
assert.match(source, /accountUsername\.readOnly = true/);
assert.doesNotMatch(source, /skills_json|parseSkills\(|parameters/);

void (async function () {
class Element {
  constructor() {
    this.dataset = {};
    this.listeners = new Map();
    this.hidden = false;
    this.disabled = false;
    this.readOnly = false;
    this.checked = false;
    this._value = "";
    this.textContent = "";
    this.classList = {toggle() {}};
  }
  get value() { return this._value; }
  set value(value) { this._value = value == null ? "" : String(value); }
  addEventListener(type, listener) {
    if (!this.listeners.has(type)) this.listeners.set(type, []);
    this.listeners.get(type).push(listener);
  }
  async dispatch(type, event) {
    const current = event || {};
    current.type = type;
    let result;
    for (const listener of this.listeners.get(type) || []) result = listener(current);
    return result;
  }
  focus() {}
  scrollIntoView() {}
}

const profileRoot = new Element();
profileRoot.dataset = {canWrite: "true", provisioning: "true", csrf: "csrf-test"};
const editor = new Element();
const title = new Element();
const saveButton = new Element();
const newButton = new Element();
const editButton = new Element();
editButton.dataset.id = "existing-ai";
editButton.classList.contains = () => false;
const existingProfile = {
  id: "existing-ai", version: 7, account: {id: "acct", username: "user"}, character: {id: "hero", name: "Hero"},
  personality: {name: "谨慎", prompt: "careful", traits: ["patient"], values: {risk: "low"}},
  goal: {kind: "leveling", description: "照顾指定宠物", target_level: 80, target_character_id: "stable-pet-42", stop_when_completed: false,
    character_build: {weights: {vital: 0, strength: 1, toughness: 0, dexterity: 1}, reserve_points: 2},
    metadata: {target_kind: "pet", supply_item: "reviewed-meat", stat_policy: "vital"}}, skills: []
};
const controls = {};
for (const name of [
  "id", "expected_version", "account_id", "account_username", "character_id", "character_name",
  "character_slot", "model_config_id", "status", "daily_token_budget", "external_spend_limit",
  "personality_name", "personality_prompt", "goal_kind", "target_level", "goal_description", "unlimited_funds",
  "target_kind", "target_character_id", "stop_when_completed", "character_build_enabled",
  "build_vital", "build_strength", "build_toughness", "build_dexterity", "build_reserve_points", "life_decision_interval", "initial_mount"
]) controls[name] = new Element();
controls.character_slot.value = "0";
controls.status.value = "stopped";
controls.daily_token_budget.value = "100000";
controls.external_spend_limit.value = "0";
controls.unlimited_funds.checked = true;
controls.initial_mount.checked = true;
const skillInputs = [
  Object.assign(new Element(), {value: "stoneage-play", checked: true, dataset: {version: "1.0.0"}}),
  Object.assign(new Element(), {value: "stoneage-leveling", checked: false, dataset: {version: "1.0.0"}})
];
const form = new Element();
form.elements = {namedItem(name) { return controls[name]; }};
form.querySelectorAll = selector => selector.includes(":checked") ? skillInputs.filter(input => input.checked) : skillInputs;
form.reset = () => {
  skillInputs.forEach(input => { input.checked = input.value === "stoneage-play"; input.disabled = false; });
};
const labels = {
  "ai-account-id-field": new Element(), "ai-account-username-field": new Element(), "ai-character-id-field": new Element()
};
const nodes = {
  "ai-profile-admin": profileRoot, "ai-profile-editor": editor, "ai-profile-form": form,
  "ai-profile-editor-title": title, "ai-profile-save": saveButton, "ai-profile-new": newButton
};
const requests = [];
const context = {
  document: {
    getElementById(id) { return nodes[id] || labels[id] || null; },
    querySelectorAll(selector) { return selector === ".ai-edit-profile, .ai-view-profile" ? [editButton] : []; }
  },
  fetch: async (url, options) => {
    requests.push({url, options});
    return {ok: true, async json() { return options.method === "GET" ? {profile: existingProfile} : {}; }};
  },
  window: {location: {reload() {}}}
};
vm.runInNewContext(source, context, {filename: "internal/admin/static/ai.js"});
newButton.dispatch("click");
controls.character_name.value = "新角色";
await form.dispatch("submit", {preventDefault() {}});
assert.equal(requests.length, 1);
assert.equal(requests[0].url, "/api/ai/profiles/provision");
const provisionBody = JSON.parse(requests[0].options.body);
assert.deepEqual(provisionBody.skill_names, ["stoneage-play"]);
assert.equal(provisionBody.skills, undefined);
assert.deepEqual(provisionBody.initial_state, {mode: "birth", mount: true});
assert.equal(provisionBody.account_id, undefined);
assert.equal(provisionBody.account_username, undefined);
assert.equal(provisionBody.character_id, undefined);
assert.equal(controls.account_username.readOnly, true);
assert.equal(labels["ai-account-id-field"].hidden, true);
assert.equal(labels["ai-account-username-field"].hidden, true);
assert.equal(labels["ai-character-id-field"].hidden, true);
assert.equal(provisionBody.goal.kind, "life");
assert.equal(provisionBody.goal.stop_when_completed, false);
assert.equal(controls.stop_when_completed.checked, false);
assert.equal(controls.stop_when_completed.disabled, true);

await editButton.dispatch("click");
assert.equal(controls.target_kind.value, "pet");
assert.equal(controls.target_character_id.value, "stable-pet-42");
assert.equal(controls.stop_when_completed.checked, false);
assert.equal(controls.character_build_enabled.checked, true);
assert.equal(Number(controls.build_strength.value), 1);
assert.equal(Number(controls.build_reserve_points.value), 2);
controls.personality_prompt.value = "updated style";
await form.dispatch("submit", {preventDefault() {}});
const editedBody = JSON.parse(requests.at(-1).options.body);
assert.equal(requests.at(-1).options.method, "PATCH");
assert.equal(editedBody.initial_state, undefined);
assert.deepEqual(editedBody.personality.traits, ["patient"]);
assert.deepEqual(editedBody.personality.values, {risk: "low"});
assert.equal(editedBody.personality.prompt, "updated style");
assert.deepEqual(editedBody.goal, existingProfile.goal);
controls.target_kind.value = "character";
controls.target_character_id.value = "";
controls.stop_when_completed.checked = true;
await form.dispatch("submit", {preventDefault() {}});
const changedGoal = JSON.parse(requests.at(-1).options.body).goal;
assert.equal(changedGoal.metadata.target_kind, "character");
assert.equal(changedGoal.metadata.supply_item, "reviewed-meat");
assert.equal(changedGoal.target_character_id, "");
assert.equal(changedGoal.stop_when_completed, true);
controls.character_build_enabled.checked = false;
await form.dispatch("submit", {preventDefault() {}});
assert.equal(JSON.parse(requests.at(-1).options.body).goal.character_build, undefined);

controls.goal_kind.value = "life";
await controls.goal_kind.dispatch("change");
assert.equal(controls.life_decision_interval.disabled, false);
assert.equal(controls.stop_when_completed.checked, false);
assert.equal(controls.stop_when_completed.disabled, true);
controls.life_decision_interval.value = "900";
await form.dispatch("submit", {preventDefault() {}});
assert.deepEqual(JSON.parse(requests.at(-1).options.body).goal.life, {decision_interval_seconds: 900});
controls.goal_kind.value = "leveling";
await controls.goal_kind.dispatch("change");
assert.equal(controls.life_decision_interval.disabled, true);
assert.equal(controls.stop_when_completed.disabled, false);
await form.dispatch("submit", {preventDefault() {}});
assert.equal(JSON.parse(requests.at(-1).options.body).goal.life, undefined);

newButton.dispatch("click");
controls.character_name.value = "另一个角色";
await form.dispatch("submit", {preventDefault() {}});
const freshBody = JSON.parse(requests.at(-1).options.body);
assert.equal(freshBody.personality.values, undefined);
assert.deepEqual(freshBody.goal.metadata, {});
assert.equal(freshBody.goal.target_character_id, "");
assert.equal(freshBody.goal.character_build, undefined);
assert.deepEqual(freshBody.goal.life, {decision_interval_seconds: 300});
assert.equal(controls.life_decision_interval.value, "300");

existingProfile.goal = {kind: "life", life: {decision_interval_seconds: 1200, activities: ["idle", "chat"]}, stop_when_completed: false};
await editButton.dispatch("click");
assert.equal(controls.life_decision_interval.value, "1200");
assert.equal(controls.life_decision_interval.disabled, false);
await form.dispatch("submit", {preventDefault() {}});
assert.deepEqual(JSON.parse(requests.at(-1).options.body).goal.life, existingProfile.goal.life);
newButton.dispatch("click");
assert.equal(controls.life_decision_interval.value, "300");
assert.equal(controls.life_decision_interval.disabled, false);

console.log("admin AI native skill form tests passed");
})();
