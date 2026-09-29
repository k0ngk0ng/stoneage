const assert = require("assert");
const fs = require("fs");
const path = require("path");

const html = fs.readFileSync(path.join(__dirname, "runtimeassets/index.html"), "utf8");
const start = html.indexOf("  function splitAIObservationEscaped");
const end = html.indexOf("  function receiveSystemState", start);
if (start < 0 || end <= start) throw new Error("AI observation parser boundary missing");

function unescapeCharacterOption(value) {
  return String(value || "")
    .replace(/\\y/g, "\\")
    .replace(/\\z/g, "|")
    .replace(/\\n/g, "\n")
    .replace(/\\c/g, ",");
}

const app = {
  petSlots: [
    null,
    { id: "old-slot-one", name: "保留显示资料" },
    { id: "old-slot-two", name: "旧稳定身份" },
    null,
    null,
  ],
  status: {},
  serverState: {},
  pc: {},
};
const parser = new Function(
  "app",
  "unescapeCharacterOption",
  `${html.slice(start, end)}; return {parseAIObservation, applyAIObservation, splitAIObservationEscaped};`,
)(app, unescapeCharacterOption);

const payload = [
  "AI",
  "v=1",
  "chara=12",
  "pet=2,a\\c b\\z c\\y,37",
  "pet=2,latest,38",
  "pet=0,unknown,4",
  "end=1,2,3,4,5,6",
  "now=7,8,9,10,11,12",
  "ride=120",
].join("|");
const observation = parser.parseAIObservation(payload);
assert.deepStrictEqual(observation.pets.map(pet => pet.slot), [0, 2], "pet records sort by slot");
assert.deepStrictEqual(observation.pets.find(pet => pet.slot === 2), {slot: 2, unique: "latest", level: 38}, "duplicate slots use the newest record");
assert.deepStrictEqual(parser.splitAIObservationEscaped("a\\c,b\\z,c\\y,d"), ["a\\c", "b\\z", "c\\y", "d"], "escaped delimiters stay inside a pet record");

const applied = parser.applyAIObservation(observation);
assert.strictEqual(applied, observation, "apply returns the validated observation");
assert.strictEqual(app.petSlots[0].id, undefined, "unknown unique does not create an identity");
assert.strictEqual(app.petSlots[0].level, 4);
assert.strictEqual(app.petSlots[1], null, "unreported old slot is cleared");
assert.strictEqual(app.petSlots[2].id, "latest", "new stable identity replaces old identity");
assert.deepStrictEqual(app.petSlots.map(pet => pet?.slot ?? null), [0, null, 2, null, null]);
assert.deepStrictEqual(app.status.aiEndEvent, [1, 2, 3, 4, 5, 6]);
assert.deepStrictEqual(app.status.aiNowEvent, [7, 8, 9, 10, 11, 12]);
assert.strictEqual(app.pc.learnRide, 120);

app.status.skillPoints = 7;
parser.applyAIObservation(payload + "|stat_points=0");
assert.strictEqual(app.status.skillPoints, 0, "server zero replaces local stat points");
parser.applyAIObservation(payload + "|stat_points=3");
assert.strictEqual(app.status.skillPoints, 3);
parser.applyAIObservation(payload);
assert.strictEqual(app.status.skillPoints, 3, "legacy response does not invent a point count");
for (const tail of ["-1", "1.5", "2147483648", "1|stat_points=2"]) {
  assert.strictEqual(parser.applyAIObservation(payload + "|stat_points=" + tail), null);
  assert.strictEqual(app.status.skillPoints, 3, "invalid points cannot overwrite current count");
}

assert.strictEqual(parser.parseAIObservation("AI|v=1|chara=12|end=0,0,0,0,0,0|now=0,0,0,0,0,0"), null, "missing ride is rejected");
assert.strictEqual(parser.parseAIObservation("AI|v=2|chara=12|end=0,0,0,0,0,0|now=0,0,0,0,0,0|ride=0"), null, "unsupported version is rejected");

console.log("AI observation parser and slot rebuild tests passed");

const itemObservation=parser.applyAIObservation(payload+"|items=6,2414;5,2415|equipment=3,701");
assert.strictEqual(itemObservation.itemsKnown,true);
assert.deepStrictEqual(itemObservation.items,[{slot:3,templateId:701},{slot:5,templateId:2415},{slot:6,templateId:2414}]);
for(const field of ["5,0","4,2415","-1,2415","20,2415","5,2415;5,2414","5,2415,extra"]){
  assert.strictEqual(parser.applyAIObservation(payload+"|items="+field),null);
  assert.strictEqual(app.status.aiObservation,itemObservation,"invalid metadata must not replace known identity evidence");
}
assert.strictEqual(parser.applyAIObservation(payload).itemsKnown,false,"legacy response clears item ID evidence");

parser.applyAIObservation(payload+"|items=5,2415");
app.inventory=[{index:5,name:"old"}];
const inventoryStart=html.indexOf("  function receiveInventory(text,indexed=true)");
const inventoryEnd=html.indexOf("  const INVENTORY_SLOT_CENTERS",inventoryStart);
const receiveInventory=new Function("app","decimal","unescapeCharacterOption","$","renderInventory","renderTrade",`${html.slice(inventoryStart,inventoryEnd)};return receiveInventory;`)(app,(value,fallback=0)=>value===""?fallback:Number(value),unescapeCharacterOption,()=>({}),()=>{},()=>{});
receiveInventory("5|replacement||0||100|0|0|1|0");
assert.strictEqual(app.status.aiObservation.itemsKnown,false,"slot replacement invalidates old template identity");
