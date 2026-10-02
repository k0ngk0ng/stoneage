"use strict";
const assert=require("node:assert/strict"),fs=require("node:fs"),vm=require("node:vm");
const html=fs.readFileSync(__dirname+"/runtimeassets/index.html","utf8");
const section=(a,b)=>{const i=html.indexOf(a),j=html.indexOf(b,i);assert(i>=0&&j>i);return html.slice(i,j);};
const digits="0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ";
const encode=n=>{let s="";do{s=digits[n%62]+s;n=Math.floor(n/62);}while(n);return s;};
const app={petSlots:Array(5).fill(null),battleState:{},petSkills:[]};
let activeSlot=2;
const ctx=vm.createContext({app,Number,String,Boolean,Protocol:{base62:s=>[...s].reduce((n,c)=>n*62+digits.indexOf(c),0)},
  decimal:(s,f=0)=>s===""?f:Number(s),stateNumber:s=>Number(s||0),unescapeCharacterOption:s=>s.replace(/\\z/g,"|"),
  battleActivePetSlot:()=>activeSlot,battleFieldAllowed:()=>true,battleLevelAllowed:()=>true});
vm.runInContext(section("  const PET_FIELDS=","  const PARTY_FIELDS="),ctx);
vm.runInContext(section("  function applyMaskedFields(","  /* CHAR_makeStatusString"),ctx);
vm.runInContext('function receiveK(data){const parts=data.split("|"),kind=parts[0];switch(kind[0]){'+section('      case "K": {','      case "E": {')+'}}',ctx);
vm.runInContext(section("  function battlePetSkillSlotAllowed(","  function battleMagicEntry("),ctx);
const full=(cap,rebirth)=>ctx.receiveK(`K2|1|100|80|100|0|0|10|100|3|20|21|22|95|10|20|30|40|${cap}|1|${rebirth}|pet|nickname|`);
full("3","0");
let p=app.petSlots[2];
assert.equal(p.slot,2);assert.equal(p.maxSkill,3);assert.equal(p.maxSkillKnown,true);
assert.equal(p.transmigrationKnown,true);assert.equal(p.wind,40);
assert.equal(ctx.battleUsablePetSkill({index:2,name:"attack"}),true);
assert.equal(ctx.battleUsablePetSkill({index:3,name:"sleep"}),false);
const mask=4096|8192|16384|32768|65536|131072|262144|524288|1048576;
ctx.receiveK(`K2|${encode(mask)}|96|40|30|20|10|5|0|new\\zname|new nickname|`);
p=app.petSlots[2];
assert.deepEqual([p.slot,p.ai,p.earth,p.water,p.fire,p.wind,p.maxSkill,p.changeNameFlag,p.name,p.freeName],[2,96,40,30,20,10,5,0,"new|name","new nickname"]);
for(const invalid of ["","-1","8","bogus","2147483648"]){
  ctx.receiveK(`K2|${encode(131072)}|${invalid}|`);assert.equal(p.maxSkillKnown,false);
}
p.id="old";app.petSkills[2]=[{index:1,name:"old"}];
full("0","1");assert.equal(app.petSlots[2].id,undefined);assert.equal(app.petSkills[2].length,0);
assert.equal(ctx.battlePetSkillSlotAllowed(6),true);assert.equal(ctx.battlePetSkillSlotAllowed(7),false);
full("0","0");assert.equal(ctx.battlePetSkillSlotAllowed(0),false);
full("0","");assert.equal(app.petSlots[2].transmigrationKnown,false);assert.equal(ctx.battlePetSkillSlotAllowed(0),true);
ctx.receiveK("K2|0|");assert.equal(app.petSlots[2],null);
if(process.env.STONEAGE_BATTLE_PARITY_DIR){
  const cases=JSON.parse(fs.readFileSync(process.env.STONEAGE_BATTLE_PARITY_DIR+"/passed.json","utf8")).pet_capacity_cases;
  assert.equal(cases.length,6);activeSlot=0;
  for(const row of cases){
    ctx.receiveK(Buffer.from(row.status,"hex").toString("utf8"));
    assert.equal(app.petSlots[0].maxSkill,row.slots);assert.equal(app.petSlots[0].transmigration,row.transmigration);
    assert.equal(ctx.battlePetSkillSlotAllowed(row.index),row.accepted,"Web gate must match actual native dispatch");
  }
  console.log("Six actual native K packets and command outcomes match Web skill eligibility.");
}
console.log("Web full/masked K fields, skill capacity, transmigration and escaped names passed.");
