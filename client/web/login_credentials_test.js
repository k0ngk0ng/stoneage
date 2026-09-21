"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const root = path.resolve(__dirname, "../..");
const html = fs.readFileSync(path.join(__dirname, "runtimeassets/index.html"), "utf8");
const script = html.match(/<script>([\s\S]*?)<\/script>/)[1];
let error = "";
const inputs = {account: {value: "Probe"}, password: {value: ""}};
let opened = 0;
const context = {
  window: {}, TextEncoder, TextDecoder, console,
  $: name => inputs[name], app: {}, assetState: {},
  loginStatus: message => { error = message; },
  unlockAudio() {}, playSoundEffect() {}, setMusicMode() {}, hydrateAlbumCatalog() {},
  openServerSelection() { opened++; },
};
vm.createContext(context);
vm.runInContext(script.slice(0, script.indexOf("  class HTTPTransport")) + "\n})();", context);
vm.runInContext(script.slice(script.indexOf("  function canonicalAccount("), script.indexOf("  /* Never let a stale DOM class")), context);

const adminPatterns = ["account_new.html", "account_detail.html"].map(name => {
  const template = fs.readFileSync(path.join(root, "internal/admin/templates", name), "utf8");
  const input = template.match(/<input[^>]*name="password"[^>]*>/)[0];
  assert.ok(!input.includes("maxlength="), "admin must report excessive length without truncating");
  return new RegExp("^(?:" + input.match(/pattern="([^"]+)"/)[1] + ")$", "v");
});
for (const id of ["account", "password"]) {
  assert.ok(!html.match(new RegExp('<input id="' + id + '"[^>]*>'))[0].includes("maxlength="));
}

// Use synthetic credentials, including every permitted punctuation byte.
const accepted = ["a", "AbCD@efg7^&H", "a".repeat(12), ...Array.from({length: 94}, (_, i) => String.fromCharCode(33 + i))];
const rejected = ["", "a".repeat(13), "AbCD@efg7^&HIJ", "a".repeat(15), "a".repeat(16), "with space", " leading", "trailing ", "bad\n", "bad\r", "bad\t", "bad\0", "bad\x7f", "中文", "é", "😀"];
const protocol = context.window.StoneAgeProtocol;
for (const password of accepted) {
  inputs.password.value = password;
  const before = opened;
  assert.equal(context.acceptLoginCredentials(), true);
  assert.equal(opened, before + 1);
  assert.equal(context.app.account, "probe");
  assert.equal(context.app.password, password, "login must preserve exact password bytes");
  for (const pattern of adminPatterns) assert.equal(pattern.test(password), true);
  assert.equal(Buffer.from(protocol.decodeString(protocol.encodeString(password))).toString(), password);
  const raw = protocol.rawMessage(1, "ClientLogin", ["probe", protocol.encodeString(password)]);
  assert.deepEqual(Buffer.from(protocol.decodePacket(protocol.encodePacket(raw))), Buffer.from(raw));
}
for (const password of rejected) {
  inputs.password.value = password;
  error = "";
  const before = opened;
  assert.equal(context.acceptLoginCredentials(), false);
  assert.equal(opened, before, "invalid credentials must not proceed to server selection");
  assert.ok(error);
  assert.equal(inputs.password.value, password, "invalid input must remain intact for correction");
  // HTML pattern matches the entire value (unlike JavaScript's trailing-newline $ behavior).
  for (const pattern of adminPatterns) assert.equal(pattern.exec(password)?.[0] === password, false);
}
for (const account of ["a".repeat(16), "bad@name", "bad name", "中文"]) {
  assert.equal(context.validateLoginCredentials(account, "valid"), false);
}
assert.match(script.slice(script.indexOf("  async function connectLogin("), script.indexOf("  async function connectLogin(") + 1100), /validateLoginCredentials\(account,password\)/);
console.log("Login credential validation and protocol round trips passed.");
