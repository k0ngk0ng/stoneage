const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');

const source = fs.readFileSync(path.join(__dirname, 'automation.js'), 'utf8');

function button() {
  return {
    textContent: '',
    disabled: false,
    toggleAttribute(name, value) {
      if (name === 'disabled') this.disabled = Boolean(value);
    },
  };
}

function fixture(control = { mode: 'manual', generation: 7 }, seek = false) {
  const calls = [];
  const transport = {
    id: 'session-a', base: '', closed: false,
    control: { control },
    setControl(value) { this.control = value; return value; },
  };
  const app = { transport, systemSettings: {}, phase: 'world' };
  const root = { StoneAgeWebClient: { app } };
  const status = { textContent: '', classList: { toggle() {} } };
  const nodes = {
    '#ai-automation-mode': { value: 'battle' },
    '#ai-leveling-seek': { checked: seek },
    '#ai-build-section': button(),
    '#ai-supply-section': button(),
    '#ai-include-dependencies': button(),
    '#ai-start': button(),
    '#ai-pause': button(),
    '#ai-resume': button(),
    '#ai-takeover': button(),
    '#ai-control-status': status,
  };
  const panel = { querySelector: id => nodes[id] || {}, classList: { toggle() {} } };
  const locked = new Map();
  vm.runInNewContext(source, {
    window: root,
    fetch: async (url, options) => {
      calls.push({ url, options });
      if (url.endsWith('/control')) return { ok: true, json: async () => ({ control, automation_available: control.available !== false }) };
      return { ok: true, json: async () => ({ control: { mode: 'battle', generation: 8, reason: '自动战斗中' }, automation_active: true, automation_mode: 'battle', automation_available: true }) };
    },
  });
  /* The page is supplied after the module loads, exactly as the other panel
     fixtures do: the module publishes its API before it touches the document,
     so this exercises the control path without a full page. */
  root.document = {
    body: { classList: { toggle(name, value) { locked.set(name, Boolean(value)); } }, dataset: {} },
    getElementById: id => id === 'ai-control-panel' ? panel : id === 'ai-control-status' ? status : {},
    createElement: () => ({ dataset: {}, classList: { toggle() {} }, setAttribute() {} }),
  };
  return { api: root.StoneAgeAutomation, app, calls, nodes, locked };
}

/* Auto battle takes the command bar and nothing else. Locking the whole page
   the way the executor modes do cost the player the result screen as well:
   with everything intercepted, 返回世界 only worked after handing control back. */
function lockedPageFixture() {
  const listeners = [];
  const control = { mode: 'battle', generation: 8 };
  const transport = { id: 'session-a', base: '', closed: false, control: { control }, setControl(value) { return value; } };
  const app = { transport, systemSettings: {}, petSlots: [], phase: 'world' };
  const root = { StoneAgeWebClient: { app } };
  class Element {}
  /* closest() only has to honour the allowlist: a target matches when the
     selector the module passes literally names it. */
  class FakeElement extends Element {
    constructor(token) { super(); this.token = token; }
    closest(selector) { return this.token && String(selector).includes(this.token) ? this : null; }
  }
  /* The panel already exists in this fixture, so ensurePanel() returns it
     instead of building one; this test only needs the document listener. */
  const element = {
    dataset: {}, textContent: '', hidden: false,
    classList: { toggle() {} }, setAttribute() {}, addEventListener() {}, appendChild() {},
    replaceChildren() {}, querySelector: () => null,
  };
  root.document = {
    addEventListener(type, handler, capture) { listeners.push({ type, handler, capture }); },
    body: { classList: { toggle() {} }, dataset: {}, appendChild() {} },
    getElementById: id => id === 'ai-control-panel' ? element : {},
    createElement: () => element,
  };
  vm.runInNewContext(source, { window: root, Element, fetch: async () => ({ ok: true, json: async () => ({}) }) });
  return { api: root.StoneAgeAutomation, listeners, FakeElement };
}

