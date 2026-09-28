"""One commander coordinates all members; only this runtime writes actions."""
from __future__ import annotations

import asyncio
import json
import time
import tomllib
from pathlib import Path

from .contract import ContractError, selectable, team_observation, validate_plan
from .storage import Store
from .strategies import Basic, Hybrid, LLM, ProcessPlugin
from .learning import Learned, RULES_VERSION
from .transport import CommandError, Member


class Runner:
    def __init__(self, config, strategy=None):
        self.config = config
        self.store = Store(config["state_dir"])
        self.members = [Member(config["sactl"], m, self.store) for m in config["members"]]
        self.mode = config["mode"]
        self.basic = Basic()
        self.strategy = strategy or self.make_strategy(config)
        self.stop_requested = False
        self.ids = {}
        self.completed = set()
        self.last_decision = None

    @staticmethod
    def make_strategy(config):
        selected = config["strategy"]
        if selected == "basic":
            return Basic()
        if selected == "llm":
            return LLM(config["llm"])
        if selected in ("learned", "hybrid"):
            learned = Learned(config["model"], config["mode"])
            return Hybrid(learned, LLM(config["llm"])) if selected == "hybrid" else learned
        for plugin in config.get("plugins", []):
            if plugin["id"] == selected:
                if config["mode"] not in plugin["modes"]:
                    raise ValueError("plugin does not support configured mode")
                return ProcessPlugin(plugin)
        raise ValueError(f"strategy {selected!r} is not installed")

    def report(self, state, **fields):
        print(json.dumps({"state": state, **fields}, ensure_ascii=False), flush=True)

    async def initialize(self):
        # Claims are acquired before starting any sessions. If one is already
        # controlled, cleanup releases only this instance's acquired claims.
        for member in self.members:
            await member.start()
        for member in self.members:
            # Read only expected identity, never copy credentials into records.
            with Path(member.config["config"]).open("rb") as source:
                login = tomllib.load(source)
            if not login.get("character") or not login.get("account"):
                raise ValueError(f"member {member.id}: sactl config must specify account and character")
            login_deadline = time.monotonic()+45
            while True:
                try:
                    observation = (await member.call("observe", timeout=20))["data"]
                    if observation.get("Player", {}).get("HasStatus"):
                        break
                except CommandError as error:
                    if error.response.get("kind") not in ("session", "unknown"):
                        raise
                    self.store.record("login_retry", {"kind":error.response.get("kind")}, member=member.id)
                if time.monotonic() >= login_deadline:
                    raise TimeoutError(f"member {member.id}: login did not produce an authoritative player state")
                await asyncio.sleep(.5)
            if observation["Account"] != login["account"] or observation["Character"] != login["character"]:
                raise ContractError(f"member {member.id}: socket belongs to a different identity")
            status = (await member.call("ladder", "status"))["data"]["snapshot"]
            self.ids[member.id] = status["self"]["id"]
            if status.get("match"):
                self.store.pin(status["match"]["id"], self.strategy.id + ":" + self.strategy.version)
            await member.call("query", "BTIME", timeout=3)
            view = (await member.call("battle-state"))["data"]
            if view.get("schema_version") != 1:
                raise ContractError("sactl battle-state schema 1 is required")
            if view["battle"].get("Clock", {}).get("RulesVersion") != RULES_VERSION:
                raise ContractError("server does not advertise compatible BTIME rules; update the game server before queueing")
            await member.call("auto-battle", "off")
            await member.mutate("strategy", "manual")
        if len(set(self.ids.values())) != self.mode:
            raise ContractError("same character configured more than once")

    async def statuses(self):
        responses = await asyncio.gather(*(m.call("ladder", "status", timeout=4) for m in self.members), return_exceptions=True)
        result = {}
        for member, response in zip(self.members, responses):
            if isinstance(response, BaseException):
                if member.process and member.process.returncode is not None:
                    await member.start()
                    self.store.record("daemon_restarted", {}, member=member.id)
                continue
            status = response["data"]["snapshot"]
            if status["self"]["id"] != self.ids[member.id]:
                raise ContractError("character identity changed during execution")
            result[member.id] = status
        return result

    def validate_match(self, status):
        match = status["match"]
        if match["mode"] != self.mode:
            raise ContractError("active match mode differs from configured team")
        own = {p["id"] for p in match["teams"][match["side"]]["members"]}
        if own != set(self.ids.values()):
            raise ContractError("active team contains missing or uncontrolled members")
        self.store.pin(match["id"], self.strategy.id + ":" + self.strategy.version)
        return match["id"]

    async def collect(self, member):
        # BTIME is observer-only. It reads the native deadline without resetting it.
        await member.call("query", "BTIME", timeout=3)
        stream, cursor = self.store.cursor(member.id)
        reply = await member.call("battle-events", cursor, stream, timeout=3)
        batch = reply["data"]
        self.store.ingest(member.id, batch)
        view = batch["observation"]
        view["reserved_actors"] = {}
        for actor in ("player", "pet"):
            intent = self.store.intent(member.id, view["match_id"], view["turn"], actor)
            if intent:
                view["reserved_actors"][actor] = intent
        return view

    async def acknowledge(self, statuses):
        for member in self.members:
            result = statuses.get(member.id, {}).get("result")
            if not result:
                continue
            # Drain final effects before ACK can clear the match identity.
            stream, cursor = self.store.cursor(member.id)
            reply = await member.call("battle-events", cursor, stream, timeout=3)
            self.store.ingest(member.id, reply["data"])
            self.store.result(result)
            await member.mutate("ack")
            self.completed.add(result["id"])
            self.report("result", match_id=result["id"], winner_side=result["winner_side"], rated=result["rated"])

    async def battle(self, statuses):
        active = [s for s in statuses.values() if s.get("match") and s["phase"] == "battle"]
        if not active:
            return
        match_id = self.validate_match(active[0])
        if any(self.validate_match(s) != match_id for s in active):
            raise ContractError("squad split across matches")
        views = await asyncio.gather(*(self.collect(m) for m in self.members), return_exceptions=True)
        groups = {}
        for member, view in zip(self.members, views):
            if isinstance(view, BaseException):
                self.store.record("observation_failure", {"error_type": type(view).__name__}, match_id, member.id)
                continue
            battle = view["battle"]
            if view["match_id"] != match_id or not battle["Active"] or battle["Ended"] or not battle.get("Clock", {}).get("Known"):
                continue
            groups.setdefault(view["turn"], {})[member.id] = view
        if not groups:
            return
        # A delayed member cannot drag a newer turn backwards.
        turn = max(groups)
        team = team_observation(groups[turn], set(self.ids.values()), self.mode)
        remaining = min((v["battle"]["Clock"]["DeadlineMS"] - v["battle"]["Clock"]["ServerNowMS"] -
                         max(0, time.time()*1000-v["battle"]["Clock"]["ReceivedAtMS"]))/1000
                        for v in team["members"].values())
        # Briefly wait for a coherent full squad. Missing members never consume
        # the final submission reserve or prevent online players from acting.
        if team["missing_ids"] and remaining > 15:
            return
        if remaining <= 2 or not selectable(team):
            return
        deadline = time.monotonic() + min(15, remaining-2)
        history = self.store.history(match_id)
        try:
            decision = await asyncio.wait_for(self.strategy.decide(team, history, deadline), max(.01, deadline-time.monotonic()))
            validate_plan(team, decision.plan)
        except (ContractError, TimeoutError, OSError, ValueError) as error:
            self.store.record("strategy_fallback", {"strategy": self.strategy.id, "error_type": type(error).__name__}, match_id)
            decision = await self.basic.decide(team, history, deadline)
        if time.monotonic() >= deadline + 1:
            return
        self.store.record("turn", {"turn": turn, "team": team, "plan": decision.plan,
            "strategy": decision.strategy, "version": decision.version, "diagnostics": decision.diagnostics,
            "summary": {m: [{"id": p["BattleID"], "hp": p["HP"], "max_hp": p["MaxHP"], "flags": p["Flags"]}
                             for p in v["battle"]["Participants"]] for m,v in team["members"].items()}}, match_id)
        await asyncio.gather(*(self.dispatch(m, team, decision.plan) for m in self.members))

    async def dispatch(self, member, team, plan):
        orders = [o for o in plan["orders"] if o["member_id"] == member.id]
        for order in sorted(orders, key=lambda o: o["actor"] != "player"):
            try:
                fresh = (await member.call("battle-state", timeout=3))["data"]
            except (CommandError, TimeoutError):
                return
            original = team["members"][member.id]
            if fresh["observation_id"] != original["observation_id"]:
                return
            clock = fresh["battle"].get("Clock", {})
            if not clock.get("Known") or clock["DeadlineMS"]-clock["ServerNowMS"]-max(0,time.time()*1000-clock["ReceivedAtMS"]) <= 500:
                return
            candidate = next((c for c in fresh["candidates"] if c["id"] == order["candidate_id"]), None)
            if not candidate or not candidate["ready"]:
                continue
            selection = {"match_id": plan["match_id"], "turn": plan["turn"],
                         "observation_id": fresh["observation_id"], "candidate_id": candidate["id"]}
            if not self.store.reserve(member.id, selection, order["actor"]):
                continue
            try:
                response = await member.call("battle-act", json.dumps(selection), timeout=3, check=False)
            except (CommandError, TimeoutError):
                response = {"ok": False, "kind": "unknown"}
            self.store.finish_intent(member.id, selection, order["actor"], response)

    async def contacts(self, leader, target):
        # Local squads can share a spawn cell. Serialize their rendezvous so
        # two verified targets cannot walk into the same name-card tile.
        claim = leader.meeting_claim()
        if claim is None:
            return None
        try:
            return await self.exchange_contact(leader,target)
        finally:
            claim.close()

    async def exchange_contact(self, leader, target):
        def selected(response):
            return next((c for c in (response["data"].get("contacts") or []) if c["id"] == self.ids[target.id]), None)
        found = selected(await leader.call("ladder", "contacts"))
        if found:
            return found
        # Normal movement and bilateral name-card exchange, as a human would.
        lead = (await leader.call("observe"))["data"]
        follower = (await target.call("observe"))["data"]
        if lead["Phase"] != "world" or follower["Phase"] != "world":
            raise ContractError("card exchange requires both members in the world")
        position = lead["Position"]
        if follower["Position"]["Floor"] != position["Floor"]:
            await target.call("warp", position["Floor"], timeout=60)
        occupied = {(a["X"], a["Y"]) for a in lead.get("Actors", [])}
        adjacent = [(1,0,"right"),(1,1,"down-right"),(0,1,"down"),(-1,1,"down-left"),
                    (-1,0,"left"),(-1,-1,"up-left"),(0,-1,"up"),(1,-1,"up-right")]
        offset = int(self.ids[target.id][-4:],16) % len(adjacent)
        adjacent = adjacent[offset:] + adjacent[:offset]
        places = [(position["X"]+dx,position["Y"]+dy,direction) for dx,dy,direction in adjacent
                  if (position["X"]+dx,position["Y"]+dy) not in occupied]
        place = None
        for destination in places:
            for attempt in range(8):
                reply = await target.call("goto", destination[0], destination[1], timeout=60, check=False)
                observed = (await target.call("observe"))["data"]
                pos = observed["Position"]
                if observed["Phase"] != "world" or pos["Floor"] != position["Floor"]:
                    raise ContractError("member left the rendezvous world/floor")
                if (pos["X"], pos["Y"]) == destination[:2]:
                    place = destination
                    break
                if (reply.get("data") or {}).get("code") != "stale_observation":
                    self.store.record("navigation_failure", {"response":reply,"position":pos,"destination":destination[:2]}, member=target.id)
                    break
            if place:
                break
        if place is None:
            raise ContractError("no reachable empty adjacent tile for a verified name-card exchange")
        for _ in range(10):
            seen = (await leader.call("observe"))["data"]
            people = [a for a in seen.get("Actors", []) if (a["X"],a["Y"]) == place[:2]]
            if len(people) == 1 and people[0].get("PersistentCharacterID") == self.ids[target.id]:
                break
            await asyncio.sleep(.2)
        if len(people) != 1 or people[0].get("PersistentCharacterID") != self.ids[target.id]:
            self.store.record("contact_identity_unconfirmed", {"people":people,"expected":self.ids[target.id],"place":place}, member=target.id)
            origin = follower["Position"]
            if origin["Floor"] == position["Floor"]:
                await target.call("goto",origin["X"],origin["Y"],timeout=60,check=False)
            return None
        await target.call("social", "trade-card", 1)
        await leader.call("look", place[2])
        await leader.call("mail", "add")
        for _ in range(10):
            found = selected(await leader.call("ladder", "contacts"))
            if found:
                # Release the one verified exchange tile for the next member.
                # The original spawn cell is known reachable; sharing it is
                # harmless because no name card is sent towards that cell.
                origin = follower["Position"]
                if origin["Floor"] == position["Floor"]:
                    for _ in range(8):
                        reply = await target.call("goto", origin["X"], origin["Y"], timeout=60, check=False)
                        if reply["ok"]:
                            break
                        if (reply.get("data") or {}).get("code") != "stale_observation":
                            break
                return found
            await asyncio.sleep(.2)
        raise ContractError("name-card exchange was not confirmed")

    async def prepare(self, statuses):
        if len(statuses) != len(self.members):
            return
        if any(s["phase"] in ("battle", "countdown", "result") for s in statuses.values()):
            return
        if any(s["phase"] == "queued" for s in statuses.values()):
            return
        expected = set(self.ids.values())
        for s in statuses.values():
            if s.get("room") and not {p["id"] for p in s["room"]["members"]} <= expected:
                raise ContractError("refusing to reorganize a room containing another player's character")
        leader = self.members[0]
        lead = statuses[leader.id]
        if not lead.get("room"):
            await leader.mutate("create", self.mode)
            return
        room = lead["room"]
        if room["leader_id"] != self.ids[leader.id]:
            actual = next(m for m in self.members if self.ids[m.id] == room["leader_id"])
            await actual.mutate("leader", self.ids[leader.id])
            return
        if room["mode"] != self.mode:
            await leader.mutate("mode", self.mode)
            return
        members = {p["id"] for p in room["members"]}
        for member in self.members[1:]:
            if self.ids[member.id] in members:
                continue
            current = statuses[member.id]
            if current.get("room"):
                await member.mutate("leave")
                return
            invitation = next((i for i in (current.get("invitations") or []) if i["room_id"] == room["id"]), None)
            if invitation:
                await member.mutate("accept", invitation["id"])
            else:
                contact = await self.contacts(leader, member)
                if contact is None:
                    return
                await leader.mutate("invite", contact["slot"], contact["id"])
            return
        for member in self.members:
            status = statuses[member.id]
            if status["self"]["strategy"] != "manual":
                raise ContractError("another controller changed the member's strategy")
            mask = member.config.get("pet_mask")
            if mask is not None and status["self"]["pet_mask"] != mask:
                if status["self"]["ready"]:
                    await member.mutate("unready")
                await member.mutate("loadout", mask)
                return
            if not status["self"]["ready"]:
                await member.mutate("ready")
                return
        await leader.mutate("queue")
        self.report("queued", mode=self.mode, strategy=self.strategy.id)

    async def run(self, matches=1):
        try:
            await self.initialize()
            self.report("running", mode=self.mode, strategy=self.strategy.id)
            while True:
                statuses = await self.statuses()
                active = any(s.get("match") and s["phase"] in ("battle", "countdown") for s in statuses.values())
                pending_result = any(s.get("result") for s in statuses.values())
                if len(statuses)==len(self.members) and not active and not pending_result and (self.stop_requested or matches and len(self.completed)>=matches):
                    for member in self.members:
                        if statuses[member.id]["phase"] == "queued":
                            await self.members[0].mutate("cancel")
                            break
                    return
                try:
                    if pending_result:
                        await self.acknowledge(statuses)
                    elif active:
                        await self.battle(statuses)
                    elif not self.stop_requested and not (matches and len(self.completed)>=matches):
                        await self.prepare(statuses)
                except CommandError as error:
                    code = (error.response.get("data") or {}).get("code")
                    if error.response.get("kind") not in ("unknown", "session") and code not in (
                            "outcome_unknown", "stale_revision", "cooldown", "member_not_ready", "room_locked"):
                        raise
                    self.store.record("reconcile", {"kind": error.response.get("kind"), "code": code})
                    await asyncio.sleep(1)
                except TimeoutError:
                    self.store.record("reconcile", {"kind": "timeout"})
                    await asyncio.sleep(1)
                await asyncio.sleep(.2)
        finally:
            await asyncio.gather(*(m.close() for m in self.members), return_exceptions=True)
            self.store.close()
