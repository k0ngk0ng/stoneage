package airuntime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAgentNotesAreProfileScopedAndUpsertInPlace(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	first := testProfile()
	first.ID = "agent-note-one"
	second := testProfile()
	second.ID = "agent-note-two"
	if _, err := store.CreateProfile(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProfile(ctx, second); err != nil {
		t.Fatal(err)
	}

	createdAt := time.Date(2026, 9, 16, 8, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return createdAt }
	note, err := store.UpsertAgentNote(ctx, first.ID, "route", "Visit the village first.")
	if err != nil {
		t.Fatal(err)
	}
	if note.ID == 0 || note.ProfileID != first.ID || note.Key != "route" || note.Text != "Visit the village first." {
		t.Fatalf("created note = %#v", note)
	}
	if !note.CreatedAt.Equal(createdAt) || !note.UpdatedAt.Equal(createdAt) {
		t.Fatalf("created note times = %#v", note)
	}

	createdID := note.ID
	createdTimestamp := note.CreatedAt
	createdAt = createdAt.Add(time.Minute)
	updated, err := store.UpsertAgentNote(ctx, first.ID, " route ", "Keep enough food for the return trip. ")
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != createdID || !updated.CreatedAt.Equal(createdTimestamp) || updated.Text != "Keep enough food for the return trip." || !updated.UpdatedAt.Equal(createdAt) {
		t.Fatalf("updated note = %#v", updated)
	}

	if _, err := store.UpsertAgentNote(ctx, second.ID, "route", "Use the second profile's plan."); err != nil {
		t.Fatal(err)
	}
	firstNotes, err := store.ListAgentNotes(ctx, first.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	secondNotes, err := store.ListAgentNotes(ctx, second.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstNotes) != 1 || firstNotes[0].ID != createdID || firstNotes[0].Text != updated.Text {
		t.Fatalf("first profile notes = %#v", firstNotes)
	}
	if len(secondNotes) != 1 || secondNotes[0].ProfileID != second.ID || secondNotes[0].Text != "Use the second profile's plan." {
		t.Fatalf("second profile notes = %#v", secondNotes)
	}

	var memories, events int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM ai_memories`).Scan(&memories); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM ai_events WHERE profile_id=?`, first.ID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if memories != 0 || events != 1 { // only profile.created; notes are not game events.
		t.Fatalf("note writes changed confirmed state: memories=%d events=%d", memories, events)
	}
}

func TestAgentNotesLimitValidationAndDeleteIsolation(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	profile := testProfile()
	profile.ID = "agent-note-validation"
	other := testProfile()
	other.ID = "agent-note-other"
	if _, err := store.CreateProfile(ctx, profile); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProfile(ctx, other); err != nil {
		t.Fatal(err)
	}

	tooLongKey := strings.Repeat("k", MaxAgentNoteKeyBytes+1)
	tooLongText := strings.Repeat("t", MaxAgentNoteTextBytes+1)
	invalidUTF8 := string([]byte{0xff})
	for _, test := range []struct {
		name string
		key  string
		text string
	}{
		{name: "empty key", key: " ", text: "text"},
		{name: "empty text", key: "empty-text", text: " \n\t "},
		{name: "long key", key: tooLongKey, text: "text"},
		{name: "long text", key: "long-text", text: tooLongText},
		{name: "invalid key", key: invalidUTF8, text: "text"},
		{name: "invalid text", key: "invalid-text", text: invalidUTF8},
		{name: "control key", key: "line\nkey", text: "text"},
		{name: "control text", key: "control-text", text: "text\x00"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := store.UpsertAgentNote(ctx, profile.ID, test.key, test.text); err == nil {
				t.Fatal("invalid note was accepted")
			}
		})
	}

	for i := 0; i < MaxAgentNoteLimit+5; i++ {
		if _, err := store.UpsertAgentNote(ctx, profile.ID, "key-"+strings.Repeat("0", 2)+string(rune('a'+i%26))+string(rune('0'+i/26)), "note"); err != nil {
			t.Fatalf("insert note %d: %v", i, err)
		}
	}
	defaultNotes, err := store.ListAgentNotes(ctx, profile.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(defaultNotes) != DefaultAgentNoteLimit {
		t.Fatalf("default note limit = %d, want %d", len(defaultNotes), DefaultAgentNoteLimit)
	}
	allNotes, err := store.ListAgentNotes(ctx, profile.ID, MaxAgentNoteLimit+1)
	if err != nil {
		t.Fatal(err)
	}
	if len(allNotes) != MaxAgentNoteLimit {
		t.Fatalf("maximum note limit = %d, want %d", len(allNotes), MaxAgentNoteLimit)
	}

	if err := store.DeleteAgentNote(ctx, other.ID, "key-00a0"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-profile delete error = %v", err)
	}
	if err := store.DeleteAgentNote(ctx, profile.ID, allNotes[0].Key); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteAgentNote(ctx, profile.ID, allNotes[0].Key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete error = %v", err)
	}
	if err := store.DeleteAgentNote(ctx, "missing-profile", "key"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing profile delete error = %v", err)
	}
	if _, err := store.ListAgentNotes(ctx, "missing-profile", 10); err != nil {
		t.Fatalf("missing profile list error = %v", err)
	}

	var tableSQL string
	if err := store.DB().QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='ai_agent_notes'`).Scan(&tableSQL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tableSQL, "REFERENCES ai_profiles") || strings.Contains(strings.ToLower(tableSQL), "ai_memories") {
		t.Fatalf("unexpected agent note schema = %s", tableSQL)
	}
}

func TestAgentNotesCascadeWhenProfileIsDeleted(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	profile := testProfile()
	profile.ID = "agent-note-cascade"
	created, err := store.CreateProfile(ctx, profile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertAgentNote(ctx, created.ID, "before-delete", "remove with profile"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteProfileCAS(ctx, created.ID, created.Version, "test"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM ai_agent_notes WHERE profile_id=?`, created.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("agent notes after profile deletion = %d", count)
	}
	if _, err := store.UpsertAgentNote(ctx, created.ID, "after-delete", "must fail"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("upsert deleted profile error = %v", err)
	}
}
