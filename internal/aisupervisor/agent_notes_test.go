package aisupervisor

import (
	"context"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

func TestSelfAuthoredNotesRecallIsScopedAndSeparateFromFacts(t *testing.T) {
	ctx := context.Background()
	store := testSupervisorStore(t)
	profile, err := store.CreateProfile(ctx, lifeTestProfile("notes-owner", 60))
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.CreateProfile(ctx, lifeTestProfile("notes-other", 60))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertAgentNote(ctx, profile.ID, "plans", "meet-by-the-village-tree"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertAgentNote(ctx, other.ID, "plans", "another-players-private-plan"); err != nil {
		t.Fatal(err)
	}
	for _, custom := range []bool{false, true} {
		config := supervisorTestConfig()
		if custom {
			config.Prompt = func(_ airuntime.Profile, _ Snapshot) (string, error) { return "custom-base", nil }
		}
		supervisor, err := New(ctx, store, nil, config)
		if err != nil {
			t.Fatal(err)
		}
		prompt, err := supervisor.buildPromptContext(ctx, profile, Snapshot{GameReady: true})
		_ = supervisor.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(prompt, "meet-by-the-village-tree") || strings.Contains(prompt, "another-players-private-plan") || !strings.Contains(prompt, "not confirmed game facts") {
			t.Fatal("private notes missing, mixed across profiles, or presented as facts")
		}
		if custom && !strings.HasPrefix(prompt, "custom-base") {
			t.Fatal("custom prompt lost")
		}
	}
	memories, err := store.ListMemories(ctx, profile.ID, 10)
	if err != nil || len(memories) != 0 {
		t.Fatal("self-authored notes promoted to confirmed memories")
	}
}
