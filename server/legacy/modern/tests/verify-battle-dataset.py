#!/usr/bin/env python3
"""Validate real-engine generated data, not a substitute battle simulator."""
import argparse
import importlib.util
import json
from pathlib import Path
from types import SimpleNamespace

parser = argparse.ArgumentParser()
parser.add_argument("root", type=Path)
parser.add_argument("--exporter", type=Path, required=True)
parser.add_argument("--matches", type=int, required=True)
parser.add_argument("--points", type=int, default=120)
parser.add_argument("--censored", action="store_true")
args = parser.parse_args()
spec = importlib.util.spec_from_file_location("exporter", args.exporter)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
paths = list(args.root.glob("????-??-??/*/metadata.json"))
assert len(paths) == args.matches, (len(paths), args.matches)
opts = SimpleNamespace(mode="pvp-1v1", format="builds", equal_points=True, include_abnormal=False)
transitions = 0
normal = 0
groups = {}
for path in paths:
    metadata, events, result = module.load_match(path.parent)
    opts.format = "builds"
    rows = module.export_match(metadata, events, result, opts)
    assert len(rows) == 1, (path, result)
    row = rows[0]
    assert row["fairness"]["budget_raw"] == [args.points*100]*2
    assert all(b["allocation"]["unspent_points"] == 0 for b in row["builds"])
    exp = metadata["experiment"]
    groups.setdefault(exp["pair_index"], []).append(row)
    opts.format = "transitions"
    steps = module.export_match(metadata, events, result, opts)
    assert len(steps) == result["turn"]*2, (len(steps), result)
    assert all(s["submitted_actions"] and s["resolution_commands"] for s in steps)
    assert all(s["resolution_order"] for s in steps)
    terminal = [s for s in steps if s["terminated"] or s["truncated"]]
    assert len(terminal) == 2
    if args.censored:
        assert result["end_reason"] == "turn_limit" and result["winner_side"] == -1
        assert all(s["truncated"] and s["reward"] == 0 for s in terminal)
    elif result["end_reason"] == "defeat":
        normal += 1
        assert sorted(s["reward"] for s in terminal) == [-1, 1]
    transitions += len(steps)
for group in groups.values():
    ordered = sorted(group, key=lambda row: row["experiment"]["match_index"])
    for first, second in zip(ordered[::2], ordered[1::2]):
        assert first["builds"][0]["allocation"] == second["builds"][1]["allocation"]
        assert first["builds"][1]["allocation"] == second["builds"][0]["allocation"]
        assert first["split_group"] == second["split_group"]
if not args.censored:
    assert normal > 0
print(json.dumps({"matches": len(paths), "transitions": transitions, "normal_results": normal, "valid": True}))
