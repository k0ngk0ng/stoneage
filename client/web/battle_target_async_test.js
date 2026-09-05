const fs = require("node:fs");
const assert = require("node:assert/strict");

const html = fs.readFileSync(__dirname + "/index.html", "utf8");
const start = html.indexOf("  async function sendBattleTarget(target){");
const end = html.indexOf("  function battleActivePet(", start);
assert.ok(start >= 0 && end > start, "production target handler missing");
const cancelStart = html.indexOf("  function battleCancelPendingUi(state){");
const cancelEnd = html.indexOf("  function battleCancelPendingDamage(", cancelStart);
assert.ok(cancelStart >= 0 && cancelEnd > cancelStart, "production selector cleanup missing");

function harness(kind) {
  const action = {kind, index: 0, targetType: 6};
  const state = {pendingAction: action, commandPending: {}, turnKey: 1};
  const app = {battle: true, battleState: state, battleCommands: []};
  const requests = [], errors = [], effects = [];
  const node = {classList: {add() {}}, textContent: "", scrollTop: 0, scrollHeight: 0};
  const deps = {
    app, $: () => node,
    battleActionAllowed: () => true,
    rejectFullBattleCapture: () => false,
    rejectUnavailableBattleMagic: () => false,
    battleTargetSelectable: () => true,
    battleActionCommand: () => kind === "pet" ? "W|0|A" : "H|A",
    battleButtonCommandForAction: () => kind === "pet" ? "" : "H|A",
    setBattleMenuPressedCommand: () => {},
    battleSetCommandLock: (current, owner, locked) => { current[owner + "Locked"] = locked; effects.push("lock"); },
    battleStartCommandPending: (current, owner, command) => { current.commandPending[owner] = command; },
    battleClearCommandPending: (current, owner) => { current.commandPending[owner] = null; effects.push("clear"); },
    battleActivePetSlot: () => 0,
    playSoundEffect: () => {},
    renderBattleWorld: () => { effects.push("render"); },
    renderBattle: () => {}, renderBattleTargets: () => {},
    clearBattlePetChoiceTimer: () => {},
    closeBattlePopup: () => { effects.push("close"); },
    maybeOpenBattlePetSkillMenu: () => { effects.push("pet-stage"); },
    reportError: error => { errors.push(error); },
    send: (name, fields) => new Promise((resolve, reject) => { requests.push({name, fields, resolve, reject}); }),
  };
  const handlers = new Function(...Object.keys(deps), html.slice(start, end) + html.slice(cancelStart, cancelEnd) + "\nreturn {sendTarget:sendBattleTarget,cancel:battleCancelPendingUi};")(...Object.values(deps));
  return {state, app, requests, errors, effects, ...handlers};
}

async function run() {
  for (const kind of ["pet", "attack"]) {
    const test = harness(kind);
    const pending = test.sendTarget({battleId: 10});
    await test.sendTarget({battleId: 10});
    assert.equal(test.requests.length, 1, "pointerup/click pair must send once");
    test.requests[0].reject(new Error("bridge unavailable"));
    await pending;
    assert.equal(test.errors.length, 1);
    assert.equal(test.state.targetSelectionPending, false, "failed target write must release retry latch");
    const retry = test.sendTarget({battleId: 10});
    assert.equal(test.requests.length, 2, "same target must be clickable after a failed write");
    test.requests[1].resolve();
    await retry;
    assert.equal(test.state.pendingAction, null);
    assert.equal(test.app.battleCommands.length, 1);
  }
  for (const accepted of [true, false]) for (const replacedBattle of [true, false]) {
    const test = harness("pet");
    const pending = test.sendTarget({battleId: 10});
    const nextAction = {kind: "pet", index: 1, targetType: 6};
    if (replacedBattle) test.app.battleState = {pendingAction: nextAction};
    else {
      test.state.turnKey++;
      test.cancel(test.state);
      assert.equal(test.state.targetSelectionPending, false, "new BP/turn cleanup must release the old selector latch");
      test.state.pendingAction = nextAction;
      test.state.commandPending.pet = "W|1|B";
      test.state.targetSelectionPending = true;
    }
    const count = test.effects.length;
    if (accepted) test.requests[0].resolve();
    else test.requests[0].reject(new Error("old request failure"));
    await pending;
    assert.equal(test.app.battleState.pendingAction, nextAction, "old HTTP result must not erase current target choice");
    assert.equal(test.effects.length, count, "old HTTP result must not repaint, unlock, or close a newer menu");
    if (!replacedBattle) {
      assert.equal(test.state.commandPending.pet, "W|1|B");
      assert.equal(test.state.targetSelectionPending, true);
    }
  }
  console.log("battle target async vectors OK");
}

run().catch(error => { console.error(error); process.exitCode = 1; });
