package airuntime

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

func TestSubjectRecallSurvivesRecentTrafficAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "recall.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"first", "second"} {
		p := testProfile()
		p.ID = id
		if _, err := store.CreateProfile(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	record := func(profile, key, subject string) {
		t.Helper()
		if _, err := store.RecordGameObservation(ctx, profile, key, "pet.level", subject, map[string]any{"level": 7}); err != nil {
			t.Fatal(err)
		}
	}
	record("first", "old", "pet-owned")
	record("second", "foreign", "pet-owned")
	for i := 0; i < 40; i++ {
		record("first", fmt.Sprint(i), "unrelated")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, err := store.ListConfirmedSubjectMemories(ctx, "first", "pet-owned", 4)
	if err != nil || len(got) != 1 || got[0].ProfileID != "first" {
		t.Fatalf("recall: %+v %v", got, err)
	}
	if _, err := store.db.ExecContext(ctx, "UPDATE ai_memories SET confirmed=0 WHERE id=?", got[0].ID); err == nil {
		t.Fatal("schema accepted unconfirmed memory")
	}
	got, err = store.ListConfirmedSubjectMemories(ctx, "first", "pet-owned", 4)
	if err != nil || len(got) != 1 {
		t.Fatalf("confirmed recall after rejected write: %+v %v", got, err)
	}
	for _, subject := range []string{"", "pet-owned' OR 1=1 --"} {
		got, err = store.ListConfirmedSubjectMemories(ctx, "first", subject, 4)
		if err != nil || len(got) != 0 {
			t.Fatalf("nonmatching subject: %+v %v", got, err)
		}
	}
	got, err = store.ListConfirmedSubjectMemories(ctx, "first", "unrelated", 1000)
	if err != nil || len(got) != 32 {
		t.Fatalf("bounded recall: %d %v", len(got), err)
	}
}
