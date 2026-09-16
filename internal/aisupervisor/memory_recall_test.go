package aisupervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestPromptRecallsCurrentPetBeyondRecentWindow(t *testing.T) {
	ctx := context.Background()
	store := testSupervisorStore(t)
	profile := testSupervisorProfile("recall-profile")
	profile.Goal.TargetCharacterID = "pet-current"
	if _, err := store.CreateProfile(ctx, profile); err != nil {
		t.Fatal(err)
	}
	record := func(key, subject, text string) {
		t.Helper()
		if _, err := store.RecordGameObservation(ctx, profile.ID, key, "experience", subject, map[string]string{"text": text}); err != nil {
			t.Fatal(err)
		}
	}
	record("pet", "pet-current", "remember-this-pet")
	record("character", profile.Character.ID, "remember-this-character")
	record("irrelevant", "pet-previous", "old-other-pet")
	for i := 0; i < 40; i++ {
		record(fmt.Sprint(i), "chat", fmt.Sprintf("recent-message-%02d", i))
	}
	supervisor, err := New(ctx, store, nil, supervisorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := supervisor.buildPrompt(profile, Snapshot{GameReady: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"remember-this-pet", "remember-this-character", "recent-message-39"} {
		if !strings.Contains(prompt, marker) {
			t.Fatalf("missing %s", marker)
		}
	}
	if strings.Contains(prompt, "old-other-pet") {
		t.Fatal("unrelated old memory selected")
	}
	memories, err := supervisor.promptMemories(ctx, profile.ID, "pet-current", profile.Character.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) > promptMemoryLimit {
		t.Fatal("memory count exceeded")
	}
	seen := map[int64]bool{}
	used := 0
	for _, memory := range memories {
		if seen[memory.ID] {
			t.Fatal("duplicate memory")
		}
		seen[memory.ID] = true
		encoded, _ := json.Marshal(memory)
		used += len(encoded)
	}
	encodedArray, err := json.Marshal(memories)
	if err != nil {
		t.Fatal(err)
	}
	if used > promptMemoryBytes || len(encodedArray) > promptMemoryBytes {
		t.Fatal("memory byte budget exceeded")
	}
	// The same recent item selected through both paths must appear only once.
	memories, err = supervisor.promptMemories(ctx, profile.ID, "chat", "chat")
	if err != nil {
		t.Fatal(err)
	}
	seen = map[int64]bool{}
	for _, memory := range memories {
		if seen[memory.ID] {
			t.Fatal("duplicate target/recent memory")
		}
		seen[memory.ID] = true
	}
}
func TestRelevantMemoryDoesNotConsumeEntirePromptBudget(t *testing.T) {
	ctx := context.Background()
	store := testSupervisorStore(t)
	profile := testSupervisorProfile("recall-budget")
	if _, err := store.CreateProfile(ctx, profile); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordGameObservation(ctx, profile.ID, "large", "experience", "target", strings.Repeat("x", 10000)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		if _, err := store.RecordGameObservation(ctx, profile.ID, fmt.Sprint(i), "experience", "other", map[string]string{"text": "small recent memory"}); err != nil {
			t.Fatal(err)
		}
	}
	supervisor, err := New(ctx, store, nil, supervisorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	memories, err := supervisor.promptMemories(ctx, profile.ID, "target")
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != promptMemoryLimit {
		t.Fatalf("oversized old context displaced recent events: %d", len(memories))
	}
	for _, m := range memories {
		if m.Subject == "target" {
			t.Fatal("large recalled memory consumed reserved recent budget")
		}
	}
}
