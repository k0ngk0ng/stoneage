#!/usr/bin/env python3
"""Execute the actual C coordinator with deterministic game-adapter callbacks.

These tests do not impersonate a battle engine: the start callback only tests
the coordinator's all-or-nothing battle handoff. Native combat is checked by
the separate engine integration tests.
"""
import ctypes as C
from contextlib import closing
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[4]
BUILD = ROOT / "build/ladder/tests"
BUILD.mkdir(parents=True, exist_ok=True)
(BUILD / "tmp").mkdir(exist_ok=True)
LIBRARY = BUILD / f"core-{os.getpid()}.so"
subprocess.run([
    "cc", "-std=gnu99", "-Wall", "-Wextra", "-Werror", "-shared", "-fPIC",
    str(ROOT / "server/legacy/modern/tests/ladder-core-faults.c"), "-lsqlite3",
    "-o", str(LIBRARY),
], check=True, env=dict(os.environ, TMPDIR=str(BUILD / "tmp"),
                       CLANG_MODULE_CACHE_PATH=str(BUILD / "module-cache")))
LIB = C.CDLL(str(LIBRARY))


class Profile(C.Structure):
    _fields_ = [("id", C.c_char * 65), ("name_hex", C.c_char * 129),
                ("character", C.c_int), ("online", C.c_int), ("idle", C.c_int),
                ("pet_mask", C.c_int), ("active_pet", C.c_int), ("ride_pet", C.c_int),
                ("busy_reason", C.c_char_p), ("character_power", C.c_double),
                ("pet_power", C.c_double * 5), ("loadout_hash", C.c_ulonglong)]


class Stats(C.Structure):
    _fields_ = [(k, C.c_int) for k in ["player_kills", "pet_kills", "deaths", "assists", "revives"]] + [
        (k, C.c_longlong) for k in ["damage", "damage_taken", "pet_damage", "pet_damage_taken", "ride_damage_taken", "healing"]]


class ContactEntry(C.Structure):
    _fields_ = [("id", C.c_char * 65), ("name_hex", C.c_char * 129), ("online", C.c_int)]


NOW = C.CFUNCTYPE(C.c_longlong)
PROFILE = C.CFUNCTYPE(C.c_int, C.c_int, C.POINTER(Profile))
CONTACT = C.CFUNCTYPE(C.c_int, C.c_int, C.c_int)
CONTACT_ENTRY = C.CFUNCTYPE(C.c_int, C.c_int, C.c_int, C.POINTER(ContactEntry))
SEND = C.CFUNCTYPE(None, C.c_int, C.c_char_p)
START = C.CFUNCTYPE(C.c_int, C.c_char_p, C.POINTER(C.c_int), C.POINTER(C.c_int), C.c_int)
ABANDON = C.CFUNCTYPE(None, C.c_int)
FINISH = C.CFUNCTYPE(None, C.c_int, C.c_int, C.c_char_p)
PREPARE = START
CANCEL_PREPARE = C.CFUNCTYPE(None, C.c_char_p)


class Hooks(C.Structure):
    _fields_ = [("now", NOW), ("profile", PROFILE), ("contact", CONTACT), ("send", SEND),
                ("start", START), ("abandon", ABANDON), ("finish", FINISH),
                ("prepare", PREPARE), ("cancel_prepare", CANCEL_PREPARE), ("contact_entry", CONTACT_ENTRY)]


LIB.Ladder_Init.argtypes = [C.c_char_p, C.POINTER(Hooks)]
LIB.Ladder_Request.argtypes = [C.c_int, C.c_char_p]
LIB.Ladder_End.argtypes = [C.c_int, C.c_int, C.c_int, C.c_char_p, C.c_int]
LIB.Ladder_Stats.argtypes = [C.c_int, C.c_int, C.POINTER(Stats)]
LIB.Ladder_EquivalentPoints.argtypes = [C.c_double] * 4
LIB.Ladder_EquivalentPoints.restype = C.c_double
LIB.Ladder_PowerGap.argtypes = [C.c_double] * 2
LIB.Ladder_PowerGap.restype = C.c_double
LIB.Ladder_RatingDelta.argtypes = [C.c_double] * 3


