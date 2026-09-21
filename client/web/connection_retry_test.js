"use strict";
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");
const html = fs.readFileSync(__dirname + "/runtimeassets/index.html", "utf8");
function section(start, end) {
  const a = html.indexOf(start), b = html.indexOf(end, a);
  assert(a >= 0 && b > a);
  return html.slice(a, b);
}
async function settle() {for (let i = 0; i < 20; i++) await Promise.resolve();}
function harness(connect = async () => {throw new Error("network unavailable");}) {
  const timers = new Map(), dialogs = [], attempts = [], waits = [], inputs = {account: {}, password: {}};
  let now = 0, next = 0, visible = "world";
  const oldTransport = {id: "old"};
  const app = {phase: "world", account: "test", password: "secret", transport: oldTransport, connectionToken: 1};
  const context = {
    app, soundState: {}, connectionRetry: {active: false, attempt: 0, generation: 0, timer: 0, timeout: 0},
    worldScreen: {classList: {contains: () => visible !== "world"}},
    window: {
      setTimeout(fn, ms) {const id = ++next;timers.set(id, {fn, at: now + ms});return id;},
      clearTimeout(id) {timers.delete(id);},
    },
    $: id => inputs[id], show: () => {visible = "world";},
    openLocalDialog: (title, message, buttons) => dialogs.push({title, message, buttons}), closeLocalDialog() {},
    openServerSelection() {app.phase = "connecting";visible = "server";},
    closeTransport(expected, options) {
      if (!expected || app.transport === expected) {app.transport = null;app.connectionToken++;}
      waits.push(options);return Promise.resolve(true);
    },
    async connectLogin() {attempts.push(now);app.transport = {id: `new-${attempts.length}`};await connect(context);},
    returnToLogin() {context.cancelConnectionRetry();context.closeTransport();app.phase = "login";},
    stopSoundEffects() {}, stopBackgroundMusic() {}, setWorldState() {}, status() {},
    returnToAccountLogin() {app.phase = "login";}, loginStatus() {},
    connectionFailureMessage: error => String(error),
  };
  vm.createContext(context);
  vm.runInContext(section("  const CONNECTION_RETRY_DELAYS=", "  async function connectLogin("), context);
  vm.runInContext(section("  function showConnectionFailure(", "  const CHAR_LIST_LOCK_RETRY_DELAY_MS="), context);
  return {
    context, app, oldTransport, timers, dialogs, attempts, waits,
    async nextTimer() {
      const [id, timer] = [...timers].sort((a, b) => a[1].at - b[1].at)[0] || [];
      assert(timer, "expected pending retry timer");timers.delete(id);now = timer.at;timer.fn();await settle();
    },
  };
}
(async () => {
  const failed = harness();
  failed.context.handleConnectionLost("TCP closed", failed.oldTransport, 1);
  assert.match(failed.dialogs.at(-1).message, /原因：TCP closed/);
  assert.equal(failed.app.connectionDiagnostics[0].message, "TCP closed");
  for (let i = 0; i < 3; i++) await failed.nextTimer();
  assert.deepEqual(failed.attempts, [1000, 3000, 7000]);
  assert.equal(failed.timers.size, 0);
  assert.match(failed.dialogs.at(-1).message, /3 次未成功/);
  assert.match(failed.dialogs.at(-1).message, /原因：TCP closed/);
  assert(failed.waits.every(options => options?.waitForPeer), "close old session before replacing it");

  const timeout = harness(async () => {});
  timeout.context.retryAfterDisconnect();
  await timeout.nextTimer();
  await timeout.nextTimer(); // Connected but no CharList within 15 seconds.
  await timeout.nextTimer();
  assert.equal(timeout.attempts.length, 2);
  timeout.context.cancelConnectionRetry();
  assert.equal(timeout.timers.size, 0);

  const cancelled = harness();
  cancelled.context.retryAfterDisconnect();
  cancelled.dialogs.at(-1).buttons[0].handler();
  await settle();
  assert.equal(cancelled.app.phase, "login");
  assert.equal(cancelled.timers.size, 0);
  assert.equal(cancelled.attempts.length, 0);

  const inFlight = harness(async () => {});
  inFlight.context.retryAfterDisconnect();await inFlight.nextTimer();
  const backHandler = section('  $("server-back").addEventListener("click",()=>{', '  /* LOGIN.CPP::inputIdPassword()');
  inFlight.context.$ = () => ({focus() {}, addEventListener(name, handler) {handler();}});
  vm.runInContext(backHandler, inFlight.context);
  assert.equal(inFlight.app.phase, "login");
  assert.equal(inFlight.app.transport, null, "cancel during reconnect closes the pending session");
  assert.equal(inFlight.timers.size, 0);

  const denied = harness(async context => {context.showConnectionFailure("密码不正确");});
  denied.context.retryAfterDisconnect();await denied.nextTimer();
  assert.equal(denied.timers.size, 0, "authentication refusal must stop the retry budget");
  assert.equal(denied.attempts.length, 1);

  const dropped = harness(async () => {});
  dropped.context.retryAfterDisconnect();await dropped.nextTimer();
  dropped.context.handleConnectionLost("closed during login", dropped.app.transport, dropped.app.connectionToken);
  await dropped.nextTimer();
  assert.deepEqual(dropped.attempts, [1000, 3000], "disconnect during login must consume the same budget");
  dropped.context.cancelConnectionRetry();

  const stale = harness(async () => {});
  stale.context.retryAfterDisconnect();await stale.nextTimer();
  const replacement = stale.app.transport;
  stale.context.handleConnectionLost("late old poll", stale.oldTransport, 1);
  assert.equal(stale.app.transport, replacement, "old poll cannot close replacement connection");
  assert.equal(stale.attempts.length, 1);
  assert.equal(stale.app.connectionDiagnostics, undefined, "stale failures cannot overwrite current diagnostics");
  stale.context.cancelConnectionRetry();

  const lateWrite = harness();
  lateWrite.context.Protocol = {CLIENT_FIELDS: {W: []}, packetMessage: () => new Uint8Array(1)};
  lateWrite.context.addWire = () => {};
  lateWrite.context.addEvent = () => {};
  lateWrite.app.messageID = 1;
  let rejectWrite;
  lateWrite.app.transport.send = () => new Promise((resolve, reject) => {rejectWrite = reject;});
  vm.runInContext(section("  function send(functionName,values)", "  function decodeValue("), lateWrite.context);
  vm.runInContext(section("  function reportError(", "  function textOr("), lateWrite.context);
  const write = lateWrite.context.send("W", []).catch(error => error);
  const newTransport = {id: "replacement"};lateWrite.app.transport = newTransport;lateWrite.app.connectionToken++;
  rejectWrite(new Error("Failed to fetch"));
  lateWrite.context.reportError(await write);
  assert.equal(lateWrite.app.transport, newTransport, "late write failure cannot disconnect a new login");
  assert.equal(lateWrite.timers.size, 0);

  const logout = harness();logout.app.logoutPending = "in-place";
  logout.context.handleConnectionLost("expected close", logout.oldTransport, 1);
  assert.equal(logout.timers.size, 0, "intentional logout must not reconnect");
  assert.equal(logout.app.connectionDiagnostics, undefined, "intentional logout is not an error");
  const recordLogout = harness();recordLogout.app.logoutPending = "record-point";
  recordLogout.context.handleConnectionLost("closed during logout", recordLogout.oldTransport, 1);
  assert.equal(recordLogout.app.phase, "login");
  assert.equal(recordLogout.timers.size, 0, "record-point logout must not reconnect");
  assert.match(section("  function receiveCharacterList(", "  function escapeHTML("), /cancelConnectionRetry\(\)/);
  require("node:child_process").execFileSync(process.execPath, [__dirname + "/event_recovery_test.js"], {stdio: "inherit"});
  console.log("bounded connection retry tests passed");
})().catch(error => {console.error(error);process.exitCode = 1;});
