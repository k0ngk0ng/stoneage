#!/usr/bin/env python3
"""Round-trip a native ladder checkpoint through an isolated real SAAC.

This tests the SAAC listener/save/load boundary, not gateway/player login or
the whole Web/sactl match flow. The caller supplies a dedicated build folder.
"""
import argparse
import os
from pathlib import Path
import socket
import subprocess
import time


def base62(value):
    digits = b"0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
    negative = value < 0
    value = abs(value)
    encoded = b""
    while value:
        value, remainder = divmod(value, 62)
        encoded = bytes([digits[remainder]]) + encoded
    return (b"-" if negative else b"") + (encoded or b"0")


def escape(value):
    if isinstance(value, int):
        return base62(value)
    if isinstance(value, str):
        value = value.encode("ascii")
    escaped = bytearray()
    replacements = {ord("\\"): b"\\\\", ord(" "): b"\\S", ord("\n"): b"\\n", ord("\r"): b"\\r"}
    i = 0
    while i < len(value):
        # The native protocol preserves both bytes of CP936 characters;
        # their second byte may itself equal an ASCII backslash.
        if value[i] >= 0x80 and i + 1 < len(value):
            escaped.extend(value[i:i + 2]); i += 2
        else:
            escaped.extend(replacements.get(value[i], bytes([value[i]]))); i += 1
    return bytes(escaped)


def unescape(value):
    decoded = bytearray()
    escapes = {ord("S"): b" ", ord("n"): b"\n", ord("r"): b"\r", ord("\\"): b"\\"}
    i = 0
    while i < len(value):
        if value[i] >= 0x80 and i + 1 < len(value):
            decoded.extend(value[i:i + 2])
            i += 2
        elif value[i] == ord("\\") and i + 1 < len(value):
            decoded.extend(escapes.get(value[i + 1], b"\\"))
            i += 2
        else:
            decoded.append(value[i])
            i += 1
    return bytes(decoded)


class SAAC:
    def __init__(self, binary, work, port):
        self.binary, self.work, self.port = binary, work, port
        self.process = self.connection = self.reader = self.log = None
        self.serial = 0

    def start(self):
        self.log = (self.work / "process.log").open("ab")
        environment = dict(os.environ, STONEAGE_PLAYER_ADMIN_DIR=str(self.work / "admin"))
        self.process = subprocess.Popen([str(self.binary)], cwd=self.work, env=environment, stdout=self.log, stderr=subprocess.STDOUT)
        deadline = time.monotonic() + 8
        while time.monotonic() < deadline:
            if self.process.poll() is not None:
                raise RuntimeError(f"SAAC exited; inspect {self.work / 'process.log'}")
            try:
                self.connection = socket.create_connection(("127.0.0.1", self.port), timeout=1)
                break
            except OSError:
                time.sleep(0.025)
        if self.connection is None:
            raise RuntimeError("isolated SAAC did not start its listener")
        self.connection.settimeout(5)
        self.reader = self.connection.makefile("rb")
        response = self.call("ACServerLogin", "ladder-fixture", "checkpoint-fixture", 0)
        assert response[2] == b"successful", "fixture server authentication failed"

    def call(self, operation, *arguments):
        self.serial += 1
        message = str(self.serial).encode() + b" " + operation.encode() + b" "
        message += b" ".join(escape(value) for value in arguments) + b" \n"
        self.connection.sendall(message)
        for _ in range(20):
            line = self.reader.readline(200000)
            if not line:
                raise RuntimeError(f"SAAC closed during {operation}")
            fields = line.rstrip(b"\r\n").split(b" ")
            if len(fields) >= 3 and fields[1] == operation.encode():
                return [unescape(field) for field in fields]
        raise RuntimeError(f"missing SAAC {operation} reply")

    def stop(self):
        # Deliberately bypass all service shutdown/save handlers.
        if self.process and self.process.poll() is None:
            self.process.kill()
        if self.process:
            self.process.wait(timeout=5)
        if self.reader:
            self.reader.close()
        if self.connection:
            self.connection.close()
        if self.log:
            self.log.close()
        self.process = self.connection = self.reader = self.log = None


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--work", type=Path, required=True)
    parser.add_argument("--archive", type=Path, required=True)
    args = parser.parse_args()
    work = args.work.resolve()
    work.mkdir(parents=True, exist_ok=False)
    for directory in ("char", "char_sleep", "log", "lock", "db", "mail", "family", "fmpoint", "fmsmemo"):
        (work / directory).mkdir()
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", 0))
        port = probe.getsockname()[1]
    (work / "acserv.cf").write_text(
        f"port {port}\npass checkpoint-fixture\nlogdir log\nlockdir lock\nchardir char\ndbdir db\n"
        "maildir mail\nfamilydir family\nfmpointdir fmpoint\nfmsmemodir fmsmemo\n"
        "rotate_interval 604800\nTotal_Charlist 3600\nExpired_mail 600\nDel_Family_or_Member 3600\nWrite_Family 600\nSameIpMun 10\n"
    )
    payload = args.archive.read_bytes()
    assert payload and b"LadderTest600" in payload, "native checkpoint artifact missing"
    account, name, token = "laddertest600", "LadderTest600", -1000001
    server = SAAC(args.binary.resolve(), work, port)
    try:
        server.start()
        reply = server.call("ACCharSave", account, name, "checkpoint", payload, 0, token, 0)
        assert reply[2] == b"successful" and reply[4] == base62(token), "durable save acknowledgement mismatch"
        archive = work / "char" / f"0x{sum(account.encode()) & 255:x}" / f"{account}.0.char"
        assert archive.is_file() and archive.stat().st_mode & 0o777 == 0o600
        assert not list(archive.parent.glob("*.tmp.*")), "successful save left a temporary archive"
        server.stop()
        server.start()
        reply = server.call("ACCharLoad", account, "unused", name, 0, "unused", token - 1)
        assert reply[2] == b"successful" and reply[3] == payload, "saved resources changed across SAAC SIGKILL/reload"
        reply = server.call("ACCharSave", account, name, "checkpoint", b"", 0, token - 4, 0)
        assert reply[2] == b"failed" and reply[4] == base62(token - 4), "empty serialization overwrote the character"
        # Fail the atomic rename in the actual service. It must send FAILED
        # with the exact token rather than acknowledging an unchecked write.
        retained = archive.with_suffix(".original")
        archive.rename(retained)
        archive.mkdir()
        try:
            reply = server.call("ACCharSave", account, name, "checkpoint", payload, 0, token - 2, 0)
            assert reply[2] == b"failed" and reply[4] == base62(token - 2), "failed save was acknowledged as durable"
            assert not list(archive.parent.glob("*.tmp.*")), "failed save left temporary resources"
        finally:
            archive.rmdir()
            retained.rename(archive)
        reply = server.call("ACCharLoad", account, "unused", name, 0, "unused", token - 3)
        assert reply[2] == b"successful" and reply[3] == payload
    finally:
        server.stop()
    print("native SAAC checkpoint: real save ACK, process kill/restart, exact archive reload and failed-save reply passed")


if __name__ == "__main__":
    main()