test('auto battle leaves the page clickable except for the battle command bar', () => {
  const f = lockedPageFixture();
  /* The command bar goes away through CSS, not by swallowing clicks, so the
     player keeps the result screen, the panels, the chat and the system menu. */
  assert.match(source, /BATTLE_LOCK_CLASS = "stoneage-ai-battle-locked"/);
  assert.match(source, /body\.\$\{BATTLE_LOCK_CLASS\} #battle-ui/);
  assert.match(source, /body\.\$\{BATTLE_LOCK_CLASS\} #battle-target-overlay/);
  const interceptors = f.listeners.filter(entry => entry.type === 'click' && entry.capture);
  assert.equal(interceptors.length, 1, 'the full lock is one capture-phase click interceptor');
  const intercept = interceptors[0].handler;

  let prevented = 0;
  const resultClose = { target: new f.FakeElement('#battle-result-close'), preventDefault() { prevented++; }, stopImmediatePropagation() { prevented++; } };
  intercept(resultClose);
  const command = { target: new f.FakeElement('#battle-ui'), preventDefault() { prevented++; }, stopImmediatePropagation() { prevented++; } };
  intercept(command);
  assert.equal(prevented, 0, 'auto battle must not intercept clicks at all');

  /* The same interceptor still holds the executor modes shut. */
  f.api.publishControl({ control: { mode: 'leveling', generation: 9 } });
  const gameplay = { target: new f.FakeElement('#world-actions'), preventDefault() { prevented++; }, stopImmediatePropagation() { prevented++; } };
  intercept(gameplay);
  assert.equal(prevented, 2, 'the executor lock still blocks gameplay clicks');

  /* The full lock keeps the result screen usable, too. */
  assert.match(source, /LOCK_ALLOWED = "[^"]*#battle-result-screen/);
  assert.match(source, /target\.closest\(LOCK_ALLOWED\)/);
});

test('auto battle sends only the generation and locks the browser against the running loop', async () => {
  const f = fixture();
  await f.api.start();

  const call = f.calls.find(entry => entry.url.endsWith('/battle-auto'));
  assert.ok(call, 'the panel must start auto battle through its own endpoint');
  assert.equal(call.url, '/api/sessions/session-a/battle-auto');
  assert.equal(call.options.method, 'POST');
  assert.ok(!f.calls.some(entry => entry.url.includes('/automation/start')), 'auto battle carries no task or budget config');
  assert.equal(call.options.body, '{"generation":7,"mode":"battle"}', 'auto battle answers turns and walks nowhere');

  const control = f.api.currentControl();
  assert.equal(control.mode, 'battle');
  assert.equal(control.generation, 8);
  /* The fight runs at full speed, but the page stays the player's: only the
     battle command bar is taken away, so 接管 is not the sole clickable thing. */
  assert.equal(f.locked.get('stoneage-ai-locked'), false);
  assert.equal(f.locked.get('stoneage-ai-battle-locked'), true);
  assert.equal(f.app.systemSettings.battleAnimationSpeed, 10);

  /* The server's pause handler accepts the executor modes only, so the browser
     must not offer a pause the server would refuse; handing control back is
     always available while the loop owns the session. */
  assert.equal(f.nodes['#ai-pause'].disabled, true);
  assert.equal(f.nodes['#ai-takeover'].disabled, false);
  assert.equal(f.nodes['#ai-start'].disabled, true);
  assert.match(f.nodes['#ai-control-status'].textContent, /自动战斗/);
});

test('the panel only follows a walk the loop is actually doing', () => {
  const page = fs.readFileSync(path.join(__dirname, 'index.html'), 'utf8');
  assert.ok(page.includes('"automation_state"'), 'the state must reach the panel');
  const follow = source.slice(source.indexOf('function followAutomationWalk'), source.indexOf('function updateFromTransport'));
  assert.match(follow, /seeking !== true/, 'an answer-only loop must not query the position');
  assert.match(follow, /app\.pendingMove/, 'a query cancels the move the player is making');
});

test('the panel reports what the running loop last decided', async () => {
  const f = fixture();
  f.api.publishControl({
    control: { mode: 'battle', generation: 8, reason: '自动战斗中' },
    automation_active: true,
    automation_mode: 'battle',
    automation_note: 'looking for a fight at (468,523)',
    automation_state: { in_battle: false, battles: 3, wins: 3, losses: 0, seeking: true },
  });
  assert.match(f.nodes['#ai-control-status'].textContent, /上次决策：looking for a fight at \(468,523\)/);

  /* The executor modes have plans and receipts to inspect; the note is for the
     battle loop, and a stale one must not read as if it were still happening. */
  f.api.publishControl({ control: { mode: 'manual', generation: 9 }, automation_note: 'looking for a fight at (468,523)' });
  assert.ok(!/上次决策/.test(f.nodes['#ai-control-status'].textContent));
});

/* "Is it fighting?" is the whole question a player has about a mode they are
   not watching, so the panel answers it in words and in numbers. */
test('the panel says whether the loop is fighting and how it has gone', async () => {
  const f = fixture();
  assert.match(source, /control\.mode === MODE_LEVELING\) \? control\.automationState/, 'leveling runs the same loop');
  f.api.publishControl({
    control: { mode: 'battle', generation: 8, reason: '自动战斗中' },
    automation_active: true,
    automation_mode: 'battle',
    automation_state: { in_battle: true, turn: 4, enemies: 2, battles: 6, wins: 5, losses: 1, seeking: false },
  });
  assert.match(f.nodes['#ai-control-status'].textContent, /状态：战斗中（第 4 回合 · 敌人 2） · 本次 6 场（胜 5 负 1）/);

  f.api.publishControl({
    control: { mode: 'battle', generation: 8 },
    automation_active: true,
    automation_mode: 'battle',
    automation_state: { in_battle: false, turn: 0, enemies: 0, battles: 6, wins: 5, losses: 1, seeking: true },
  });
  assert.match(f.nodes['#ai-control-status'].textContent, /状态：找架打（原地来回走） · 本次 6 场（胜 5 负 1）/);

  /* A loop that cannot walk has to say so; otherwise "nothing is happening"
     and "it is broken" look identical. */
  f.api.publishControl({
    control: { mode: 'battle', generation: 8 },
    automation_active: true,
    automation_mode: 'battle',
    automation_state: { in_battle: false, battles: 6, wins: 5, losses: 1, seeking: true, blocked: 'window' },
  });
  assert.match(f.nodes['#ai-control-status'].textContent, /状态：找架打受阻（有窗口未关，先点掉）/);

  /* Before the first fight there is nothing to count, and the line must not
     claim a record of zero. */
  f.api.publishControl({
    control: { mode: 'battle', generation: 8 },
    automation_active: true,
    automation_mode: 'battle',
    automation_state: { in_battle: false, turning: 0, battles: 0, wins: 0, losses: 0, seeking: false },
  });
  assert.match(f.nodes['#ai-control-status'].textContent, /状态：待机$/m);
});

test('leveling runs the light loop, and only walks when asked', async () => {
  const parked = fixture({ mode: 'manual', generation: 7, available: false }, false);
  parked.nodes['#ai-automation-mode'].value = 'leveling';
  await parked.api.start();
  const call = parked.calls.find(entry => entry.url.endsWith('/battle-auto'));
  assert.ok(call, 'without a task executor leveling must start the light loop');
  assert.equal(call.options.body, '{"generation":7,"mode":"leveling","seek":false}');
  assert.ok(!/来回走/.test(parked.nodes['#ai-control-status'].textContent), 'the status must not promise a walk it will not do');

  const walking = fixture({ mode: 'manual', generation: 7, available: false }, true);
  walking.nodes['#ai-automation-mode'].value = 'leveling';
  await walking.api.start();
  const walkingCall = walking.calls.find(entry => entry.url.endsWith('/battle-auto'));
  assert.equal(walkingCall.options.body, '{"generation":7,"mode":"leveling","seek":true}');
  assert.match(walking.nodes['#ai-control-status'].textContent, /来回走/);

  /* With the executor configured, leveling keeps its full path. */
  const full = fixture({ mode: 'manual', generation: 7, available: true }, true);
  full.nodes['#ai-automation-mode'].value = 'leveling';
  await full.api.start();
  assert.ok(full.calls.some(entry => entry.url.includes('/automation/start')), 'a configured executor keeps its task plan');
});

/* Taking the lease at the login or character screen refuses the page's own
   login packet, so a loop started there locks the page out of its own game. */
test('automation cannot start before the character is in the world', async () => {
  const f = fixture();
  f.app.phase = 'character-list';
  await assert.rejects(() => f.api.start(), /先进入世界/);
  assert.ok(!f.calls.some(entry => entry.url.endsWith('/battle-auto')), 'nothing may be claimed yet');
});

test('auto battle cannot start while another owner holds the session', async () => {
  const f = fixture({ mode: 'leveling', generation: 9 });
  await assert.rejects(() => f.api.start(), /人工控制/);
  assert.ok(!f.calls.some(entry => entry.url.endsWith('/battle-auto')));
});

test('auto battle stays startable without the task automation executor', async () => {
  const f = fixture();
  /* A bridge that reports no executor still serves auto battle. */
  f.api.publishControl({ control: { mode: 'manual', generation: 7 }, automation_available: false });
  assert.equal(f.nodes['#ai-start'].disabled, false);
  f.nodes['#ai-automation-mode'].value = 'quest';
  f.api.publishControl({ control: { mode: 'manual', generation: 7 }, automation_available: false });
  assert.equal(f.nodes['#ai-start'].disabled, true, 'the task modes still need the executor');
});

test('the panel offers auto battle and leaves no client-side battle loop behind', () => {
  assert.match(source, /<option value="battle">自动战斗<\/option>/);
  /* The transport copies a known list of automation_* fields out of every
     control envelope. A field missing from that list reaches the panel as
     undefined, which is exactly how the battle status line went missing. */
  const page = fs.readFileSync(path.join(__dirname, 'index.html'), 'utf8');
  const copier = page.slice(page.indexOf('const next={...raw};'), page.indexOf('this.control=next;'));
  for (const field of ['automation_state', 'automation_note']) {
    assert.ok(copier.includes(`"${field}"`), `the control envelope must carry ${field}`);
  }
  assert.match(source, /BATTLE_LOCK_CLASS = "stoneage-ai-battle-locked"/);
  assert.ok(!/maybeAutoBattleTurn/.test(source));
  const html = fs.readFileSync(path.join(__dirname, 'index.html'), 'utf8');
  assert.ok(!/maybeAutoBattleTurn|systemSettings\.autoBattle/.test(html), 'the page must not run a second auto battle');
});
