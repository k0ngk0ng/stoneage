const fs=require("node:fs"),assert=require("node:assert/strict");
const html=fs.readFileSync(__dirname+"/index.html","utf8");
function section(from,to){const start=html.indexOf(from),end=html.indexOf(to,start);assert.ok(start>=0&&end>start,from);return html.slice(start,end);}
const rules=section("  function npcInteractionRange(","  function pendingTalkActor(");
const talk=section("  async function talkToTarget(","  async function talkToFacingNPC(");
const approach=section("  function approachNPC(","  async function refreshTalkServerPosition(");
const resume=section("  function resumePendingTalk(","  function approachNPC(");
const same=(a,b)=>a[0]===b[0]&&a[1]===b[1];

async function run(){
  {
    const app={floor:1005,npcMetadataByKey:new Map([["1005:17:13",{template:"npcgen_winhealer",interactionRange:2}]])};
    const api=new Function("app","npcMetadataKey",rules+"return {npcInteractionRange,npcUsesLookInteraction};")(app,(f,x,y)=>`${f}:${x}:${y}`);
    const nurse={x:17,y:13,npcInteraction:"look"};
    assert.equal(api.npcInteractionRange(nurse),2,"live actor may use already-loaded coordinate metadata before template hydration");
    assert.equal(api.npcUsesLookInteraction(nurse,1),true);
    assert.equal(api.npcUsesLookInteraction(nurse,2),false);
    assert.equal(api.npcInteractionRange({...nurse,x:18}),1,"do not infer nearby or similarly named NPC capabilities");
  }
  for(const template of ["npcgen_winhealer","windowhealer","npcgen_signboard","npcgen_man",""])for(const distance of [1,2,3])for(const configuredRange of [undefined,1,2]){
    const healer=["npcgen_winhealer","windowhealer"].includes(template);
    const target={id:240,name:"NPC",x:17,y:13,npcTemplate:template,npcInteractionRange:configuredRange,npcInteraction:template==="npcgen_man"?"talk":"look"};
    const app={phase:"world",battle:false,transport:{},actors:new Map([[240,target]]),position:[17,13+distance],serverPosition:[17,13+distance],serverPositionReceivedAt:Date.now(),moveQueue:[],direction:0};
    const sends=[],approaches=[];
    const deps={app,LEGACY_TURN_SEND_WAIT_MS:0,actorIsOwn:()=>false,npcFilterForTalk:()=>true,
      sameMovePoint:same,setWorldState:()=>{},forgetTalkTarget:()=>{},refreshTalkServerPosition:()=>{throw new Error("unexpected coordinate refresh");},
      approachNPC:actor=>{approaches.push(actor);return true;},directionFor:()=>3,serverDirectionFromClient:()=>0,setLocalActorAction:()=>{},
      window:{setTimeout(fn,delay){if(delay===60)fn();return 1;}},send:async(name,fields)=>{sends.push([name,fields]);},reportError:error=>{throw error;}};
    const invoke=new Function(...Object.keys(deps),rules+talk+"return talkToTarget;")(...Object.values(deps));
    await invoke(target);
    const inRange=distance<=(healer?(configuredRange||1):1);
    assert.equal(approaches.length,inRange?0:1,`${template} range ${distance}`);
    const useTalk=target.npcInteraction==="talk"||(healer&&distance===2);
    assert.deepEqual(sends.map(row=>row[0]),!inRange?[]:useTalk?["L","TK"]:["L"],`${template} must choose the supported dispatch at distance ${distance}`);
    if(inRange&&useTalk)assert.deepEqual(sends[1][1],[17,13+distance,"P|hi",0,3]);
  }
  for(const healer of [true,false]){
    const target={id:240,name:"Nurse",x:17,y:13,npcTemplate:healer?"npcgen_winhealer":"npcgen_signboard",npcInteractionRange:2};
    const app={position:[20,20],cursor:{},serverPositionVersion:1};let destination=null;
    const deps={app,MOVE_SERVER_WALK_INTERVAL_MS:10,localCellWalkable:(x,y)=>x===17&&y===15,actorOccupiesCell:()=>false,
      routeFromCells:(_from,to)=>[to],forgetTalkTarget:()=>{},setWorldState:()=>{},rememberTalkTarget:()=>{},setMoveTarget:point=>{destination=point;}};
    const invoke=new Function(...Object.keys(deps),rules+approach+"return approachNPC;")(...Object.values(deps));
    assert.equal(invoke(target),healer);
    assert.deepEqual(destination,healer?[17,15]:null,"only healer may approach the accessible counter-side tile");
    if(healer)assert.deepEqual(app.pendingTalk.serverPositionTarget,[17,15]);
  }
  for(const healer of [true,false]){
    const target={id:240,x:17,y:13,npcTemplate:healer?"npcgen_winhealer":"npcgen_signboard",npcInteractionRange:2};
    const pending={id:240,serverPositionTarget:[17,15],serverPositionVersion:1};
    const app={phase:"world",battle:false,pendingTalk:pending,position:[17,15],serverPosition:[17,15],serverPositionVersion:2,cursor:{}};
    const timers=[],talks=[];
    const deps={app,pendingTalkActor:()=>target,sameMovePoint:same,forgetTalkTarget:()=>{},setWorldState:()=>{},rememberTalkTarget:()=>{},
      window:{setTimeout:fn=>timers.push(fn)},talkToTarget:async actor=>{talks.push(actor);return true;}};
    const invoke=new Function(...Object.keys(deps),rules+resume+"return resumePendingTalk;")(...Object.values(deps));
    invoke();
    assert.equal(timers.length,healer?1:0,"counter approach must resume at the same range used for routing");
    if(healer){await timers[0]();assert.equal(talks[0],target);assert.equal(app.pendingTalk,null);}
  }
  console.log("NPC counter interaction vectors OK");
}
run().catch(error=>{console.error(error);process.exitCode=1;});
