from __future__ import annotations

import json
import re
from pathlib import Path


def load(path):
    path = Path(path).resolve()
    config = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(config, dict) or config.get("schema_version") != 1:
        raise ValueError("configuration schema_version must be 1")
    members = config.get("members", [])
    mode = config.get("mode", len(members))
    if type(mode) is not int or not 1 <= mode <= 5 or len(members) != mode:
        raise ValueError("configure exactly 1–5 members matching mode")
    def absolute(value):
        if not isinstance(value, str) or not value:
            raise ValueError("path must be a nonempty string")
        p = Path(value).expanduser()
        return str((p if p.is_absolute() else path.parent / p).resolve())
    ids, sockets = set(), set()
    for member in members:
        if not isinstance(member, dict) or not re.fullmatch(r"[a-z][a-z0-9_-]{0,31}", member.get("id", "")):
            raise ValueError("member IDs must be unique lowercase aliases")
        member["config"], member["socket"] = absolute(member["config"]), absolute(member["socket"])
        if member["id"] in ids or member["socket"] in sockets:
            raise ValueError("members cannot share an ID or socket")
        ids.add(member["id"])
        sockets.add(member["socket"])
        if "pet_mask" in member and (type(member["pet_mask"]) is not int or not 0 <= member["pet_mask"] <= 31):
            raise ValueError("pet_mask must be 0–31")
    config["mode"] = mode
    config["state_dir"] = absolute(config["state_dir"])
    config.setdefault("strategy", "basic")
    config.setdefault("fallback", "basic")
    config.setdefault("sactl", "sactl")
    if "/" in config["sactl"]:
        config["sactl"] = absolute(config["sactl"])
    if config["fallback"] != "basic":
        raise ValueError("fallback currently must be basic")
    if config["strategy"] in ("llm", "hybrid") and "llm" not in config:
        raise ValueError("selected strategy requires local llm configuration")
    if config["strategy"] in ("learned", "hybrid"):
        config["model"] = absolute(config["model"])
    return config
