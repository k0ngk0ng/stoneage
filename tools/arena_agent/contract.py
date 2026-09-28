"""Provider-independent, strictly validated squad observations and plans."""
from __future__ import annotations

import hashlib
import json
from dataclasses import dataclass


class ContractError(ValueError):
    pass


def digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def team_observation(views: dict, expected_ids: set[str], mode: int):
    """Only combine the same team's same match/turn. Never select a newer
    turn by averaging or by guessing that every member has received it.
    Offline members may be absent; identity and expected roster stay explicit.
    """
    if not views or not 1 <= mode <= 5 or len(expected_ids) != mode:
        raise ContractError("invalid or empty squad")
    matches, turns, sides, identities = set(), set(), set(), set()
    for member, view in views.items():
        if view.get("schema_version") != 1 or view.get("character_id") not in expected_ids:
            raise ContractError(f"unexpected member observation: {member}")
        battle = view["battle"]
        if not battle["Active"] or battle["Ended"] or not battle["MyNoKnown"]:
            raise ContractError("member has no active battle seat")
        bid = battle["MyNo"]
        if bid not in (*range(5), *range(10, 15)):
            raise ContractError("invalid player seat")
        identities.add(view["character_id"])
        matches.add(view["match_id"])
        turns.add(view["turn"])
        sides.add(bid // 10)
        if view["mode"] != mode:
            raise ContractError("mode mismatch")
    if len(matches) != 1 or "" in matches or len(turns) != 1 or len(sides) != 1 or len(identities) != len(views):
        raise ContractError("mixed match, turn, side or duplicated identity")
    team = {"schema_version": 1, "match_id": next(iter(matches)), "turn": next(iter(turns)),
            "mode": mode, "side": next(iter(sides)), "members": views,
            "missing_ids": sorted(expected_ids - identities)}
    rules = {v["battle"].get("Clock",{}).get("RulesVersion","") for v in views.values()}
    if len(rules) != 1:
        raise ContractError("team observes inconsistent server rule versions")
    team["rules_version"] = next(iter(rules))
    team["observation_id"] = digest({m: v["observation_id"] for m, v in sorted(views.items())})
    return team


def selectable(team):
    """Include the pet slot in the joint plan before the player's write.
    Readiness is checked again during serial dispatch for that member.
    """
    slots = {}
    for member, view in team["members"].items():
        battle = view["battle"]
        for candidate in view["candidates"]:
            actor = candidate["actor"]
            if actor not in ("player", "pet"):
                raise ContractError("unsupported actor")
            if battle["PlayerSubmitted" if actor == "player" else "PetSubmitted"]:
                continue
            if actor in view.get("reserved_actors", {}):
                continue
            slots.setdefault((member, actor), {})[candidate["id"]] = candidate
    return slots


def plan_for(team, choices):
    return {"schema_version": 1, "match_id": team["match_id"], "turn": team["turn"],
            "observation_id": team["observation_id"], "orders": [
                {"member_id": member, "actor": actor, "candidate_id": action_id}
                for (member, actor), action_id in sorted(choices.items())]}


def validate_plan(team, plan):
    if not isinstance(plan, dict) or set(plan) != {"schema_version", "match_id", "turn", "observation_id", "orders"}:
        raise ContractError("plan must match the exact schema")
    for key in ("schema_version", "match_id", "turn", "observation_id"):
        if type(plan[key]) is not type(team[key]) or plan[key] != team[key]:
            raise ContractError(f"stale/invalid plan {key}")
    slots = selectable(team)
    seen = set()
    if not isinstance(plan["orders"], list) or len(plan["orders"]) != len(slots):
        raise ContractError("one order per unsubmitted actor is required")
    for order in plan["orders"]:
        if not isinstance(order, dict) or set(order) != {"member_id", "actor", "candidate_id"}:
            raise ContractError("invalid order schema")
        if any(not isinstance(value, str) for value in order.values()):
            raise ContractError("order fields must be strings")
        key = order["member_id"], order["actor"]
        if key in seen or order["candidate_id"] not in slots.get(key, {}):
            raise ContractError("duplicate, foreign or unavailable order")
        seen.add(key)
    return plan


@dataclass(frozen=True)
class Decision:
    plan: dict
    strategy: str
    version: str
    diagnostics: dict
