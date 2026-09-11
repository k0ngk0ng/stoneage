"use strict";
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");
const html = fs.readFileSync(__dirname + "/index.html", "utf8");
function section(start, end) {
  const a = html.indexOf(start), b = html.indexOf(end, a);
  assert(a >= 0 && b > a);
  return html.slice(a, b);
}
const transportSource = section("  class HTTPTransport", "  /* Keep the browser's logical direction");
const pollSource = section("  async function pollLoop(", "  function handlePacket(");
const diagnosticSource = section("  function recordConnectionDiagnostic(", "  function cancelConnectionRetry(");
async function settle() {for (let i = 0; i < 30; i++) await Promise.resolve();}
const ok = data => ({ok: true, json: async () => data});
const batch = (...events) => ({acknowledged: true, events});
function harness(fetch) {
  const timers = new Map(), requests = [], diagnostics = [], disconnected = [];
  let nextTimer = 0;
  const context = {
    AbortController, Date,
    Protocol: {fromBase64: value => value, toBase64: value => value},
    setTimeout(fn, delay) {const id = ++nextTimer;timers.set(id, {fn, delay});return id;},
    clearTimeout(id) {timers.delete(id);},
    fetch: async (url, options) => {requests.push({url, options});return fetch(url, options);},
    app: {connectionToken: 1},
    addEvent: message => diagnostics.push(message), setWorldState() {},
    handleConnectionLost: reason => disconnected.push(reason),
  };
  vm.createContext(context);
  vm.runInContext(transportSource + diagnosticSource + pollSource + "\nglobalThis.Transport=HTTPTransport;", context);
  const transport = new context.Transport();
  transport.id = "test";transport.eventAck = true;
  context.app.transport = transport;
  return {
    context, transport, requests, timers, diagnostics, disconnected,
    async tick(delay) {
      const entry = [...timers].find(([, timer]) => timer.delay === delay);
      assert(entry, `missing ${delay}ms timer`);
      timers.delete(entry[0]);entry[1].fn();await settle();
    },
  };
}
async function run() {
  // A response can be lost after the bridge has read TCP. Repeat the old ACK,
  // process the retained response once, then advance on the next request.
  let calls = 0;
  const replay = harness(async () => {
    if (++calls === 1) throw new TypeError("Failed to fetch");
    return ok(batch({seq: 1, packet: "C"}, {seq: 2, packet: "XYD"}));
  });
  const pending = replay.transport.events();await settle();await replay.tick(250);
  const response = await pending;
  assert.equal(replay.transport.eventCursor, 0, "receiving is not acknowledging processing");
  assert(replay.requests.every(request => request.url.endsWith("&ack=0")));
  response.events.forEach(event => replay.transport.acknowledgeEvent(event));
  await replay.transport.events();
  assert(replay.requests.at(-1).url.endsWith("&ack=2"));
  assert.equal(replay.timers.size, 0);

  for (const eventAck of [true, false]) {
    const capability = harness(async () => ok({id: "created", greeting: "L", event_ack: eventAck}));
    await capability.transport.connect();
    assert.equal(capability.transport.eventAck, eventAck);
  }
  const legacy = harness(async () => {throw new TypeError("Failed to fetch");});
  legacy.transport.eventAck = false;
  await assert.rejects(legacy.transport.events(), /Failed to fetch/);
  assert.equal(legacy.requests.length, 1, "legacy destructive polls must not be retried");
  assert(!legacy.requests[0].url.includes("ack="));

  for (const status of [404, 410, 400]) {
    const missing = harness(async () => ({ok: false, status, text: async () => "session unavailable"}));
    await assert.rejects(missing.transport.events(), /session unavailable/);
    assert.equal(missing.requests.length, 1, `HTTP ${status} is terminal`);
  }
  const exhausted = harness(async () => ({ok: false, status: 502, text: async () => "upstream unavailable"}));
  const failure = exhausted.transport.events().catch(error => error);
  await settle();for (const delay of [250, 750, 1500]) await exhausted.tick(delay);
  assert.match((await failure).message, /upstream unavailable/);
  assert.equal(exhausted.requests.length, 4);
  assert.equal(exhausted.timers.size, 0);

  const conflict = harness(async () => conflict.requests.length === 1
    ? {ok: false, status: 409, text: async () => "another events poll is already pending"}
    : ok(batch()));
  const conflictPoll = conflict.transport.events();await settle();await conflict.tick(250);await conflictPoll;
  assert.equal(conflict.requests.length, 2);

  for (const invalid of [
    {events: []}, batch({seq: 2, packet: "gap"}), batch({seq: -1}), batch({seq: 1}, {seq: 1}),
  ]) {
    const malformed = harness(async () => ok(invalid));
    await assert.rejects(malformed.transport.events());
    assert.equal(malformed.transport.eventCursor, 0);
    assert.equal(malformed.requests.length, 1, "invalid sequence data must not be replayed forever");
  }

  const truncated = harness(async () => truncated.requests.length === 1
    ? {ok: true, json: async () => {throw new SyntaxError("Unexpected end of JSON input");}}
    : ok(batch({seq: 1, packet: "M"})));
  const truncatedPoll = truncated.transport.events();await settle();await truncated.tick(250);
  assert.equal((await truncatedPoll).events[0].packet, "M");

  const timeout = harness(async (_url, options) => timeout.requests.length === 1
    ? new Promise((_resolve, reject) => options.signal.addEventListener("abort", () => {
      const error = new Error("aborted");error.name = "AbortError";reject(error);
    }, {once: true})) : ok(batch()));
  const timeoutPoll = timeout.transport.events();await settle();await timeout.tick(30000);await timeout.tick(250);
  await timeoutPoll;assert.equal(timeout.timers.size, 0);

  for (const duringBackoff of [true, false]) {
    const closing = harness(async (_url, options) => {
      if (options.method === "DELETE") return {ok: true};
      if (duringBackoff) throw new TypeError("Failed to fetch");
      return new Promise((_resolve, reject) => options.signal.addEventListener("abort", () => {
        const error = new Error("aborted");error.name = "AbortError";reject(error);
      }, {once: true}));
    });
    const closingPoll = closing.transport.events();await settle();await closing.transport.close();
    assert.equal((await closingPoll).closed, true);
    assert.equal(closing.timers.size, 0);
    assert.equal(closing.requests.filter(request => !request.options.method).length, 1);
  }

  const writes = harness(async () => {throw new TypeError("Lost POST response");});
  await assert.rejects(writes.transport.send("attack"), /Lost POST response/);
  assert.equal(writes.requests.length, 1, "non-idempotent game commands must never be automatically replayed");

  // Exercise the actual poll loop. A renderer failure must not close the
  // healthy session, lose the next packet or run a replayed event twice.
  const rendering = harness(async () => {
    rendering.context.app.polling = false;
    return ok(batch({seq: 1, packet: "bad-render"}, {seq: 2, packet: "XYD"}));
  });
  const handled = [];
  rendering.context.handlePacket = packet => {handled.push(packet);if (packet === "bad-render") throw new ReferenceError("draw failed");};
  await rendering.context.pollLoop(rendering.transport, 1);
  assert.deepEqual(handled, ["bad-render", "XYD"]);
  assert.equal(rendering.disconnected.length, 0);
  assert.equal(rendering.transport.eventCursor, 2);
  assert.equal(rendering.context.app.transport, rendering.transport);
  assert.match(rendering.context.app.connectionDiagnostics[0].message, /draw failed/);
  await rendering.context.pollLoop(rendering.transport, 1);
  assert.deepEqual(handled, ["bad-render", "XYD"], "replayed acknowledged packets are not applied twice");

  const terminal = harness(async () => ok({...batch({seq: 1, packet: "final"}, {seq: 2, closed: true, error: "HTTP event queue overflow"}), closed: true}));
  let finalPacket = false;terminal.context.handlePacket = () => {finalPacket = true;};
  await terminal.context.pollLoop(terminal.transport, 1);
  assert(finalPacket);
  assert.deepEqual(terminal.disconnected, ["HTTP event queue overflow"]);

  const stale = harness(async () => {
    stale.context.app.transport = {id: "replacement"};stale.context.app.connectionToken++;
    return ok(batch({seq: 1, closed: true}));
  });
  stale.context.handlePacket = () => {throw new Error("stale packet applied");};
  await stale.context.pollLoop(stale.transport, 1);
  assert.equal(stale.disconnected.length, 0);
  console.log("event recovery: ACK replay, bounded retries, cancellation, legacy safety and renderer isolation passed");
}
run().catch(error => {console.error(error);process.exitCode = 1;});
