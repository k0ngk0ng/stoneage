"use strict";
const assert = require("node:assert/strict"), fs = require("node:fs");
const html = fs.readFileSync(__dirname + "/runtimeassets/index.html", "utf8");
function slice(start, end) {
  const a=html.indexOf(start),b=html.indexOf(end,a);
  assert(a>=0&&b>a);return html.slice(a,b);
}
const decode=new Function(slice("  function unescapeCharacterOption(","  function parseMapWindowHeader(")+";return unescapeCharacterOption;")();
const app={inventory:[],status:{aiObservation:{itemsKnown:true,equipmentKnown:true,items:[{slot:5,id:1234}]}},trade:null};
const receive=new Function("app","decimal","unescapeCharacterOption","$","renderInventory","renderTrade",
  slice("  function receiveInventory(","  const INVENTORY_SLOT_CENTERS")+";return receiveInventory;")(
  app,(s,f=0)=>{const n=Number.parseInt(s,10);return Number.isFinite(n)?n:f;},decode,()=>({}),()=>{},()=>{});
const record=String.raw`meat\zname|literal\yz|0|memo\zpipe\nline\z0|24008|0|1|0|0`;
for(const full of [false,true]) {
  app.inventory=[{index:19,name:"old"}];
  if(full) receive("|||||||||".repeat(5)+record+"|next||0||2|0|1|0|0",false);
  else receive("5|"+record+"|6|next||0||2|0|1|0|0",true);
  const item=app.inventory.find(x=>x.index===5);
  assert.equal(item.name,"meat|name");assert.equal(item.name2,String.raw`literal\z`);
  assert.equal(item.memo,"memo|pipe\nline|0");assert.equal(item.graphic,24008);assert.equal(item.target,1);
  assert.equal(app.inventory.some(x=>x.index===19),!full);
  assert.equal(app.status.aiObservation.itemsKnown,false);
  receive("5|||||||||",true);
  assert(!app.inventory.some(x=>x.index===5));assert(app.inventory.some(x=>x.index===6));
}
assert.equal(decode(String.raw`place\z0`),"place","legacy character sentinel preserved");
assert.equal(decode(String.raw`literal\yz`,{stripSentinel:false}),String.raw`literal\z`);
console.log("Web item field escapes, literal backslashes, full/delta inventory and consumption passed.");
