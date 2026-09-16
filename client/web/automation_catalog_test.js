const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');

function fixture(fetch) {
  let controlUpdates = 0;
  const transport = { id: 'session-a', base: '', closed: false, setControl() { controlUpdates++; } };
  const app = { transport, petSlots: [] };
  const root = { StoneAgeWebClient: { app } };
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, 'automation.js'), 'utf8'), { window: root, fetch });
  const select = {
    value: '', options: [],
    replaceChildren() { this.value = ''; this.options = []; },
    appendChild(option) { this.options.push(option); },
  };
  const details = { textContent: '' };
  const nodes = { '#ai-task-id': select, '#ai-task-details': details, '#ai-automation-mode': { value: 'quest' } };
  const panel = { querySelector: id => nodes[id] };
  root.document = {
    getElementById: id => id === 'ai-control-panel' ? panel : {},
    createElement: () => ({ dataset: {} }),
  };
  return { api: root.StoneAgeAutomation, transport, select, details, controlUpdates: () => controlUpdates };
}
const response = tasks => ({ ok: true, json: async () => ({ knowledge_revision: 'revision', tasks }) });
const task = { id: 'quest-a', name: '成年礼', description: '<img onerror=bad>', requirements: ['人物至少 30 级'], preparation_notes: '请准备补给', dependencies: [{ id: 'intro', name: '前置任务' }], requires_pet: true, review_blockers: [] };

test('catalog is cached per session, preserves selection, renders facts as text, and never changes the control lease', async () => {
  let calls = 0;
  let tasks = [task];
  const f = fixture(async url => { assert.match(url, /automation\/tasks$/); calls++; return response(tasks); });
  await Promise.all([f.api.refreshTasks(), f.api.refreshTasks()]);
  assert.equal(calls, 1);
  assert.equal(f.select.value, '');
  f.select.value = task.id;
  await f.api.refreshTasks(true);
  assert.equal(f.select.value, task.id);
  assert.match(f.details.textContent, /人物至少 30 级/);
  assert.match(f.details.textContent, /前置任务/);
  assert.match(f.details.textContent, /<img onerror=bad>/);
  assert.equal(f.controlUpdates(), 0);
  await f.api.refreshTasks();
  assert.equal(calls, 2);
  tasks = [];
  await f.api.refreshTasks(true);
  assert.equal(f.select.value, '');
  assert.match(f.details.textContent, /尚未提供任务/);
});

test('a late catalog response from the old session cannot replace the new session catalog', async () => {
  let finishOld;
  const f = fixture(url => url.includes('session-a') ? new Promise(resolve => { finishOld = resolve; }) : Promise.resolve(response([{ ...task, id: 'new-task' }])));
  const old = f.api.refreshTasks();
  f.transport.id = 'session-b';
  await f.api.refreshTasks();
  finishOld(response([task]));
  await old;
  assert.deepEqual(Array.from(f.api.state.tasks, item => item.id), ['new-task']);
});

test('review blockers explain an unavailable quest and prevent a start request', async () => {
  let starts = 0;
  const f = fixture(async url => {
    if (url.endsWith('/control')) return { ok: true, json: async () => ({ mode: 'manual', generation: 1 }) };
    if (url.endsWith('/automation/start')) starts++;
    return response([{ ...task, review_blockers: ['宠物准备要求尚未核实'] }]);
  });
  await f.api.refreshTasks();
  f.select.value = task.id;
  await f.api.refreshTasks(true);
  assert.match(f.details.textContent, /暂不可执行：宠物准备要求尚未核实/);
  await assert.rejects(f.api.start(), /宠物准备要求尚未核实/);
  assert.equal(starts, 0);
});

test('catalog failure leaves no selectable cached quest', async () => {
  let fails = false;
  const f = fixture(async () => fails ? { ok: false, status: 503, text: async () => '任务目录尚未配置' } : response([task]));
  await f.api.refreshTasks();
  f.select.value = task.id;
  fails = true;
  await f.api.refreshTasks(true);
  assert.equal(f.api.state.taskLoaded, false);
  assert.equal(f.select.value, '');
  assert.match(f.details.textContent, /任务目录尚未配置/);
});