class Coordinator(unittest.TestCase):
    def setUp(self):
        self.clock = 1_000_000
        self.serial = 0
        self.profiles = {}
        self.contacts = {}
        self.events = {}
        self.battles = []
        self.abandoned = []
        self.start_ok = True
        self.prepare_result = 1
        self.cancelled_preparations = []
        self.temp = tempfile.TemporaryDirectory(dir=BUILD)
        self.database = str(Path(self.temp.name) / "ladder.db").encode()
        self.callbacks = Hooks(NOW(lambda: self.clock), PROFILE(self.profile), CONTACT(self.contact),
                               SEND(self.send), START(self.start), ABANDON(self.abandon), FINISH(self.finish),
                               PREPARE(lambda *_: self.prepare_result), CANCEL_PREPARE(lambda match: self.cancelled_preparations.append(match.decode())),
                               CONTACT_ENTRY(self.contact_entry))
        self.assertEqual(LIB.Ladder_Init(self.database, C.byref(self.callbacks)), 1)
        for c in range(20):
            self.profiles[c] = Profile(f"pc1_{c:032x}".encode(), f"Player{c}".encode().hex().encode(),
                                       c, 1, 1, 0, -1, -1, b"", 400.0, (C.c_double * 5)(), c+1)
            self.events[c] = []

    def tearDown(self):
        LIB.Ladder_Shutdown()
        self.temp.cleanup()

    def profile(self, c, out):
        if c not in self.profiles:
            return 0
        out[0] = self.profiles[c]
        return 1

    def contact(self, c, index):
        return self.contacts.get((c, index), -1)

    def contact_entry(self, c, index, out):
        target = self.contacts.get((c, index), -1)
        if target < 0:
            return 0
        profile = self.profiles[target]
        out[0] = ContactEntry(profile.id, profile.name_hex, profile.online)
        return 1

    def send(self, c, raw):
        self.events[c].append(json.loads(raw.decode().removeprefix("LADDER|")))

    def start(self, match, chars, masks, mode):
        if not self.start_ok:
            return -1
        self.battles.append((match.decode(), [chars[i] for i in range(mode*2)], [masks[i] for i in range(mode*2)]))
        for c in self.battles[-1][1]:
            self.profiles[c].idle = 0
        return len(self.battles)

    def abandon(self, c):
        self.abandoned.append(c)

    def finish(self, battle, winner, reason):
        LIB.Ladder_End(battle, winner, 3, reason, 1)

    def last(self, c):
        return self.events[c][-1]

    def snapshot(self, c):
        return self.request(c, "status")["snapshot"]

    def request(self, c, op, arg="", expect="ok", revision=None, request_id=None):
        if op not in ("status", "result", "contacts") and revision is None:
            self.request(c, "status")
            revision = self.last(c)["revision"]
        self.serial += 1
        if op == "invite" and ":" not in arg:
            target = self.contacts.get((c, int(arg)), -1)
            identity = self.profiles[max(0, target)].id.decode()
            arg = f"{arg}:{identity}"
        request_id = request_id or f"request_{self.serial}"
        wire = f"LADDER|1|{request_id}|{revision or 0}|{op}|{arg}".encode()
        LIB.Ladder_Request(c, wire)
        event = self.last(c)
        self.assertEqual(event["request_id"], request_id)
        self.assertEqual(bytes.fromhex(event["request_hex"]), wire)
        self.assertEqual(event["code"], expect, event)
        return event

    def team(self, members, mode=None):
        leader = members[0]
        self.request(leader, "create", str(mode or len(members)))
        for index, member in enumerate(members[1:]):
            self.contacts[(leader, index)] = member
            self.request(leader, "invite", str(index))
            invite = self.snapshot(member)["invitations"][0]["id"]
            self.request(member, "accept", invite)

    def queue(self, members):
        for c in members:
            self.request(c, "ready")
        self.request(members[0], "queue")

    def battle(self, n=1):
        a = list(range(n))
        b = list(range(n, n*2))
        self.team(a)
        self.team(b)
        self.queue(a)
        self.queue(b)
        LIB.Ladder_Tick()
        self.assertEqual(self.snapshot(0)["phase"], "countdown")
        self.clock += 10000
        LIB.Ladder_Tick()
        self.assertEqual(self.snapshot(0)["phase"], "battle")
        return a, b

    def test_formula_and_ratings(self):
        self.assertAlmostEqual(LIB.Ladder_EquivalentPoints(700, 172.5, 127.5, 50), 400)
        self.assertAlmostEqual(LIB.Ladder_PowerGap(2500, 2650), 150/2575)
        self.assertEqual(LIB.Ladder_RatingDelta(1000, 1000, 1), 16)
        self.assertEqual(LIB.Ladder_RatingDelta(1200, 1400, 1), 24)
        self.assertEqual(LIB.Ladder_RatingDelta(1200, 1400, 0), -8)

    def test_strategy_default_switch_in_battle_and_durable_preference(self):
        self.assertEqual(self.snapshot(0)["self"]["strategy"], "manual")
        self.battle()
        before = self.snapshot(0)
        self.request(0, "strategy", "manual")
        after = self.snapshot(0)
        self.assertEqual(after["self"]["strategy"], "manual")
        self.assertEqual(after["match"]["id"], before["match"]["id"])
        self.assertEqual(after["self"]["ready"], before["self"]["ready"])
        self.assertEqual(self.snapshot(1)["self"]["strategy"], "manual")
        self.request(0, "loadout", "1", expect="result_or_match_pending")
        self.profiles[0].online = 0
        LIB.Ladder_Disconnected(0)
        self.clock += 1000
        self.profiles[0].online = 1
        LIB.Ladder_Reconnected(0)
        self.assertEqual(self.snapshot(0)["self"]["strategy"], "manual")
        self.request(1, "strategy", "local_advanced")
        LIB.Ladder_Shutdown()
        self.assertEqual(LIB.Ladder_Init(self.database, C.byref(self.callbacks)), 1)
        self.assertEqual(self.snapshot(0)["self"]["strategy"], "manual")
        self.assertEqual(self.snapshot(1)["self"]["strategy"], "local_advanced")
        self.assertEqual(self.snapshot(2)["self"]["strategy"], "manual")

    def test_strategy_write_failure_keeps_previous_selection(self):
        self.request(0, "strategy", "manual")
        with closing(sqlite3.connect(self.database.decode())) as con, con:
            con.execute("CREATE TRIGGER reject_strategy BEFORE UPDATE ON ladder_preference BEGIN SELECT RAISE(ABORT,'test'); END")
        self.request(0, "strategy", "basic", expect="database_unavailable")
        self.assertEqual(self.snapshot(0)["self"]["strategy"], "manual")
        with closing(sqlite3.connect(self.database.decode())) as con, con:
            self.assertEqual(con.execute("SELECT strategy FROM ladder_preference").fetchone()[0], "manual")
            con.execute("DROP TRIGGER reject_strategy")
        self.request(0, "strategy", "basic")
        self.assertEqual(self.snapshot(0)["self"]["strategy"], "basic")

    def test_team_counts_pending_invites_and_lock(self):
        self.team([0], 2)
        self.request(0, "ready")
        self.request(0, "queue", expect="team_size_mismatch")
        self.contacts[(0, 0)] = 1
        self.request(0, "invite", "0")
        invitation = self.snapshot(1)["invitations"][0]["id"]
        self.request(0, "mode", "1")
        self.request(1, "accept", invitation, expect="invitation_expired")
        self.request(0, "invite", "0")
        invitation = self.snapshot(1)["invitations"][0]["id"]
        self.queue([0])
        self.request(1, "accept", invitation, expect="invitation_expired")
        for op, arg in [("invite", "0"), ("mode", "2"), ("leave", ""), ("loadout", "0")]:
            self.request(0, op, arg, expect="room_locked")
        self.request(0, "cancel")
        self.assertEqual(self.snapshot(0)["phase"], "lobby")

    def test_room_capacity_and_invitation_authority(self):
        self.team([0, 1, 2, 3, 4])
        self.contacts[(0, 4)] = 5
        self.request(0, "invite", "4", expect="room_full")
        self.request(1, "mode", "1", expect="leader_required")
        self.request(0, "leader", self.profiles[1].id.decode())
        self.request(0, "queue", expect="leader_required")
        self.team([5])
        self.contacts[(5, 0)] = 6
        self.request(5, "invite", "0")
        invite = self.snapshot(6)["invitations"][0]["id"]
        self.request(7, "accept", invite, expect="invitation_expired")

    def test_idempotency_and_stale_revision(self):
        rev = self.request(0, "status")["revision"]
        one = self.request(0, "create", "1", revision=rev, request_id="same")
        again = self.request(0, "create", "1", revision=rev, request_id="same")
        self.assertTrue(again["replay"])
        self.assertEqual(one["snapshot"]["room"]["id"], again["snapshot"]["room"]["id"])
        self.assertEqual(one["applied_revision"], again["applied_revision"])
        self.request(0, "create", "2", revision=rev, request_id="same", expect="request_conflict")
        self.request(0, "ready", revision=rev, expect="stale_revision")

    def test_receipts_survive_restart_and_clock_rollback(self):
        revision = self.request(0, "status")["revision"]
        original = self.request(0, "create", "1", revision=revision, request_id="durable")
        LIB.Ladder_Shutdown()
        self.clock -= 500_000
        self.assertEqual(LIB.Ladder_Init(self.database, C.byref(self.callbacks)), 1)
        replay = self.request(0, "create", "1", revision=revision, request_id="durable")
        self.assertTrue(replay["replay"])
        self.assertEqual(replay["applied_revision"], original["applied_revision"])
        self.assertEqual(replay["receipt_boot"], original["server_boot"])
        self.assertNotEqual(replay["server_boot"], replay["receipt_boot"])
        self.assertGreater(replay["revision"], original["revision"])
        self.assertFalse(replay["snapshot"].get("room"))
        self.request(0, "create", "2", revision=revision, request_id="durable", expect="request_conflict")
        self.request(0, "create", "1", revision=revision, request_id="new_id", expect="stale_revision")

    def test_refused_receipt_eviction_cannot_make_old_request_executable(self):
        revision = self.request(0, "status")["revision"]
        self.request(0, "ready", revision=revision, request_id="refused", expect="not_in_room")
        self.request(0, "create", "1")
        self.request(0, "ready", revision=revision, request_id="refused", expect="not_in_room")
        for _ in range(33):
            self.request(0, "strategy", "manual")
        with closing(sqlite3.connect(self.database.decode())) as con:
            self.assertEqual(con.execute("SELECT COUNT(*) FROM ladder_receipt WHERE player=?", (self.profiles[0].id.decode(),)).fetchone()[0], 32)
        self.request(0, "ready", revision=revision, request_id="refused", expect="stale_revision")
        self.assertFalse(self.snapshot(0)["self"]["ready"])

    def test_receipt_write_failure_rolls_back_room_and_notifications(self):
        revision = self.request(0, "status")["revision"]
        with closing(sqlite3.connect(self.database.decode())) as con:
            con.execute("CREATE TRIGGER reject_receipt BEFORE INSERT ON ladder_receipt BEGIN SELECT RAISE(ABORT,'test'); END")
        before = len(self.events[0])
        failed = self.request(0, "create", "1", revision=revision, request_id="atomic", expect="storage_unavailable")
        self.assertEqual(len(self.events[0]), before + 1)
        self.assertEqual(failed["revision"], revision)
        self.assertFalse(failed["snapshot"].get("room"))
        with closing(sqlite3.connect(self.database.decode())) as con:
            self.assertEqual(con.execute("SELECT COUNT(*) FROM ladder_receipt").fetchone()[0], 0)
            con.execute("DROP TRIGGER reject_receipt")
        self.request(0, "create", "1", revision=revision, request_id="atomic")

    def test_watermark_failure_rolls_back_strategy_and_receipt(self):
        self.request(0, "strategy", "basic")
        revision = self.request(0, "status")["revision"]
        with closing(sqlite3.connect(self.database.decode())) as con:
            con.execute("CREATE TRIGGER reject_watermark BEFORE UPDATE ON ladder_revision BEGIN SELECT RAISE(ABORT,'test'); END")
        failed = self.request(0, "strategy", "manual", revision=revision, request_id="atomic_strategy", expect="storage_unavailable")
        self.assertEqual(failed["snapshot"]["self"]["strategy"], "basic")
        with closing(sqlite3.connect(self.database.decode())) as con:
            self.assertEqual(con.execute("SELECT strategy FROM ladder_preference").fetchone()[0], "basic")
            self.assertEqual(con.execute("SELECT COUNT(*) FROM ladder_receipt WHERE request_id='atomic_strategy'").fetchone()[0], 0)
            con.execute("DROP TRIGGER reject_watermark")
        self.request(0, "strategy", "manual", revision=revision, request_id="atomic_strategy")

    def test_receipt_failure_preserves_unacknowledged_result(self):
        self.battle()
        LIB.Ladder_End(1, 0, 2, b"defeat", 1)
        revision = self.request(0, "status")["revision"]
        with closing(sqlite3.connect(self.database.decode())) as con:
            con.execute("CREATE TRIGGER reject_ack_receipt BEFORE INSERT ON ladder_receipt BEGIN SELECT RAISE(ABORT,'test'); END")
        reply = self.request(0, "ack", revision=revision, request_id="atomic_ack", expect="storage_unavailable")
        self.assertIsNotNone(reply["snapshot"]["result"])
        with closing(sqlite3.connect(self.database.decode())) as con:
            self.assertEqual(con.execute("SELECT ack FROM ladder_participant WHERE player=?", (self.profiles[0].id.decode(),)).fetchone()[0], 0)
            con.execute("DROP TRIGGER reject_ack_receipt")
        self.request(0, "ack", revision=revision, request_id="atomic_ack")
        self.assertIsNone(self.snapshot(0)["result"])

    def test_notifications_observe_committed_receipt_and_strategy(self):
        observed = []

        def inspect(c, raw):
            self.send(c, raw)
            with closing(sqlite3.connect(self.database.decode())) as con:
                observed.append((con.execute("SELECT code FROM ladder_receipt WHERE request_id='visible'").fetchone(),
                                 con.execute("SELECT strategy FROM ladder_preference WHERE player=?", (self.profiles[0].id.decode(),)).fetchone()))

        revision = self.request(0, "status")["revision"]
        callback = SEND(inspect)
        self.callbacks.send = callback
        # Reinitialize to install the callback; obtain a fresh published revision.
        LIB.Ladder_Shutdown()
        self.assertEqual(LIB.Ladder_Init(self.database, C.byref(self.callbacks)), 1)
        revision = self.request(0, "status")["revision"]
        observed.clear()
        self.request(0, "strategy", "manual", revision=revision, request_id="visible")
        self.assertGreaterEqual(len(observed), 2)
        self.assertTrue(all(row == (("ok",), ("manual",)) for row in observed), observed)

    def test_refused_receipt_survives_restart(self):
        revision = self.request(0, "status")["revision"]
        original = self.request(0, "ready", revision=revision, request_id="refused_restart", expect="not_in_room")
        LIB.Ladder_Shutdown()
        self.assertEqual(LIB.Ladder_Init(self.database, C.byref(self.callbacks)), 1)
        self.request(0, "create", "1")
        replay = self.request(0, "ready", revision=revision, request_id="refused_restart", expect="not_in_room")
        self.assertTrue(replay["replay"])
        self.assertEqual(replay["applied_revision"], original["applied_revision"])
        self.assertFalse(replay["snapshot"]["self"]["ready"])

    def test_unpersisted_revision_is_never_sent_to_client(self):
        with closing(sqlite3.connect(self.database.decode())) as con:
            con.execute("CREATE TRIGGER reject_publication BEFORE UPDATE ON ladder_revision BEGIN SELECT RAISE(ABORT,'test'); END")
        LIB.Ladder_Request(0, b"LADDER|1|unpublished|0|status|")
        self.assertEqual(self.events[0], [])
        with closing(sqlite3.connect(self.database.decode())) as con:
            self.assertEqual(con.execute("SELECT high FROM ladder_revision").fetchone()[0], 0)
            con.execute("DROP TRIGGER reject_publication")
        revision = self.request(0, "status")["revision"]
        with closing(sqlite3.connect(self.database.decode())) as con:
            self.assertEqual(con.execute("SELECT high FROM ladder_revision").fetchone()[0], revision)
        LIB.Ladder_Shutdown()
        self.clock -= 500_000
        self.assertEqual(LIB.Ladder_Init(self.database, C.byref(self.callbacks)), 1)
        self.assertGreater(self.request(0, "status")["revision"], revision)

    def test_revision_exhaustion_never_publishes_inexact_or_reused_version(self):
        LIB.Ladder_Shutdown()
        maximum = 2**53 - 1
        with closing(sqlite3.connect(self.database.decode())) as con, con:
            con.execute("UPDATE ladder_revision SET high=?", (maximum - 1,))
        self.assertEqual(LIB.Ladder_Init(self.database, C.byref(self.callbacks)), 1)
        revision = self.request(0, "status")["revision"]
        self.assertEqual(revision, maximum)
        before = len(self.events[0])
        self.request(0, "strategy", "manual", revision=revision, expect="version_exhausted")
        self.assertEqual(len(self.events[0]), before + 1)
        self.request(1, "status", expect="version_exhausted")
        with closing(sqlite3.connect(self.database.decode())) as con:
            self.assertEqual(con.execute("SELECT high FROM ladder_revision").fetchone()[0], maximum)
            self.assertEqual(con.execute("SELECT COUNT(*) FROM ladder_receipt").fetchone()[0], 0)
            self.assertEqual(con.execute("SELECT COUNT(*) FROM ladder_preference").fetchone()[0], 0)
        LIB.Ladder_Shutdown()
        self.assertEqual(LIB.Ladder_Init(self.database, C.byref(self.callbacks)), 0)

    def test_receipt_pruning_failure_rolls_back_current_operation(self):
        for _ in range(32):
            self.request(0, "strategy", "basic")
        revision = self.request(0, "status")["revision"]
        with closing(sqlite3.connect(self.database.decode())) as con:
            con.execute("CREATE TRIGGER reject_pruning BEFORE DELETE ON ladder_receipt BEGIN SELECT RAISE(ABORT,'test'); END")
        reply = self.request(0, "strategy", "manual", revision=revision, request_id="pruning", expect="storage_unavailable")
        self.assertEqual(reply["snapshot"]["self"]["strategy"], "basic")
        with closing(sqlite3.connect(self.database.decode())) as con:
            self.assertEqual(con.execute("SELECT COUNT(*) FROM ladder_receipt").fetchone()[0], 32)
            self.assertEqual(con.execute("SELECT strategy FROM ladder_preference").fetchone()[0], "basic")
            con.execute("DROP TRIGGER reject_pruning")
        self.request(0, "strategy", "manual", revision=revision, request_id="pruning")

    def test_receipt_and_effect_are_atomic_at_process_exit(self):
        for boundary, exit_code in (("before", 73), ("after", 74)):
            with self.subTest(boundary=boundary):
                crash_db = str(Path(self.temp.name) / f"receipt-{boundary}.db")
                child = subprocess.run([sys.executable, __file__, "--receipt-crash", crash_db, boundary],
                                       env=dict(os.environ, PYTHONDONTWRITEBYTECODE="1"), capture_output=True, text=True)
                self.assertEqual(child.returncode, exit_code, child.stdout + child.stderr)
                with closing(sqlite3.connect(crash_db)) as con:
                    receipt = con.execute("SELECT code FROM ladder_receipt WHERE request_id='crash_request'").fetchone()
                    strategy = con.execute("SELECT strategy FROM ladder_preference").fetchone()
                    self.assertEqual(receipt, ("ok",) if boundary == "after" else None)
                    self.assertEqual(strategy, ("manual",) if boundary == "after" else ("basic",))
                LIB.Ladder_Shutdown()
                self.assertEqual(LIB.Ladder_Init(crash_db.encode(), C.byref(self.callbacks)), 1)
                wire = Path(crash_db + ".request").read_bytes()
                LIB.Ladder_Request(0, wire)
                reply = self.last(0)
                self.assertEqual(reply["code"], "ok" if boundary == "after" else "stale_revision")
                self.assertEqual(reply["replay"], boundary == "after")
                self.assertEqual(reply["snapshot"]["self"]["strategy"], "manual" if boundary == "after" else "basic")

    def test_receipts_are_scoped_to_stable_character_identity(self):
        revision = self.request(0, "status")["revision"]
        self.request(0, "create", "1", revision=revision, request_id="identity")
        LIB.Ladder_Shutdown()
        self.profiles[0].id = b"replacement_character"
        self.assertEqual(LIB.Ladder_Init(self.database, C.byref(self.callbacks)), 1)
        reply = self.request(0, "create", "1", revision=revision, request_id="identity", expect="stale_revision")
        self.assertFalse(reply["replay"])
        self.assertFalse(reply["snapshot"].get("room"))

    def test_contact_identity_rejections_are_explicit(self):
        self.request(0, "create", "2")
        for outcome, code in ((-2, "contact_identity_required"), (-3, "contact_identity_changed"), (-1, "contact_unavailable")):
            self.contacts[(0, 0)] = outcome
            self.request(0, "invite", "0", expect=code)
        self.assertFalse(self.snapshot(1)["invitations"])

    def test_selected_contact_cannot_follow_reused_slot(self):
        self.request(0, "create", "2")
        self.contacts[(0, 3)] = 1
        # The server also fails closed if an older client sends only a slot.
        wire = f'LADDER|1|old_client|{self.last(0)["revision"]}|invite|3'.encode()
        LIB.Ladder_Request(0, wire)
        self.assertEqual(self.last(0)["code"], "invalid_request")
        self.assertFalse(self.snapshot(1)["invitations"])
        selected = self.request(0, "contacts")["contacts"][0]
        arg = f'{selected["slot"]}:{selected["id"]}'
        # Card exchange/removal is independent of the ladder room revision.
        self.contacts[(0, 3)] = 2
        self.request(0, "invite", arg, expect="contact_slot_changed")
        self.assertFalse(self.snapshot(1)["invitations"])
        self.assertFalse(self.snapshot(2)["invitations"])
        selected = self.request(0, "contacts")["contacts"][0]
        self.request(0, "invite", f'{selected["slot"]}:{selected["id"]}')
        self.assertEqual(len(self.snapshot(2)["invitations"]), 1)

    def test_full_contact_directory_fits_legacy_wire_without_match_snapshot(self):
        self.profiles[1].name_hex = b"ff" * 64
        self.profiles[1].id = b"x" * 64
        self.contacts.update({(0, slot): 1 for slot in range(80)})
        self.battle()
        result = self.request(0, "contacts")
        self.assertEqual(len(result["contacts"]), 80)
        self.assertEqual(result["event"], "contacts_lookup")
        self.assertEqual(result["sequence"], 0)
        self.assertNotIn("match", result["snapshot"])
        self.assertLess(len(json.dumps(result)), 24000)

    def admin_snapshot(self, offset=0):
        buf = C.create_string_buffer(60000)
        turn = C.CFUNCTYPE(C.c_int, C.c_int)(lambda battle: 7)
        self.assertEqual(LIB.Ladder_AdminSnapshot(buf, len(buf), offset, turn), 1)
        return json.loads(buf.value)

    def test_admin_snapshot_is_read_only_and_tracks_match_lifecycle(self):
        self.assertEqual(self.admin_snapshot()["queued_total"], 0)
        self.team([0]); self.queue([0]); self.clock += 1234
        before = self.snapshot(0)
        snap = self.admin_snapshot()
        self.assertEqual(snap["queued_total"], 1)
        self.assertEqual(snap["queues"][0]["wait_ms"], 1234)
        self.assertEqual(snap["queues"][0]["members"][0]["name_hex"], self.profiles[0].name_hex.decode())
        self.assertEqual(self.snapshot(0), before)
        self.team([1]); self.queue([1]); LIB.Ladder_Tick()
        snap = self.admin_snapshot()
        self.assertEqual(snap["queued_total"], 0)
        self.assertEqual(snap["active_total"], 1)
        self.assertEqual(snap["matches"][0]["phase"], "countdown")
        self.clock += 10001; LIB.Ladder_Tick()
        snap = self.admin_snapshot()
        self.assertEqual(snap["matches"][0]["phase"], "battle")
        self.assertEqual(snap["matches"][0]["turn"], 7)
        self.assertEqual(len(snap["matches"][0]["teams"]), 2)
        self.assertEqual(self.admin_snapshot(8)["matches"], [])
        LIB.Ladder_End(1, 0, 7, b"defeat", 1); LIB.Ladder_Tick()
        self.assertEqual(self.admin_snapshot()["active_total"], 0)
        buf = C.create_string_buffer(8)
        self.assertEqual(LIB.Ladder_AdminSnapshot(buf, len(buf), 0, None), 0)

    def test_admin_snapshot_pages_queues_without_truncating_totals(self):
        for n in range(12):
            self.team([n]); self.queue([n])
        first, second = self.admin_snapshot(), self.admin_snapshot(8)
        self.assertEqual(first["queued_total"], 12)
        self.assertEqual(len(first["queues"]), 8)
        self.assertEqual(len(second["queues"]), 4)
        self.assertEqual(len({q["id"] for q in first["queues"] + second["queues"]}), 12)

    def test_matching_deadline_all_modes_and_no_fillers(self):
        for n in range(1, 6):
            if n > 1:
                self.tearDown()
                self.setUp()
            a, b = self.battle(n)
            self.assertEqual(self.battles[0][1], a+b)
            self.assertEqual(len(self.snapshot(0)["match"]["teams"][0]["members"]), n)

    def test_power_hard_limit_and_new_team_tolerance(self):
        self.profiles[1].character_power = 425  # 6.06%, needs both teams to wait.
        self.team([0]); self.queue([0])
        self.clock += 100000
        self.team([1]); self.queue([1])
        LIB.Ladder_Tick()
        self.assertEqual(self.snapshot(0)["phase"], "queued")
        self.clock += 30000
        LIB.Ladder_Tick()
        self.assertEqual(self.snapshot(0)["phase"], "countdown")

    def test_strong_member_cannot_hide_in_average(self):
        self.profiles[0].character_power = 700
        self.profiles[1].character_power = 100
        self.team([0, 1]); self.team([2, 3])
        self.queue([0, 1]); self.queue([2, 3])
        self.clock += 1000000
        LIB.Ladder_Tick()
        self.assertEqual(self.snapshot(0)["phase"], "queued")

    def test_oldest_queue_chooses_closest_opponent_before_newer_pair(self):
        for player, power in enumerate((400, 402, 410)):
            self.profiles[player].character_power = power
            self.team([player])
        for player in (2, 0, 1):
            self.queue([player])
            self.clock += 1000
        LIB.Ladder_Tick()
        match = self.snapshot(2)["match"]
        self.assertIsNotNone(match)
        self.assertEqual({p["id"] for team in match["teams"] for p in team["members"]},
                         {self.profiles[i].id.decode() for i in (2, 1)})
        self.assertEqual(self.snapshot(0)["phase"], "queued")

    def test_wait_thresholds_and_hard_power_rating_limits(self):
        cases = [(425, 1000, 29999, False), (425, 1000, 30000, True),
                 (440, 1000, 89999, False), (440, 1000, 90000, True),
                 (449, 1000, 900000, False),
                 (400, 1100, 0, True), (400, 1101, 29999, False),
                 (400, 1200, 30000, True), (400, 1201, 89999, False),
                 (400, 1300, 90000, True), (400, 1301, 900000, False)]
        for index, (power, rating, age, matched) in enumerate(cases):
            if index:
                self.tearDown()
                self.setUp()
            with self.subTest(power=power, rating=rating, age=age):
                self.profiles[1].character_power = power
                with closing(sqlite3.connect(self.database.decode())) as connection, connection:
                    connection.execute("INSERT INTO ladder_rating(player,mode,rating) VALUES(?,1,?)",
                                       (self.profiles[1].id.decode(), rating))
                self.team([0]); self.team([1]); self.queue([0]); self.queue([1])
                self.clock += age
                LIB.Ladder_Tick()
                self.assertEqual(self.snapshot(0)["phase"], "countdown" if matched else "queued")

    def test_config_change_cancels_queue_and_no_partial_battle(self):
        self.team([0]); self.queue([0])
        self.profiles[0].loadout_hash += 1
        LIB.Ladder_Tick()
        self.assertEqual(self.snapshot(0)["phase"], "lobby")
        self.team([1]); self.queue([0]); self.queue([1])
        LIB.Ladder_Tick()
        self.start_ok = False
        self.clock += 10000
        LIB.Ladder_Tick()
        self.assertEqual(self.snapshot(0)["phase"], "lobby")
        self.assertEqual(self.snapshot(1)["phase"], "lobby")
        self.assertEqual(self.battles, [])

    def test_reconnect_budget_is_cumulative(self):
        self.battle(2)
        self.profiles[0].online = 0
        LIB.Ladder_Disconnected(0)
        self.clock += 40000
        self.profiles[0].online = 1
        LIB.Ladder_Reconnected(0)
        self.assertEqual(self.snapshot(0)["self"]["reconnect_remaining_ms"], 20000)
        self.profiles[0].online = 0
        LIB.Ladder_Disconnected(0)
        self.clock += 20000
        LIB.Ladder_Tick()
        self.assertEqual(self.abandoned, [0])
        self.assertEqual(self.snapshot(1)["phase"], "battle")

    def test_late_reconnect_cannot_beat_timeout_tick(self):
        self.battle(2)
        for elapsed in (40000, 20000):
            self.profiles[0].online = 0
            LIB.Ladder_Disconnected(0)
            self.clock += elapsed
            self.profiles[0].online = 1
            LIB.Ladder_Reconnected(0)  # No intervening tick.
        self.assertEqual(self.abandoned, [0])
        self.assertTrue(self.snapshot(0)["self"]["abandoned"])
        self.assertEqual(LIB.Ladder_CharacterBattle(0), -1)
        LIB.Ladder_Reconnected(0)
        LIB.Ladder_Tick()
        self.assertEqual(self.abandoned, [0])

    def test_escape_retains_final_rating_responsibility(self):
        self.battle()
        self.assertEqual(LIB.Ladder_Abandon(0), 1)
        LIB.Ladder_Tick()
        result = self.snapshot(0)["result"]
        self.assertEqual(result["reason"], "abandonment")
        self.assertEqual(result["members"][0]["rating_delta"], -16)
        self.assertTrue(result["members"][0]["abandoned"])

    def test_countdown_failure_preserves_healthy_opponent_queue_age(self):
        self.team([0]); self.queue([0])
        queued = self.snapshot(0)["room"]["queued_at_ms"]
        self.clock += 30000
        self.team([1]); self.queue([1])
        LIB.Ladder_Tick()
        self.profiles[1].online = 0
        LIB.Ladder_Disconnected(1)
        LIB.Ladder_Tick()
        healthy = self.snapshot(0)
        self.assertEqual(healthy["phase"], "queued")
        self.assertEqual(healthy["room"]["queued_at_ms"], queued)
        self.assertTrue(healthy["self"]["ready"])
        self.profiles[1].online = 1
        LIB.Ladder_Reconnected(1)
        self.assertEqual(self.snapshot(1)["phase"], "lobby")

    def test_reused_native_slot_has_no_old_reservation_or_room(self):
        self.team([0]); self.team([1]); self.queue([0]); self.queue([1])
        LIB.Ladder_Tick()
        previous = self.snapshot(0)["self"]["id"]
        LIB.Ladder_Detached(0)
        self.profiles[0].id = b"different_persistent_identity"
        LIB.Ladder_Reconnected(0)
        state = self.snapshot(0)
        self.assertNotEqual(state["self"]["id"], previous)
        self.assertIsNone(state["room"])
        self.assertEqual(LIB.Ladder_Reserved(0), 0)
        self.assertEqual(self.snapshot(1)["phase"], "queued")

    def test_offline_room_and_player_slots_are_reclaimed(self):
        for n in range(1030):
            self.profiles[0].id = f"persistent_{n}".encode()
            LIB.Ladder_Reconnected(0)
            self.team([0])
            LIB.Ladder_Detached(0)
            self.clock += 300001
            LIB.Ladder_Tick()
        self.profiles[0].id = b"returning_player"
        LIB.Ladder_Reconnected(0)
        self.assertEqual(self.snapshot(0)["phase"], "idle")

    def test_offline_result_survives_room_expiration(self):
        self.battle()
        LIB.Ladder_End(1, 0, 2, b"defeat", 1)
        result = self.snapshot(0)["result"]
        LIB.Ladder_Detached(0)
        self.request(1, "ack")
        self.clock += 300001
        LIB.Ladder_Tick()
        LIB.Ladder_Reconnected(0)
        state = self.snapshot(0)
        self.assertIsNone(state["room"])
        self.assertEqual(state["result"], result)
        self.request(0, "create", "1", expect="result_or_match_pending")
        self.request(0, "ack")
        self.team([0])

    def test_unavailable_receipts_keep_request_id_and_parser_is_strict(self):
        self.request(0, "status", "extra", expect="invalid_request")
        self.request(0, "create", "01", expect="invalid_request")
        for raw in ("+1", "01", " 1", "18446744073709551616"):
            LIB.Ladder_Request(0, f"LADDER|1|bad_revision|{raw}|status|".encode())
            self.assertEqual(self.last(0)["code"], "invalid_request")
            self.assertEqual(self.last(0)["request_id"], "bad_revision")
        del self.profiles[19]
        self.request(19, "status", expect="identity_unavailable")
        LIB.Ladder_Shutdown()
        self.request(0, "status", expect="unavailable")

    def test_settlement_durable_once_and_all_members(self):
        self.battle(5)
        stats = Stats(player_kills=1, damage=500, damage_taken=200)
        LIB.Ladder_Stats(1, 0, C.byref(stats))
        LIB.Ladder_End(1, 0, 7, b"defeat", 1)
        LIB.Ladder_End(1, 0, 7, b"defeat", 1)
        result = self.snapshot(0)["result"]
        self.assertEqual(len(result["members"]), 10)
        self.assertEqual(sum(m["rating_delta"] for m in result["members"]), 0)
        self.assertEqual(result["members"][0]["statistics"]["damage"], 500)
        self.assertEqual(result["members"][0]["rating_after"], 1016)
        self.request(19, "result", result["id"], expect="result_not_found")
        LIB.Ladder_Shutdown()
        self.assertEqual(LIB.Ladder_Init(self.database, C.byref(self.callbacks)), 1)
        restored = self.snapshot(0)
        self.assertEqual(restored["result"]["id"], result["id"])
        self.assertEqual(restored["ratings"][4], 1016)
        self.request(0, "ack")
        self.assertIsNone(self.snapshot(0)["result"])
        self.assertEqual(self.request(0, "result", result["id"])["snapshot"]["result"], result)

    def test_transaction_failure_retries_without_partial_points(self):
        self.battle()
        with closing(sqlite3.connect(self.database.decode())) as con, con:
            con.execute("CREATE TRIGGER reject_rating BEFORE INSERT ON ladder_rating BEGIN SELECT RAISE(ABORT,'test'); END")
        LIB.Ladder_End(1, 0, 2, b"defeat", 1)
        self.assertEqual(self.snapshot(0)["phase"], "settling")
        with closing(sqlite3.connect(self.database.decode())) as con, con:
            self.assertEqual(con.execute("SELECT COUNT(*) FROM ladder_match").fetchone()[0], 0)
            con.execute("DROP TRIGGER reject_rating")
        LIB.Ladder_Tick()
        self.assertEqual(self.snapshot(0)["result"]["members"][0]["rating_after"], 1016)
        LIB.Ladder_Tick()
        with closing(sqlite3.connect(self.database.decode())) as con, con:
            self.assertEqual(con.execute("SELECT COUNT(*) FROM ladder_match").fetchone()[0], 1)

    def test_match_reservation_must_commit_before_countdown_or_native_start(self):
        self.team([0]); self.team([1]); self.queue([0]); self.queue([1])
        with closing(sqlite3.connect(self.database.decode())) as con, con:
            con.execute("CREATE TRIGGER reject_reservation BEFORE INSERT ON ladder_pending_member BEGIN SELECT RAISE(ABORT,'test'); END")
        LIB.Ladder_Tick()
        self.clock += 20000
        LIB.Ladder_Tick()
        self.assertEqual(self.snapshot(0)["phase"], "queued")
        self.assertEqual(self.snapshot(1)["phase"], "queued")
        self.assertEqual(self.battles, [])
        with closing(sqlite3.connect(self.database.decode())) as con, con:
            self.assertEqual(con.execute("SELECT COUNT(*) FROM ladder_pending_match").fetchone()[0], 0)
            con.execute("DROP TRIGGER reject_reservation")
        LIB.Ladder_Tick()
        self.assertEqual(self.snapshot(0)["phase"], "countdown")
        with closing(sqlite3.connect(self.database.decode())) as con:
            self.assertEqual(con.execute("SELECT COUNT(*) FROM ladder_pending_member").fetchone()[0], 2)

    def test_pre_match_save_waits_for_confirmation_within_countdown(self):
        self.prepare_result = 0
        self.team([0]); self.team([1]); self.queue([0]); self.queue([1]); LIB.Ladder_Tick()
        self.clock += 9999
        LIB.Ladder_Tick()
        self.assertEqual(self.snapshot(0)["phase"], "countdown")
        self.assertEqual(self.battles, [])
        self.prepare_result = 1
        self.clock += 1
        LIB.Ladder_Tick()
        self.assertEqual(self.snapshot(0)["phase"], "battle")
        self.assertEqual(self.cancelled_preparations, [])

    def test_pre_match_save_timeout_cancels_without_penalizing_players(self):
        self.prepare_result = 0
        self.team([0]); self.team([1]); self.queue([0]); self.queue([1]); LIB.Ladder_Tick()
        match_id = self.snapshot(0)["match"]["id"]
        self.clock += 10000
        LIB.Ladder_Tick()
        self.assertEqual(self.snapshot(0)["phase"], "lobby")
        self.assertEqual(self.snapshot(1)["cooldown_until_ms"], 0)
        self.assertEqual(self.battles, [])
        self.assertEqual(self.cancelled_preparations, [match_id])
        with closing(sqlite3.connect(self.database.decode())) as con:
            self.assertEqual(con.execute("SELECT COUNT(*) FROM ladder_pending_match").fetchone()[0], 0)

    def test_pre_match_save_failure_releases_preparation_before_start(self):
        self.prepare_result = -1
        self.team([0]); self.team([1]); self.queue([0]); self.queue([1]); LIB.Ladder_Tick()
        self.assertEqual(self.snapshot(0)["phase"], "lobby")
        self.assertEqual(len(self.cancelled_preparations), 1)
        self.assertEqual(self.battles, [])

    def test_countdown_restart_returns_unrated_result_without_native_start(self):
        self.team([0]); self.team([1]); self.queue([0]); self.queue([1]); LIB.Ladder_Tick()
        match_id = self.snapshot(0)["match"]["id"]
        LIB.Ladder_Shutdown()
        self.assertEqual(LIB.Ladder_Init(self.database, C.byref(self.callbacks)), 1)
        state = self.snapshot(0)
        self.assertEqual(state["phase"], "result")
        self.assertEqual(state["result"]["id"], match_id)
        self.assertEqual(state["result"]["reason"], "server_restart")
        self.assertFalse(state["result"]["rated"])
        self.assertTrue(state["result"]["statistics_incomplete"])
        self.assertNotIn("online", state["result"]["members"][0], "recovery invented live player state")
        self.assertNotIn("abandoned", state["result"]["members"][0], "recovery invented an abandonment decision")
        self.assertEqual(self.battles, [])
        self.request(0, "create", "1", expect="result_or_match_pending")
        self.request(0, "ack"); self.team([0]); self.queue([0])

    def test_cancel_storage_failure_keeps_reservation_until_cleanup_commits(self):
        self.team([0]); self.team([1]); self.queue([0]); self.queue([1]); LIB.Ladder_Tick()
        with closing(sqlite3.connect(self.database.decode())) as con, con:
            con.execute("CREATE TRIGGER reject_cancel BEFORE DELETE ON ladder_pending_match BEGIN SELECT RAISE(ABORT,'test'); END")
        self.start_ok = False
        self.clock += 10000
        LIB.Ladder_Tick()
        self.assertEqual(self.snapshot(0)["phase"], "settling")
        self.assertEqual(LIB.Ladder_Reserved(0), 1)
        self.start_ok = True
        LIB.Ladder_Tick()
        self.assertEqual(self.battles, [], "a pending cancellation entered native combat")
        with closing(sqlite3.connect(self.database.decode())) as con, con:
            con.execute("DROP TRIGGER reject_cancel")
        LIB.Ladder_Tick()
        self.assertEqual(self.snapshot(0)["phase"], "lobby")
        LIB.Ladder_Shutdown()
        self.assertEqual(LIB.Ladder_Init(self.database, C.byref(self.callbacks)), 1)
        self.assertIsNone(self.snapshot(0)["result"], "cancelled reservation reappeared after restart")

    def test_abandonment_cooldown_survives_reconnect_and_restart(self):
        self.battle()
        self.assertEqual(LIB.Ladder_Abandon(0), 1)
        until = self.snapshot(0)["cooldown_until_ms"]
        LIB.Ladder_Tick(); self.request(0, "ack")
        LIB.Ladder_Shutdown()
        self.profiles[0].idle = 1
        self.assertEqual(LIB.Ladder_Init(self.database, C.byref(self.callbacks)), 1)
        self.assertEqual(self.snapshot(0)["cooldown_until_ms"], until)
        self.team([0]); self.request(0, "ready", expect="cooldown")
        self.clock = until
        self.request(0, "ready")

    def test_expired_reconnect_cannot_act_while_cooldown_storage_is_unavailable(self):
        self.battle()
        with closing(sqlite3.connect(self.database.decode())) as con, con:
            con.execute("CREATE TRIGGER reject_cooldown BEFORE INSERT ON ladder_cooldown BEGIN SELECT RAISE(ABORT,'test'); END")
        self.profiles[0].online = 0
        LIB.Ladder_Disconnected(0)
        self.clock += 60000
        self.profiles[0].online = 1
        LIB.Ladder_Reconnected(0)
        self.assertEqual(LIB.Ladder_CharacterBattle(0), -1)
        self.assertEqual(self.abandoned, [])
        with closing(sqlite3.connect(self.database.decode())) as con, con:
            con.execute("DROP TRIGGER reject_cooldown")
        LIB.Ladder_Tick()
        self.assertEqual(self.abandoned, [0])
        self.assertEqual(self.snapshot(0)["result"]["reason"], "abandonment")

    def test_failed_terminal_checkpoint_recovers_as_unrated_interruption(self):
        self.battle()
        with closing(sqlite3.connect(self.database.decode())) as con, con:
            con.execute("CREATE TRIGGER reject_terminal BEFORE UPDATE ON ladder_pending_match BEGIN SELECT RAISE(ABORT,'test'); END")
        LIB.Ladder_End(1, 0, 7, b"defeat", 1)
        self.assertEqual(self.snapshot(0)["phase"], "settling")
        LIB.Ladder_Shutdown()
        self.assertEqual(LIB.Ladder_Init(self.database, C.byref(self.callbacks)), 1)
        result = self.snapshot(0)["result"]
        self.assertEqual(result["reason"], "server_restart")
        self.assertFalse(result["rated"])
        self.assertTrue(all(p["rating_delta"] == 0 for p in result["members"]))

    def test_process_exit_recovers_active_match_and_exact_pending_terminal(self):
        for scenario in ("battle", "terminal"):
            with self.subTest(scenario=scenario):
                LIB.Ladder_Shutdown()
                crash_db = str(Path(self.temp.name) / f"crash-{scenario}.db").encode()
                subprocess.run([sys.executable, __file__, "--crash-fixture", crash_db.decode(), scenario], check=True)
                if scenario == "terminal":
                    # Startup may not accept fresh matches while recovery is
                    # blocked; its journal must remain intact for the retry.
                    self.assertEqual(LIB.Ladder_Init(crash_db, C.byref(self.callbacks)), 0)
                    with closing(sqlite3.connect(crash_db.decode())) as con, con:
                        self.assertEqual(con.execute("SELECT terminal FROM ladder_pending_match").fetchone()[0], 1)
                        self.assertEqual(con.execute("SELECT COUNT(*) FROM ladder_match").fetchone()[0], 0)
                        self.assertEqual(con.execute("SELECT COUNT(*) FROM ladder_rating").fetchone()[0], 0)
                        con.execute("DROP TRIGGER reject_rating")
                self.assertEqual(LIB.Ladder_Init(crash_db, C.byref(self.callbacks)), 1)
                state = self.snapshot(0)
                result = state["result"]
                self.assertEqual(len(result["members"]), 10)
                self.assertEqual(result["rated"], scenario == "terminal")
                self.assertEqual(result["statistics_incomplete"], scenario == "battle")
                self.assertEqual(result["reason"], "defeat" if scenario == "terminal" else "server_restart")
                self.assertEqual(state["ratings"][4], 1016 if scenario == "terminal" else 1000)
                if scenario == "terminal":
                    self.assertEqual(result["members"][0]["statistics"]["damage"], 537)
                    self.assertEqual(result["turns"], 7)
                self.assertEqual(self.snapshot(9)["result"], result)
                self.request(19, "result", result["id"], expect="result_not_found")
                LIB.Ladder_Shutdown()
                self.assertEqual(LIB.Ladder_Init(crash_db, C.byref(self.callbacks)), 1)
                self.assertEqual(self.snapshot(0)["result"], result)
                with closing(sqlite3.connect(crash_db.decode())) as con:
                    self.assertEqual(con.execute("SELECT COUNT(*) FROM ladder_pending_match").fetchone()[0], 0)
                    self.assertEqual(con.execute("SELECT COUNT(*) FROM ladder_pending_member").fetchone()[0], 0)
                    self.assertEqual(con.execute("SELECT COUNT(*) FROM ladder_match").fetchone()[0], 1)
                    self.assertEqual(con.execute("SELECT COUNT(*) FROM ladder_participant WHERE ack=0").fetchone()[0], 10)

    def test_result_ack_keeps_team_and_does_not_wait_for_opponent(self):
        self.battle(2)
        room = self.snapshot(0)["room"]["id"]
        LIB.Ladder_End(1, 0, 2, b"defeat", 1)
        self.request(0, "ack")
        self.request(0, "ready", expect="result_or_match_pending")
        self.request(1, "ack")
        self.assertEqual(self.snapshot(0)["room"]["id"], room)
        self.assertEqual(self.snapshot(0)["phase"], "lobby")
        self.assertEqual(self.snapshot(2)["phase"], "result")
        for c in [0, 1]: self.profiles[c].idle = 1
        self.queue([0, 1])
        self.assertEqual(self.snapshot(0)["phase"], "queued")

    def test_abort_has_no_rating_change(self):
        self.battle()
        LIB.Ladder_End(1, -1, 2, b"server_abort", 0)
        result = self.snapshot(0)["result"]
        self.assertFalse(result["rated"])
        self.assertTrue(all(p["rating_delta"] == 0 for p in result["members"]))


