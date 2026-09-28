import asyncio
import copy
import json
import tempfile
import time
import unittest
from pathlib import Path
from unittest.mock import patch

from arena_agent.contract import ContractError, plan_for, team_observation, validate_plan
from arena_agent.storage import Store
from arena_agent.strategies import Basic, Hybrid, LLM
from arena_agent.transport import Member


def fixture(mode=1):
    views = {}
    for n in range(mode):
        view = {"schema_version": 1, "character_id": f"char-{n}", "match_id": "match", "turn": 4,
                "mode": mode, "observation_id": f"obs-{n}", "battle": {
                    "Active": True, "Ended": False, "MyNoKnown": True, "MyNo": n,
                    "Clock": {"RulesVersion":"stoneage-native-ladder-v1"},
                    "PlayerSubmitted": False, "PetSubmitted": False},
                "candidates": [
                    {"id": "attack-11", "actor": "player", "kind": "attack", "target": 11, "index": -1, "ready": True},
                    {"id": "attack-10", "actor": "player", "kind": "attack", "target": 10, "index": -1, "ready": True},
                    {"id": "friendly", "actor": "player", "kind": "attack", "target": 1, "index": -1, "ready": True},
                    {"id": "guard", "actor": "player", "kind": "guard", "target": n, "index": -1, "ready": True},
                    {"id": "pet-wait", "actor": "pet", "kind": "wait", "target": -1, "index": -1, "ready": False},
                    {"id": "pet-attack", "actor": "pet", "kind": "skill", "skill_id": 1, "target": 10, "index": 0, "ready": False},
                ]}
        views[f"member-{n}"] = view
    return team_observation(views, {f"char-{n}" for n in range(mode)}, mode)


class ContractTests(unittest.IsolatedAsyncioTestCase):
    async def test_same_identity_cannot_use_two_sockets(self):
        with tempfile.TemporaryDirectory() as directory:
            store = Store(Path(directory)/"state")
            members = []
            for n in range(2):
                socket = str(Path(directory)/f"{n}.sock")
                path = Path(directory)/f"{n}.toml"
                path.write_text(f'socket_path = {json.dumps(socket)}\naccount = "fixture"\ncharacter = "fixture"\naddress = {json.dumps(directory)}\n')
                members.append(Member("sactl",{"id":str(n),"config":str(path),"socket":socket},store))
            try:
                with patch.object(members[0],"call",return_value={"ok":True}):
                    await members[0].start()
                with self.assertRaises(RuntimeError):
                    await members[1].start()
            finally:
                await members[0].close()
                await members[1].close()
                store.close()

    async def test_one_joint_plan_for_all_modes(self):
        for mode in range(1, 6):
            team = fixture(mode)
            original = copy.deepcopy(team)
            decision = await Basic().decide(team, [], time.monotonic()+1)
            self.assertEqual(team, original)
            self.assertEqual(len(decision.plan["orders"]), mode*2)
            self.assertEqual({o["candidate_id"] for o in decision.plan["orders"]}, {"attack-10", "pet-attack"})
            validate_plan(team, decision.plan)

    async def test_no_member_can_override_or_repeat(self):
        team = fixture(2)
        team["members"]["member-0"]["reserved_actors"] = {"player": "uncertain"}
        plan = (await Basic().decide(team, [], time.monotonic()+1)).plan
        self.assertEqual(len(plan["orders"]), 3)
        bad = copy.deepcopy(plan)
        bad["orders"][0]["candidate_id"] = "invented"
        with self.assertRaises(ContractError):
            validate_plan(team, bad)
        bad = copy.deepcopy(plan)
        bad["turn"] += 1
        with self.assertRaises(ContractError):
            validate_plan(team, bad)
        bad = copy.deepcopy(plan)
        bad["orders"][1] = bad["orders"][0]
        with self.assertRaises(ContractError):
            validate_plan(team, bad)

    def test_cannot_mix_turns_or_opponent_private_state(self):
        for field, value in (("turn", 5), ("match_id", "other"), ("character_id", "enemy")):
            team = fixture(2)
            team["members"]["member-1"][field] = value
            with self.assertRaises(ContractError):
                team_observation(team["members"], {"char-0", "char-1"}, 2)
        team = fixture(2)
        team["members"]["member-1"]["battle"]["MyNo"] = 10
        with self.assertRaises(ContractError):
            team_observation(team["members"], {"char-0", "char-1"}, 2)

    async def test_llm_schema_and_hybrid_fallback(self):
        team = fixture()
        local = await Basic().decide(team, [], time.monotonic()+1)
        llm = LLM({"endpoint": "http://127.0.0.1:8000/v1/chat/completions", "model": "fixture"})
        with patch.object(llm, "request_async", return_value=local.plan):
            final = await llm.decide(team, [], time.monotonic()+1)
        self.assertEqual(final.plan, local.plan)
        with patch.object(llm, "request_async", return_value={"orders": []}):
            with self.assertRaises(ContractError):
                await llm.decide(team, [], time.monotonic()+1)
        with patch.object(llm, "request_async", side_effect=ContractError("HTTP failure")):
            final = await Hybrid(Basic(), llm).decide(team, [], time.monotonic()+1)
        self.assertEqual(final.plan, local.plan)
        self.assertEqual(final.diagnostics["fallback"], "learned")

    def test_llm_context_budget_keeps_current_state(self):
        llm = LLM({"endpoint": "http://localhost:8000/v1/chat/completions", "model": "fixture", "context_bytes": 16000})
        history = [{"turn": i, "events": "x"*12000} for i in range(10)]
        payload = llm.payload(fixture(), history)
        current = json.loads(payload["messages"][1]["content"])
        self.assertIn("earlier_history", current)
        self.assertEqual(current["team"]["match_id"], "match")
        self.assertEqual(len(history), 10)


