"""Outcome-trained joint-plan value model, with shared training/inference features.

This small CPU baseline learns correlations with team wins, not a proof of an
optimal policy. Native self-play evaluation is required before promotion.
"""
from __future__ import annotations

import json
import asyncio
import math
import random
import sqlite3
import time
from collections import defaultdict
from pathlib import Path

from .contract import ContractError, Decision, digest, plan_for, selectable, validate_plan

FEATURE_VERSION = "joint-observed-v1"
RULES_VERSION = "stoneage-native-ladder-v1"


def ratio(a, b):
    return max(0., min(1., float(a or 0) / max(1., float(b or 0))))


def features(team, choices):
    """Pure observed-state features. No names, session IDs, enemy private stats,
    result labels, future packets, or strategy identities enter the model.
    Team interactions couple members' actions in one score.
    """
    out = defaultdict(float, bias=1., mode=team["mode"]/5)
    participants = {}
    for view in team["members"].values():
        for p in view["battle"].get("Participants", []):
            participants.setdefault(p["BattleID"], p)
    for side, label in ((team["side"], "ally"), (1-team["side"], "enemy")):
        roster = [p for bid,p in participants.items() if bid//10 == side]
        out[label+"_hp"] = sum(ratio(p["HP"],p["MaxHP"]) for p in roster)/max(1,len(roster))
        out[label+"_alive"] = sum(p["HP"] > 0 for p in roster)/max(1,len(roster))
    attacks = defaultdict(int)
    slots = selectable(team)
    scale = max(1, len(slots))
    for key, candidate_id in choices.items():
        c = slots[key][candidate_id]
        member, actor = key
        view = team["members"][member]
        bid = view["battle"]["MyNo"] + (5 if actor == "pet" else 0)
        me = participants.get(bid, {})
        own = view.get("own", {})
        if actor == "pet":
            own = next((p for p in view.get("pets", []) if p.get("Slot") == own.get("BattlePetSlot")), {})
        target = c["target"]
        p = participants.get(target, {})
        kind = c["kind"]
        prefix = actor+":"+kind
        out[prefix] += 1/scale
        out[prefix+":own_hp"] += ratio(me.get("HP"),me.get("MaxHP"))/scale
        for stat in ("Attack", "Defense", "Quick", "Vital", "Strength", "Toughness", "Dexterity", "Earth", "Water", "Fire", "Wind"):
            if stat in own:
                out[prefix+":"+stat] += min(10., max(0.,float(own[stat]))/100)/scale
                out[prefix+":"+stat+":known"] += 1/scale
        if actor == "player":
            out[prefix+":mp"] += ratio(view["battle"].get("MyMP"),own.get("MaxMP"))/scale
        if 0 <= target < 20:
            enemy = target//10 != team["side"]
            relation = ":enemy" if enemy else ":ally"
            out[prefix+relation] += 1/scale
            out[prefix+relation+":target_hp"] += ratio(p.get("HP"),p.get("MaxHP"))/scale
            out[prefix+relation+":target_player"] += (target%10 < 5)/scale
            if kind == "attack" or kind == "skill" and c.get("skill_id") == 1:
                attacks[target] += 1
        if c.get("skill_id"):
            out["skill:"+str(c["skill_id"])] += 1/scale
    for target, count in attacks.items():
        label = "enemy" if target//10 != team["side"] else "ally"
        hp = ratio(participants.get(target, {}).get("HP"), participants.get(target, {}).get("MaxHP"))
        out["focus:"+label] += count*(count-1)/(scale*scale)
        out["focus_low_hp:"+label] += count*(count-1)*(1-hp)/(scale*scale)
    return dict(out)


def score(weights, vector):
    return sum(weights.get(k, 0)*v for k,v in vector.items())


def sigmoid(value):
    return 1/(1+math.exp(-max(-30.,min(30.,value))))


def action_class(team,candidate):
    target = candidate["target"]
    relation = "enemy" if 0 <= target < 20 and target//10 != team["side"] else "ally" if 0 <= target < 20 else "group_or_none"
    return f'{candidate["actor"]}:{candidate["kind"]}:{candidate.get("skill_id",0)}:{relation}'


class Learned:
    id = "learned"

    def __init__(self, path, mode):
        self.model = json.loads(Path(path).read_text())
        m = self.model
        if m.get("schema_version") != 1 or m.get("features") != FEATURE_VERSION or m.get("rules") != RULES_VERSION:
            raise ValueError("model feature/rules schema is incompatible")
        if mode not in m.get("modes", []):
            raise ValueError(f"model has no training coverage for {mode}v{mode}")
        if not m.get("weights") or any(not isinstance(v,(int,float)) or not math.isfinite(v) for v in m["weights"].values()):
            raise ValueError("model weights are invalid")
        self.version = digest(m)
        self.mode = mode

    async def decide(self, team, history, deadline):
        if team["mode"] != self.mode:
            raise ContractError("model mode mismatch")
        if team.get("rules_version") != self.model["rules"]:
            raise ContractError("server rules differ from model rules")
        # Beam search keeps joint focus/healing interactions; members never
        # run independent policies. Width bounds latency even with full menus.
        beam = [(0., {})]
        for key, candidates in sorted(selectable(team).items()):
            candidates = {k:v for k,v in candidates.items() if action_class(team,v) in self.model["action_support"]}
            if not candidates:
                raise ContractError("model has no observed action class for this slot; use configured fallback")
            expanded = []
            for _, choices in beam:
                for candidate in candidates:
                    if time.monotonic() >= deadline:
                        raise TimeoutError("local inference deadline")
                    new = {**choices, key: candidate}
                    expanded.append((score(self.model["weights"], features(team,new)),new))
            beam = sorted(expanded,key=lambda x:x[0],reverse=True)[:8]
            await asyncio.sleep(0)
        value, choices = beam[0]
        return Decision(validate_plan(team,plan_for(team,choices)), self.id,self.version,
                        {"estimated_win_probability":sigmoid(value), "model_status":self.model["status"], "beam_width":8})


def examples(paths):
    rows, excluded = [], defaultdict(int)
    for path in paths:
        db = sqlite3.connect(Path(path).resolve().as_uri()+"?mode=ro",uri=True)
        try:
            results = {r[0]:json.loads(r[1]) for r in db.execute("SELECT match_id,body FROM results")}
            gaps = {r[0] for r in db.execute("SELECT DISTINCT match_id FROM records WHERE kind='event_gap'")}
            gaps.update(r[0] for r in db.execute("SELECT DISTINCT match_id FROM battle_intents WHERE status='uncertain'"))
            for match_id,body in db.execute("SELECT match_id,body FROM records WHERE kind='turn' ORDER BY id"):
                result = results.get(match_id)
                row = json.loads(body)
                team,plan = row["team"],row["plan"]
                if team.get("rules_version") != RULES_VERSION:
                    excluded["unknown_or_incompatible_rules"] += 1
                    continue
                if not result or result.get("reason") != "defeat" or result.get("winner_side") not in (0,1) or match_id in gaps or team["missing_ids"]:
                    excluded["incomplete_or_noncombat"] += 1
                    continue
                choices = {(o["member_id"],o["actor"]):o["candidate_id"] for o in plan["orders"]}
                valid = True
                for (member,actor),candidate in choices.items():
                    intent = db.execute("SELECT status,body FROM battle_intents WHERE member=? AND match_id=? AND turn=? AND actor=?",
                                        (member,match_id,plan["turn"],actor)).fetchone()
                    if not intent or intent[0] != "written" or json.loads(intent[1])["candidate_id"] != candidate:
                        valid = False
                if not choices or not valid:
                    excluded["unconfirmed_plan"] += 1
                    continue
                validate_plan(team,plan)
                # Same roster and mode remain in one split across rematches and
                # across both teams' databases. Prevent outcome-label leakage.
                roster = sorted(p["id"] for p in result["members"])
                group = digest({"mode":team["mode"],"roster":roster})
                slots = selectable(team)
                rows.append({"x":features(team,choices),"y":int(result["winner_side"]==team["side"]),
                             "support":sorted({action_class(team,slots[k][v]) for k,v in choices.items()}),
                             "group":group,"match":match_id,"mode":team["mode"]})
        finally:
            db.close()
    return rows,dict(excluded)


def metrics(rows,weights):
    if not rows:
        return None
    loss = sum(-(r["y"]*math.log(max(1e-12,sigmoid(score(weights,r["x"]))))+
                 (1-r["y"])*math.log(max(1e-12,1-sigmoid(score(weights,r["x"]))))) for r in rows)/len(rows)
    return {"rows":len(rows),"matches":len({r["match"] for r in rows}),"log_loss":loss,
            "accuracy":sum((score(weights,r["x"])>=0)==bool(r["y"]) for r in rows)/len(rows)}


def train(paths, output, seed=1, epochs=80):
    rows,excluded = examples(paths)
    if len(rows)<4 or len({r["y"] for r in rows})<2:
        raise ValueError("need at least four confirmed plans with both winning and losing outcomes")
    groups = sorted({r["group"] for r in rows})
    rng = random.Random(seed)
    rng.shuffle(groups)
    held = set()
    for mode in sorted({r["mode"] for r in rows}):
        candidates = [g for g in groups if any(r["group"] == g and r["mode"] == mode for r in rows)]
        if len(candidates) > 1:
            held.update(candidates[:max(1,len(candidates)//5)])
    training = [r for r in rows if r["group"] not in held]
    testing = [r for r in rows if r["group"] in held]
    weights = {}
    counts = defaultdict(int)
    for r in training: counts[r["match"]] += 1
    for epoch in range(epochs):
        rng.shuffle(training)
        for r in training:
            error = r["y"]-sigmoid(score(weights,r["x"]))
            rate = .15/(1+epoch*.03)/counts[r["match"]]
            for key,value in r["x"].items():
                weights[key] = weights.get(key,0)+rate*(error*value-.001*weights.get(key,0))
    model = {"schema_version":1,"features":FEATURE_VERSION,"rules":RULES_VERSION,
             "status":"experimental", "algorithm":"joint-linear-outcome-v1", "modes":sorted({r["mode"] for r in training}),
             "action_support":sorted({s for r in training for s in r["support"]}),
             "weights":weights,"training":{"seed":seed,"epochs":epochs,"groups":len(groups),"excluded":excluded,
                 "training_groups":sorted(set(groups)-held), "evaluation_groups":sorted(held),
                 "train":metrics(training,weights),"held_out":metrics(testing,weights),
                 "held_out_baseline":metrics(testing,{})},
             "limitations":["Outcome correlation is not a causal action value.",
                             "No claim of win-rate improvement without native paired-policy evaluation.",
                             "Only configured modes and observed loadouts have data coverage."]}
    target = Path(output)
    target.parent.mkdir(parents=True,exist_ok=True)
    # Exclusive creation keeps already pinned models immutable.
    with target.open('x',encoding='utf-8') as file:
        json.dump(model,file,ensure_ascii=False,indent=2,allow_nan=False)
        file.write('\n')
    return {"model":str(target),"version":digest(model),"training":model["training"],"modes":model["modes"]}


def evaluate(paths, model_path):
    model = json.loads(Path(model_path).read_text())
    for mode in model["modes"]:
        Learned(model_path,mode)
    rows,excluded = examples(paths)
    used = set(model["training"]["training_groups"])
    independent = [r for r in rows if r["group"] not in used and r["mode"] in model["modes"]]
    if not independent:
        raise ValueError("no independent evaluation matches; collect new matchups outside the training roster groups")
    return {"model_version":digest(model),"excluded":excluded,"overlapping_rows":sum(r["group"] in used for r in rows),
            "value_prediction":metrics(independent,model["weights"]),"constant_baseline":metrics(independent,{}),
            "note":"Prediction quality is not policy win rate; use native learned-versus-basic matches for that."}
