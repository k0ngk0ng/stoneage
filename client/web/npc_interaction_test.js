const fs=require("node:fs"),assert=require("node:assert/strict");
const html=fs.readFileSync(__dirname+"/index.html","utf8");
function section(from,to){const start=html.indexOf(from),end=html.indexOf(to,start);assert.ok(start>=0&&end>start,from);return html.slice(start,end);}
const rules=html.match(/  const DIRS=.*;/)[0]+html.match(/  const clientDirectionFromServer=.*;/)[0]+section("  function npcInteractionRange(","  function pendingTalkActor(");
const talk=section("  async function talkToTarget(","  async function talkToFacingNPC(");
const approach=section("  function approachNPC(","  async function refreshTalkServerPosition(");
const resume=section("  function resumePendingTalk(","  function approachNPC(");
const same=(a,b)=>a[0]===b[0]&&a[1]===b[1];

async function run(){
  {
    // Exercise the real S:C map transition with a connected transport. A warp
    // must clear the old scene without requesting any arbitrary object ID.
    const app={floor:5511,position:[17,12],map:{floor:5511},transport:{},
      actors:new Map([[0,{name:"龙王"}]]),actorDetailRequests:new Set(),
      mapWindowRevisionByKey:new Map(),npcMetadataByKey:new Map(),status:{},mapDimensionsByFloor:new Map()};
    const sends=[];
    const deps={app,stateNumber:Number,MAX_LIVE_MAP_WINDOW:37,
      send:async(...args)=>sends.push(args),reportError:error=>{throw error;}};
    for(const name of ["localMovePredictionActive","partyFollowLeaderMember","clearPartyFollow",
      "bindMapFloorTransitionTarget","startMapFloorTransition","recordServerPosition","cancelPendingMove",
      "syncPartyFollowMembership","clearInitialMapChecksum","clearMapWindowRequest","requestNPCMetadata",
      "autoMapRequestAllowed","setStatus","setWorldState","updateHUD","renderWorld"])deps[name]=()=>false;
    const source=section('  function receiveSystemState(data){','      case "D": {')+'}}';
    const receive=new Function(...Object.keys(deps),source+';return receiveSystemState;')(...Object.values(deps));
    receive("C4004|30|30|15|20");
    assert.equal(app.floor,4004);
    assert.equal(app.actors.size,0,"entering the meat shop clears the previous floor's dragon");
    assert.deepEqual(sends,[],"map entry must rely on server visibility pushes, never C(0)");
    app.actors.set(1943,{name:"卡鲁它那的肉店"});
    receive("C4004|30|30|16|20");
    assert.equal(app.actors.get(1943).name,"卡鲁它那的肉店","position refresh preserves the shopkeeper");
    assert.deepEqual(sends,[],"position status must not cause a refresh request loop");
    assert.ok(!/send\("C",\[0\]\)/.test(html),"login, inventory and pets must not request object zero as a refresh");
    const details=new Function("app","send","reportError",section("  function requestActorDetails(id){","  function receiveActions(text)")+"return requestActorDetails;")(app,deps.send,deps.reportError);
    assert.equal(details(0),true,"object zero is still valid when explicitly observed in an actor update");
    assert.deepEqual(sends,[["C",[0]]]);
    const handlers=new Map();
    const bind=id=>({addEventListener:(_event,handler)=>handlers.set(id,handler)});
    for(const id of ["request-inventory","request-pets"]){
      const line=html.split("\n").find(line=>line.includes(`$("${id}").addEventListener`));
      new Function("$","send","reportError",line)(bind,deps.send,deps.reportError);
    }
    sends.length=0;handlers.get("request-inventory")();handlers.get("request-pets")();
    assert.deepEqual(sends,[["S",["i"]],...[0,1,2,3,4].map(slot=>["S",[`k${slot}`]]),["KS",[-1]]],
      "refresh controls request player status categories instead of an unrelated actor");
  }

  {
    const app={floor:1005,npcMetadataByKey:new Map(),actors:new Map(),npcMetadataRequest:0};
    const deps={app,stateNumber:Number,unescapeCharacterOption:x=>x,isInsideFloor:()=>true,
      actorIsOwn:()=>false,ensureFieldActorAnimation:()=>{},renderWorld:()=>{},updateHUD:()=>{},clientDirectionFromServer:x=>x};
    const merge=new Function(...Object.keys(deps),section("  function npcMetadataKey(","  function removeStaticNPCAt(")+"return mergeStaticNPCMetadata;")(...Object.values(deps));
    const dragon={id:-1,floor:5511,x:17,y:12,graphic:100373,name:"龙王"};
    merge([dragon],1005);
    assert.equal(app.actors.size,0,"another floor's dragon must not appear in a shop");
    assert.equal(app.npcMetadataByKey.size,0,"wrong-floor metadata must not rename live shopkeepers");
    merge([{...dragon,id:-2,floor:1005,name:"Shopkeeper"}],1005);
    assert.equal(app.actors.get(-2).name,"Shopkeeper","valid same-floor NPC still renders");
    const fetches=[];let payload={floor:5511,npcs:[dragon]};
    const fetch=async()=>{fetches.push(1);return {ok:true,json:async()=>payload};};
    const request=new Function("app","fetch","mergeStaticNPCMetadata",section("  function requestNPCMetadata(","  function applyMaskedFields(")+"return requestNPCMetadata;")(app,fetch,merge);
    request(1005);for(let i=0;i<12;i++)await Promise.resolve();
    assert.equal(app.npcMetadataReady,false,"wrong-floor response must remain retryable");
    assert.equal(app.actors.get(-2).name,"Shopkeeper","wrong-floor response leaves valid shop metadata intact");
    payload={floor:1005,npcs:null};request(1005);for(let i=0;i<12;i++)await Promise.resolve();
    assert.equal(app.npcMetadataReady,true,"an empty floor is a valid response");
    assert.equal(app.actors.size,0);
    app.floor=5511;payload={floor:5511,npcs:[dragon]};request(5511);for(let i=0;i<12;i++)await Promise.resolve();
    assert.equal(app.actors.get(-1).name,"龙王","dragon remains present on its legitimate floor");
  }

  {
    const app={floor:1005,npcMetadataByKey:new Map([["1005:17:13",{template:"npcgen_winhealer",interactionRange:2}]])};
    const api=new Function("app","npcMetadataKey",rules+"return {npcInteractionRange,npcUsesLookInteraction};")(app,(f,x,y)=>`${f}:${x}:${y}`);
    const nurse={x:17,y:13,npcInteraction:"look"};
    assert.equal(api.npcInteractionRange(nurse),2,"live actor may use already-loaded coordinate metadata before template hydration");
    assert.equal(api.npcUsesLookInteraction(nurse,1),true);
    assert.equal(api.npcUsesLookInteraction(nurse,2),false);
    assert.equal(api.npcInteractionRange({...nurse,x:18}),1,"do not infer nearby or similarly named NPC capabilities");
  }
  for(const template of ["npcgen_shop","npcgen_petshop","changeevent"]){
    const app={floor:1005,npcMetadataByKey:new Map()};
    const api=new Function("app","npcMetadataKey",rules+"return {npcInteractionRange,npcUsesLookInteraction};")(app,(f,x,y)=>`${f}:${x}:${y}`);
    const shop={x:17,y:13,npcTemplate:template,npcInteraction:"talk"};
    assert.equal(api.npcInteractionRange(shop),2,`${template} keeps the native counter range`);
    assert.equal(api.npcUsesLookInteraction(shop,2),false,`${template} uses TK across the counter`);
  }
  {
    const app={floor:1005,npcMetadataByKey:new Map()};
    const api=new Function("app","npcMetadataKey",rules+"return npcInteractionRange;")(app,(f,x,y)=>`${f}:${x}:${y}`);
    assert.equal(api({x:17,y:13,npcTemplate:"npcgen_man",npcInteraction:"talk"}),1,"ordinary TALKEDFUNC NPC stays adjacent-only");
    assert.equal(api({x:17,y:13,npcTemplate:"npcgen_shopkeeper",npcInteraction:"talk"}),1,"similarly named NPC does not inherit shop range");
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
  for(const template of ["npcgen_shop","npcgen_petshop","changeevent"])for(const facing of [true,false]){
    const target={id:240,name:"Shop",x:17,y:13,wireDirection:facing?4:0,npcTemplate:template,npcInteraction:"talk",direction:7};
    const app={phase:"world",battle:false,transport:{},actors:new Map([[240,target]]),position:[17,15],serverPosition:[17,15],serverPositionReceivedAt:Date.now(),moveQueue:[],direction:0};
    const sends=[],approaches=[];
    const deps={app,LEGACY_TURN_SEND_WAIT_MS:0,actorIsOwn:()=>false,npcFilterForTalk:()=>true,
      sameMovePoint:same,setWorldState:()=>{},forgetTalkTarget:()=>{},refreshTalkServerPosition:()=>{throw new Error("unexpected coordinate refresh");},
      approachNPC:actor=>{approaches.push(actor);return true;},directionFor:()=>3,serverDirectionFromClient:()=>0,setLocalActorAction:()=>{},
      window:{setTimeout(fn,delay){if(delay===60)fn();return 1;}},send:async(name,fields)=>{sends.push([name,fields]);},reportError:error=>{throw error;}};
    const invoke=new Function(...Object.keys(deps),rules+talk+"return talkToTarget;")(...Object.values(deps));
    await invoke(target);
    assert.equal(approaches.length,facing?0:1,`${template} only talks across the counter from the facing side`);
    assert.deepEqual(sends.map(row=>row[0]),facing?["L","TK"]:[],`${template} sends the native turn and talk pair from two cells`);
    if(facing)assert.deepEqual(sends[1][1],[17,15,"P|hi",0,3]);

    for(const direction of [undefined,0]){
      const invalidTarget={...target};delete invalidTarget.wireDirection;
      if(direction===undefined)delete invalidTarget.direction;
      else invalidTarget.direction=direction;
      const invalidApp={...app,actors:new Map([[240,invalidTarget]])};
      const invalidSends=[],invalidApproaches=[];
      const invalidDeps={...deps,app:invalidApp,approachNPC:actor=>{invalidApproaches.push(actor);return true;},send:async(name,fields)=>{invalidSends.push([name,fields]);}};
      const invalidInvoke=new Function(...Object.keys(invalidDeps),rules+talk+"return talkToTarget;")(...Object.values(invalidDeps));
      await invalidInvoke(invalidTarget);
      assert.equal(invalidApproaches.length,1,`${template} rejects distance-two direct talk with ${direction===undefined?"missing":"wrong"} facing`);
      assert.deepEqual(invalidSends,[],`${template} must not send L/TK from a non-facing distance-two tile`);
    }
  }
  {
    // Karutana ticket seller: the server accepts hi two cells along dir 2.
    // Run the actual NPC pointer-release branch twice, as a double click does.
    const target={id:240,kind:"character",charType:29,name:"门票贩卖员",x:82,y:67,
      wireDirection:2,npcTemplate:"changeevent",npcInteraction:"talk"};
    const app={phase:"world",battle:false,transport:{},floor:4000,npcMetadataByKey:new Map(),
      actors:new Map([[240,target]]),position:[84,67],serverPosition:[84,67],
      serverPositionReceivedAt:Date.now(),moveQueue:[],direction:0,pointerLookTargetId:240,pointerLookPointerId:1};
    const sends=[],tasks=[];
    const deps={app,LEGACY_TURN_SEND_WAIT_MS:500,actorIsOwn:()=>false,npcFilterForTalk:()=>true,
      sameMovePoint:same,setWorldState:()=>{},forgetTalkTarget:()=>{},
      refreshTalkServerPosition:()=>{throw new Error("unexpected refresh");},
      approachNPC:()=>{throw new Error("ticket seller is already in native talk range");},
      directionFor:()=>1,serverDirectionFromClient:()=>6,setLocalActorAction:()=>{},npcMetadataKey:(f,x,y)=>`${f}:${x}:${y}`,
      window:{setTimeout(fn,delay){if(delay===60)fn();return 1;}},send:async(...args)=>sends.push(args),reportError:error=>{throw error;}};
    const talkToTarget=new Function(...Object.keys(deps),rules+talk+"return talkToTarget;")(...Object.values(deps));
    const releaseSource=section('    if(event.button===0&&app.pointerLookTargetId!==null){','    /* A held move is a continuous');
    const release=new Function("app","worldPointerIsUiTarget","worldTileFromPointer","actorAtPointer","isTalkableActor","talkToTarget","reportError",
      'return function(event){'+releaseSource+'};')(app,()=>false,()=>[82,67],()=>target,()=>true,
        target=>{const task=talkToTarget(target);tasks.push(task);return task;},deps.reportError);
    for(let click=0;click<2;click++){
      app.pointerLookTargetId=240;app.pointerLookPointerId=1;
      release({button:0,pointerId:1});await tasks[tasks.length-1];
    }
    assert.deepEqual(sends,[["L",[6]],["TK",[84,67,"P|hi",0,3]]],
      "double clicking the ticket seller turns and sends hi once without a spurious walk");
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
  for(const template of ["npcgen_shop","npcgen_petshop","changeevent"]){
    const app={position:[17,15],floor:1005,npcMetadataByKey:new Map([["1005:17:13",{template,direction:4}]])};
    const can=new Function("app","npcMetadataKey",rules+"return npcCanInteractFrom;")(app,(f,x,y)=>`${f}:${x}:${y}`);
    const shop={x:17,y:13,npcTemplate:template,wireDirection:4};
    assert.equal(can(shop),true,"south-facing counter accepts the south tile");
    for(const point of [[17,11],[15,13],[19,13],[18,15],[19,15],[17,16]])assert.equal(can(shop,point),false,`reject unsupported counter position ${point}`);
    assert.equal(can({...shop,wireDirection:undefined,direction:7}),true,"local direction fallback");
    assert.equal(can({x:17,y:13}),true,"exact coordinate metadata supplies template and direction");
    assert.equal(can({...shop,wireDirection:0}),false,"live facing overrides static metadata");
    app.npcMetadataByKey.clear();
    assert.equal(can({...shop,wireDirection:undefined}),false,"unknown facing does not guess");
    for(const point of [[17,12],[16,13],[18,14]])assert.equal(can(shop,point),true,"adjacent behavior remains available on every side");
    for(const facing of [true,false]){
      const target={...shop,id:240,wireDirection:facing?4:0};
      const routeApp={position:[20,20],cursor:{},serverPositionVersion:1};let destination=null;
      const deps={app:routeApp,MOVE_SERVER_WALK_INTERVAL_MS:10,localCellWalkable:(x,y)=>x===17&&y===15,actorOccupiesCell:()=>false,
        routeFromCells:(_from,to)=>[to],forgetTalkTarget:()=>{},setWorldState:()=>{},rememberTalkTarget:()=>{},setMoveTarget:point=>{destination=point;}};
      const invoke=new Function(...Object.keys(deps),rules+approach+"return approachNPC;")(...Object.values(deps));
      assert.equal(invoke(target),facing,"route may stop across counter only on NPC facing side");
      assert.deepEqual(destination,facing?[17,15]:null);
      const pending={id:240,serverPositionTarget:[17,15],serverPositionVersion:1};
      const resumeApp={phase:"world",battle:false,pendingTalk:pending,position:[17,15],serverPosition:[17,15],serverPositionVersion:2,cursor:{}};
      const timers=[],talks=[];
      const resumeDeps={app:resumeApp,pendingTalkActor:()=>target,sameMovePoint:same,forgetTalkTarget:()=>{},setWorldState:()=>{},rememberTalkTarget:()=>{},
        window:{setTimeout:fn=>timers.push(fn)},talkToTarget:async actor=>{talks.push(actor);return true;}};
      const resumeInvoke=new Function(...Object.keys(resumeDeps),rules+resume+"return resumePendingTalk;")(...Object.values(resumeDeps));
      resumeInvoke();
      assert.equal(timers.length,facing?1:0,"resume uses same facing rule as routing");
      if(facing){await timers[0]();assert.equal(talks[0],target);assert.equal(resumeApp.pendingTalk,null);}
    }
  }
  console.log("NPC counter interaction vectors OK");
}
run().catch(error=>{console.error(error);process.exitCode=1;});
