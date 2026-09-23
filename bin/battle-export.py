#!/usr/bin/env python3
"""Export validated battle trajectories or equal-budget build outcomes as JSONL.

Only player-visible observations enter model inputs. Server commands are
separate labels. Truncated, corrupt and unsupported records fail closed.
"""
import argparse
import collections
import gzip
import hashlib
import json
from pathlib import Path
import sys


class InvalidMatch(ValueError):
    pass


def read_json(path):
    with path.open(encoding="utf-8") as stream:
        return json.load(stream)


def clean(event):
    return {k: v for k, v in event.items() if k not in
            {"schema_version", "match_id", "seq", "turn", "at", "type", "side", "observation_id"}}


def load_match(directory):
    metadata = read_json(directory / "metadata.json")
    result = read_json(directory / "result.json")
    if metadata.get("schema_version") != 1 or result.get("schema_version") != 1:
        raise InvalidMatch("unsupported schema")
    if not result.get("trajectory_complete") or not result.get("storage_complete") or result.get("dropped_events") != 0:
        raise InvalidMatch("incomplete recording")
    path = directory / "events.jsonl"
    opener = path.open
    if not path.exists():
        path = directory / "events.jsonl.gz"
        opener = lambda **kw: gzip.open(path, "rt", **kw)
    with opener(encoding="utf-8") as stream:
        events = [json.loads(line) for line in stream]
    if not events or metadata.get("seq") != 1:
        raise InvalidMatch("missing initial sequence")
    for expected, event in enumerate([metadata, *events, result], 1):
        if event.get("seq") != expected or event.get("match_id") != metadata["match_id"] or event.get("schema_version") != 1:
            raise InvalidMatch("sequence or match identity mismatch")
    if result.get("winner_side") not in (-1, 0, 1):
        raise InvalidMatch("invalid winner")
    return metadata, events, result


def observations(events):
    output = {}
    names = {"visible_actor": "actors", "own_actor": "own_actors", "allocation": "allocations",
             "pet_skill": "pet_skills", "item": "items"}
    ended = set()
    for event in events:
        kind = event["type"]
        if kind == "observation":
            obs = clean(event)
            obs.update({"observation_id": event["seq"], "turn": event["turn"], "side": event["side"]})
            for key in names.values():
                obs[key] = []
            output[event["seq"]] = obs
        elif kind in names or kind == "observation_end":
            ref = event["observation_id"]
            if ref not in output or ref in ended or event["side"] != output[ref]["side"] or event["turn"] != output[ref]["turn"]:
                raise InvalidMatch("broken observation reference")
            if kind == "observation_end":
                ended.add(ref)
            else:
                output[ref][names[kind]].append(clean(event))
    if set(output) != ended:
        raise InvalidMatch("unfinished observation")
    for obs in output.values():
        if not any(a["pet_slot"] == -1 for a in obs["own_actors"]) or not any(a["pet_slot"] == -1 for a in obs["allocations"]):
            raise InvalidMatch("missing own build")
    return output


def allocation(obs):
    return next(a for a in obs["allocations"] if a["pet_slot"] == -1)


def budget(build):
    keys = ("vitality_raw", "strength_raw", "toughness_raw", "dexterity_raw")
    if build.get("raw_units_per_point") != 100 or any(type(build.get(k)) is not int or build[k] < 0 for k in keys):
        raise InvalidMatch("invalid point units")
    unspent = build.get("unspent_points")
    if type(unspent) is not int or unspent < 0:
        raise InvalidMatch("invalid unspent points")
    value = sum(build[k] for k in keys) + unspent * 100
    if value != build.get("budget_raw"):
        raise InvalidMatch("point budget mismatch")
    return value


def initial_observations(obs):
    initial = {}
    for row in obs.values():
        initial.setdefault(row["my_bid"], row)
    return initial


def fairness(initial):
    if len(initial) != 2 or {o["side"] for o in initial.values()} != {0, 1}:
        return {"equal_points": False, "controlled_context": False}
    pair = sorted(initial.values(), key=lambda o: o["side"])
    builds = [allocation(o) for o in pair]
    equal = budget(builds[0]) == budget(builds[1])
    def controls(obs):
        a = allocation(obs)
        own = next(v for v in obs["own_actors"] if v["pet_slot"] == -1)
        return {
            "level": a["level"], "rebirths": a["rebirths"], "charm": a["charm"], "luck": a["luck"],
            "base_elements": a["base_elements"], "unspent_points": a["unspent_points"],
            "active_pet_slot": own["active_pet_slot"], "ride_pet_slot": own["ride_pet_slot"],
            "pets": [p for p in obs["own_actors"] if p["pet_slot"] >= 0],
            "pet_allocations": [p for p in obs["allocations"] if p["pet_slot"] >= 0],
            "pet_skills": obs["pet_skills"], "items": obs["items"],
            "full_health": own["hp"] == own["max_hp"] and own["mp"] == own["max_mp"],
            "visible_status": [{k: a[k] for k in ("flags", "ride_flag")} for a in obs["actors"] if a["bid"] == obs["my_bid"]],
        }
    contexts = [controls(o) for o in pair]
    controlled = contexts[0] == contexts[1] and contexts[0]["full_health"] and all(a["unspent_points"] == 0 for a in builds)
    return {"equal_points": equal, "controlled_context": controlled,
            "budget_raw": [budget(a) for a in builds], "controls": contexts,
            "experimental_control_verified": False}


