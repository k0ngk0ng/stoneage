#!/usr/bin/env python3
"""Real authenticated sactl -> gateway -> GMSV -> SAAC ladder regression.

Run in an isolated Linux environment (the cached toolchain container with
--network=none works). Supply built binaries; this script never downloads,
builds, or contacts production. All state goes into a NEW directory in build/.
It covers 1v1, accepted-action and automatic-strategy reconnect, a GMSV crash,
2v2 through 5v5 via real card exchange/invitations, and archived/replaced card
identity checks. It is not a Web acceptance test.
"""
import argparse
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import time

ROOT = Path(__file__).resolve().parents[4]


class Fixture:
    def __init__(self, args):
        self.work = args.work.resolve()
        if not self.work.is_relative_to(ROOT / "build"):
            raise ValueError("fixture must be inside this repository's build directory")
        self.work.mkdir(parents=True, exist_ok=False)
        self.binaries = {name: getattr(args, name).resolve() for name in ("sactl", "gateway", "gmsv", "saac", "seed")}
        self.processes, self.logs, self.completed = {}, [], []
        self.scenario = args.scenario
        self.player_count = 2 if self.scenario in ("basic", "reconnect", "receipts", "cross-map") else 10
        self.env = dict(os.environ, TMPDIR=str(self.work / "tmp"), XDG_STATE_HOME=str(self.work / "state"),
                        XDG_CONFIG_HOME=str(self.work / "state"), STONEAGE_PLAYER_ADMIN_DIR=str(self.work / "admin"),
                        STONEAGE_LADDER_DB=str(self.work / "ladder.db"), STONEAGE_GMSV_TRUSTED_GATEWAY_HOST="127.0.0.1",
                        STONEAGE_GATEWAY_ROUTES="", STONEAGE_GATEWAY_TRUSTED_PROXY_HOSTS="", PYTHONDONTWRITEBYTECODE="1")
        for name in ("tmp", "state", "admin", "logs", "saac", "gmsv"):
            (self.work / name).mkdir()
        # Reserve three distinct ephemeral ports until all have been selected.
        probes = [socket.socket() for _ in range(3)]
        try:
            for probe in probes:
                probe.bind(("127.0.0.1", 0))
            self.saac_port, self.gmsv_port, self.gateway_port = [p.getsockname()[1] for p in probes]
        finally:
            for probe in probes:
                probe.close()

    def start(self, name, argv, directory=None):
        previous = self.processes.get(name)
        assert previous is None or previous.poll() is not None, f"{name} is still running"
        log = (self.work / "logs" / (name + ".log")).open("ab")
        self.logs.append(log)
        process = subprocess.Popen(list(map(str, argv)), cwd=directory or self.work, env=self.env,
                                   stdout=log, stderr=subprocess.STDOUT)
        self.processes[name] = process
        return process

    def kill(self, name):
        process = self.processes[name]
        assert process.poll() is None, f"{name} already exited"
        process.kill()
        process.wait(timeout=5)

    def close(self):
        for process in reversed(list(self.processes.values())):
            if process.poll() is None:
                process.terminate()
        for process in reversed(list(self.processes.values())):
            try:
                process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)
        for log in self.logs:
            log.close()

    def wait_for(self, description, predicate, timeout=30):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            value = predicate()
            if value:
                return value
            time.sleep(.1)
        raise AssertionError(description + " timed out")

    def listener(self, port, name):
        def ready():
            assert self.processes[name].poll() is None, f"{name} exited; inspect fixture logs"
            try:
                with socket.create_connection(("127.0.0.1", port), timeout=.1):
                    return True
            except OSError:
                return False
        self.wait_for(name + " listener", ready)

    def prepare(self):
        source = ROOT / "server/legacy/source/2.5/gmsv"
        shutil.copytree(source / "data", self.work / "gmsv/data")
        for name in ("log", "Dengon", "Schedule"):
            (self.work / "gmsv" / name).mkdir()
        shutil.copyfile(source / "log.cf", self.work / "gmsv/log/log.cf")
        shutil.copyfile(source / "badpetstring.txt", self.work / "gmsv/badpetstring.txt")
        for name in ("char", "char_sleep", "log", "lock", "db", "mail", "family", "fmpoint", "fmsmemo"):
            (self.work / "saac" / name).mkdir()
        (self.work / "saac/acserv.cf").write_text(
            f"port {self.saac_port}\npass ladder-fixture\nlogdir log\nlockdir lock\nchardir char\ndbdir db\n"
            "maildir mail\nfamilydir family\nfmpointdir fmpoint\nfmsmemodir fmsmemo\nrotate_interval 604800\n"
            "Total_Charlist 3600\nExpired_mail 600\nDel_Family_or_Member 3600\nWrite_Family 600\nSameIpMun 32\n")
        overrides = {"acserv": "127.0.0.1", "acservport": str(self.saac_port), "acpasswd": "ladder-fixture",
                     "port": str(self.gmsv_port), "gameservname": "ladder-fixture", "gameservid": "ladder-fixture",
                     "usememoryunitnum": "8000000"}
        config = []
        for line in (ROOT / "config/gmsv/setup.cf.example").read_text().splitlines():
            key = line.split("=", 1)[0]
            config.append(key + "=" + overrides[key] if key in overrides else line)
        (self.work / "gmsv/setup.cf").write_text("\n".join(config) + "\n")
        subprocess.run([self.binaries["seed"], self.work], env=self.env, check=True)
        for i in range(self.player_count):
            user = f"ladderqa{i:02d}"
            config = {"socket_path": str(self.work / (user + ".sock")), "transport": "tcp",
                      "address": f"127.0.0.1:{self.gateway_port}", "account": user,
                      "password_file": str(self.work / (user + ".password")), "map_directory": str(self.work / "gmsv/data")}
            (self.work / (user + ".toml")).write_text("".join(f"{k} = {json.dumps(v)}\n" for k, v in config.items()))
        self.start("saac", [self.binaries["saac"]], self.work / "saac")
        self.listener(self.saac_port, "saac")
        self.start_gmsv()
        self.start("gateway", [self.binaries["gateway"], "-listen", f"127.0.0.1:{self.gateway_port}",
                               "-upstream", f"127.0.0.1:{self.gmsv_port}", "-auth-db", self.work / "auth.db", "-auth-required"])
        self.listener(self.gateway_port, "gateway")
        subprocess.run([self.binaries["sactl"], "version"], env=self.env, check=True)
        help_result = subprocess.run([self.binaries["sactl"], "--help"], env=self.env, check=True, capture_output=True)
        (self.work / "sactl-help.txt").write_bytes(help_result.stdout)
        for i in range(self.player_count):
            self.start_client(i)
            self.wait_for("authenticated character list", lambda: self.cli(i, "chars", check=False).get("ok"))
            self.cli(i, "create-character", f"LadderQA{i:02d}")
            self.cli(i, "enter", f"LadderQA{i:02d}")
            self.wait_for("world status", lambda: self.observe(i)["Player"]["HasStatus"])
            assert self.status(i)["self"]["idle"], "inert login window blocked ladder preparation"
            config = self.work / f"ladderqa{i:02d}.toml"
            with config.open("a") as stream:
                stream.write(f'character = "LadderQA{i:02d}"\n')

    def start_gmsv(self):
        self.start("gmsv", [self.binaries["gmsv"], "-f", "setup.cf"], self.work / "gmsv")
        self.listener(self.gmsv_port, "gmsv")

    def start_client(self, i):
        path = self.work / f"ladderqa{i:02d}.sock"
        self.start(f"client{i}", [self.binaries["sactl"], "serve", "--config", self.work / f"ladderqa{i:02d}.toml"])
        def ready():
            if not path.exists():
                return False
            with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as probe:
                try:
                    probe.connect(str(path))
                    return True
                except OSError:
                    return False
        self.wait_for("sactl daemon", ready)

    def cli(self, i, *args, check=True):
        result = subprocess.run([self.binaries["sactl"], "--config", self.work / f"ladderqa{i:02d}.toml",
                                 "--json", *map(str, args)], env=self.env, capture_output=True, text=True, timeout=65)
        try:
            reply = json.loads(result.stdout)
        except ValueError:
            raise AssertionError(f"invalid CLI response for {args}: {result.stdout} {result.stderr}")
        with (self.work / "commands.jsonl").open("a") as stream:
            stream.write(json.dumps({"player": i, "args": args, "reply": reply}) + "\n")
        if check:
            assert result.returncode == 0 and reply["ok"], (args, reply)
        return reply

    def observe(self, i):
        return self.cli(i, "observe")["data"]

    def status(self, i):
        return self.cli(i, "ladder", "status")["data"]["snapshot"]

    def start_match(self, strategy):
        before = {}
        for i in (0, 1):
            # Prior scenarios may have restarted every authenticated session.
            # Do not capture the empty projection as a resource baseline.
            self.wait_for("pre-match login status", lambda: self.observe(i)["Player"]["HasStatus"])
            self.status(i)  # Ordered receipt fences the remaining login packets.
            before[i] = self.observe(i)
        for i in (0, 1):
            if not self.status(i)["room"]:
                self.cli(i, "ladder", "create", 1)
            self.cli(i, "ladder", "strategy", strategy)
            self.cli(i, "ladder", "ready")
            self.cli(i, "ladder", "queue")
        self.wait_for("countdown", lambda: self.status(0)["phase"] == "countdown")
        return before

    def ready_battle(self):
        def ready():
            obs = self.observe(0)
            return obs if obs["Battle"]["Active"] and obs["Battle"]["CommandReady"] and obs["Battle"]["BAReceived"] else None
        return self.wait_for("battle command phase", ready, 40)

    def finish(self, before, match=None):
        event = self.cli(0, "ladder", "wait", 0, "1s")["data"]
        deadline = time.monotonic() + 90
        while event["snapshot"]["snapshot"]["phase"] != "result":
            assert time.monotonic() < deadline, "battle failed to settle"
            event = self.cli(0, "ladder", "wait", event["cursor"], "2s", "--stream", event["stream"])["data"]
        for i in (0, 1):
            result = self.cli(i, "ladder", "result")["data"]["snapshot"]["result"]
            assert result["rated"] and result["mode"] == 1 and len(result["members"]) == 2
            assert match is None or result["id"] == match
            assert sum(p["rating_delta"] for p in result["members"]) == 0
            assert sum(p["statistics"]["damage"] for p in result["members"]) > 0
            self.resources_restored(i, before[i])
            self.cli(i, "ladder", "ack")
            state = self.status(i)
            assert state["phase"] == "lobby" and state["room"] and not state["self"]["ready"]

    def resources_restored(self, i, before):
        self.wait_for("restored character status", lambda: self.observe(i)["Player"]["HasStatus"])
        after = self.observe(i)
        for key in ("HP", "MP", "Gold"):
            assert after["Player"][key] == before["Player"][key], key
        assert after["Inventory"] == before["Inventory"]
        # Session identity changes on reconnect; actual pet resources do not.
        for field in ("HP", "MaxHP", "Name", "Level", "EXP", "Attack", "Defense", "Quick", "Skills"):
            assert [p[field] for p in after["Pets"]] == [p[field] for p in before["Pets"]], field

    def passed(self, name):
        self.completed.append(name)
        (self.work / "passed.json").write_text(json.dumps(self.completed, indent=2))
        print("PASS:", name, flush=True)

    def run(self):
        self.prepare()
        if self.scenario in ("all", "receipts"):
            self.durable_receipts()
        if self.scenario in ("all", "basic"):
            self.basic()
        if self.scenario in ("all", "basic", "reconnect"):
            self.auto_reconnect()
        if self.scenario in ("all", "multiplayer"):
            self.multiplayer()
        if self.scenario in ("all", "multi-reconnect"):
            self.multiplayer_reconnect()
        if self.scenario == "contacts":
            self.exchange_cards()
            self.contact_recreation()
        if self.scenario in ("all", "contact-restart"):
            self.contact_directory_restart()
        if self.scenario in ("all", "cross-map"):
            self.cross_map()

    def cross_map(self):
        for i in (0, 1):
            if self.status(i)["room"]:
                self.cli(i, "ladder", "leave")
        source_floor = self.observe(0)["Position"]["Floor"]
        # Choose a real destination from this fixture's authoritative map
        # relations rather than injecting coordinates or native state.
        relations = (self.work / "gmsv/data/map/mapwarp.txt").read_text(encoding="cp936")
        destinations = []
        for line in relations.splitlines():
            parts = line.split(":")
            if len(parts) == 5 and parts[:2] == ["NONE", "NULL"]:
                origin = list(map(int, parts[2].split(",")))
                target = list(map(int, parts[3].split(",")))
                if origin[0] == source_floor and target[0] != source_floor:
                    destinations.append(target[0])
        assert destinations, "fixture source floor has no unconditional outgoing warp"
        target_floor = destinations[0]
        self.cli(0, "warp", target_floor)
        self.wait_for("different map reached", lambda: self.observe(0)["Position"]["Floor"] == target_floor)
        positions = {i: self.observe(i)["Position"] for i in (0, 1)}
        assert positions[0]["Floor"] != positions[1]["Floor"]
        # A normal face-to-face DU request cannot select the remote character.
        self.cli(1, "duel", check=False)
        for i in (0, 1):
            self.status(i)
            assert not self.observe(i)["Battle"]["Active"]
        before = self.start_match("basic")
        battle = self.ready_battle()["Battle"]
        self.finish(before, battle["LadderID"])
        for i in (0, 1):
            after = self.observe(i)["Position"]
            for key in ("Floor", "X", "Y"):
                assert after[key] == positions[i][key], (i, positions[i], after)
        (self.work / "cross-map.json").write_text(json.dumps({"positions": positions, "match": battle["LadderID"]}, indent=2))
        self.passed("normal duel cannot target a remote map; real cross-map ladder combat settles without relocating either player")

    def contact_directory_restart(self):
        for i in range(10):
            if self.status(i)["room"]:
                self.cli(i, "ladder", "leave")
        self.exchange_cards()
        expected = {}
        for leader in (0, 1):
            expected[leader] = {p["id"] for p in self.cli(leader, "ladder", "contacts")["data"]["contacts"]}
            assert len(expected[leader]) >= 4
        for i in range(10):
            self.cli(i, "logout")
            self.kill(f"client{i}")
        self.kill("gmsv")
        self.kill("saac")
        self.start("saac", [self.binaries["saac"]], self.work / "saac")
        self.listener(self.saac_port, "saac")
        self.start_gmsv()
        for i in range(10):
            self.start_client(i)
            self.wait_for(f"contact directory restart login {i}", lambda: self.observe(i)["Player"]["HasStatus"], 40)
            self.status(i)
        for leader in (0, 1):
            entries = self.cli(leader, "ladder", "contacts")["data"].get("contacts") or []
            assert {p["id"] for p in entries} == expected[leader], (leader, entries, expected[leader])
            assert all(p["online"] for p in entries)
        self.passed("cold GMSV/SAAC restart preserves archived contact identities while the presence directory rebuilds")
        # An explicit deletion notification must still remove online owners'
        # cards; ordinary lookup failures must not emulate that notification.
        self.cli(8, "logout")
        self.kill("client8")
        config = self.work / "ladderqa08.toml"
        config.write_text("\n".join(line for line in config.read_text().splitlines() if not line.startswith("character =")) + "\n")
        self.start_client(8)
        self.cli(8, "delete-character", "LadderQA08", "LadderQA08")
        self.wait_for("explicit deleted contact", lambda: self.contact_slot(0, 8) is None)
        self.passed("explicit character deletion still removes the online owner's card after directory recovery")

    def durable_receipts(self):
        records = []
        for name, operation, argument, code in (
                ("refused", "ready", None, "not_in_room"),
                ("strategy", "strategy", "manual", "ok"),
                ("room", "create", 1, "ok")):
            revision = self.cli(0, "ladder", "status")["data"]["revision"]
            args = ["ladder", operation]
            if argument is not None:
                args.append(argument)
            args += ["--request-id", "durable_" + name, "--revision", revision]
            reply = self.cli(0, *args, check=code == "ok")["data"]
            assert reply["code"] == code and not reply["replay"], reply
            assert reply["server_boot"] == reply["receipt_boot"], reply
            records.append((args, reply))
        old_revision = records[-1][1]["revision"]
        old_boot = records[-1][1]["server_boot"]
        # Drop all client caches as well as the server process. Recovery must
        # come from SQLite through a newly authenticated game connection.
        for i in range(self.player_count):
            self.kill(f"client{i}")
        self.kill("gmsv")
        self.start_gmsv()
        for i in range(self.player_count):
            self.start_client(i)
        self.wait_for("receipt recovery authentication", lambda: self.cli(0, "ladder", "status", check=False).get("ok"), 40)
        current = self.cli(0, "ladder", "status")["data"]
        assert current["server_boot"] != old_boot and current["revision"] > old_revision, current
        assert current["snapshot"]["room"] is None and current["snapshot"]["self"]["strategy"] == "manual", current
        for args, original in records:
            replay = self.cli(0, *args, check=original["ok"])["data"]
            assert replay["replay"] and replay["code"] == original["code"], replay
            assert replay["request_wire"] == original["request_wire"], replay
            assert replay["applied_revision"] == original["applied_revision"], replay
            assert replay["receipt_boot"] == old_boot and replay["server_boot"] == current["server_boot"], replay
            assert replay["snapshot"]["room"] is None, replay
        room_args = list(records[-1][0])
        room_args[2] = 2
        self.expect_code(0, "request_conflict", *room_args[1:])
        self.passed("GMSV and sactl restart replay durable success/refusal without recreating transient room")
        # Evict the historical records through real authorized commands.
        for _ in range(33):
            self.cli(0, "ladder", "strategy", "manual")
        self.kill("client0")
        self.start_client(0)
        self.wait_for("receipt eviction authentication", lambda: self.cli(0, "ladder", "status", check=False).get("ok"), 40)
        for args, _ in records:
            self.expect_code(0, "stale_revision", *args[1:])
        assert self.status(0)["room"] is None
        self.passed("evicted durable receipts reject original revisions over authenticated network")

    def auto_reconnect(self):
        # Keep the opponent manual so the match cannot finish before the
        # reconnect checks. Player 0 must resume without another strategy write.
        before = self.start_match("manual")
        obs = self.ready_battle()
        match, turn = obs["Battle"]["LadderID"], obs["Battle"]["Turn"]
        self.cli(0, "ladder", "strategy", "basic")

        def submitted_after(minimum_turn):
            battle = self.observe(0)["Battle"]
            return (battle["LadderID"] == match and battle["Turn"] >= minimum_turn
                    and battle["PlayerSubmitted"] and battle["PetSubmitted"])

        self.wait_for("initial automatic commands", lambda: submitted_after(turn))
        self.kill("client0")
        time.sleep(1.5)
        self.start_client(0)
        # Both commands were accepted before disconnect, so CommandReady is
        # correctly false on rejoin. Wait for the restored battle, not a menu.
        def rejoined():
            battle = self.observe(0)["Battle"]
            return battle["Active"] and battle["BAReceived"] and battle["LadderID"] == match
        self.wait_for("authenticated automatic battle rejoin", rejoined)
        assert self.status(0)["self"]["strategy"] == "basic"
        assert 0 < self.status(0)["self"]["reconnect_remaining_ms"] < 60000
        self.cli(1, "battle", "H|FF")
        self.cli(1, "battle", "W|FF|FF")
        self.wait_for("new turn automatically answered after reconnect", lambda: submitted_after(turn + 1), 15)
        self.cli(1, "ladder", "strategy", "basic")
        self.finish(before, match)
        self.passed("persisted basic strategy automatically answers a new turn after authenticated reconnect")

    def basic(self):
        self.finish(self.start_match("basic"))
        for i in (0, 1):
            def ended_journal():
                battles = self.cli(i, "battle-log")["data"]["battles"]
                return battles[0] if battles and battles[0]["ended"] else None
            battle_log = self.wait_for("settled battle journal", ended_journal)
            assert len(battle_log["roster"]) == 4
            assert sum(p["player"] for p in battle_log["roster"]) == 2
            assert any(entry["damage"] > 0 for entry in battle_log["logs"])
            assert any(entry["actor"] in (5, 15) and entry["damage"] > 0
                       for entry in battle_log["logs"]), "pet actions missing from shared journal"
            (self.work / f"battle-journal-{i}.json").write_text(json.dumps(battle_log, indent=2))
        self.passed("normal shared battle journal retains player/pet actions and terminal state")
        revision = self.cli(0, "ladder", "status")["data"]["revision"]
        args = ("ladder", "loadout", 1, "--request-id", "network_retry", "--revision", revision)
        original = self.cli(0, *args)["data"]
        replay = self.cli(0, *args)["data"]
        # The shared client may immediately return the already confirmed
        # original receipt. Also observe the new server replay on the wire.
        assert replay["applied_revision"] == original["applied_revision"]
        def server_replayed():
            envelope = self.observe(0).get("Ladder") or {}
            return envelope if envelope.get("request_id") == "network_retry" and envelope.get("replay") else None
        replay = self.wait_for("server request replay", server_replayed)
        assert replay["request_wire"] == original["request_wire"]
        assert replay["applied_revision"] == original["applied_revision"]
        self.passed("authenticated 1v1, default strategy, settlement, resources, ACK and exact request replay")

        before = self.start_match("manual")
        obs = self.ready_battle()
        social_flags = obs["Player"]["SocialFlags"]
        assert obs["Player"]["SocialFlagsKnown"]
        match, slot, turn = obs["Battle"]["LadderID"], obs["Battle"]["MyNo"], obs["Battle"]["Turn"]
        self.cli(0, "battle", "T|FF")
        assert self.observe(0)["Battle"]["PlayerSubmitted"]
        event = self.cli(0, "ladder", "wait", 0, "1s")["data"]
        self.kill("client0")
        time.sleep(1.5)
        self.start_client(0)
        obs = self.ready_battle()
        assert obs["Battle"]["LadderID"] == match and obs["Battle"]["MyNo"] == slot
        assert obs["Player"]["SocialFlagsKnown"] and obs["Player"]["SocialFlags"] == social_flags
        assert obs["Battle"]["Turn"] == turn and obs["Battle"]["PlayerSubmitted"]
        assert 0 < self.status(0)["self"]["reconnect_remaining_ms"] < 60000
        new = self.cli(0, "ladder", "wait", event["cursor"], "1s", "--stream", event["stream"])["data"]
        assert new["gap"] and new["stream"] != event["stream"]
        assert new["snapshot"]["snapshot"]["match"]["id"] == match
        for i in (0, 1):
            self.cli(i, "ladder", "strategy", "basic")
        self.finish(before, match)
        self.passed("same-battle authenticated reconnect preserves accepted action, charges budget, restores stream and settles")

        before = self.start_match("manual")
        obs = self.ready_battle()
        match, turn = obs["Battle"]["LadderID"], obs["Battle"]["Turn"]
        ratings = {i: self.status(i)["ratings"] for i in (0, 1)}
        for i in (0, 1):
            self.cli(i, "battle", "H|FF")
            self.cli(i, "battle", "W|FF|FF")
        def combat_ran():
            battle = self.observe(0)["Battle"]
            return battle["Turn"] > turn and any(p["HP"] < p["MaxHP"] for p in battle["Participants"])
        self.wait_for("combat damage", combat_ran)
        assert any(p["HP"] < p["MaxHP"] for p in self.observe(0)["Battle"]["Participants"])
        self.kill("gmsv")
        self.start_gmsv()
        for i in (0, 1):
            self.wait_for("authenticated crash recovery", lambda: self.cli(i, "ladder", "status", check=False).get("ok"), 40)
            state = self.status(i)
            result = state["result"]
            assert state["phase"] == "result" and result["id"] == match
            assert result["reason"] == "server_restart" and result["statistics_incomplete"] and not result["rated"]
            assert state["ratings"] == ratings[i]
            self.resources_restored(i, before[i])
            self.cli(i, "ladder", "ack")
        self.passed("GMSV SIGKILL and real relogin restore pre-match resources plus unrated technical settlement")

    def expect_code(self, i, code, *args):
        reply = self.cli(i, "ladder", *args, check=False)
        codes = (code,) if isinstance(code, str) else code
        assert not reply["ok"] and reply["data"]["code"] in codes, reply

    def contact_slot(self, leader, target):
        name = f"LadderQA{target:02d}"
        entries = self.cli(leader, "ladder", "contacts")["data"].get("contacts") or []
        return next((p["slot"] for p in entries if p["name"] == name), None)

    def invite(self, leader, target):
        slot = self.contact_slot(leader, target)
        assert slot is not None
        entries = self.cli(leader, "ladder", "contacts")["data"]["contacts"]
        selected = next(p for p in entries if p["slot"] == slot)
        self.cli(leader, "ladder", "invite", slot, selected["id"])
        invitations = self.status(target)["invitations"]
        assert len(invitations) == 1
        assert invitations[0]["from"]["id"] == self.status(leader)["self"]["id"]
        return invitations[0]["id"]

    def move_to(self, i, x, y):
        for _ in range(5):
            reply = self.cli(i, "goto", x, y, check=False)
            position = self.observe(i)["Position"]
            assert position["Floor"] == 1006
            if position["X"] == x and position["Y"] == y:
                return
            # Nearby players can advance the shared observation revision
            # during path execution. Reobserve before continuing the route.
            assert reply.get("error") == "aigame snapshot revision is stale", reply
        raise AssertionError("movement repeatedly lost its observation revision")

    def exchange_cards(self):
        # After the crash scenario these clients reconnect lazily. CharLogin
        # succeeds before the asynchronous map/player packets arrive; do not
        # issue movement against the initial unknown position.
        for i in range(self.player_count):
            def world_ready():
                obs = self.observe(i)
                return obs["Player"]["HasStatus"] and obs["Position"]["Floor"] == 1006
            self.wait_for(f"player {i} world after reconnect", world_ready, 40)
        # Only normal player actions build the roster: no injected contacts,
        # native character indices, matchmaking hooks, or privileged commands.
        for leader in (0, 1):
            self.move_to(leader, 14, 21)
            for target in range(leader + 2, 10, 2):
                self.move_to(target, 15, 21)
                self.cli(target, "social", "trade-card", 1)
                self.cli(leader, "look", "right")
                self.cli(leader, "mail", "add")
                self.wait_for("exchanged contact", lambda: self.contact_slot(leader, target) is not None)
                self.move_to(target, 10 + target, 25)
            # These floor cells were already reached by the first card
            # recipient; multiple players may share a field coordinate.
            self.move_to(leader, 12 + leader, 25)
        self.passed("real map movement and bilateral card exchange for two five-player rosters")

    def multiplayer(self):
        # A preceding 1v1/automatic-reconnect scenario deliberately retains
        # its rooms after ACK. Start this roster scenario through normal leave.
        for i in range(10):
            if self.status(i)["room"]:
                self.cli(i, "ladder", "leave")
        self.exchange_cards()
        ids = {i: self.status(i)["self"]["id"] for i in range(10)}
        assert len(set(ids.values())) == 10
        for mode in range(2, 6):
            teams = [list(range(side, mode * 2, 2)) for side in (0, 1)]
            participants = teams[0] + teams[1]
            before = {i: self.observe(i) for i in participants}
            ratings = {i: self.status(i)["ratings"] for i in participants}
            for i in participants:
                self.cli(i, "ladder", "strategy", "basic")
            for side, team in enumerate(teams):
                leader = team[0]
                if mode == 2:
                    self.cli(leader, "ladder", "create", mode)
                else:
                    self.cli(leader, "ladder", "mode", mode)
                invite = self.invite(leader, team[-1])
                if mode == 2 and side == 0:
                    self.expect_code(3, "invitation_expired", "accept", invite)
                    self.cli(team[-1], "ladder", "decline", invite)
                    assert not self.status(team[-1])["invitations"]
                    invite = self.invite(leader, team[-1])
                self.cli(team[-1], "ladder", "accept", invite)
                room = self.status(leader)["room"]
                assert {p["id"] for p in room["members"]} == {ids[i] for i in team}
                self.expect_code(team[-1], "leader_required", "queue")
                self.cli(leader, "ladder", "ready")
                self.expect_code(leader, "member_not_ready", "queue")
                for i in team[1:]:
                    self.cli(i, "ladder", "ready")
                if mode == 2 and side == 0:
                    expired = self.invite(leader, 4)
                self.cli(leader, "ladder", "queue")
                self.expect_code(team[-1], ("room_locked", "result_or_match_pending"), "loadout", 1)
                if mode == 2 and side == 0:
                    self.expect_code(4, "invitation_expired", "accept", expired)
            match_state = self.wait_for("multiplayer countdown", lambda: (s if (s := self.status(0))["phase"] == "countdown" else None))
            match = match_state["match"]
            assert match["mode"] == mode and len(match["teams"]) == 2
            roster = {p["id"] for team in match["teams"] for p in team["members"]}
            assert roster == {ids[i] for i in participants}
            event = self.cli(0, "ladder", "wait", 0, "1s")["data"]
            deadline = time.monotonic() + 90
            while event["snapshot"]["snapshot"]["phase"] != "result":
                assert time.monotonic() < deadline, f"{mode}v{mode} did not settle"
                event = self.cli(0, "ladder", "wait", event["cursor"], "2s", "--stream", event["stream"])["data"]
            for i in participants:
                result = self.cli(i, "ladder", "result")["data"]["snapshot"]["result"]
                assert result["id"] == match["id"] and result["mode"] == mode and result["rated"]
                assert len(result["members"]) == mode * 2 and {p["id"] for p in result["members"]} == roster
                assert sum(p["rating_delta"] for p in result["members"]) == 0
                assert sum(p["statistics"]["damage"] for p in result["members"]) > 0
                updated = self.status(i)["ratings"]
                assert all(updated[n] == ratings[i][n] for n in range(5) if n != mode - 1)
                self.resources_restored(i, before[i])
            # One side may acknowledge, prepare, and queue again while the
            # other side keeps its previous result open.
            for i in teams[0]:
                self.cli(i, "ladder", "ack")
            for i in teams[0]:
                self.cli(i, "ladder", "ready")
            self.cli(0, "ladder", "queue")
            assert self.status(1)["phase"] == "result"
            assert self.status(0)["phase"] == "queued"
            self.cli(0, "ladder", "cancel")
            for i in teams[1]:
                self.cli(i, "ladder", "ack")
            for side, team in enumerate(teams):
                s = self.status(side)
                assert s["phase"] == "lobby" and len(s["room"]["members"]) == mode
                assert all(not p["ready"] for p in s["room"]["members"])
            self.passed(f"real {mode}v{mode} invitation/readiness, native combat, independent Elo, resources and independent requeue")
        self.contact_recreation()

    def multiplayer_reconnect(self):
        for i in range(10):
            if self.status(i)["room"]:
                self.cli(i, "ladder", "leave")
        self.exchange_cards()
        teams = [list(range(side, 10, 2)) for side in (0, 1)]
        before = {i: self.observe(i) for i in range(10)}
        for team in teams:
            self.cli(team[0], "ladder", "create", 5)
            for i in team[1:]:
                self.cli(i, "ladder", "accept", self.invite(team[0], i))
            for i in team:
                self.cli(i, "ladder", "strategy", "manual")
                self.cli(i, "ladder", "ready")
            self.cli(team[0], "ladder", "queue")
        self.ready_battle()
        initial = {}
        for i in range(10):
            def entered():
                b = self.observe(i)["Battle"]
                return b if b["Active"] and b["BAReceived"] and b["LadderID"] else None
            initial[i] = self.wait_for(f"participant {i} native entrance", entered, 20)
        match = initial[0]["LadderID"]
        assert all(b["LadderID"] == match and b["BAReceived"] for b in initial.values())
        assert len({b["MyNo"] for b in initial.values()}) == 10
        # Keep the remaining players manual and unsubmitted so accepted
        # actions can be checked through multiple disconnect patterns.
        for i in (0, 1, 2, 3):
            self.cli(i, "battle", "T|FF")
            self.cli(i, "battle", "W|FF|FF")
            def accepted():
                b = self.observe(i)["Battle"]
                return b["PlayerSubmitted"] and b["PetSubmitted"]
            self.wait_for(f"participant {i} accepted actions", accepted, 10)
        evidence = []
        preserved_accepted = 0
        for dropped in ([0, 1, 2, 3], teams[0], list(range(10))):
            prior = {i: self.observe(i)["Battle"] for i in range(10)}
            budgets = {i: self.status(i)["self"]["reconnect_remaining_ms"] for i in range(10)}
            # Send all kill signals before waiting: no member can reconnect
            # while the others are still being deliberately disconnected.
            for i in dropped:
                self.processes[f"client{i}"].kill()
            for i in dropped:
                self.processes[f"client{i}"].wait(timeout=5)
            time.sleep(1.5)
            recovered = {}
            for i in reversed(dropped):
                self.start_client(i)
                def rejoined():
                    b = self.observe(i)["Battle"]
                    return b if b["Active"] and b["BAReceived"] and b["LadderID"] == match else None
                b = self.wait_for(f"multiplayer rejoin {i}", rejoined, 20)
                assert b["MyNo"] == initial[i]["MyNo"], (i, b)
                assert b["Turn"] >= prior[i]["Turn"], (i, "rejoin rewound the battle", b)
                if b["Turn"] == prior[i]["Turn"]:
                    assert b["PlayerSubmitted"] == prior[i]["PlayerSubmitted"], (i, b)
                    assert b["PetSubmitted"] == prior[i]["PetSubmitted"], (i, b)
                    preserved_accepted += int(b["PlayerSubmitted"] and b["PetSubmitted"])
                else:
                    # A live battle does not pause for these sequential
                    # logins. A new manual turn must reopen the menus rather
                    # than replay actions accepted for the previous turn.
                    assert not b["PlayerSubmitted"] and not b["PetSubmitted"], (i, b)
                    assert b["CommandReady"], (i, b)
                budget = self.status(i)["self"]["reconnect_remaining_ms"]
                assert 0 < budget < budgets[i], (i, budgets[i], budget)
                recovered[i] = {"before": prior[i], "battle": b, "budget_before_ms": budgets[i], "budget_after_ms": budget}
            for i in set(range(10)) - set(dropped):
                assert self.status(i)["self"]["reconnect_remaining_ms"] == budgets[i]
            evidence.append({"dropped": dropped, "recovered": recovered})
            (self.work / "multiplayer-reconnect.json").write_text(json.dumps(evidence, indent=2))
        assert preserved_accepted >= 4, "no complete same-turn accepted-action reconnect coverage"
        # Resume real combat and verify a common authoritative settlement.
        for i in range(10):
            self.cli(i, "ladder", "strategy", "basic")
        self.wait_for("reconnected multiplayer settlement", lambda: self.status(0)["phase"] == "result", 90)
        results = []
        for i in range(10):
            result = self.cli(i, "ladder", "result")["data"]["snapshot"]["result"]
            assert result["id"] == match and result["mode"] == 5 and result["rated"]
            assert len(result["members"]) == 10
            assert sum(p["rating_delta"] for p in result["members"]) == 0
            self.resources_restored(i, before[i])
            results.append(result)
        assert all(result == results[0] for result in results)
        for i in range(10):
            self.cli(i, "ladder", "ack")
        assert all(self.status(i)["phase"] == "lobby" for i in range(10))
        self.passed("5v5 mixed players, whole team and all ten disconnect/rejoin preserve seats, actions, cumulative budgets and settlement")

    def contact_recreation(self):
        for i in range(10):
            if self.status(i)["room"]:
                self.cli(i, "ladder", "leave")
        # Persist the owner's real SAAC archive, then keep the owner offline
        # while the target is deleted. Native deletion notifications remove
        # cards held by online players, which cannot test an archived old card.
        self.cli(0, "logout")
        self.kill("client0")
        old_id = self.status(8)["self"]["id"]
        self.cli(8, "logout")
        self.kill("client8")
        config = self.work / "ladderqa08.toml"
        config.write_text("\n".join(line for line in config.read_text().splitlines() if not line.startswith("character =")) + "\n")
        self.start_client(8)
        self.cli(8, "delete-character", "LadderQA08", "LadderQA08")
        self.cli(8, "create-character", "LadderQA08")
        self.cli(8, "enter", "LadderQA08")
        self.wait_for("recreated character", lambda: self.observe(8)["Player"]["HasStatus"])
        # Deletion required disabling automatic entry. Restore the fixture's
        # selected character for the subsequent daemon-restart scenarios.
        with config.open("a") as stream:
            stream.write('character = "LadderQA08"\n')
        assert self.status(8)["self"]["id"] != old_id
        assert self.status(8)["ratings"] == [1000] * 5
        self.start_client(0)
        self.wait_for("owner archive reload", lambda: self.observe(0)["Player"]["HasStatus"])
        old_slot = self.contact_slot(0, 8)
        assert old_slot is not None, "offline owner's archived card was lost"
        self.cli(0, "ladder", "create", 2)
        self.expect_code(0, "contact_identity_changed", "invite", old_slot, old_id)
        assert not self.status(8)["invitations"]
        self.move_to(0, 14, 21)
        self.move_to(8, 15, 21)
        self.cli(8, "social", "trade-card", 1)
        self.cli(0, "look", "right")
        self.cli(0, "mail", "add")
        # Both card operations use the same ordered game connection. A status
        # receipt fences the exchange before attempting the new invitation.
        self.status(0)
        invite = self.invite(0, 8)
        self.cli(8, "ladder", "accept", invite)
        self.passed("persisted cards reject deleted/recreated same-name characters until explicit card exchange")
        self.cli(8, "ladder", "leave")
        slot = self.contact_slot(0, 8)
        selected = next(p for p in self.cli(0, "ladder", "contacts")["data"]["contacts"] if p["slot"] == slot)
        self.cli(0, "mail", "remove-contact", slot)
        self.wait_for("deleted card", lambda: self.contact_slot(0, 8) is None)
        self.move_to(8, 18, 25)
        self.move_to(1, 15, 21)
        self.cli(1, "social", "trade-card", 1)
        self.cli(0, "look", "right")
        self.cli(0, "mail", "add")
        self.wait_for("replacement card", lambda: self.contact_slot(0, 1) == slot)
        self.expect_code(0, "contact_slot_changed", "invite", slot, selected["id"])
        assert not self.status(1)["invitations"]
        invite = self.invite(0, 1)
        self.cli(1, "ladder", "accept", invite)
        self.passed("reused contact slots reject the previous selection and accept a freshly selected identity")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--work", type=Path, required=True)
    parser.add_argument("--scenario", choices=("all", "basic", "multiplayer", "reconnect", "contacts", "receipts", "multi-reconnect", "contact-restart", "cross-map"), default="all")
    for name in ("sactl", "gateway", "gmsv", "saac", "seed"):
        parser.add_argument("--" + name, type=Path, required=True)
    fixture = Fixture(parser.parse_args())
    try:
        fixture.run()
    finally:
        fixture.close()


if __name__ == "__main__":
    main()
