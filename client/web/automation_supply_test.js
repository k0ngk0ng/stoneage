const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');

function select() {
  return {
    value: '', options: [],
    replaceChildren() { this.value = ''; this.options = []; },
    appendChild(option) { this.options.push(option); },
  };
}

function fixture(responseData) {
  const transport = { id: 'session-a', base: '', closed: false };
  const app = { transport, character: 'AutomationHero', petSlots: [] };
  const root = { StoneAgeWebClient: { app } };
  const nodes = {
    '#ai-automation-mode': { value: 'leveling' },
    '#ai-task-id': select(),
    '#ai-task-details': { textContent: '' },
    '#ai-supply-enabled': { checked: false },
    '#ai-supply-item': select(),
    '#ai-supply-target': { value: '10' },
    '#ai-supply-reorder': { value: '3' },
    '#ai-supply-details': { textContent: '' },
    '#ai-target-kind': { value: 'character' },
    '#ai-target-level': { value: '10' },
    '#ai-budget-reserve': { value: '0' },
    '#ai-budget-max': { value: '1000' },
    '#ai-time-limit': { value: '3600' },
    '#ai-death-limit': { value: '0' },
    '#ai-target-policy': { value: 'all' },
    '#ai-offline-continue': { checked: false },
  };
  const panel = { querySelector: id => nodes[id] };
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, 'automation.js'), 'utf8'), {
    window: root,
    fetch: async url => {
      assert.match(url, /\/automation\/tasks$/);
      return { ok: true, json: async () => responseData };
    },
  });
  root.document = {
    getElementById: id => id === 'ai-control-panel' ? panel : {},
    createElement: () => ({ dataset: {} }),
  };
  return { api: root.StoneAgeAutomation, nodes };
}

const directory = {
  knowledge_revision: 'revision',
  tasks: [],
  supplies: [{ alias: 'hometown-meat', name: '小的肉', unit_price: 12, template_id: 2344, npc: '萨姆吉尔村', floor: 1, x: 5, y: 6 }],
};

test('leveling loads reviewed supplies and sends the opt-in supply policy', async () => {
  const { api, nodes } = fixture(directory);
  await api.refreshTasks();
  assert.deepEqual(api.state.supplies.map(item => item.alias), ['hometown-meat']);
  assert.match(nodes['#ai-supply-item'].options[1].textContent, /小的肉（萨姆吉尔村）/);
  assert.match(nodes['#ai-supply-item'].options[1].textContent, /单价 12/);
  assert.equal(api.selectedConfig().supply, undefined);

  nodes['#ai-supply-enabled'].checked = true;
  assert.throws(() => api.selectedConfig(), /已审核补给物品/);
  nodes['#ai-supply-item'].value = 'hometown-meat';
  await api.refreshTasks(true);
  assert.deepEqual(JSON.parse(JSON.stringify(api.selectedConfig().supply)), { item: 'hometown-meat', target_count: 10, reorder_count: 3 });
  assert.match(nodes['#ai-supply-details'].textContent, /单价：12 石币/);
  assert.match(nodes['#ai-supply-details'].textContent, /补满最高费用：120 石币/);
  assert.match(nodes['#ai-supply-details'].textContent, /2 个背包空位/);
});

test('leveling supply thresholds are bounded and the policy cannot leak into quests', async () => {
  const { api, nodes } = fixture(directory);
  await api.refreshTasks();
  nodes['#ai-supply-enabled'].checked = true;
  nodes['#ai-supply-item'].value = 'hometown-meat';
  for (const [target, reorder] of [['1', '1'], ['14', '3'], ['10', '0'], ['10', '10'], ['2.5', '1'], ['10', '1.5']]) {
    nodes['#ai-supply-target'].value = target;
    nodes['#ai-supply-reorder'].value = reorder;
    assert.throws(() => api.selectedConfig(), /补满数量|补货阈值/);
  }
  nodes['#ai-supply-target'].value = '10';
  nodes['#ai-supply-reorder'].value = '3';
  nodes['#ai-automation-mode'].value = 'quest';
  assert.equal(api.selectedConfig().supply, undefined);
});

test('an empty reviewed supply catalog explains why opt-in supply is unavailable', async () => {
  const { api, nodes } = fixture({ tasks: [], supplies: [] });
  await api.refreshTasks();
  assert.match(nodes['#ai-supply-details'].textContent, /当前没有已审核补给/);
  nodes['#ai-supply-enabled'].checked = true;
  nodes['#ai-supply-item'].value = 'stale-meat';
  assert.throws(() => api.selectedConfig(), /已审核补给物品/);
});