def common(metadata, result):
    experiment = metadata.get("experiment")
    # All repetitions and swapped sides of one matchup belong to one split.
    group = (f'{metadata["ruleset_id"]}:{experiment["run_seed"]}:{experiment["pair_index"]}'
             if experiment else metadata["match_id"])
    split = int(hashlib.sha256(group.encode()).hexdigest()[:8], 16) % 100
    return {"schema_version": 1, "match_id": metadata["match_id"], "mode": metadata["mode"],
            "ruleset_id": metadata["ruleset_id"], "release": metadata["release"],
            "experiment": experiment, "split_group": group,
            "suggested_split": "train" if split < 80 else "validation" if split < 90 else "test",
            "winner_side": result["winner_side"], "end_reason": result["end_reason"]}


def export_match(metadata, events, result, args):
    mode = metadata["mode"]
    if args.mode != "all" and mode != args.mode and not (args.mode == "pvp-1v1" and mode == "pvp"):
        return []
    if args.mode == "pvp-1v1" and metadata["players_per_side"] != [1, 1]:
        return []
    if result["end_reason"] not in ("defeat", "turn_limit") and not args.include_abnormal:
        return []
    obs = observations(events)
    initial = initial_observations(obs)
    fair = fairness(initial)
    if args.equal_points and (mode != "pvp" or not fair["equal_points"]):
        return []
    base = common(metadata, result)
    if args.format == "builds":
        # Equal budget alone is insufficient: require matching observed
        # conditions. Even then observational data does not prove causality.
        if mode != "pvp" or metadata["players_per_side"] != [1, 1] or not fair["equal_points"] or not fair["controlled_context"]:
            return []
        return [{**base, "fairness": fair,
                 "builds": [{"side": o["side"], "allocation": allocation(o), "initial_observation": o}
                            for o in sorted(initial.values(), key=lambda o: o["side"])],
                 "outcome": "censored" if result["winner_side"] < 0 else "win_loss",
                 "rounds": result["turn"]}]
    requests = collections.defaultdict(list)
    executed = collections.defaultdict(list)
    resolution_order = collections.defaultdict(list)
    for event in events:
        if event["type"] == "action_request":
            ref = event["observation_id"]
            if ref not in obs or event["side"] != obs[ref]["side"]:
                raise InvalidMatch("action has no matching observation")
            requests[ref].append(clean(event))
        elif event["type"] == "execution_command":
            executed[(event["decision_turn"], event["bid"])].append(clean(event))
        elif event["type"] == "action_resolution":
            resolution_order[event["decision_turn"]].append(clean(event))
    by_player = collections.defaultdict(list)
    for row in obs.values():
        by_player[row["my_bid"]].append(row)
    rows = []
    for bid, states in by_player.items():
        for current, nxt in zip(states, states[1:]):
            turn = current["turn"]
            if nxt["turn"] != turn+1:
                raise InvalidMatch("nonconsecutive decision states")
            terminal = nxt["turn"] == result["turn"]
            winner = result["winner_side"]
            reward = (1 if winner == current["side"] else -1) if terminal and winner >= 0 else 0
            rows.append({**base, "actor_bid": bid, "side": current["side"], "turn": turn,
                         "observation": current, "submitted_actions": requests[current["observation_id"]],
                         "resolution_commands": executed[(turn, bid)] + executed[(turn, bid+5)],
                         "resolution_order": resolution_order[turn],
                         "next_observation": nxt, "reward": reward,
                         "terminated": terminal and winner >= 0,
                         "truncated": terminal and winner < 0,
                         "policy_version": next((p["policy_version"] for p in metadata["players"] if p["bid"] == bid), None)})
    return rows


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("root", type=Path)
    parser.add_argument("--format", choices=("transitions", "builds"), default="transitions")
    parser.add_argument("--mode", choices=("all", "pve", "pvp", "pvp-1v1"), default="pvp-1v1")
    parser.add_argument("--equal-points", action="store_true")
    parser.add_argument("--include-abnormal", action="store_true")
    args = parser.parse_args()
    if not args.root.is_dir():
        parser.error("record root must be an existing directory")
    counts = collections.Counter()
    directories = [args.root] if (args.root / "metadata.json").exists() else sorted(p.parent for p in args.root.glob("????-??-??/*/metadata.json"))
    for directory in directories:
        counts["matches_seen"] += 1
        try:
            metadata, events, result = load_match(directory)
            rows = export_match(metadata, events, result, args)
        except (OSError, ValueError, KeyError, TypeError, StopIteration, EOFError):
            counts["invalid_or_incomplete"] += 1
            continue
        counts["exported_matches" if rows else "filtered_matches"] += 1
        for row in rows:
            print(json.dumps(row, ensure_ascii=False, separators=(",", ":")))
            counts["rows"] += 1
    print(json.dumps(dict(counts), sort_keys=True), file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
