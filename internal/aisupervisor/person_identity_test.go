package aisupervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

func TestVisiblePersonRecallsPastConversationWithoutNewChat(t *testing.T) {
	ctx := context.Background()
	store := testSupervisorStore(t)
	profile := testSupervisorProfile("visible-person-recall")
	if _, err := store.CreateProfile(ctx, profile); err != nil {
		t.Fatal(err)
	}
	const id = "pc1_0123456789abcdef0123456789abcdef"
	if _, err := store.RecordGameObservation(ctx, profile.ID, "past-chat", "chat.message", id, map[string]any{"speaker_identity": "server_chat_v1", "speaker_character_id": id, "context_id": "past-session", "text": "remember-our-old-conversation"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		if _, err := store.RecordGameObservation(ctx, profile.ID, fmt.Sprint(i), "experience", "other", map[string]int{"value": i}); err != nil {
			t.Fatal(err)
		}
	}
	supervisor, err := New(ctx, store, nil, supervisorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"actors", "party"} {
		payload, _ := json.Marshal(map[string]any{"connected": true, "ready": true, role: []map[string]any{{"id": 42, "persistent_character_id": id}}})
		snapshot := Snapshot{GameReady: true, Context: payload}
		prompt, err := supervisor.buildPromptContext(ctx, profile, snapshot)
		if err != nil || !strings.Contains(prompt, "remember-our-old-conversation") {
			t.Fatalf("%s recall failed: %v %s", role, err, prompt)
		}
		snapshot.GameReady = false
		if len(currentPersonSubjects(snapshot)) != 0 {
			t.Fatal("offline people recall")
		}
	}
	payload := json.RawMessage(`{"connected":true,"ready":true,"actors":[{"id":42,"name":"pc1_0123456789abcdef0123456789abcdef"}]}`)
	if len(currentPersonSubjects(Snapshot{GameReady: true, Context: payload})) != 0 {
		t.Fatal("display name became identity")
	}
}

func TestCurrentPeopleRecallIsBoundedAndExcludesSelfAndDuplicates(t *testing.T) {
	const self = "pc1_00000000000000000000000000000000"
	const first = "pc1_11111111111111111111111111111111"
	const second = "pc1_22222222222222222222222222222222"
	const third = "pc1_33333333333333333333333333333333"
	person := func(id string) map[string]string { return map[string]string{"persistent_character_id": id} }
	payload, err := json.Marshal(map[string]any{"connected": true, "ready": true, "persistent_character_id": self,
		"party":  []map[string]string{person(self), person(first), person(first)},
		"actors": []map[string]string{person("invalid"), person(first), person(second), person(third)}})
	if err != nil {
		t.Fatal(err)
	}
	ids := currentPersonSubjects(Snapshot{GameReady: true, Context: payload})
	if len(ids) != 2 || ids[0] != first || ids[1] != second {
		t.Fatalf("unexpected recall subjects: %v", ids)
	}
}

func TestActiveSpeakerRecallSurvivesCrowdedScene(t *testing.T) {
	ctx := context.Background()
	store := testSupervisorStore(t)
	profile := testSupervisorProfile("crowded-conversation")
	profile.Goal.TargetCharacterID = "target-pet"
	if _, err := store.CreateProfile(ctx, profile); err != nil {
		t.Fatal(err)
	}
	const speaker = "pc1_11111111111111111111111111111111"
	const self = "pc1_22222222222222222222222222222222"
	if _, err := store.RecordGameObservation(ctx, profile.ID, "old-conversation", "chat.message", speaker, map[string]any{"speaker_identity": "server_chat_v1", "speaker_character_id": speaker, "context_id": "old-session", "text": "crowded-scene-old-conversation"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		if _, err := store.RecordGameObservation(ctx, profile.ID, fmt.Sprint(i), "experience", "other", map[string]int{"value": i}); err != nil {
			t.Fatal(err)
		}
	}
	supervisor, err := New(ctx, store, nil, supervisorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	now := supervisor.cfg.Clock()
	payload, err := json.Marshal(map[string]any{
		"connected": true, "ready": true, "persistent_character_id": self,
		"party": []map[string]string{{"persistent_character_id": "pc1_33333333333333333333333333333333"}, {"persistent_character_id": "pc1_44444444444444444444444444444444"}},
		"chat": []map[string]string{
			{"speaker_character_id": speaker, "context_id": "current-session", "at": now.Format(time.RFC3339Nano)},
			{"speaker_character_id": self, "context_id": "current-session", "at": now.Format(time.RFC3339Nano)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := Snapshot{GameReady: true, Context: payload}
	ids := recentChatSubjects(snapshot, now)
	if len(ids) != 1 || ids[0] != speaker {
		t.Fatalf("self consumed conversation recall: %v", ids)
	}
	for _, custom := range []bool{false, true} {
		if custom {
			supervisor.cfg.Prompt = func(_ airuntime.Profile, _ Snapshot) (string, error) { return "custom", nil }
		}
		prompt, err := supervisor.buildPromptContext(ctx, profile, snapshot)
		if err != nil || !strings.Contains(prompt, "crowded-scene-old-conversation") {
			t.Fatalf("active speaker history lost (custom=%v): %v %s", custom, err, prompt)
		}
	}
}
