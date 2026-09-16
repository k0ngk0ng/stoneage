package airuntime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRecordGameObservationIsProfileScopedAndCrossProcessIdempotent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "ai.db")
	first, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	profileOne := testProfile()
	profileOne.ID = "observation-one"
	if _, err := first.CreateProfile(context.Background(), profileOne); err != nil {
		t.Fatal(err)
	}
	profileTwo := testProfile()
	profileTwo.ID = "observation-two"
	if _, err := first.CreateProfile(context.Background(), profileTwo); err != nil {
		t.Fatal(err)
	}

	content := json.RawMessage(`{"level":7}`)
	created, err := first.RecordGameObservation(context.Background(), profileOne.ID, "character-level:one:7", "character.level", "character-one", content)
	if err != nil || !created {
		t.Fatalf("first observation = %v, %v", created, err)
	}
	created, err = first.RecordGameObservation(context.Background(), profileOne.ID, "character-level:one:7", "character.level", "character-one", content)
	if err != nil || created {
		t.Fatalf("duplicate observation = %v, %v", created, err)
	}
	created, err = first.RecordGameObservation(context.Background(), profileTwo.ID, "character-level:one:7", "character.level", "character-one", content)
	if err != nil || !created {
		t.Fatalf("cross-profile observation = %v, %v", created, err)
	}

	second, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	created, err = second.RecordGameObservation(context.Background(), profileOne.ID, "character-level:one:7", "character.level", "character-one", content)
	if err != nil || created {
		t.Fatalf("cross-process duplicate = %v, %v", created, err)
	}

	memories, err := first.ListMemories(context.Background(), profileOne.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 1 || memories[0].SourceEventID == 0 || !memories[0].Confirmed {
		t.Fatalf("profile-one memories = %#v", memories)
	}
	events, err := first.ListEvents(context.Background(), profileOne.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 { // profile.created and the observation
		t.Fatalf("profile-one events = %#v", events)
	}
	if events[0].Kind != EventGameObservation || events[0].Actor != "game" {
		t.Fatalf("observation event = %#v", events[0])
	}
	var detail map[string]json.RawMessage
	if err := json.Unmarshal(events[0].Detail, &detail); err != nil {
		t.Fatal(err)
	}
	var eventKey string
	if err := json.Unmarshal(detail["event_key"], &eventKey); err != nil || len(eventKey) != 64 {
		t.Fatalf("hashed event key = %q, %v", eventKey, err)
	}
	var storedContent map[string]int
	if err := json.Unmarshal(memories[0].Content, &storedContent); err != nil || storedContent["level"] != 7 {
		t.Fatalf("stored content = %s, %v", memories[0].Content, err)
	}
	var dedupeCount int
	if err := first.DB().QueryRow(`SELECT COUNT(*) FROM ai_observation_dedupe WHERE profile_id=?`, profileOne.ID).Scan(&dedupeCount); err != nil {
		t.Fatal(err)
	}
	if dedupeCount != 1 {
		t.Fatalf("dedupe rows = %d", dedupeCount)
	}
}

func TestRecordGameObservationRejectsUnboundedOrSensitiveContent(t *testing.T) {
	store := testStore(t)
	profile := testProfile()
	profile.ID = "observation-validation"
	if _, err := store.CreateProfile(context.Background(), profile); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		content any
	}{
		{name: "oversized", content: strings.Repeat("x", MaxObservedContentBytes+1)},
		{name: "credential field", content: json.RawMessage(`{"api_key":"secret"}`)},
		{name: "raw packet field", content: json.RawMessage(`{"raw_packet":"0102"}`)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			created, err := store.RecordGameObservation(context.Background(), profile.ID, test.name, "game.fact", "subject", test.content)
			if err == nil || created {
				t.Fatalf("invalid observation = %v, %v", created, err)
			}
		})
	}
	var count int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM ai_memories WHERE profile_id=?`, profile.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("invalid observation created %d memories", count)
	}
	if _, err := store.RecordGameObservation(context.Background(), profile.ID, "", "game.fact", "subject", `{"ok":true}`); err == nil {
		t.Fatal("empty event key was accepted")
	}
}

func TestRecordGameObservationConcurrentStoresClaimOnce(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "ai.db")
	first, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	profile := testProfile()
	profile.ID = "observation-concurrent"
	if _, err := first.CreateProfile(context.Background(), profile); err != nil {
		t.Fatal(err)
	}
	second, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	type result struct {
		created bool
		err     error
	}
	results := make(chan result, 2)
	var group sync.WaitGroup
	for _, store := range []*Store{first, second} {
		group.Add(1)
		go func(store *Store) {
			defer group.Done()
			created, err := store.RecordGameObservation(context.Background(), profile.ID, "same-event", "game.fact", "subject", `{"value":1}`)
			results <- result{created: created, err: err}
		}(store)
	}
	group.Wait()
	close(results)
	createdCount := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.created {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Fatalf("concurrent claim count = %d", createdCount)
	}
}

func TestRecordGameObservationRollsBackAllRowsOnMemoryFailure(t *testing.T) {
	store := testStore(t)
	profile := testProfile()
	profile.ID = "observation-atomic"
	if _, err := store.CreateProfile(context.Background(), profile); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`CREATE TRIGGER reject_observation_memory
        BEFORE INSERT ON ai_memories WHEN NEW.kind='atomic.test'
        BEGIN SELECT RAISE(ABORT, 'observation test failure'); END`); err != nil {
		t.Fatal(err)
	}
	created, err := store.RecordGameObservation(context.Background(), profile.ID, "atomic-event", "atomic.test", "subject", `{"value":1}`)
	if err == nil || created {
		t.Fatalf("failed observation = %v, %v", created, err)
	}
	var memories, dedupe, observations int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM ai_memories WHERE profile_id=?`, profile.ID).Scan(&memories); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM ai_observation_dedupe WHERE profile_id=?`, profile.ID).Scan(&dedupe); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM ai_events WHERE profile_id=? AND kind=?`, profile.ID, EventGameObservation).Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if memories != 0 || dedupe != 0 || observations != 0 {
		t.Fatalf("partial observation rows: memories=%d dedupe=%d events=%d", memories, dedupe, observations)
	}
}
