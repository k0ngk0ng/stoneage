const fs = require("node:fs");
const assert = require("node:assert/strict");
const html = fs.readFileSync(__dirname + "/runtimeassets/index.html", "utf8");
const start = html.indexOf("  function battleActorTraveling(");
const end = html.indexOf("  function battleTargetHighlightIds(", start);
assert.ok(start >= 0 && end > start, "production target eligibility helpers missing");
const functionLine = name => {
  const from = html.indexOf("  function " + name + "(");
  assert.ok(from >= 0, name + " missing");
  return html.slice(from, html.indexOf("\n", from));
};
const app = {battleState: {myNo: 0, participants: [], motions: []}, character: "Master"};
const api = new Function("app", `
  const BATTLE_BC_DEATH=1<<1, BATTLE_BP_BOOMERANG=1<<2;
  ${functionLine("battleSide")}
  ${functionLine("battleIsEnemy")}
  ${html.slice(start, end)}
  return {battleTargetCandidates,battleTargetSelectable};
`)(app);

// BATTLEMENU.CPP::BattleSetWazaHitBox has no ACT_ATR_TRAVEL test for
// native PETSKILL targets 0..7. Attack/boomerang retain their travel guard.
for (const myNo of [0, 10]) for (const travel of ["flag", "moving", "actTravel", "appear", "escape", "escape-fail", "fade", "capture", "none", "expired"]) {
  const state = app.battleState;
  state.myNo = myNo;
  state.participants = Array.from({length: 20}, (_, battleId) => ({battleId, hp: 100, maxHp: 100, flags: 0, travel: travel === "flag", moving: travel === "moving", actTravel: travel === "actTravel"}));
  state.motions = ["flag", "moving", "actTravel", "none"].includes(travel) ? [] : state.participants.map(item => ({kind: travel === "expired" ? "appear" : travel, actor: item.battleId, until: Date.now() + (travel === "expired" ? -10000 : 10000)}));
  // A dead target is still invalid for these ordinary skills.
  const deadId = myNo === 0 ? 12 : 2;
  state.participants[deadId].dead = true;
  state.participants[deadId].flags = 2;
  state.participants[deadId].hp = 0;
  state.participants[myNo === 0 ? 8 : 18].hp = 0;
  const live = state.participants.filter(item => !item.dead && item.hp > 0).map(item => item.battleId);
  const ownSide = id => Math.floor(id / 10) === Math.floor(myNo / 10);
  for (let targetType = 0; targetType <= 7; targetType++) {
    const action = {kind: "pet", targetType};
    const expected = live.filter(id => {
      switch (targetType) {
        case 0: case 5: return id === myNo + 5;
        case 1: case 4: return true;
        case 2: return ownSide(id);
        case 3: return !ownSide(id);
        case 6: return id !== myNo + 5;
        case 7: return id !== myNo && id !== myNo + 5;
      }
    });
    const actual = state.participants.filter(item => api.battleTargetSelectable(action, item, state)).map(item => item.battleId);
    assert.deepEqual(actual, expected, `pet target ${targetType}, side ${myNo}, motion ${travel}`);
    if ([0, 1, 6, 7].includes(targetType)) {
      assert.deepEqual(api.battleTargetCandidates(action).map(item => item.battleId), expected, "candidate and click eligibility must agree");
    }
  }
  for (const bpFlags of [0, 4]) {
    state.bpFlags = bpFlags;
    const expected = ["none", "expired"].includes(travel) ? live.filter(id => id !== myNo && (!bpFlags || Math.floor(id / 5) !== Math.floor(myNo / 5))) : [];
    assert.deepEqual(api.battleTargetCandidates({kind: "attack"}).map(item => item.battleId), expected, "ordinary/boomerang attack must preserve its travel and row rules");
    for (const item of state.participants) assert.equal(api.battleTargetSelectable({kind: "attack"}, item, state), expected.includes(item.battleId));
  }
  for (const kind of ["magic", "item", "capture"]) {
    const action = {kind, targetType: 1};
    const expected = ["none", "expired"].includes(travel) ? live.filter(id => kind !== "capture" || !ownSide(id)) : [];
    assert.deepEqual(api.battleTargetCandidates(action).map(item => item.battleId), expected, "pet-only change must not alter another command's eligibility");
    for (const item of state.participants) assert.equal(api.battleTargetSelectable(action, item, state), expected.includes(item.battleId));
  }
}
console.log("battle target native-rule vectors OK");
