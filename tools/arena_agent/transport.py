"""Use the normal sactl client; never open a game protocol connection here."""
from __future__ import annotations

import asyncio
import fcntl
import hashlib
import json
import os
import tomllib
import uuid
from pathlib import Path


class CommandError(RuntimeError):
    def __init__(self, response):
        self.response = response
        super().__init__((response.get("data") or {}).get("code") or response.get("kind") or "command_failed")


class Member:
    def __init__(self, binary, config, store):
        self.binary, self.config, self.store = binary, config, store
        self.id = config["id"]
        self.lock = asyncio.Lock()
        self.process = None
        self.lock_file = None
        self.identity_lock = None
        self.socket_identity = None
        self.service_key = None

    def meeting_claim(self):
        path = Path(__file__).resolve().parents[2] / "build/local-arena/ownership" / (self.service_key+".meeting.lock")
        handle = path.open("a+")
        os.chmod(path,0o600)
        try:
            fcntl.flock(handle,fcntl.LOCK_EX|fcntl.LOCK_NB)
        except BlockingIOError:
            handle.close()
            return None
        return handle

    def claim(self):
        # Same socket => same commander lock even with different state dirs.
        path = Path(self.config["socket"] + ".commander.lock")
        path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        self.lock_file = path.open("a+")
        os.chmod(path, 0o600)
        try:
            fcntl.flock(self.lock_file, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            self.lock_file.close()
            self.lock_file = None
            raise RuntimeError(f"member {self.id} already has a commander") from None

    def command(self, *args):
        return [self.binary, "--config", self.config["config"], "--socket", self.config["socket"],
                "--json", *map(str, args)]

    async def call(self, *args, timeout=8, check=True):
        async with self.lock:
            process = await asyncio.create_subprocess_exec(*self.command(*args),
                stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.DEVNULL)
            try:
                stdout, _ = await asyncio.wait_for(process.communicate(), timeout)
            except BaseException:
                if process.returncode is None:
                    process.kill()
                    await process.wait()
                raise
            try:
                response = json.loads(stdout)
                if not isinstance(response, dict) or type(response.get("ok")) is not bool:
                    raise ValueError()
            except (ValueError, UnicodeError):
                # The daemon may have submitted before its caller lost the reply.
                raise CommandError({"ok": False, "kind": "unknown"}) from None
            if check and not response["ok"]:
                raise CommandError(response)
            return response

    async def start(self):
        with Path(self.config["config"]).open("rb") as source:
            configured = tomllib.load(source)
        self.service_key = hashlib.sha256(json.dumps([configured.get(k) for k in ("transport","address")]).encode()).hexdigest()
        if Path(configured.get("socket_path", "")).expanduser().resolve() != Path(self.config["socket"]).resolve():
            raise ValueError(f"member {self.id}: socket must match sactl config socket_path")
        if self.lock_file is None:
            self.claim()
        if self.identity_lock is None:
            identity = json.dumps([configured.get(k) for k in ("transport", "address", "account", "character")])
            directory = Path(__file__).resolve().parents[2] / "build/local-arena/ownership"
            directory.mkdir(parents=True, exist_ok=True, mode=0o700)
            path = directory / (hashlib.sha256(identity.encode()).hexdigest()+".lock")
            self.identity_lock = path.open("a+")
            os.chmod(path, 0o600)
            try:
                fcntl.flock(self.identity_lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError:
                self.identity_lock.close()
                self.identity_lock = None
                raise RuntimeError(f"member {self.id} identity already has a commander") from None
        try:
            await self.call("ping", timeout=2)
            return
        except (CommandError, TimeoutError):
            pass
        socket = Path(self.config["socket"])
        if self.process and self.process.returncode is not None and socket.exists() and self.socket_identity:
            stat = socket.stat()
            if socket.is_socket() and (stat.st_dev,stat.st_ino) == self.socket_identity:
                socket.unlink()
        if Path(self.config["socket"]).exists():
            raise RuntimeError(f"existing socket for {self.id} is unresponsive; refusing a second daemon")
        log_path = self.store.directory / (self.id + "-sactl.log")
        with log_path.open("ab") as log:
            os.chmod(log_path, 0o600)
            self.process = await asyncio.create_subprocess_exec(self.binary, "serve", "--config", self.config["config"],
                stdout=log, stderr=log)
        for _ in range(40):
            if self.process.returncode is not None:
                raise RuntimeError(f"sactl daemon for {self.id} exited")
            try:
                await self.call("ping", timeout=1)
                stat = socket.stat()
                self.socket_identity = (stat.st_dev,stat.st_ino)
                return
            except (CommandError, TimeoutError):
                await asyncio.sleep(.1)
        raise RuntimeError(f"sactl daemon for {self.id} did not become ready")

    async def mutate(self, operation, *args):
        pending = self.store.pending(self.id)
        if pending:
            # Recover the exact prior operation before making a new one.
            reply = await self._retry(pending)
            if pending["operation"] == operation and pending["args"] == list(map(str, args)):
                return reply
        status = await self.call("ladder", "status")
        pending = {"operation": operation, "args": list(map(str, args)), "request_id": uuid.uuid4().hex,
                   "revision": status["data"]["revision"]}
        self.store.set_pending(self.id, pending)
        return await self._retry(pending)

    async def _retry(self, pending):
        response = None
        for attempt in range(3):
            try:
                response = await self.call("ladder", pending["operation"], *pending["args"],
                    "--request-id", pending["request_id"], "--revision", pending["revision"], timeout=10, check=False)
            except (CommandError, TimeoutError):
                response = {"ok": False, "kind": "unknown"}
            if response.get("kind") != "unknown" and (response.get("data") or {}).get("code") != "outcome_unknown":
                self.store.record("ladder_receipt", {"request": pending, "response": response}, member=self.id)
                self.store.set_pending(self.id, None)
                if not response["ok"]:
                    raise CommandError(response)
                return response
            await asyncio.sleep(.1 * (attempt+1))
        raise CommandError(response)

    async def close(self):
        # Only shut down daemons we started. Existing sessions stay owned by
        # their operator. Kill and wait before releasing the commander lock.
        if self.process and self.process.returncode is None:
            self.process.terminate()
            try:
                await asyncio.wait_for(self.process.wait(), 5)
            except TimeoutError:
                self.process.kill()
                await self.process.wait()
        if self.lock_file:
            self.lock_file.close()
            self.lock_file = None
        if self.identity_lock:
            self.identity_lock.close()
            self.identity_lock = None
