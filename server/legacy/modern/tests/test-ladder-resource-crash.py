#!/usr/bin/env python3
"""Ten-player real-network crash recovery with food, equipment and reserve pets.

Only isolated fixture archives are provisioned while GMSV and SAAC are stopped.
All match operations and resource consumption subsequently use authenticated CLI
commands. Requires a checkpoint.data emitted by run-ladder-native-smoke.sh.
"""
import argparse
import importlib.util
import json
from pathlib import Path

spec = importlib.util.spec_from_file_location("ladder_network", Path(__file__).with_name("test-ladder-network.py"))
network = importlib.util.module_from_spec(spec)
spec.loader.exec_module(network)

ESCAPES = {"\n": "n", ",": "c", "|": "z", "\\": "y"}


def escape(value):
    return "".join("\\" + ESCAPES[c] if c in ESCAPES else c for c in value)


def unescape(value):
    reverse = {v: k for k, v in ESCAPES.items()}
    result, i = [], 0
    while i < len(value):
        if value[i] == "\\":
            i += 1
            assert i < len(value) and value[i] in reverse, "unexpected fixture archive escape"
            result.append(reverse[value[i]])
        else:
            result.append(value[i])
        i += 1
    return "".join(result)


def fields(payload):
    return dict(line.split("=", 1) for line in payload.splitlines() if line)


def read_archive(path):
    name, options, payload = path.read_bytes().decode("cp936").rstrip("\n").split("|", 2)
    return name, options, fields(unescape(payload))


