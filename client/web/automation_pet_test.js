const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');

function fixture() {
  const app = { petSlots: [] };
  const sent = [];
  /* The module runs in its own realm, so a recorded packet is flattened to a
     string: deepStrictEqual would otherwise fail on the foreign prototypes. */
  const root = { StoneAgeWebClient: { app, send: (name, values) => { sent.push(`${name}|${(values || []).join(",")}`); return Promise.resolve(); } } };
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, 'automation.js'), 'utf8'), { window: root });
  function select() {
    return {
      value: '', options: [],
      replaceChildren() { this.options = []; this.value = ''; },
      appendChild(option) { this.options.push(option); },
    };
  }
  const nodes = {
    '#ai-automation-mode': { value: 'quest' },
    '#ai-task-id': { value: 'quest-a' },
    '#ai-target-pet': select(),
    '#ai-quest-pet': select(),
  };
  const panel = { querySelector: id => nodes[id] };
  root.document = {
    getElementById: id => id === 'ai-control-panel' ? panel : {},
    createElement: () => ({ dataset: {} }),
  };
  return { app, nodes, api: root.StoneAgeAutomation, sent };
}

test('human quest selection uses owned stable identity and never silently replaces a lost pet', () => {
  const { app, nodes, api } = fixture();
  app.petSlots = [{ stableId: 'pet-a', name: 'Same', level: 8 }, { stableId: 'pet-b', name: 'Same', level: 90 }, { name: 'Unknown' }];
  api.refreshPets();
  assert.equal(api.selectedConfig().selected_pet_id, '');
  assert.equal(nodes['#ai-quest-pet'].options.at(-1).disabled, true);
  nodes['#ai-quest-pet'].value = 'pet-a';
  assert.equal(api.selectedConfig().selected_pet_id, 'pet-a');
  app.petSlots.reverse();
  api.refreshPets();
  assert.equal(api.selectedConfig().selected_pet_id, 'pet-a');
  app.petSlots = [{ stableId: 'pet-b', name: 'Same', level: 90 }];
  api.refreshPets();
  assert.equal(api.selectedConfig().selected_pet_id, '');
  nodes['#ai-automation-mode'].value = 'leveling';
  nodes['#ai-quest-pet'].value = 'pet-b';
  assert.equal(api.selectedConfig().selected_pet_id, '');
});

test('human character allocation is opt-in and keeps the exact selected weights and reserve', () => {
  const { nodes, api } = fixture();
  nodes['#ai-automation-mode'].value = 'leveling';
  nodes['#ai-build-enabled'] = { checked: false };
  for (const [key, value] of Object.entries({ vital: 0, strength: 2, toughness: 0, dexterity: 1, reserve: 4 })) {
    nodes[`#ai-build-${key}`] = { value: String(value) };
  }
  assert.equal(api.selectedConfig().character_build, undefined);
  nodes['#ai-build-enabled'].checked = true;
  const configured = api.selectedConfig().character_build;
  assert.deepEqual(JSON.parse(JSON.stringify(configured)), {
    weights: { vital: 0, strength: 2, toughness: 0, dexterity: 1 }, reserve_points: 4,
  });
  nodes['#ai-build-strength'].value = '7';
  assert.equal(configured.weights.strength, 2);
  assert.equal(api.selectedConfig().character_build.weights.strength, 7);
  nodes['#ai-automation-mode'].value = 'quest';
  assert.equal(api.selectedConfig().character_build, undefined);
});

test('invalid human allocation choices are rejected without silently changing the policy', () => {
  const { nodes, api } = fixture();
  nodes['#ai-automation-mode'].value = 'leveling';
  nodes['#ai-build-enabled'] = { checked: true };
  for (const key of ['vital', 'strength', 'toughness', 'dexterity', 'reserve']) nodes[`#ai-build-${key}`] = { value: '0' };
  assert.throws(() => api.selectedConfig(), /正权重/);
  for (const value of ['-1', '101', '1.5', 'invalid']) {
    nodes['#ai-build-strength'].value = value;
    assert.throws(() => api.selectedConfig(), /整数/);
  }
  nodes['#ai-build-strength'].value = '1';
  nodes['#ai-build-reserve'].value = '1001';
  assert.throws(() => api.selectedConfig(), /整数/);
});

test('running or paused automation keeps character build inputs locked until manual takeover', () => {
  const { nodes, api } = fixture();
  let disabled = false;
  nodes['#ai-build-section'] = { toggleAttribute(name, value) { if (name === 'disabled') disabled = value; } };
  nodes['#ai-control-status'] = { textContent: '', classList: { toggle() {} } };
  for (const mode of ['leveling', 'paused']) {
    api.publishControl({ mode, generation: 2 });
    assert.equal(disabled, true);
  }
  api.publishControl({ mode: 'manual', generation: 3 });
  assert.equal(disabled, false);
});

test('the panel asks the server for a pet serial instead of leaving every pet unselectable', () => {
  const { app, api, sent } = fixture();
  app.petSlots = [{ name: '乌力乌力', level: 1 }];
  api.refreshPets();
  assert.equal(sent[0], 'S|AI', 'an unidentified pet is asked about');
  /* The panel refreshes on every control update; the ask must not become a
     packet storm. */
  api.refreshPets();
  assert.equal(sent.length, 1);
  app.petSlots = [{ name: '乌力乌力', id: '1789743703i10' }];
  api.refreshPets();
  assert.equal(sent.length, 1, 'an identified pet needs no request');
});

test('no pet identity request while a mode owns the session', () => {
  const { app, nodes, api, sent } = fixture();
  nodes['#ai-control-status'] = { textContent: '', classList: { toggle() {} } };
  app.petSlots = [{ name: '乌力乌力', level: 1 }];
  api.publishControl({ mode: 'leveling', generation: 2 });
  api.refreshPets();
  assert.equal(sent.length, 0, 'a task mode refuses outside packets, so asking is pointless');
  api.publishControl({ mode: 'manual', generation: 3 });
  api.refreshPets();
  assert.equal(sent[0], 'S|AI', 'the player keeps the session again');
});

test('prerequisite execution is opt-in for quests and cannot leak into leveling', () => {
  const { nodes, api } = fixture();
  assert.equal(api.selectedConfig().include_dependencies, false);
  nodes['#ai-include-dependencies'] = { checked: true };
  assert.equal(api.selectedConfig().include_dependencies, true);
  nodes['#ai-automation-mode'].value = 'leveling';
  assert.equal(api.selectedConfig().include_dependencies, false);
});
