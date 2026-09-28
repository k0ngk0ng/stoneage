#!/usr/bin/env python3
"""Real sactl -> gateway -> native game regression for the local commander.

Requires prepared native binaries. Creates only new synthetic accounts in an
isolated repository build directory. No production or user sessions involved.
"""
import argparse
import asyncio
import importlib.util
import json
import sqlite3
import random
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / "tools"))
from arena_agent.runtime import Runner
from arena_agent.contract import Decision, plan_for, selectable, validate_plan


class Explore:
    id = "explore"
    def __init__(self,seed):
        self.version = "explore-v1:"+str(seed)
        self.rng = random.Random(seed)

    async def decide(self,team,history,deadline):
        choices = {}
        for key,candidates in selectable(team).items():
            attacks = [c for c in candidates.values() if 0 <= c["target"] < 20 and c["target"]//10 != team["side"]
                       and (c["kind"] == "attack" or c["kind"] == "skill" and c.get("skill_id") == 1)]
            options = attacks if attacks and self.rng.random()<.85 else list(candidates.values())
            choices[key] = self.rng.choice(options)["id"]
        return Decision(validate_plan(team,plan_for(team,choices)),self.id,self.version,{"collection_policy":True})


class ClockChecked:
    def __init__(self, runner):
        self.runner, self.inner = runner, runner.strategy
        self.id, self.version = self.inner.id, self.inner.version
        self.checked = False

    async def decide(self, team, history, deadline):
        if not self.checked:
            member = self.runner.members[0]
            original = team["members"][member.id]["battle"]["Clock"]
            for _ in range(3):
                await member.call("query", "BTIME")
                view = (await member.call("battle-state"))["data"]
                current = view["battle"]["Clock"]
                assert current["Known"] and current["ServerTurn"] == original["ServerTurn"]
                assert current["RulesVersion"] == "stoneage-native-ladder-v1"
                assert current["DeadlineMS"] == original["DeadlineMS"], "observer query changed native deadline"
                assert current["ServerNowMS"] >= original["ServerNowMS"]
            self.checked = True
        return await self.inner.decide(team,history,deadline)


async def scenario(fixture, args):
    selected = list(range(args.mode * 2))
    before = {i: fixture.observe(i) for i in selected}
    for i in selected:
        try:
            fixture.cli(i, "stop")
        except AssertionError as error:
            if "read response: EOF" not in str(error):
                raise
        fixture.processes[f"client{i}"].wait(timeout=5)
    runners = []
    for side in range(2):
        config = {"schema_version": 1, "sactl": str(fixture.binaries["sactl"]), "mode": args.mode,
                  "state_dir": str(fixture.work / f"commander-{side}"), "strategy": args.strategy if side == 0 and args.strategy != "explore" else "basic", "fallback": "basic",
                  "members": [{"id": f"member-{i}", "config": str(fixture.work / f"ladderqa{i:02d}.toml"),
                               "socket": str(fixture.work / f"ladderqa{i:02d}.sock")} for i in selected if i % 2 == side]}
        if args.model:
            config["model"] = str(args.model)
        runners.append(Runner(config, strategy=Explore(args.policy_seed) if side == 0 and args.strategy == "explore" else None))
        runners[-1].strategy = ClockChecked(runners[-1])
    async def disconnect():
        while True:
            count = runners[0].store.db.execute("SELECT count(*) FROM battle_intents WHERE status='written'").fetchone()[0]
            if count:
                member = runners[0].members[0]
                member.process.kill()
                await member.process.wait()
                return
            await asyncio.sleep(.01)
    jobs = [runner.run(args.matches) for runner in runners]
    if args.reconnect:
        jobs.append(disconnect())
    await asyncio.wait_for(asyncio.gather(*jobs), args.timeout)
    evidence = []
    for side in range(2):
        assert runners[side].strategy.checked
        db = sqlite3.connect(fixture.work / f"commander-{side}/arena.sqlite3")
        results = [json.loads(row[0]) for row in db.execute("SELECT body FROM results")]
        assert len(results) == args.matches, results
        assert all(r["mode"] == args.mode and r["rated"] and r["reason"] == "defeat" for r in results), results
        assert all(sum(p["rating_delta"] for p in r["members"]) == 0 for r in results)
        turns = db.execute("SELECT count(*) FROM records WHERE kind='turn'").fetchone()[0]
        written = db.execute("SELECT count(*) FROM battle_intents WHERE status='written'").fetchone()[0]
        assert turns > 0 and written > 0, (turns, written)
        if args.reconnect and side == 0:
            assert db.execute("SELECT count(*) FROM records WHERE kind='daemon_restarted'").fetchone()[0] > 0
        wins = sum(r["winner_side"] == next(p["side"] for p in r["members"] if p["id"] in runners[side].ids.values()) for r in results)
        evidence.append({"commander": side, "strategy":runners[side].strategy.id, "version":runners[side].strategy.version,
                         "results": len(results), "wins":wins,"decisions": turns, "written": written})
        db.close()
    # Normal login after the commander exits, and native resource restore.
    for i in selected:
        fixture.start_client(i)
        fixture.wait_for("relogin", lambda: fixture.cli(i, "observe", check=False).get("ok"))
        fixture.resources_restored(i, before[i])
    (fixture.work / "commander-passed.json").write_text(json.dumps({"mode": args.mode, "evidence": evidence}, indent=2)+"\n")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--work", type=Path, required=True)
    for name in ("sactl", "gateway", "gmsv", "saac", "seed"):
        parser.add_argument("--"+name, type=Path, required=True)
    parser.add_argument("--mode", type=int, choices=range(1,6), default=1)
    parser.add_argument("--matches", type=int, default=2)
    parser.add_argument("--timeout", type=int, default=180)
    parser.add_argument("--strategy", choices=("basic", "learned", "explore"), default="basic")
    parser.add_argument("--policy-seed", type=int, default=1)
    parser.add_argument("--model", type=Path)
    parser.add_argument("--reconnect", action="store_true")
    args = parser.parse_args()
    args.scenario = "basic" if args.mode == 1 else "multiplayer"
    source = ROOT / "server/legacy/modern/tests/test-ladder-network.py"
    spec = importlib.util.spec_from_file_location("ladder_fixture", source)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    fixture = module.Fixture(args)
    try:
        fixture.prepare()
        asyncio.run(scenario(fixture, args))
    finally:
        fixture.close()


if __name__ == "__main__":
    main()