if __name__ == "__main__":
    if len(sys.argv) == 4 and sys.argv[1] == "--receipt-crash":
        fixture = Coordinator()
        fixture.setUp()
        LIB.Ladder_Shutdown()
        fixture.database = sys.argv[2].encode()
        assert LIB.Ladder_Init(fixture.database, C.byref(fixture.callbacks)) == 1
        fixture.request(0, "strategy", "basic")
        revision = fixture.request(0, "status")["revision"]
        wire = f"LADDER|1|crash_request|{revision}|strategy|manual".encode()
        Path(sys.argv[2] + ".request").write_bytes(wire)
        LIB.Ladder_TestExitAtCommit(int(sys.argv[3] == "after"))
        LIB.Ladder_Request(0, wire)
        raise AssertionError("commit hook was not reached")
    elif len(sys.argv) == 4 and sys.argv[1] == "--crash-fixture":
        fixture = Coordinator()
        fixture.setUp()
        LIB.Ladder_Shutdown()
        fixture.database = sys.argv[2].encode()
        assert LIB.Ladder_Init(fixture.database, C.byref(fixture.callbacks)) == 1
        fixture.battle(5)
        if sys.argv[3] == "terminal":
            with closing(sqlite3.connect(fixture.database.decode())) as con, con:
                con.execute("CREATE TRIGGER reject_rating BEFORE INSERT ON ladder_rating BEGIN SELECT RAISE(ABORT,'test'); END")
            LIB.Ladder_Stats(1, 0, C.byref(Stats(damage=537)))
            LIB.Ladder_End(1, 0, 7, b"defeat", 1)
            assert fixture.snapshot(0)["phase"] == "settling"
        # Deliberately skip Ladder_Shutdown, sqlite3_close and all finalizers.
        os._exit(0)
    else:
        unittest.main()
