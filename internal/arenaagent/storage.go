package arenaagent

import (
	"database/sql"
	"fmt"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Store latches the first persistence failure. The runner checks Err before
// every mutation; uncertain writes never become retryable after a crash.
type Store struct {
	DB        *sql.DB
	Directory string
	mu        sync.Mutex
	err       error
}

func OpenStore(directory string) (*Store, error) {
	if e := os.MkdirAll(directory, 0700); e != nil {
		return nil, e
	}
	path := filepath.Join(directory, "arena.sqlite3")
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	f.Close()
	if e = os.Chmod(path, 0600); e != nil {
		return nil, e
	}
	db, e := sql.Open("sqlite", path)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	_, e = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA busy_timeout=5000;
 CREATE TABLE IF NOT EXISTS records(id INTEGER PRIMARY KEY,at_ms INTEGER NOT NULL,kind TEXT NOT NULL,match_id TEXT NOT NULL,member TEXT NOT NULL,body TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS events(member TEXT,stream TEXT,sequence INTEGER,match_id TEXT,body TEXT NOT NULL,PRIMARY KEY(member,stream,sequence));
 CREATE TABLE IF NOT EXISTS cursors(member TEXT PRIMARY KEY,stream TEXT,cursor INTEGER);
 CREATE TABLE IF NOT EXISTS battle_intents(member TEXT,match_id TEXT,turn INTEGER,actor TEXT,status TEXT NOT NULL,body TEXT NOT NULL,PRIMARY KEY(member,match_id,turn,actor));
 CREATE TABLE IF NOT EXISTS pending_ladder(member TEXT PRIMARY KEY,body TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS results(match_id TEXT PRIMARY KEY,body TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS match_policies(match_id TEXT PRIMARY KEY,policy TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS commander_models(artifact TEXT PRIMARY KEY,body BLOB NOT NULL);
 CREATE TABLE IF NOT EXISTS commander_selection(id INTEGER PRIMARY KEY CHECK(id=1),body TEXT NOT NULL);
 CREATE INDEX IF NOT EXISTS records_match ON records(match_id,id);`)
	if e != nil {
		db.Close()
		return nil, e
	}
	return &Store{DB: db, Directory: directory}, nil
}
func (s *Store) Err() error { s.mu.Lock(); defer s.mu.Unlock(); return s.err }
func (s *Store) tx(fn func(*sql.Tx) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return
	}
	tx, e := s.DB.Begin()
	if e != nil {
		s.err = e
		return
	}
	defer tx.Rollback()
	if e = fn(tx); e == nil {
		e = tx.Commit()
	}
	if e != nil {
		s.err = fmt.Errorf("evidence persistence failed: %w", e)
	}
}
func record(tx *sql.Tx, kind string, body any, match, member string) error {
	_, e := tx.Exec("INSERT INTO records(at_ms,kind,match_id,member,body) VALUES(?,?,?,?,?)", time.Now().UnixMilli(), kind, match, member, string(enc(body)))
	return e
}
func (s *Store) Record(kind string, body any, match, member string) {
	s.tx(func(tx *sql.Tx) error { return record(tx, kind, body, match, member) })
}
func (s *Store) Cursor(member string) (stream string, cursor int64) {
	s.tx(func(tx *sql.Tx) error {
		e := tx.QueryRow("SELECT stream,cursor FROM cursors WHERE member=?", member).Scan(&stream, &cursor)
		if e == sql.ErrNoRows {
			return nil
		}
		return e
	})
	return
}
func (s *Store) Ingest(member string, b Object) {
	s.tx(func(tx *sql.Tx) error {
		if yes(b["gap"]) {
			affected := map[string]bool{str(obj(b["observation"])["match_id"]): true}
			for _, event := range objects(b["events"]) {
				affected[str(event["match_id"])] = true
			}
			var previous string
			e := tx.QueryRow("SELECT match_id FROM events WHERE member=? ORDER BY rowid DESC LIMIT 1", member).Scan(&previous)
			if e == nil {
				affected[previous] = true
			} else if e != sql.ErrNoRows {
				return e
			}
			for id := range affected {
				if e := record(tx, "event_gap", Object{"stream": b["stream"], "cursor": b["cursor"]}, id, member); e != nil {
					return e
				}
			}
		}
		for _, event := range objects(b["events"]) {
			if _, e := tx.Exec("INSERT OR IGNORE INTO events VALUES(?,?,?,?,?)", member, str(b["stream"]), integer(event["sequence"]), str(event["match_id"]), string(enc(event))); e != nil {
				return e
			}
		}
		_, e := tx.Exec("INSERT OR REPLACE INTO cursors VALUES(?,?,?)", member, str(b["stream"]), integer(b["cursor"]))
		return e
	})
}
func (s *Store) Reserve(member string, selection Object, actor string) bool {
	ok := false
	s.tx(func(tx *sql.Tx) error {
		r, e := tx.Exec("INSERT OR IGNORE INTO battle_intents VALUES(?,?,?,?,?,?)", member, str(selection["match_id"]), integer(selection["turn"]), actor, "uncertain", string(enc(selection)))
		if e == nil {
			n, x := r.RowsAffected()
			e = x
			ok = n == 1
		}
		if e == nil && ok {
			e = record(tx, "submission_intent", Object{"selection": selection, "actor": actor}, str(selection["match_id"]), member)
		}
		return e
	})
	return ok && s.Err() == nil
}
func (s *Store) Intent(member, match string, turn int, actor string) (status string) {
	s.tx(func(tx *sql.Tx) error {
		e := tx.QueryRow("SELECT status FROM battle_intents WHERE member=? AND match_id=? AND turn=? AND actor=?", member, match, turn, actor).Scan(&status)
		if e == sql.ErrNoRows {
			return nil
		}
		return e
	})
	return
}
func (s *Store) Finish(member string, selection Object, actor string, response Object) {
	s.tx(func(tx *sql.Tx) error {
		match := str(selection["match_id"])
		if e := record(tx, "submission", Object{"selection": selection, "actor": actor, "response": response}, match, member); e != nil {
			return e
		}
		args := []any{member, match, integer(selection["turn"]), actor}
		code := str(obj(response["data"])["code"])
		if !yes(response["ok"]) && (code == "stale_observation" || code == "not_ready" || code == "invalid_candidate" || code == "automation_conflict") {
			_, e := tx.Exec("DELETE FROM battle_intents WHERE member=? AND match_id=? AND turn=? AND actor=?", args...)
			return e
		}
		if yes(response["ok"]) {
			_, e := tx.Exec("UPDATE battle_intents SET status='written' WHERE member=? AND match_id=? AND turn=? AND actor=?", args...)
			return e
		}
		return nil
	})
}
func (s *Store) Pending(member string) (value Object) {
	s.tx(func(tx *sql.Tx) error {
		var body string
		e := tx.QueryRow("SELECT body FROM pending_ladder WHERE member=?", member).Scan(&body)
		if e == sql.ErrNoRows {
			return nil
		}
		if e != nil {
			return e
		}
		return decode([]byte(body), &value)
	})
	return
}
func (s *Store) SetPending(member string, p Object) {
	s.tx(func(tx *sql.Tx) error {
		if p == nil {
			_, e := tx.Exec("DELETE FROM pending_ladder WHERE member=?", member)
			return e
		}
		_, e := tx.Exec("INSERT OR REPLACE INTO pending_ladder VALUES(?,?)", member, string(enc(p)))
		return e
	})
}
func (s *Store) Result(v Object) {
	s.tx(func(tx *sql.Tx) error {
		_, e := tx.Exec("INSERT OR IGNORE INTO results VALUES(?,?)", str(v["id"]), string(enc(v)))
		return e
	})
}
func (s *Store) Pin(match, policy string) error {
	return s.pinSelection(match, policy, nil)
}
func (s *Store) pinSelection(match, policy string, selection *modelSelection) error {
	var actual string
	s.tx(func(tx *sql.Tx) error {
		inserted, e := tx.Exec("INSERT OR IGNORE INTO match_policies VALUES(?,?)", match, policy)
		if e != nil {
			return e
		}
		if n, e := inserted.RowsAffected(); e != nil {
			return e
		} else if n != 0 && selection != nil {
			if e = record(tx, "match_model", selection, match, ""); e != nil {
				return e
			}
		}
		return tx.QueryRow("SELECT policy FROM match_policies WHERE match_id=?", match).Scan(&actual)
	})
	if e := s.Err(); e != nil {
		return e
	}
	if actual != policy {
		return fmt.Errorf("active match was started with a different strategy version")
	}
	return nil
}
func (s *Store) History(match string) []Object {
	history, _ := s.HistorySnapshot(match)
	return history
}

// HistorySnapshot captures append-only command records and public events in
// one database transaction. The cutoff survives later writes and clock changes.
func (s *Store) HistorySnapshot(match string) (out []Object, cutoff int64) {
	type item struct {
		at   int64
		body Object
	}
	items := []item{}
	s.tx(func(tx *sql.Tx) error {
		if e := tx.QueryRow("SELECT COALESCE(MAX(id),0) FROM records").Scan(&cutoff); e != nil {
			return e
		}
		rows, e := tx.Query("SELECT id,at_ms,kind,member,body FROM records WHERE match_id=? AND id<=? AND kind IN ('turn','event_gap','submission_intent','submission') ORDER BY id", match, cutoff)
		if e != nil {
			return e
		}
		for rows.Next() {
			var id, at int64
			var kind, member, body string
			var v Object
			if e = rows.Scan(&id, &at, &kind, &member, &body); e != nil {
				break
			}
			if e = decode([]byte(body), &v); e != nil {
				break
			}
			if kind == "event_gap" {
				v = Object{"kind": kind, "member": member, "event_gap": v}
			} else if kind == "submission" || kind == "submission_intent" {
				v = Object{"member": member, kind: v}
			}
			v["record_id"], v["record_kind"] = id, kind
			items = append(items, item{at, v})
		}
		re := rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		if re != nil {
			return re
		}
		rows, e = tx.Query("SELECT member,stream,body FROM events WHERE match_id=? ORDER BY member,stream,sequence", match)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var member, stream, body string
			var v Object
			if e = rows.Scan(&member, &stream, &body); e != nil {
				return e
			}
			if e = decode([]byte(body), &v); e != nil {
				return e
			}
			items = append(items, item{int64(num(v["at_ms"])), Object{"turn": v["turn"], "member": member, "stream": stream, "event": v, "summary": v["effects"]}})
		}
		return rows.Err()
	})
	sort.SliceStable(items, func(i, j int) bool { return items[i].at < items[j].at })
	out = []Object{}
	for _, v := range items {
		out = append(out, v.body)
	}
	return out, cutoff
}