class StoreTests(unittest.TestCase):
    def test_unknown_write_survives_restart_and_known_rejection_releases(self):
        with tempfile.TemporaryDirectory() as directory:
            selection = {"match_id": "match", "turn": 3, "observation_id": "obs", "candidate_id": "attack"}
            store = Store(directory)
            self.assertTrue(store.reserve("a", selection, "player"))
            store.close()
            store = Store(directory)
            self.assertFalse(store.reserve("a", selection, "player"))
            store.finish_intent("a", selection, "player", {"ok": False, "kind": "unknown"})
            self.assertFalse(store.reserve("a", selection, "player"))
            store.finish_intent("a", selection, "player", {"ok": False, "data": {"code": "stale_observation"}})
            self.assertTrue(store.reserve("a", selection, "player"))
            store.close()

    def test_cursor_and_deduplication(self):
        with tempfile.TemporaryDirectory() as directory:
            store = Store(directory)
            batch = {"stream": "stream", "cursor": 2, "gap": False, "observation": {"match_id": "match"},
                     "events": [{"sequence": n, "match_id": "match"} for n in (1, 2)]}
            store.ingest("a", batch)
            store.ingest("a", batch)
            self.assertEqual(store.db.execute("SELECT count(*) FROM events").fetchone()[0], 2)
            self.assertEqual(store.cursor("a"), ("stream", 2))
            store.pin("match", "v1")
            with self.assertRaises(ValueError):
                store.pin("match", "v2")
            store.close()

    def test_same_member_cannot_have_two_commanders(self):
        with tempfile.TemporaryDirectory() as directory:
            store = Store(directory)
            config = {"id": "a", "socket": str(Path(directory)/"a.sock")}
            a, b = Member("sactl", config, store), Member("sactl", config, store)
            a.claim()
            with self.assertRaises(RuntimeError):
                b.claim()
            a.lock_file.close()
            b.claim()
            b.lock_file.close()
            store.close()


if __name__ == "__main__":
    unittest.main()
