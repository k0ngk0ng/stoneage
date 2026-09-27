#!/usr/bin/env python3
"""Real-browser ladder/reauthentication test against serve-ladder-web-fixture.py.

Requires a running fixture, its loopback URL and a dedicated agent-browser
wrapper whose profile/download/cache paths are under build/. Gameplay is never
mocked: UI ladder controls and native B commands use the real Web TCP bridge,
and independent sactl daemons control the other participants.
"""
import argparse
import json
from pathlib import Path
import subprocess
import time
from urllib.parse import urlparse

ROOT = Path(__file__).resolve().parents[4]


class BrowserTest:
    def __init__(self, args):
        self.args = args
        self.work = args.work.resolve()
        self.browser = args.browser.resolve()
        if not self.work.is_relative_to(ROOT / "build") or not self.browser.is_relative_to(ROOT / "build"):
            raise ValueError("fixture and browser wrapper must be inside this repository's build/")
        parsed = urlparse(args.url)
        if parsed.scheme != "http" or parsed.hostname not in ("127.0.0.1", "localhost"):
            raise ValueError("the fixture URL must be local HTTP")
        self.ready = json.loads((self.work / "web-ready.json").read_text())
        if self.ready["browser_account"] != "ladderqa00":
            raise ValueError("not a synthetic ladder fixture")
        self.artifacts = self.work / args.name
        self.artifacts.mkdir(exist_ok=False)
        self.managed_daemons = {}

    def start_cli(self, player):
        config = self.ready["player_configs"][player]
        remote_work = str(Path(config).parent)
        command = ["docker", "exec", "-e", f"TMPDIR={remote_work}/tmp",
                   "-e", f"XDG_STATE_HOME={remote_work}/state", "-e", f"XDG_CONFIG_HOME={remote_work}/state",
                   "-e", f"STONEAGE_PLAYER_ADMIN_DIR={remote_work}/admin", self.args.container,
                   self.args.sactl, "serve", "--config", config]
        log = (self.artifacts / f"cross-cli-{player}.log").open("wb")
        daemon = subprocess.Popen(command, stdout=log, stderr=subprocess.STDOUT)
        self.managed_daemons[player] = (daemon, log)
        deadline = time.monotonic() + 10
        while True:
            assert daemon.poll() is None, "cross-client daemon exited"
            ping = subprocess.run(["docker", "exec", self.args.container, self.args.sactl,
                                   "--config", config, "--json", "ping"], capture_output=True, timeout=5)
            if ping.returncode == 0:
                return
            assert time.monotonic() < deadline, "cross-client daemon unavailable"
            time.sleep(.1)

    def stop_cli(self, player):
        daemon, log = self.managed_daemons[player]
        if daemon.poll() is None:
            self.cli("stop", player=player)
            daemon.wait(timeout=10)
        log.close()
        del self.managed_daemons[player]

    def cli_battle(self, player):
        deadline = time.monotonic() + 15
        while True:
            battle = self.cli("observe", player=player)["Battle"]
            if battle["Active"] and battle["BAReceived"] and battle["LadderID"]:
                return battle
            assert time.monotonic() < deadline, "CLI did not restore battle"
            time.sleep(.1)

    def run_browser(self, *args, code=None):
        result = subprocess.run([self.browser, *args], input=code, capture_output=True, text=True, timeout=40)
        if result.returncode:
            # Do not include argv: fill's last argument can be a fixture password.
            raise AssertionError(f"browser {args[0]} failed: {result.stdout} {result.stderr}")
        return result.stdout

    def evaluate(self, code):
        return json.loads(self.run_browser("eval", "--stdin", code=code))

    def wait(self, predicate, timeout=30):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if self.evaluate("Boolean(" + predicate + ")"):
                return
            time.sleep(.2)
        raise AssertionError(f"browser condition timed out after {timeout}s: {predicate}")

    def click(self, name):
        self.run_browser("find", "role", "button", "click", "--name", name, "--exact")

    def cli(self, *args, player=1):
        config = (self.ready["opponent_config"] if player == 1 else
                  self.ready["player_configs"][player])
        command = ["docker", "exec", self.args.container, self.args.sactl,
                   "--config", config, "--json", *map(str, args)]
        result = subprocess.run(command, capture_output=True, text=True, timeout=40)
        reply = json.loads(result.stdout)
        with (self.artifacts / "opponent.jsonl").open("a") as stream:
            stream.write(json.dumps({"player": player, "args": args, "reply": reply}) + "\n")
        assert result.returncode == 0 and reply["ok"], (args, reply)
        return reply.get("data")

    def save(self, name, value):
        (self.artifacts / name).write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n")

    def login(self):
        self.run_browser("open", self.args.url)
        self.run_browser("fill", "#account", self.ready["browser_account"])
        password = (self.work / Path(self.ready["password_file"]).name).read_text()
        self.run_browser("fill", "#password", password)
        self.run_browser("click", "#login-form button")
        self.wait("Array.from(document.querySelectorAll('#server-list button')).some(b=>b.textContent === '游戏服务器' && !b.disabled)")
        self.click("游戏服务器")
        self.run_browser("wait", 'button[aria-label="登入人物"]')
        self.click("登入人物")
        self.wait("!!window.StoneAgeLadder.state.envelope?.snapshot.self.id")

    def panel(self):
        if not self.evaluate("StoneAgeLadder.state.open"):
            # Login state can arrive while the toggle is still hidden/covered.
            # Check its actual hit target: world loading flags can remain set
            # during a directly restored battle without obstructing this UI.
            self.wait("(()=>{const t=document.querySelector('#ladder-toggle');if(!t||t.hidden)return false;const r=t.getBoundingClientRect(),hit=document.elementFromPoint(r.x+r.width/2,r.y+r.height/2);return r.width>0&&(hit===t||t.contains(hit));})()", timeout=90)
            self.run_browser("click", "#ladder-toggle")
        self.wait("StoneAgeLadder.state.open && !document.querySelector('#ladder-panel').hidden && !StoneAgeLadder.state.busy")
        # Invitation/ACK controls can scroll a long roster. Restore the top
        # before selecting strategy/mode so the sticky header does not cover
        # controls that the browser's nearest-edge scrolling placed beneath it.
        self.evaluate("(()=>{document.querySelector('#ladder-panel').scrollTop=0;return true;})()")

    def strategy(self, name):
        self.panel()
        # Strategy hosting remains a compatibility API for these native
        # fixtures; human players no longer have a strategy selector.
        assert self.evaluate("!document.querySelector('[aria-label=\"天梯战斗策略\"]')")
        self.evaluate(f"(async()=>{{await StoneAgeLadder.perform('strategy',{json.dumps(name)});return true;}})()")
        self.wait(f"StoneAgeLadder.state.envelope.snapshot.self.strategy === {json.dumps(name)} && !StoneAgeLadder.state.busy")

    def web_ready_queue(self):
        self.panel()
        # Other real clients may have just acknowledged/changed readiness.
        # Let their authoritative room update reach the browser before acting
        # on its revision; an intentionally stale action should be rejected.
        self.wait("StoneAgeLadder.state.envelope.snapshot.room.members.every(p=>p.id === StoneAgeLadder.state.envelope.snapshot.self.id || p.ready)")
        self.click("准备")
        self.wait("StoneAgeLadder.state.envelope.snapshot.self.ready && !StoneAgeLadder.state.busy")
        self.click("开始匹配")
        self.wait("!StoneAgeLadder.state.busy")
        self.click("关闭天梯面板")

    def acknowledge(self):
        self.panel()
        self.click("确认结算，返回队伍")
        self.wait("StoneAgeLadder.state.envelope.snapshot.phase === 'lobby' && !StoneAgeLadder.state.busy")
        assert not self.evaluate("StoneAgeLadder.state.envelope.snapshot.self.ready")

    def monitor(self):
        self.evaluate("""(()=>{clearInterval(window.ladderQA?.timer);window.ladderQA={samples:[],clicks:[]};
          if(!window.ladderQAClickListener){window.ladderQAClickListener=true;
            document.addEventListener('click',e=>{const b=e.target.closest('button');
              if(b?.id==='ladder-toggle'||b?.closest('#ladder-panel'))ladderQA.clicks.push({at:Date.now(),id:b.id,text:b.textContent,open:StoneAgeLadder.state.open});},true);}
          window.ladderQA.timer=setInterval(()=>{const c=StoneAgeWebClient,a=c.app,b=a.battleState;
            if(ladderQA.samples.length<6000)ladderQA.samples.push({at:Date.now(),phase:a.phase,battle:a.battle,
              match:b.ladderID,turn:b.serverTurnNo,ladder:StoneAgeLadder.state.envelope?.snapshot.phase,
              ready:!!b.ladderResultReady,hold:c.battleTerminalHoldUntil(b),panel:StoneAgeLadder.state.open});},25);return true;})()""")

    def battle(self):
        return self.evaluate("""(()=>{const c=StoneAgeWebClient,a=c.app,b=a.battleState;return {
          match:b.ladderID,turn:b.serverTurnNo,myNo:b.myNo,entry:b.entryPending,
          player:b.ladderPlayerSubmitted,pet:b.ladderPetSubmitted,transport:a.transport.id,
          playerAllowed:c.battleCommandAllowed('G'),petAllowed:c.battleCommandAllowed('W|FF|FF'),
          budget:StoneAgeLadder.state.envelope.snapshot.self.reconnect_remaining_ms};})()""")

    def finish_match(self, label, match=None, mode=1):
        # Repeated games can widen the real Elo gap. The matchmaker's 30/90s
        # expansion windows are part of gameplay, not a browser stall.
        self.wait("StoneAgeLadder.state.envelope.snapshot.phase === 'result'", timeout=150)
        # Server automation can settle much faster than the native movie.
        # Wait for actual scene completion rather than treating the result poll as UI completion.
        self.wait("!StoneAgeWebClient.app.battle && StoneAgeWebClient.app.battleState.ladderResultReady", timeout=90)
        evidence = self.evaluate("""(()=>{clearInterval(window.ladderQA?.timer);const a=StoneAgeWebClient.app;
          return {snapshot:StoneAgeLadder.state.envelope.snapshot,phase:a.phase,eo:a.battleState.eoSent,
            panel:StoneAgeLadder.state.open,samples:window.ladderQA?.samples||[]};})()""")
        self.save(label + ".json", evidence)
        result = evidence["snapshot"]["result"]
        assert evidence["phase"] == "world" and evidence["panel"] and not evidence["eo"], {
            key: evidence[key] for key in ("phase", "panel", "eo")}
        assert result["rated"] and result["mode"] == mode and len(result["members"]) == mode * 2
        assert sum(p["rating_delta"] for p in result["members"]) == 0
        assert match is None or result["id"] == match
        assert self.cli("ladder", "result")["snapshot"]["result"] == result
        assert any(r["battle"] and r["ladder"] == "result" for r in evidence["samples"])
        assert not any(r.get("match") and not r["battle"] and r["hold"] > r["at"] for r in evidence["samples"])
        self.wait("StoneAgeWebClient.app.phase === 'world' && !StoneAgeWebClient.app.mapLoading && !document.querySelector('main.scene-transition-active, main.field-loading-active')", timeout=90)
        self.save(label + "-world.json", self.evaluate("({floor:StoneAgeWebClient.app.floor,mapLoading:StoneAgeWebClient.app.mapLoading,cache:!!StoneAgeWebClient.app.mapLayerCache,position:StoneAgeWebClient.app.position})"))
        self.run_browser("screenshot", str(self.artifacts / (label + ".png")))
        print("PASS:", label, result["id"], "turns", result["turns"], flush=True)
        return evidence["snapshot"]["room"]["id"]

    def reconnect(self, participants, label):
        self.wait("StoneAgeWebClient.app.battle && !StoneAgeWebClient.app.battleState.entryPending && !StoneAgeWebClient.app.battleState.ladderAwaitingCommands", timeout=150)
        self.evaluate("(async()=>{await StoneAgeWebClient.send('B',['G']);await StoneAgeWebClient.send('B',['W|FF|FF']);return true;})()")
        self.wait("StoneAgeWebClient.app.battleState.ladderPlayerSubmitted && StoneAgeWebClient.app.battleState.ladderPetSubmitted")
        before = self.battle()
        self.save(label + "-before.json", before)
        self.evaluate("(async()=>{await StoneAgeWebClient.app.transport.close();return true;})()")
        self.login()
        self.wait("StoneAgeWebClient.app.battle && !!StoneAgeWebClient.app.battleState.lastLadderCommands && !StoneAgeWebClient.app.battleState.entryPending")
        after = self.battle()
        self.save(label + "-after.json", after)
        for key in ("match", "turn", "myNo", "player", "pet"):
            assert before[key] == after[key], (key, before, after)
        assert before["transport"] != after["transport"]
        assert after["player"] and after["pet"] and not after["playerAllowed"] and not after["petAllowed"]
        assert 0 < after["budget"] < before["budget"]
        for player in participants:
            if player:
                self.cli("battle", "G", player=player)
                self.cli("battle", "W|FF|FF", player=player)
        self.wait(f"StoneAgeWebClient.app.battleState.serverTurnNo > {after['turn']} && StoneAgeWebClient.battleCommandAllowed('G') && StoneAgeWebClient.battleCommandAllowed('W|FF|FF')")
        next_turn = self.battle()
        self.save(label + "-next-turn.json", next_turn)
        assert not next_turn["player"] and not next_turn["pet"]
        return before["match"]

    def invite(self, leader, target):
        selected = next(p for p in self.cli("ladder", "contacts", player=leader)["contacts"]
                        if p["name"] == f"LadderQA{target:02d}")
        self.cli("ladder", "invite", selected["slot"], selected["id"], player=leader)

    def multiplayer(self):
        assert self.ready.get("scenario") == "multiplayer", "requires ten-player fixture"
        self.run_browser("set", "viewport", "1280", "900")
        self.login()
        self.panel()
        assert self.evaluate("StoneAgeLadder.state.envelope.snapshot.phase") == "idle", "use a fresh fixture"
        ids = {0: self.evaluate("StoneAgeLadder.state.envelope.snapshot.self.id")}
        ids.update({i: self.cli("ladder", "status", player=i)["snapshot"]["self"]["id"] for i in range(1, 10)})
        completed = []
        for mode in range(2, 6):
            participants = list(range(mode * 2))
            strategy = "manual" if mode == 5 else "basic"
            self.panel()
            self.run_browser("select", '[aria-label="天梯模式"]', str(mode))
            self.click("创建队伍" if mode == 2 else "更改模式")
            self.wait(f"StoneAgeLadder.state.envelope.snapshot.room?.mode === {mode} && !StoneAgeLadder.state.busy")
            self.click("刷新名片")
            target = mode * 2 - 2
            self.wait(f"StoneAgeLadder.state.contacts.some(p=>p.id === {json.dumps(ids[target])})")
            selected = self.evaluate(f"StoneAgeLadder.state.contacts.find(p=>p.id === {json.dumps(ids[target])})")
            selected_value = f"{selected['slot']}:{selected['id']}"
            self.run_browser("select", '[aria-label="从名片邀请"]', selected_value)
            # Exercise a real asynchronous contacts refresh after selection.
            # The stable slot+identity choice must survive DOM reconstruction.
            self.evaluate("(async()=>{await StoneAgeLadder.refreshContacts();return true;})()")
            assert self.evaluate("document.querySelector('[aria-label=\"从名片邀请\"]').value") == selected_value
            self.click("邀请")
            self.wait("!StoneAgeLadder.state.busy")
            invitation = self.cli("ladder", "status", player=target)["snapshot"]["invitations"]
            assert len(invitation) == 1 and invitation[0]["from"]["id"] == ids[0]
            self.cli("ladder", "accept", invitation[0]["id"], player=target)
            self.wait(f"StoneAgeLadder.state.envelope.snapshot.room.members.length === {mode}")
            self.cli("ladder", "create" if mode == 2 else "mode", mode)
            self.invite(1, mode * 2 - 1)
            invitation = self.cli("ladder", "status", player=mode * 2 - 1)["snapshot"]["invitations"]
            self.cli("ladder", "accept", invitation[0]["id"], player=mode * 2 - 1)
            ratings = {i: self.cli("ladder", "status", player=i)["snapshot"]["ratings"]
                       for i in participants if i}
            self.strategy(strategy)
            for i in participants[1:]:
                self.cli("ladder", "strategy", strategy, player=i)
                self.cli("ladder", "ready", player=i)
            self.cli("ladder", "queue")
            self.monitor()
            self.web_ready_queue()
            self.wait("StoneAgeLadder.state.envelope.snapshot.phase === 'countdown'", timeout=150)
            countdown = self.evaluate("StoneAgeLadder.state.envelope.snapshot")
            self.save(f"{mode}v{mode}-countdown.json", countdown)
            roster = {p["id"] for team in countdown["match"]["teams"] for p in team["members"]}
            assert countdown["match"]["mode"] == mode and roster == {ids[i] for i in participants}
            self.panel()
            self.run_browser("screenshot", str(self.artifacts / f"{mode}v{mode}-countdown.png"))
            self.click("关闭天梯面板")
            if mode == 5:
                assert self.reconnect(participants, "5v5-reconnect") == countdown["match"]["id"]
                self.monitor()
                self.strategy("basic")
                self.click("关闭天梯面板")
                for i in participants[1:]:
                    self.cli("ladder", "strategy", "basic", player=i)
            room = self.finish_match(f"{mode}v{mode}-result", countdown["match"]["id"], mode=mode)
            result = self.evaluate("StoneAgeLadder.state.envelope.snapshot.result")
            assert {p["id"] for p in result["members"]} == roster
            for i in participants[1:]:
                snapshot = self.cli("ladder", "status", player=i)["snapshot"]
                assert snapshot["result"] == result
                assert all(snapshot["ratings"][n] == ratings[i][n] for n in range(5) if n != mode - 1)
            if mode == 5:
                self.run_browser("set", "viewport", "375", "812")
                assert self.evaluate("(()=>{const p=document.querySelector('#ladder-panel');return p.scrollWidth<=p.clientWidth;})()")
                self.run_browser("screenshot", str(self.artifacts / "5v5-mobile-result.png"))
            self.acknowledge()
            assert self.evaluate("StoneAgeLadder.state.envelope.snapshot.room.id") == room
            for i in range(2, mode * 2, 2):
                self.cli("ladder", "ack", player=i)
            for i in range(2, mode * 2, 2):
                self.cli("ladder", "ready", player=i)
            self.web_ready_queue()
            assert self.cli("ladder", "status")["snapshot"]["phase"] == "result"
            self.wait("StoneAgeLadder.state.envelope.snapshot.phase === 'queued'")
            self.panel()
            self.click("取消匹配")
            self.wait("StoneAgeLadder.state.envelope.snapshot.phase === 'lobby' && !StoneAgeLadder.state.busy")
            for i in range(1, mode * 2, 2):
                self.cli("ladder", "ack", player=i)
            completed.append(mode)
            print(f"PASS: {mode}v{mode} UI invitations, full roster, equal results and independent requeue", flush=True)
        # The browser also receives invitations: leave the retained team,
        # decline once, then accept a fresh invitation from its new leader.
        self.panel()
        self.click("退出队伍")
        self.wait("!StoneAgeLadder.state.envelope.snapshot.room && !StoneAgeLadder.state.busy")
        room = self.cli("ladder", "status", player=2)["snapshot"]["room"]
        leader = next(i for i, ident in ids.items() if ident == room["leader_id"])
        self.invite(leader, 0)
        self.wait("StoneAgeLadder.state.envelope.snapshot.invitations?.length === 1")
        self.click("拒绝")
        self.wait("(StoneAgeLadder.state.envelope.snapshot.invitations || []).length === 0 && !StoneAgeLadder.state.busy")
        self.invite(leader, 0)
        self.wait("StoneAgeLadder.state.envelope.snapshot.invitations?.length === 1")
        self.click("接受")
        self.wait("!!StoneAgeLadder.state.envelope.snapshot.room && !StoneAgeLadder.state.busy")
        assert self.evaluate("StoneAgeLadder.state.envelope.snapshot.room.id") == room["id"]
        errors = self.run_browser("errors")
        self.save("browser-errors.json", errors)
        assert not errors.strip(), errors
        self.save("passed.json", {"modes": completed, "web_send_accept_decline_invitations": True,
                  "five_player_same_battle_reconnect": True, "next_turn_unlocked": True,
                  "result_after_movie": True, "independent_ack_requeue": True,
                  "ten_player_mobile_result_no_horizontal_overflow": True})
        print("PASS: complete Web multiplayer fixture scenarios", flush=True)

    def start_manual_match(self, mode=1):
        participants = list(range(mode * 2))
        if mode > 1:
            assert self.ready.get("scenario") == "multiplayer"
        self.run_browser("set", "viewport", "1280", "900")
        self.login()
        self.panel()
        self.run_browser("select", '[aria-label="天梯模式"]', str(mode))
        self.click("创建队伍")
        self.wait("!!StoneAgeLadder.state.envelope.snapshot.room && !StoneAgeLadder.state.busy")
        if mode > 1:
            self.click("刷新名片")
            self.wait("StoneAgeLadder.state.contacts.length >= 4")
            for target in range(2, mode * 2, 2):
                selected = self.evaluate(f"StoneAgeLadder.state.contacts.find(p=>p.name === 'LadderQA{target:02d}')")
                self.run_browser("select", '[aria-label="从名片邀请"]', f"{selected['slot']}:{selected['id']}")
                self.click("邀请")
                self.wait("!StoneAgeLadder.state.busy")
                invitation = self.cli("ladder", "status", player=target)["snapshot"]["invitations"][0]
                self.cli("ladder", "accept", invitation["id"], player=target)
                self.wait(f"StoneAgeLadder.state.envelope.snapshot.room.members.length === {target//2+1}")
        self.strategy("manual")
        self.cli("ladder", "create", mode)
        for target in range(3, mode * 2, 2):
            self.invite(1, target)
            invitation = self.cli("ladder", "status", player=target)["snapshot"]["invitations"][0]
            self.cli("ladder", "accept", invitation["id"], player=target)
        for player in participants[1:]:
            self.cli("ladder", "strategy", "manual", player=player)
            self.cli("ladder", "ready", player=player)
        self.cli("ladder", "queue")
        self.web_ready_queue()
        self.wait("StoneAgeWebClient.app.battle && !StoneAgeWebClient.app.battleState.entryPending && !StoneAgeWebClient.app.battleState.ladderAwaitingCommands", timeout=60)
        return participants

    def cross_client(self, mode=1):
        participants = self.start_manual_match(mode)
        self.evaluate("(async()=>{await StoneAgeWebClient.send('B',['G']);await StoneAgeWebClient.send('B',['W|FF|FF']);return true;})()")
        self.wait("StoneAgeWebClient.app.battleState.ladderPlayerSubmitted && StoneAgeWebClient.app.battleState.ladderPetSubmitted")
        before = self.battle()
        self.save("cross-web-before.json", before)
        self.evaluate("(async()=>{await StoneAgeWebClient.app.transport.close();return true;})()")
        if mode > 1:
            teammate_before = self.cli_battle(2)
            teammate_budget = self.cli("ladder", "status", player=2)["snapshot"]["self"]["reconnect_remaining_ms"]
            self.cli("stop", player=2)
            # Both the browser player and teammate are now offline. Verify
            # that overlap from the independent opponent's server snapshot.
            roster = self.cli("ladder", "status")["snapshot"]["match"]["teams"]
            offline = [p for team in roster for p in team["members"] if not p["online"]]
            assert len(offline) == 2, offline
            self.save("mixed-offline.json", roster)
        self.start_cli(0)
        battle = self.cli_battle(0)
        state = self.cli("ladder", "status", player=0)["snapshot"]
        self.save("cross-cli-after.json", {"battle": battle, "ladder": state})
        assert battle["LadderID"] == before["match"] and battle["MyNo"] == before["myNo"]
        assert battle["Turn"] == before["turn"]
        assert battle["PlayerSubmitted"] and battle["PetSubmitted"] and not battle["CommandReady"]
        assert 0 < state["self"]["reconnect_remaining_ms"] < before["budget"]
        if mode > 1:
            self.start_cli(2)
            teammate_after = self.cli_battle(2)
            teammate_state = self.cli("ladder", "status", player=2)["snapshot"]
            for key in ("LadderID", "MyNo", "Turn", "PlayerSubmitted", "PetSubmitted"):
                assert teammate_after[key] == teammate_before[key], (key, teammate_before, teammate_after)
            assert 0 < teammate_state["self"]["reconnect_remaining_ms"] < teammate_budget
            self.save("mixed-teammate.json", {"before": teammate_before, "after": teammate_after,
                "budget_before_ms": teammate_budget, "ladder": teammate_state})
        self.stop_cli(0)
        self.login()
        self.wait("StoneAgeWebClient.app.battle && !!StoneAgeWebClient.app.battleState.lastLadderCommands && !StoneAgeWebClient.app.battleState.entryPending")
        after = self.battle()
        self.save("cross-web-after.json", after)
        for key in ("match", "turn", "myNo", "player", "pet"):
            assert after[key] == before[key], (key, before, after)
        assert not after["playerAllowed"] and not after["petAllowed"]
        assert 0 < after["budget"] < state["self"]["reconnect_remaining_ms"]
        for player in participants[1:]:
            self.cli("battle", "G", player=player)
            self.cli("battle", "W|FF|FF", player=player)
        self.wait(f"StoneAgeWebClient.app.battleState.serverTurnNo > {after['turn']} && StoneAgeWebClient.battleCommandAllowed('G') && StoneAgeWebClient.battleCommandAllowed('W|FF|FF')")
        self.save("cross-next-turn.json", self.battle())
        self.monitor()
        self.strategy("basic")
        self.click("关闭天梯面板")
        for player in participants[1:]:
            self.cli("ladder", "strategy", "basic", player=player)
        self.finish_match("cross-client-result", before["match"], mode=mode)
        result = self.evaluate("StoneAgeLadder.state.envelope.snapshot.result")
        for player in participants[1:]:
            assert self.cli("ladder", "result", player=player)["snapshot"]["result"] == result
        for player in participants[1:]:
            self.cli("ladder", "ack", player=player)
        self.acknowledge()
        self.save("passed.json", {"mode": mode, "mixed_disconnect": mode > 1,
            "web_to_sactl_to_web": True, "same_match_seat_turn": True,
            "accepted_commands_retained": True, "cumulative_budget": True,
            "next_turn_unlocked": True, "original_match_settled": True})
        print("PASS: real Web -> sactl -> Web same-character battle continuation", flush=True)

    def restart_gmsv(self):
        restarted = self.work / "web-restarted.json"
        previous = json.loads(restarted.read_text())["restarts"] if restarted.exists() else 0
        subprocess.run(["docker", "kill", "--signal=USR1", self.args.container], check=True, capture_output=True)
        deadline = time.monotonic() + 20
        while not restarted.exists() or json.loads(restarted.read_text())["restarts"] <= previous:
            assert time.monotonic() < deadline, "fixture did not restart GMSV"
            time.sleep(.2)

    def receipts(self):
        self.login()
        self.panel()
        assert self.evaluate("StoneAgeLadder.state.envelope.snapshot.phase") == "idle"
        # Execute the real authenticated write, then lose only its HTTP reply.
        # Ordered gameplay packets and the server's durable receipt are real.
        self.evaluate("""(()=>{const original=window.fetch;window.fetch=async(...args)=>{
          const request=args[1]?.body&&JSON.parse(args[1].body);
          const response=await original(...args);
          if(request?.request?.operation==='create'){
            window.fetch=original;window.ladderReceiptQA={request:request.request,reply:await response.clone().json()};
            throw new Error('fixture: lost committed HTTP response');
          }return response;
        };return true;})()""")
        self.click("创建队伍")
        self.wait("!!StoneAgeLadder.state.pending && !StoneAgeLadder.state.busy && !!window.ladderReceiptQA")
        before = self.evaluate("window.ladderReceiptQA")
        assert before["reply"]["ok"] and before["reply"]["snapshot"]["room"]
        self.save("committed-response.json", before)
        self.restart_gmsv()
        self.login()
        self.panel()
        self.wait("!!StoneAgeLadder.state.pending")
        restored = self.evaluate("({pending:StoneAgeLadder.state.pending,snapshot:StoneAgeLadder.state.envelope.snapshot})")
        assert restored["pending"] == before["request"]
        assert restored["snapshot"]["phase"] == "idle" and not restored["snapshot"].get("room")
        self.save("restored-request.json", restored)
        self.click("重试原请求")
        self.wait("!StoneAgeLadder.state.pending && !StoneAgeLadder.state.busy")
        after = self.evaluate("({reply:StoneAgeLadder.state.envelope,notice:StoneAgeLadder.state.notice,error:StoneAgeLadder.state.error})")
        assert after["reply"]["ok"] and after["reply"]["replay"]
        assert after["reply"]["server_boot"] != after["reply"]["receipt_boot"]
        assert not after["reply"]["snapshot"].get("room")
        assert "重启前" in after["notice"] and not after["error"]
        self.save("historical-receipt.json", after)
        self.run_browser("screenshot", str(self.artifacts / "historical-receipt.png"))
        self.save("passed.json", {"real_committed_write": True, "gmsv_restart": True,
                  "page_reload_restores_exact_request": True, "historical_retry_does_not_recreate_room": True})
        print("PASS: browser restores pending request and historical receipt after GMSV restart", flush=True)

    def server_crash(self):
        participants = self.start_manual_match(5)
        before = self.battle()
        ratings = {0: self.evaluate("StoneAgeLadder.state.envelope.snapshot.ratings")}
        ratings.update({i: self.cli("ladder", "status", player=i)["snapshot"]["ratings"] for i in participants[1:]})
        self.save("crash-before.json", before)
        # Commit actual battle commands before killing the real game process.
        self.evaluate("(async()=>{await StoneAgeWebClient.send('B',['H|FF']);await StoneAgeWebClient.send('B',['W|FF|FF']);return true;})()")
        for i in participants[1:]:
            self.cli("battle", "H|FF", player=i)
            self.cli("battle", "W|FF|FF", player=i)
        self.wait(f"StoneAgeWebClient.app.battleState.serverTurnNo > {before['turn']}", timeout=60)
        self.restart_gmsv()
        self.login()
        self.wait("StoneAgeLadder.state.envelope.snapshot.phase === 'result'")
        self.wait("!StoneAgeWebClient.app.battle && !StoneAgeWebClient.app.mapLoading", timeout=90)
        # Recovery should announce the result without requiring a hidden panel
        # to be manually opened or a nonexistent final battle movie to finish.
        self.wait("StoneAgeLadder.state.open && !document.querySelector('#ladder-panel').hidden")
        evidence = self.evaluate("({snapshot:StoneAgeLadder.state.envelope.snapshot,phase:StoneAgeWebClient.app.phase,text:document.querySelector('#ladder-panel').innerText})")
        result = evidence["snapshot"]["result"]
        assert result["id"] == before["match"] and result["mode"] == 5 and len(result["members"]) == 10
        assert result["reason"] == "server_restart" and not result["rated"] and result["statistics_incomplete"]
        assert evidence["snapshot"]["ratings"] == ratings[0] and evidence["phase"] == "world"
        assert "本场不计积分" in evidence["text"] and "统计未恢复" in evidence["text"]
        assert "总伤害" not in evidence["text"] and "最后一回合播放中" not in evidence["text"]
        for i in participants[1:]:
            state = self.cli("ladder", "status", player=i)["snapshot"]
            assert state["result"] == result and state["ratings"] == ratings[i]
        self.save("crash-recovered.json", evidence)
        self.run_browser("set", "viewport", "375", "812")
        assert self.evaluate("(()=>{const p=document.querySelector('#ladder-panel');return p.scrollWidth<=p.clientWidth;})()")
        self.run_browser("screenshot", str(self.artifacts / "crash-mobile.png"))
        self.click("确认结算")
        self.wait("StoneAgeLadder.state.envelope.snapshot.phase === 'idle' && !StoneAgeLadder.state.busy")
        self.click("创建队伍")
        self.wait("StoneAgeLadder.state.envelope.snapshot.phase === 'lobby' && !StoneAgeLadder.state.busy")
        self.save("passed.json", {"real_5v5_gmsv_crash": True, "reauthenticated_result_notification": True,
                  "statistics_unknown_not_zero": True, "same_result_all_ten": True, "ratings_unchanged": True,
                  "mobile_no_overflow": True, "acknowledge_and_create_again": True})
        print("PASS: Web reauthentication after real 5v5 GMSV crash shows unrated result and resumes lobby", flush=True)

    def configuration_drafts(self):
        self.login()
        self.panel()
        state = self.evaluate("StoneAgeLadder.state.envelope.snapshot")
        assert state["phase"] in ("idle", "lobby")
        if not state.get("room"):
            self.click("创建队伍")
            self.wait("!!StoneAgeLadder.state.envelope.snapshot.room && !StoneAgeLadder.state.busy")
        self.run_browser("select", '[aria-label="天梯模式"]', "3")
        self.evaluate("(async()=>{await StoneAgeLadder.refreshContacts();return true;})()")
        assert self.evaluate("document.querySelector('[aria-label=\"天梯模式\"]').value") == "3"
        self.click("更改模式")
        self.wait("StoneAgeLadder.state.envelope.snapshot.room.mode === 3 && !StoneAgeLadder.state.busy")
        # The fixture owns one pet. An explicit zero mask is a valid saved
        # configuration draft, though readiness separately requires the active
        # pet to be registered. Check the real acknowledged save of zero.
        assert self.evaluate("StoneAgeLadder.state.envelope.snapshot.self.available_pet_mask") == 1
        selector = '#ladder-panel fieldset input[value="0"]'
        self.run_browser("uncheck", selector)
        self.evaluate("(async()=>{await StoneAgeLadder.refreshContacts();return true;})()")
        assert not self.evaluate("document.querySelector('#ladder-panel fieldset input').checked")
        self.click("保存参战宠物")
        self.wait("StoneAgeLadder.state.envelope.snapshot.self.pet_mask === 0 && !StoneAgeLadder.state.busy")
        assert self.evaluate("StoneAgeLadder.state.envelope.code") == "ok"
        self.save("draft-zero-acknowledged.json", self.evaluate("StoneAgeLadder.state.envelope"))
        self.run_browser("screenshot", str(self.artifacts / "draft-zero.png"))
        self.run_browser("check", selector)
        self.click("保存参战宠物")
        self.wait("StoneAgeLadder.state.envelope.snapshot.self.pet_mask === 1 && !StoneAgeLadder.state.busy")
        self.save("passed.json", {"mode_survives_real_refresh": True, "mode_saved": 3,
                  "empty_pet_selection_survives_real_refresh": True, "zero_mask_acknowledged": True,
                  "pet_selection_restored": True})
        print("PASS: real browser mode and zero-pet drafts survive refresh and submit the selected configuration", flush=True)

    def refused_receipts(self, evicted=False):
        self.login()
        self.panel()
        state = self.evaluate("StoneAgeLadder.state.envelope.snapshot")
        assert state["phase"] in ("idle", "lobby")
        self.run_browser("select", '[aria-label="天梯模式"]', "1")
        self.click("更改模式" if state.get("room") else "创建队伍")
        self.wait("StoneAgeLadder.state.envelope.snapshot.room?.mode === 1 && !StoneAgeLadder.state.busy")
        self.run_browser("uncheck", '#ladder-panel fieldset input[value="0"]')
        self.click("保存参战宠物")
        self.wait("StoneAgeLadder.state.envelope.snapshot.self.pet_mask === 0 && !StoneAgeLadder.state.busy")
        self.evaluate("""(()=>{const original=window.fetch;window.fetch=async(...args)=>{
          const request=args[1]?.body&&JSON.parse(args[1].body);
          const response=await original(...args);
          if(request?.request?.operation==='ready'){
            window.fetch=original;window.ladderReceiptQA={request:request.request,reply:await response.clone().json()};
            throw new Error('fixture: lost refused HTTP response');
          }return response;
        };return true;})()""")
        self.click("准备")
        self.wait("!!StoneAgeLadder.state.pending && !StoneAgeLadder.state.busy && !!window.ladderReceiptQA")
        original = self.evaluate("window.ladderReceiptQA")
        assert not original["reply"]["ok"] and original["reply"]["code"] == "invalid_loadout"
        self.save("refused-response.json", original)
        self.restart_gmsv()
        if evicted:
            # Another authorized client advances the same character's receipt
            # window while the browser retains its unanswered original request.
            self.start_cli(0)
            for n in range(34):
                self.cli("ladder", "strategy", "manual" if n % 2 else "basic", player=0)
            self.stop_cli(0)
        self.login()
        self.panel()
        self.wait("!!StoneAgeLadder.state.pending")
        assert self.evaluate("StoneAgeLadder.state.pending") == original["request"]
        assert self.evaluate("StoneAgeLadder.state.envelope.snapshot.phase") == "idle"
        self.click("重试原请求")
        self.wait("!StoneAgeLadder.state.pending && !StoneAgeLadder.state.busy")
        after = self.evaluate("({reply:StoneAgeLadder.state.envelope,notice:StoneAgeLadder.state.notice,error:StoneAgeLadder.state.error})")
        assert not after["reply"]["ok"] and after["error"]
        assert after["reply"]["code"] == ("stale_revision" if evicted else "invalid_loadout")
        assert after["reply"]["request_id"] == original["request"]["request_id"]
        assert not after["reply"]["snapshot"].get("room")
        if not evicted:
            assert after["reply"]["replay"] and after["reply"]["server_boot"] != after["reply"]["receipt_boot"]
            assert "重启前" in after["notice"]
        self.save("retry-result.json", after)
        self.run_browser("screenshot", str(self.artifacts / "retry-result.png"))
        self.save("passed.json", {"real_refusal_lost_response": True, "gmsv_restart": True,
                  "exact_pending_request_restored": True, "receipt_evicted": evicted,
                  "explicit_refusal_after_retry": after["reply"]["code"], "no_room_recreated": True})
        print("PASS: browser restores refused request after restart; retry returns", after["reply"]["code"], flush=True)

    def run(self):
        if self.args.scenario in ("receipts-refused", "receipts-evicted"):
            return self.refused_receipts(self.args.scenario == "receipts-evicted")
        if self.args.scenario == "configuration-drafts":
            return self.configuration_drafts()
        if self.args.scenario == "server-crash":
            return self.server_crash()
        if self.args.scenario == "receipts":
            return self.receipts()
        if self.args.scenario == "cross-client-5":
            return self.cross_client(5)
        if self.args.scenario == "cross-client":
            return self.cross_client()
        if self.args.scenario == "multiplayer":
            return self.multiplayer()
        self.login()
        self.panel()
        phase = self.evaluate("StoneAgeLadder.state.envelope.snapshot.phase")
        if phase == "result":
            self.acknowledge()
        else:
            assert phase in ("idle", "lobby"), "fixture already has a live match"
        other = self.cli("ladder", "status")["snapshot"]
        if other["phase"] == "result":
            self.cli("ladder", "ack")
        assert self.cli("ladder", "status")["snapshot"]["phase"] in ("idle", "lobby")
        if not self.evaluate("!!StoneAgeLadder.state.envelope.snapshot.room"):
            self.click("创建队伍")
            self.wait("!!StoneAgeLadder.state.envelope.snapshot.room && !StoneAgeLadder.state.busy")
        if not self.cli("ladder", "status")["snapshot"]["room"]:
            self.cli("ladder", "create", 1)
        self.strategy("basic")
        for args in [("strategy", "basic"), ("ready",), ("queue",)]:
            self.cli("ladder", *args)
        self.monitor()
        self.web_ready_queue()
        room = self.finish_match("automatic-match")
        self.acknowledge()
        assert self.evaluate("StoneAgeLadder.state.envelope.snapshot.room.id") == room
        self.strategy("manual")
        # One team can queue while the other still holds its previous result.
        self.web_ready_queue()
        assert self.cli("ladder", "status")["snapshot"]["phase"] == "result"
        self.cli("ladder", "ack")
        for args in [("strategy", "manual"), ("ready",), ("queue",)]:
            self.cli("ladder", *args)
        self.wait("StoneAgeWebClient.app.battle && !StoneAgeWebClient.app.battleState.entryPending && !StoneAgeWebClient.app.battleState.ladderAwaitingCommands", timeout=150)
        self.evaluate("(async()=>{await StoneAgeWebClient.send('B',['G']);await StoneAgeWebClient.send('B',['W|FF|FF']);return true;})()")
        self.wait("StoneAgeWebClient.app.battleState.ladderPlayerSubmitted && StoneAgeWebClient.app.battleState.ladderPetSubmitted")
        before = self.battle()
        self.save("reconnect-before.json", before)
        self.evaluate("(async()=>{await StoneAgeWebClient.app.transport.close();return true;})()")
        self.login()
        self.wait("StoneAgeWebClient.app.battle && !!StoneAgeWebClient.app.battleState.lastLadderCommands && !StoneAgeWebClient.app.battleState.entryPending")
        after = self.battle()
        self.save("reconnect-after.json", after)
        for key in ("match", "turn", "myNo", "player", "pet"):
            assert before[key] == after[key], (key, before, after)
        assert before["transport"] != after["transport"]
        assert after["player"] and after["pet"] and not after["playerAllowed"] and not after["petAllowed"]
        assert 0 < after["budget"] < before["budget"]
        self.cli("battle", "G")
        self.cli("battle", "W|FF|FF")
        self.wait(f"StoneAgeWebClient.app.battleState.serverTurnNo > {after['turn']} && StoneAgeWebClient.battleCommandAllowed('G') && StoneAgeWebClient.battleCommandAllowed('W|FF|FF')")
        next_turn = self.battle()
        self.save("reconnect-next-turn.json", next_turn)
        assert not next_turn["player"] and not next_turn["pet"]
        self.monitor()
        self.strategy("basic")
        self.click("关闭天梯面板")
        self.cli("ladder", "strategy", "basic")
        assert self.finish_match("reconnected-match", before["match"]) == room
        self.run_browser("set", "viewport", "375", "812")
        assert self.evaluate("(()=>{const p=document.querySelector('#ladder-panel');return p.scrollWidth<=p.clientWidth;})()")
        self.run_browser("screenshot", str(self.artifacts / "mobile-result.png"))
        self.acknowledge()
        self.cli("ladder", "ack")
        self.save("passed.json", {"automatic_match": True, "same_battle_reconnect": True,
            "accepted_commands_after_entrance": True, "next_turn_unlocked": True,
            "result_after_movie": True, "independent_ack_requeue": True, "retained_room": room,
            "mobile_result_no_horizontal_overflow": True})
        print("PASS: complete Web 1v1 fixture scenarios", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--work", type=Path, required=True)
    parser.add_argument("--browser", type=Path, required=True)
    parser.add_argument("--url", required=True)
    parser.add_argument("--container", required=True)
    parser.add_argument("--sactl", required=True, help="fixture sactl binary inside its container")
    parser.add_argument("--name", default="browser-acceptance")
    parser.add_argument("--scenario", choices=("basic", "multiplayer", "cross-client", "cross-client-5", "receipts", "server-crash", "configuration-drafts", "receipts-refused", "receipts-evicted"), default="basic")
    args = parser.parse_args()
    if not args.name or Path(args.name).name != args.name or args.name in (".", ".."):
        parser.error("name must be one new artifact directory name")
    test = BrowserTest(args)
    try:
        test.run()
    except Exception:
        try:
            test.save("failure-state.json", test.evaluate("""(()=>{const a=window.StoneAgeWebClient?.app;
              return {phase:a?.phase,battle:a?.battle,ladder:window.StoneAgeLadder?.state.envelope,
                error:window.StoneAgeLadder?.state.error,samples:window.ladderQA?.samples||[],clicks:window.ladderQA?.clicks||[]};})()"""))
        except Exception:
            pass
        raise
    finally:
        for player in list(test.managed_daemons):
            test.stop_cli(player)


if __name__ == "__main__":
    main()
