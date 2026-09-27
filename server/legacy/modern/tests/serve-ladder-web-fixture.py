#!/usr/bin/env python3
"""Serve an isolated native ladder fixture for real-browser acceptance.

Uses the same synthetic accounts, maps, authentication and native binaries as
test-ladder-network.py. Account 0 is released for the browser; account 1 keeps
its sactl daemon for an independently controlled opponent. With --scenario
multiplayer, ten players exchange real name cards before account 0 is released.
Publish ONLY the
Web port on host loopback on a dedicated Docker network. All files
stay in a new build/ directory. SIGTERM or --lifetime expiry stops every child.
This launcher provides no mocked gameplay or acceptance assertions.
"""
import argparse
import importlib.util
import json
from pathlib import Path
import signal
import threading
import time


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--work", type=Path, required=True)
    for name in ("sactl", "gateway", "gmsv", "saac", "seed", "web"):
        parser.add_argument("--" + name, type=Path, required=True)
    parser.add_argument("--web-port", type=int, default=18080)
    parser.add_argument("--lifetime", type=int, default=1800)
    parser.add_argument("--scenario", choices=("basic", "multiplayer"), default="basic")
    args = parser.parse_args()
    if not 1024 <= args.web_port <= 65535 or not 1 <= args.lifetime <= 7200:
        parser.error("use an unprivileged port and a lifetime of 1..7200 seconds")
    source = Path(__file__).with_name("test-ladder-network.py")
    spec = importlib.util.spec_from_file_location("ladder_network", source)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    fixture = module.Fixture(args)
    stop = threading.Event()
    restart = threading.Event()
    signal.signal(signal.SIGUSR1, lambda *_: restart.set())
    for signum in (signal.SIGTERM, signal.SIGINT):
        signal.signal(signum, lambda *_: stop.set())
    try:
        fixture.prepare()
        if args.scenario == "multiplayer":
            fixture.exchange_cards()
        fixture.kill("client0")
        root = module.ROOT
        config = fixture.work / "web.toml"
        config.write_text(
            f'listen_address = "0.0.0.0:{args.web_port}"\n'
            f'tcp_upstream = "127.0.0.1:{fixture.gateway_port}"\n'
            '[static]\n'
            f'assets_directory = "{root}/client/web/assets/original"\n'
            f'maps_directory = "{root}/runtime/legacy-client/map"\n'
            f'audio_directory = "{root}/runtime/legacy-client/data"\n'
            f'npc_directory = "{fixture.work}/gmsv/data/npc"\n')
        fixture.start("web", [args.web.resolve(), "--config", config], root)
        fixture.listener(args.web_port, "web")
        ready = {"web_port": args.web_port, "sactl_binary": str(fixture.binaries["sactl"]),
                 "browser_account": "ladderqa00", "browser_character": "LadderQA00",
                 "password_file": str(fixture.work / "ladderqa00.password"),
                 "opponent_config": str(fixture.work / "ladderqa01.toml"),
                 "player_configs": [str(fixture.work / f"ladderqa{i:02d}.toml")
                                    for i in range(fixture.player_count)],
                 "scenario": args.scenario, "lifetime_seconds": args.lifetime}
        (fixture.work / "web-ready.json").write_text(json.dumps(ready, indent=2) + "\n")
        print(json.dumps(ready), flush=True)
        deadline = time.monotonic() + args.lifetime
        restarts = 0
        while time.monotonic() < deadline and not stop.wait(.2):
            if restart.is_set():
                restart.clear()
                fixture.kill("gmsv")
                fixture.start_gmsv()
                restarts += 1
                marker = fixture.work / "web-restarted.json"
                temporary = marker.with_suffix(".tmp")
                temporary.write_text(json.dumps({"restarts": restarts}) + "\n")
                temporary.replace(marker)
    finally:
        fixture.close()
        (fixture.work / "web-stopped.json").write_text(json.dumps({"stopped": True}) + "\n")


if __name__ == "__main__":
    main()
