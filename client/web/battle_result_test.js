"use strict";
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");
const html = fs.readFileSync(__dirname + "/runtimeassets/index.html", "utf8");
function section(start, end) {
  const a=html.indexOf(start), b=html.indexOf(end,a);
  assert(a>=0 && b>a, `missing section ${start}`);
  return html.slice(a,b);
}
function element() {
  return {children:[],dataset:{},style:{},textContent:"",className:"",
    classList:{toggle(){},add(){}},
    append(...children){this.children.push(...children);},
    replaceChildren(...children){this.children=children;},
    setAttribute(name,value){this[name]=String(value);}};
}
const nodes={"battle-result-body":element(),"battle-result-screen":element(),"battle-log":element()};
const app={character:"人物",petSlots:[{name:"休息宠物",exp:100},{name:"战斗宠物",freeName:"第二只的昵称",exp:200},null,null,{name:"第五只宠物",exp:300}],battleState:{}};
const context={app,document:{createElement:element},$:id=>nodes[id],Date,
  decimal:(value,fallback=0)=>{const n=parseInt(String(value),10);return Number.isFinite(n)?n:fallback;},
  battleBase62:value=>{const alphabet="0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";let n=0;for(const c of String(value||""))n=n*62+alphabet.indexOf(c);return n;},
};
vm.createContext(context);
vm.runInContext(html.match(/  function petDisplayName\(pet\)\{[^\n]+/)[0]+section("  function parseBattleResult(","  function finishBattleResult("),context);
assert.equal(context.battleResultSound({duel:false,characters:[{levelUp:false}]}),202);
assert.equal(context.battleResultSound({duel:false,characters:[{levelUp:false},{levelUp:true}]}),211);
assert.equal(context.battleResultSound({duel:true,characters:[{levelUp:true}]}),202);
const before=JSON.stringify(app.petSlots);
const result=context.parseBattleResult("RS","-2|0|3,1|0|2,,,,||");
assert.equal(result.characters.length,2,"empty reward slots must not become fake zero-exp rows");
assert.deepEqual(Array.from(result.characters,e=>e.petNo),[-2,1]);
app.battleState.result=result;context.renderBattleResult();
let rows=nodes["battle-result-body"].children.filter(e=>e.className.includes("result-line"));
assert.equal(rows.length,2);
assert.equal(rows[0].children[0].textContent,"人物");
assert.equal(rows[1].children[0].textContent,"第二只的昵称","reward recipient must use petNo, with the player-given name");
assert.equal(rows[1].children[2].textContent,"Exp +2");
assert.equal(rows[1].dataset.petSlot,"1");
assert(!rows.some(row=>row.children[0].textContent==="休息宠物"));
const sparse=context.parseBattleResult("RS","-2|1|A,,4|1|z,,0|0|1,石斧|药草|");
assert.deepEqual(Array.from(sparse.characters,e=>e.petNo),[-2,4,0]);
assert.equal(sparse.characters[0].exp,10);assert.equal(sparse.characters[1].exp,61);
assert.equal(sparse.characters[1].levelUp,true);
assert.deepEqual(Array.from(sparse.items),["石斧","药草"]);
app.battleState.result=sparse;context.renderBattleResult();
rows=nodes["battle-result-body"].children.filter(e=>e.className.includes("result-line"));
assert.equal(rows[1].children[0].textContent,"第五只宠物");
assert.equal(rows[1].children[1].textContent,"LvUp!");
assert.equal(JSON.stringify(app.petSlots),before,"result presentation must not change authoritative pet exp or level");
assert.equal(context.parseBattleResult("RS",",-1|0|0,99|1|1,garbage,,,||").characters.length,0);
const duel=context.parseBattleResult("RD","A|2");
assert.equal(duel.duel,true);assert.deepEqual(Array.from(duel.characters,e=>e.exp),[10,2]);
context.openBattleResult=(kind,data)=>{context.received={kind,data};};
context.addChat=()=>{throw new Error("raw RS/RD must not be written to game chat");};
vm.runInContext(`function receiveResult(kind,data){const message={function:kind},values=[data];switch(kind){${section('      case "RS": case "RD":','      case "B":')}}}`,context);
context.receiveResult("RS","-2|0|3,1|0|2,,,,||");context.receiveResult("RD","A|2");
assert.equal(context.received.kind,"RD");
console.log("battle result: native pet slots, empty rewards, level-up flags, loot, duel and chat passed");
