"use strict";
const assert = require("assert");
const fs = require("fs");
const html = fs.readFileSync(__dirname + "/index.html", "utf8");
const start = html.indexOf("  function handlePacket(");
const end = html.indexOf("  function unescapeCharacterOption", start);
assert(start >= 0 && end > start);
const calls = [];
const handle = new Function("Protocol", "deferMapTransitionPacket", "addWire", "addEvent", "textOr", "receiveSystemState", "setStatus", "renderStatus",
 html.slice(start, end) + "; return handlePacket;")(
 {decodeMessage: packet => packet}, () => { calls.push("defer"); return false; },
 () => calls.push("wire"), () => calls.push("event"), String,
 value => calls.push(["system",value]), (...args) => calls.push(args), () => calls.push("render"));
handle({function:"S", textValues:["AICHAT|1|42|3|pc1_0123456789abcdef0123456789abcdef|507c6869"]});
assert.deepStrictEqual(calls, [], "identity companion must not refresh or log browser UI");
handle({function:"S", textValues:["AI|v=1"]});
assert(calls.some(call => Array.isArray(call) && call[0] === "system" && call[1] === "AI|v=1"), "ordinary status still reaches renderer");
console.log("chat identity companion browser isolation passed");
