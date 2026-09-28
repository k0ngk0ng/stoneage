from __future__ import annotations

import argparse
import asyncio
import json
import os
import signal
import sys

from .config import load
from .runtime import Runner
from .learning import evaluate, train


def main():
    if len(sys.argv)>1 and sys.argv[1] == "simulate":
        from .simulate import main as simulate
        simulate(sys.argv[2:])
        return
    parser = argparse.ArgumentParser(description="Local sactl squad commander")
    parser.add_argument("command", choices=("check", "run", "train", "evaluate"))
    parser.add_argument("--config")
    parser.add_argument("--database", action="append", default=[])
    parser.add_argument("--output")
    parser.add_argument("--model")
    parser.add_argument("--seed", type=int, default=1)
    parser.add_argument("--epochs", type=int, default=80)
    count = parser.add_mutually_exclusive_group()
    count.add_argument("--matches", type=int, default=1)
    count.add_argument("--forever", action="store_true")
    args = parser.parse_args()
    if args.matches < 1:
        parser.error("matches must be positive; use --forever for continuous matching")
    os.umask(0o077)
    if args.command == "evaluate":
        if not args.database or not args.model:
            parser.error("evaluate requires --database and --model")
        print(json.dumps(evaluate(args.database,args.model),ensure_ascii=False))
        return
    if args.command == "train":
        if not args.database or not args.output or not 1 <= args.epochs <= 10000:
            parser.error("train requires --database, --output and epochs in 1..10000")
        print(json.dumps(train(args.database,args.output,args.seed,args.epochs),ensure_ascii=False))
        return
    if not args.config:
        parser.error("check/run requires --config")
    config = load(args.config)
    if args.command == "check":
        strategy = Runner.make_strategy(config)
        print(json.dumps({"ok": True, "mode": config["mode"], "strategy": strategy.id,
                          "members": [m["id"] for m in config["members"]]}))
        return
    async def run():
        runner = Runner(config)
        loop = asyncio.get_running_loop()
        task = asyncio.current_task()
        def stop():
            if runner.stop_requested:
                task.cancel()
            runner.stop_requested = True
        for sig in (signal.SIGINT, signal.SIGTERM):
            loop.add_signal_handler(sig, stop)
        await runner.run(0 if args.forever else args.matches)
    asyncio.run(run())


if __name__ == "__main__":
    main()