class ResourceCrash(network.Fixture):
    def archive(self, i):
        found = list((self.work / "saac/char").glob(f"*/ladderqa{i:02d}.0.char"))
        assert len(found) == 1, (i, found)
        return found[0]

    def provision(self, template):
        for i in range(10):
            self.cli(i, "logout")
            self.kill(f"client{i}")
        self.kill("gmsv")
        self.kill("saac")
        source = fields(template.read_bytes().decode("cp936"))
        pet_item = next(p for p in source["pet0"].split("|") if p.startswith("pitem5:"))
        for i in range(10):
            path = self.archive(i)
            (self.work / f"resource-original-{i}.char").write_bytes(path.read_bytes())
            name, options, data = read_archive(path)
            for key in ("lv", "vi", "str", "tou", "dx", "hp"):
                data[key] = source[key]
            # A real club with one durability remaining, plus two consumables.
            data["item2"] = f"id=100|dmce=1|mdmce=100|ucode=resource-weapon-{i}"
            for slot in (5, 6):
                food = dict(p.split("=", 1) for p in source["item5"].split("|"))
                food["ucode"] = f"resource-food-{i}-{slot}"
                data[f"item{slot}"] = "|".join(f"{k}={v}" for k, v in food.items())
            pet = dict(p.split(":", 1) for p in data["pet0"].split("|") if p)
            for key in ("lv", "vi", "str", "tou", "dx", "hp"):
                pet[key] = source[key]
            for slot in (0, 1):
                pet["ucode"] = f"resource-pet-{i}-{slot}"
                pet["name"] = f"ResourcePet{i}-{slot}"
                item = pet_item.rsplit("ucode:", 1)[0] + f"ucode:resource-petitem-{i}-{slot}"
                data[f"pet{slot}"] = "|".join(f"{k}:{v}" for k, v in pet.items()) + "|" + item + "|"
            payload = "".join(f"{k}={v}\n" for k, v in data.items())
            assert unescape(escape(payload)) == payload
            path.write_bytes((name + "|" + options + "|" + escape(payload)).encode("cp936"))
        self.start("saac", [self.binaries["saac"]], self.work / "saac")
        self.listener(self.saac_port, "saac")
        self.start_gmsv()
        for i in range(10):
            self.start_client(i)
            self.wait_for(f"resource character {i}", lambda: self.observe(i)["Player"]["HasStatus"], 40)
            self.status(i)  # Fence the login projection on the ordered connection.
            obs = self.observe(i)
            assert obs["Player"]["HP"] == int(source["hp"])
            assert len(obs["Pets"]) == 2, (i, obs["Pets"])

    def turn(self, number):
        states = {}
        for i in range(10):
            def ready():
                b = self.observe(i)["Battle"]
                return b if b["Active"] and b["Turn"] >= number and b["CommandReady"] else None
            states[i] = self.wait_for(f"resource turn {number} player {i}", ready, 40)
            assert states[i]["Turn"] == number, (i, states[i])
        return states

    def run_resources(self, template):
        self.prepare()
        self.provision(template)
        # Form contacts after offline provisioning and its SAAC restart.
        # The failure under test kills GMSV only, leaving the account service
        # and its live contact directory running throughout the match.
        self.exchange_cards()
        before = {i: self.observe(i) for i in range(10)}
        ratings = {i: self.status(i)["ratings"] for i in range(10)}
        for side in (0, 1):
            team = list(range(side, 10, 2))
            self.cli(side, "ladder", "create", 5)
            for i in team[1:]:
                self.cli(i, "ladder", "accept", self.invite(side, i))
            for i in team:
                self.cli(i, "ladder", "loadout", 3)
                self.cli(i, "ladder", "strategy", "manual")
            for i in team:
                self.cli(i, "ladder", "ready")
            self.cli(side, "ladder", "queue")
        self.ready_battle()
        initial = self.turn(0)
        match = initial[0]["LadderID"]
        checkpoint = {i: read_archive(self.archive(i))[2] for i in range(10)}
        for i in range(10):
            assert all(key in checkpoint[i] for key in ("item2", "item5", "item6", "pet0", "pet1"))
            self.cli(i, "battle", f'H|{initial[i ^ 1]["MyNo"]:X}')
            self.cli(i, "battle", "W|FF|FF")
        for number, slot in ((1, 5), (2, 6)):
            states = self.turn(number)
            for i in range(10):
                self.cli(i, "battle", f'I|{slot:X}|{states[i]["MyNo"]:X}')
                self.cli(i, "battle", "W|FF|FF")
        self.turn(3)
        consumed = {i: self.observe(i) for i in range(10)}
        (self.work / "resource-before.json").write_text(json.dumps(before, ensure_ascii=False, indent=2))
        (self.work / "resource-consumed.json").write_text(json.dumps(consumed, ensure_ascii=False, indent=2))
        for i in range(10):
            # The live inventory must differ before killing the server; merely
            # loading a seeded archive is not evidence of resource restoration.
            original_slots = {p["Index"] for p in before[i]["Inventory"] or [] if p["Name"]}
            remaining_slots = {p["Index"] for p in consumed[i]["Inventory"] or [] if p["Name"]}
            assert original_slots == {2, 5, 6} and not remaining_slots, (i, original_slots, remaining_slots)
        self.kill("gmsv")
        for i in range(10):
            self.kill(f"client{i}")
        self.start_gmsv()
        results, restored = [], {}
        for i in range(10):
            self.start_client(i)
            self.wait_for(f"resource crash relogin {i}", lambda: self.cli(i, "ladder", "status", check=False).get("ok"), 40)
            state = self.status(i)
            result = state["result"]
            assert state["phase"] == "result" and result["id"] == match
            assert result["reason"] == "server_restart" and result["statistics_incomplete"] and not result["rated"]
            assert len(result["members"]) == 10 and state["ratings"] == ratings[i]
            self.resources_restored(i, before[i])
            restored[i] = self.observe(i)
            results.append(result)
        assert all(r == results[0] for r in results)
        for i in range(10):
            self.cli(i, "ladder", "ack")
            saved_at = self.archive(i).stat().st_mtime_ns
            self.cli(i, "logout")
            self.wait_for(f"recovered archive resave {i}", lambda: self.archive(i).stat().st_mtime_ns != saved_at)
            current = read_archive(self.archive(i))[2]
            for key in ("item2", "item5", "item6", "pet0", "pet1", "hp", "mp", "gld"):
                assert current[key] == checkpoint[i][key], (i, key, current[key], checkpoint[i][key])
        (self.work / "resource-restored.json").write_text(json.dumps(restored, ensure_ascii=False, indent=2))
        (self.work / "resource-result.json").write_text(json.dumps(results[0], ensure_ascii=False, indent=2))
        self.passed("real 5v5 consumed resources survive GMSV SIGKILL, cold CLI login, unrated settlement and archive resave")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--work", type=Path, required=True)
    parser.add_argument("--resource-archive", type=Path, required=True)
    for name in ("sactl", "gateway", "gmsv", "saac", "seed"):
        parser.add_argument("--" + name, type=Path, required=True)
    args = parser.parse_args()
    args.scenario = "resources"
    fixture = ResourceCrash(args)
    try:
        fixture.run_resources(args.resource_archive)
    finally:
        fixture.close()


if __name__ == "__main__":
    main()
