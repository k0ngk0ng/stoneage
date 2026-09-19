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

function fixture(control = { mode: 'manual', generation: 7 }) {
  const calls = [];
  const transport = {
    id: 'session-a', base: '', closed: false,
    control: { control },
    setControl(value) { this.control = value; return value; },
  };
  const app = { transport, systemSettings: {} };
  const root = { StoneAgeWebClient: { app } };
  const status = { textContent: '', classList: { toggle() {} } };
  const nodes = {
    '#ai-automation-mode': { value: 'battle' },
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
      if (url.endsWith('/control')) return { ok: true, json: async () => ({ control }) };
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

test('auto battle sends only the generation and locks the browser against the running loop', async () => {
  const f = fixture();
  await f.api.start();

  const call = f.calls.find(entry => entry.url.endsWith('/battle-auto'));
  assert.ok(call, 'the panel must start auto battle through its own endpoint');
  assert.equal(call.url, '/api/sessions/session-a/battle-auto');
  assert.equal(call.options.method, 'POST');
  assert.equal(call.options.body, '{"generation":7}');
  assert.ok(!f.calls.some(entry => entry.url.includes('/automation/start')), 'auto battle carries no task or budget config');

  const control = f.api.currentControl();
  assert.equal(control.mode, 'battle');
  assert.equal(control.generation, 8);
  assert.equal(f.locked.get('stoneage-ai-locked'), true);
  assert.equal(f.app.systemSettings.battleAnimationSpeed, 10);

  /* The server's pause handler accepts the executor modes only, so the browser
     must not offer a pause the server would refuse; handing control back is
     always available while the loop owns the session. */
  assert.equal(f.nodes['#ai-pause'].disabled, true);
  assert.equal(f.nodes['#ai-takeover'].disabled, false);
  assert.equal(f.nodes['#ai-start'].disabled, true);
  assert.match(f.nodes['#ai-control-status'].textContent, /自动战斗/);
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
  assert.match(source, /LOCKED_MODES/);
  assert.ok(!/maybeAutoBattleTurn/.test(source));
  const html = fs.readFileSync(path.join(__dirname, 'index.html'), 'utf8');
  assert.ok(!/maybeAutoBattleTurn|systemSettings\.autoBattle/.test(html), 'the page must not run a second auto battle');
});
