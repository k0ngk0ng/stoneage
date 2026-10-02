"use strict";
const assert = require("node:assert/strict"), fs = require("node:fs");
const html = fs.readFileSync(__dirname + "/runtimeassets/index.html", "utf8");
const start = html.indexOf('      case "J": {', html.indexOf("  function receiveSystemState("));
const end = html.indexOf('      case "N":', start);
assert(start >= 0 && end > start);
const app = {magic: []};
const receive = new Function("app", "decimal", "stateNumber", "unescapeCharacterOption",
  'return function(data){const parts=data.split("|"),kind=parts[0];switch(kind[0]){' + html.slice(start, end) + '}};')(
  app, (s, fallback) => Number(s) || fallback, s => Number(s || 0), s => s.replace(/\\z/g, "|"));
const prefix = "J1|1|8|0|1|name\\zpipe|memo|";
receive(prefix + "id=10|");
assert.equal(app.magic[1].magicId, 10);
assert.equal(app.magic[1].magicIdKnown, true);
assert.equal(app.magic[1].name, "name|pipe");
for (const suffix of ["", "id=x|", "id=-1|", "id=2147483648|", "id=10|id=20|"]) {
  receive(prefix + "id=10|");
  receive(prefix + suffix);
  assert.equal(app.magic[1].magicIdKnown, false, "old/invalid metadata must clear previous ID");
  assert.equal(app.magic[1].magicId, 0);
}
receive(prefix + "id=0|");
assert.equal(app.magic[1].magicIdKnown, true);
receive("J1|0|");
assert.equal(app.magic[1].useFlag, 0);
assert.equal(app.magic[1].magicIdKnown, false);
assert.equal(app.magic[1].name, "");
console.log("Web native J spell identity, old-server compatibility and unequip passed.");
