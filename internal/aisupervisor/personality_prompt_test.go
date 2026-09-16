package aisupervisor

import (
	"context"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

func TestDefaultPromptIncludesPersistedProfilePersonalityAndGoal(t *testing.T) {
	store := testSupervisorStore(t)
	profile := testSupervisorProfile("prompt-personality")
	profile.Personality = airuntime.Personality{
		Name:   "谨慎探险者",
		Prompt: "personality-prompt-alpha",
		Traits: []string{"trait-alpha", "trait-observant"},
		Values: map[string]string{"risk": "value-alpha"},
	}
	profile.Goal = airuntime.Goal{
		Kind:              "leveling",
		Description:       "goal-description-alpha",
		TargetLevel:       80,
		TargetCharacterID: "pet-alpha",
		StopWhenCompleted: true,
		CharacterBuild:    &airuntime.CharacterBuild{Weights: airuntime.AttributeWeights{Strength: 1, Dexterity: 1}, ReservePoints: 3},
		Metadata: map[string]string{
			"target_kind": "pet",
			"stat_policy": "allocate-vital-alpha",
		},
	}
	if _, err := store.CreateProfile(context.Background(), profile); err != nil {
		t.Fatal(err)
	}
	persisted, err := store.GetProfile(context.Background(), profile.ID)
	if err != nil {
		t.Fatal(err)
	}

	supervisor, err := New(context.Background(), store, nil, supervisorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := supervisor.buildPrompt(persisted, Snapshot{GameReady: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{
		"谨慎探险者", "personality-prompt-alpha", "trait-alpha", "trait-observant", "value-alpha",
		"goal-description-alpha", "pet-alpha", "allocate-vital-alpha",
	} {
		if !strings.Contains(prompt, marker) {
			t.Fatalf("default prompt omitted persisted marker %q: %s", marker, prompt)
		}
	}
	if !strings.Contains(prompt, `"target_character_id":"pet-alpha"`) || !strings.Contains(prompt, `"target_kind":"pet"`) {
		t.Fatalf("default prompt omitted pet target fields: %s", prompt)
	}
	if !strings.Contains(prompt, `"character_build":{"weights":{"vital":0,"strength":1,"toughness":0,"dexterity":1},"reserve_points":3}`) {
		t.Fatalf("configured build omitted from prompt: %s", prompt)
	}
}

func TestDefaultPromptKeepsProfilePersonalityAndGoalIsolated(t *testing.T) {
	store := testSupervisorStore(t)
	first := testSupervisorProfile("prompt-isolation-first")
	first.Personality = airuntime.Personality{Name: "first-personality", Prompt: "first-prompt", Traits: []string{"first-trait"}, Values: map[string]string{"key": "first-value"}}
	first.Goal = airuntime.Goal{Kind: "first-goal", TargetCharacterID: "first-target", Metadata: map[string]string{"policy": "first-policy"}}
	second := testSupervisorProfile("prompt-isolation-second")
	second.Personality = airuntime.Personality{Name: "second-personality", Prompt: "second-prompt", Traits: []string{"second-trait"}, Values: map[string]string{"key": "second-value"}}
	second.Goal = airuntime.Goal{Kind: "second-goal", TargetCharacterID: "second-target", Metadata: map[string]string{"policy": "second-policy"}}
	for _, profile := range []airuntime.Profile{first, second} {
		if _, err := store.CreateProfile(context.Background(), profile); err != nil {
			t.Fatal(err)
		}
	}
	supervisor, err := New(context.Background(), store, nil, supervisorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	firstPersisted, err := store.GetProfile(context.Background(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondPersisted, err := store.GetProfile(context.Background(), second.ID)
	if err != nil {
		t.Fatal(err)
	}
	firstPrompt, err := supervisor.buildPrompt(firstPersisted, Snapshot{GameReady: true})
	if err != nil {
		t.Fatal(err)
	}
	secondPrompt, err := supervisor.buildPrompt(secondPersisted, Snapshot{GameReady: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"first-personality", "first-prompt", "first-trait", "first-value", "first-goal", "first-target", "first-policy"} {
		if !strings.Contains(firstPrompt, marker) || strings.Contains(secondPrompt, marker) {
			t.Fatalf("first profile marker %q crossed prompt boundary: first=%s second=%s", marker, firstPrompt, secondPrompt)
		}
	}
	for _, marker := range []string{"second-personality", "second-prompt", "second-trait", "second-value", "second-goal", "second-target", "second-policy"} {
		if !strings.Contains(secondPrompt, marker) || strings.Contains(firstPrompt, marker) {
			t.Fatalf("second profile marker %q crossed prompt boundary: first=%s second=%s", marker, firstPrompt, secondPrompt)
		}
	}
}
