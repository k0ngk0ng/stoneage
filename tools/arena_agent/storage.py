"""Crash-safe local evidence. Cursor advancement and event insertion commit
together; uncertain battle writes remain fenced across commander restarts.
"""
from __future__ import annotations

import json
import os
import sqlite3
import time
from pathlib import Path


def encoded(value):
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"))


class Store:
    def __init__(self, directory):
        self.directory = Path(directory)
        self.directory.mkdir(parents=True, exist_ok=True, mode=0o700)
        self.db = sqlite3.connect(self.directory / "arena.sqlite3")
        os.chmod(self.directory / "arena.sqlite3", 0o600)
        self.db.execute("PRAGMA journal_mode=WAL")
        self.db.execute("PRAGMA synchronous=FULL")
        self.db.executescript("""
            CREATE TABLE IF NOT EXISTS records(
                id INTEGER PRIMARY KEY, at_ms INTEGER NOT NULL, kind TEXT NOT NULL,
                match_id TEXT NOT NULL, member TEXT NOT NULL, body TEXT NOT NULL);
            CREATE TABLE IF NOT EXISTS events(
                member TEXT, stream TEXT, sequence INTEGER, match_id TEXT, body TEXT NOT NULL,
                PRIMARY KEY(member,stream,sequence));
            CREATE TABLE IF NOT EXISTS cursors(member TEXT PRIMARY KEY, stream TEXT, cursor INTEGER);
            CREATE TABLE IF NOT EXISTS battle_intents(
                member TEXT, match_id TEXT, turn INTEGER, actor TEXT, status TEXT NOT NULL,
                body TEXT NOT NULL, PRIMARY KEY(member,match_id,turn,actor));
            CREATE TABLE IF NOT EXISTS pending_ladder(member TEXT PRIMARY KEY, body TEXT NOT NULL);
            CREATE TABLE IF NOT EXISTS results(match_id TEXT PRIMARY KEY, body TEXT NOT NULL);
            CREATE TABLE IF NOT EXISTS match_policies(match_id TEXT PRIMARY KEY, policy TEXT NOT NULL);
            CREATE INDEX IF NOT EXISTS records_match ON records(match_id,id);
        """)

    def record(self, kind, body, match_id="", member=""):
        with self.db:
            self._record(kind, body, match_id, member)

    def _record(self, kind, body, match_id="", member=""):
        self.db.execute("INSERT INTO records(at_ms,kind,match_id,member,body) VALUES(?,?,?,?,?)",
                        (time.time_ns()//1_000_000, kind, match_id, member, encoded(body)))

    def cursor(self, member):
        row = self.db.execute("SELECT stream,cursor FROM cursors WHERE member=?", (member,)).fetchone()
        return row if row else ("", 0)

    def ingest(self, member, batch):
        with self.db:
            if batch["gap"]:
                affected = {batch["observation"]["match_id"]} | {e["match_id"] for e in batch["events"]}
                previous = self.db.execute("SELECT match_id FROM events WHERE member=? ORDER BY rowid DESC LIMIT 1",(member,)).fetchone()
                if previous:
                    affected.add(previous[0])
                for match_id in affected:
                    self._record("event_gap", {"previous": self.cursor(member), "stream": batch["stream"],
                                              "cursor": batch["cursor"]}, match_id, member)
            for event in batch["events"]:
                self.db.execute("INSERT OR IGNORE INTO events VALUES(?,?,?,?,?)",
                    (member, batch["stream"], event["sequence"], event["match_id"], encoded(event)))
            self.db.execute("INSERT OR REPLACE INTO cursors VALUES(?,?,?)", (member, batch["stream"], batch["cursor"]))

    def reserve(self, member, selection, actor):
        with self.db:
            row = self.db.execute("INSERT OR IGNORE INTO battle_intents VALUES(?,?,?,?,?,?)",
                (member, selection["match_id"], selection["turn"], actor, "uncertain", encoded(selection)))
            return row.rowcount == 1

    def intent(self, member, match_id, turn, actor):
        row = self.db.execute("SELECT status FROM battle_intents WHERE member=? AND match_id=? AND turn=? AND actor=?",
                              (member, match_id, turn, actor)).fetchone()
        return row[0] if row else None

    def finish_intent(self, member, selection, actor, response):
        key = (member, selection["match_id"], selection["turn"], actor)
        code = (response.get("data") or {}).get("code")
        with self.db:
            self._record("submission", {"selection": selection, "actor": actor, "response": response}, selection["match_id"], member)
            if not response.get("ok") and code in ("stale_observation", "not_ready", "invalid_candidate", "automation_conflict"):
                # Only explicit pre-write rejection allows a replacement plan.
                self.db.execute("DELETE FROM battle_intents WHERE member=? AND match_id=? AND turn=? AND actor=?", key)
            elif response.get("ok"):
                self.db.execute("UPDATE battle_intents SET status='written' WHERE member=? AND match_id=? AND turn=? AND actor=?", key)

    def pending(self, member):
        row = self.db.execute("SELECT body FROM pending_ladder WHERE member=?", (member,)).fetchone()
        return json.loads(row[0]) if row else None

    def set_pending(self, member, operation):
        with self.db:
            if operation is None:
                self.db.execute("DELETE FROM pending_ladder WHERE member=?", (member,))
            else:
                self.db.execute("INSERT OR REPLACE INTO pending_ladder VALUES(?,?)", (member, encoded(operation)))

    def result(self, value):
        with self.db:
            self.db.execute("INSERT OR IGNORE INTO results VALUES(?,?)", (value["id"], encoded(value)))

    def pin(self, match_id, policy):
        with self.db:
            self.db.execute("INSERT OR IGNORE INTO match_policies VALUES(?,?)", (match_id, policy))
            actual = self.db.execute("SELECT policy FROM match_policies WHERE match_id=?", (match_id,)).fetchone()[0]
            if actual != policy:
                raise ValueError("active match was started with a different strategy version")

    def history(self, match_id):
        items = [(at, json.loads(body)) for at,body in self.db.execute(
            "SELECT at_ms,body FROM records WHERE match_id=? AND kind='turn' ORDER BY id", (match_id,))]
        for member,body in self.db.execute("SELECT member,body FROM events WHERE match_id=? ORDER BY sequence", (match_id,)):
            event = json.loads(body)
            items.append((event["at_ms"], {"turn":event["turn"], "member":member, "event":event,
                                         "summary":event.get("effects", [])}))
        return [body for _,body in sorted(items,key=lambda item:item[0])]

    def close(self):
        self.db.close()
