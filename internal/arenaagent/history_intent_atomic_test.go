package arenaagent

import (
	"sync"
	"sync/atomic"
	"testing"
)

// A reservation is permission to dispatch. Failure to journal it must roll
// back that permission as well as the evidence, including after a restart.
func TestHistoryIntentJournalFailureRollsBackReservation(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.DB.Close() }()
	_, err = s.DB.Exec(`CREATE TRIGGER fail_intent_log BEFORE INSERT ON records
WHEN NEW.kind='submission_intent' BEGIN SELECT RAISE(ABORT,'injected log failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	selection := Object{"match_id": "match", "turn": 0, "observation_id": "observation", "candidate_id": "attack"}
	if s.Reserve("member", selection, "player") || s.Err() == nil {
		t.Fatal("journal failure must refuse dispatch and latch error")
	}
	for _, table := range []string{"battle_intents", "records"} {
		var count int
		if err := s.DB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("failed reservation escaped transaction", table, count, err)
		}
	}
	if _, err := s.DB.Exec("DROP TRIGGER fail_intent_log"); err != nil {
		t.Fatal(err)
	}
	if s.Reserve("member", selection, "player") {
		t.Fatal("latched persistence failure permitted dispatch")
	}
	if err := s.DB.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Reserve("member", selection, "player") {
		t.Fatal("rolled-back attempt poisoned restart", s.Err())
	}
	history, cutoff := s.HistorySnapshot("match")
	if s.Err() != nil || len(history) != 1 || cutoff <= 0 || obj(history[0]["submission_intent"]) == nil {
		t.Fatal("successful retry lacks exactly one committed intent", history, cutoff, s.Err())
	}
}

func TestHistoryConcurrentReservationPersistsOnlyWinningIntent(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.DB.Close() }()
	selection := Object{"match_id": "match", "turn": 0, "observation_id": "observation", "candidate_id": "attack"}
	var won atomic.Int32
	var workers sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 16; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			if s.Reserve("member", selection, "player") {
				won.Add(1)
			}
		}()
	}
	close(start)
	workers.Wait()
	if won.Load() != 1 || s.Err() != nil {
		t.Fatal("reservation is not exclusive", won.Load(), s.Err())
	}
	before, cutoff := s.HistorySnapshot("match")
	if len(before) != 1 || obj(before[0]["submission_intent"]) == nil {
		t.Fatal("duplicate or absent intent records", before)
	}
	if err := s.DB.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Reserve("member", selection, "player") || s.Intent("member", "match", 0, "player") != "uncertain" {
		t.Fatal("restart lost uncertainty; duplicate dispatch became possible", s.Err())
	}
	after, boundary := s.HistorySnapshot("match")
	if s.Err() != nil || boundary != cutoff || string(enc(before)) != string(enc(after)) {
		t.Fatal("failed duplicate changed evidence", s.Err())
	}
}
