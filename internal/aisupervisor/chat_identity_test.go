package aisupervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
	"strings"
	"testing"
	"time"
)

func TestPromptDoesNotRecallTransientChatIDAsPersistentTarget(t *testing.T) {
	ctx := context.Background()
	store := testSupervisorStore(t)
	profile := testSupervisorProfile("legacy-chat-context")
	if _, err := store.CreateProfile(ctx, profile); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordGameObservation(ctx, profile.ID, "old-chat", "chat.message", "42", map[string]any{"from_id": 42, "text": "ancient-unrelated-chat"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		if _, err := store.RecordGameObservation(ctx, profile.ID, fmt.Sprint(i), "experience", "other", map[string]int{"value": i}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.RecordGameObservation(ctx, profile.ID, "recent-chat", "chat.message", "42", map[string]any{"from_id": 42, "text": "recent-chat-text"}); err != nil {
		t.Fatal(err)
	}
	supervisor, err := New(ctx, store, nil, supervisorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	memories, err := supervisor.promptMemories(ctx, profile.ID, "42")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range memories {
		if strings.Contains(string(m.Content), "ancient-unrelated-chat") {
			t.Fatal("transient target recalled old unrelated speaker")
		}
		if m.Kind == "chat.message" {
			found = true
			var value map[string]any
			if err := json.Unmarshal(m.Content, &value); err != nil {
				t.Fatal(err)
			}
			if m.Subject != "" || value["speaker_identity"] != "unresolved" {
				t.Fatalf("legacy speaker ID presented as durable identity: %+v", m)
			}
		}
	}
	if !found {
		t.Fatal("recent conversation was lost")
	}
	prompt := appendPromptMemories("custom", memories)
	if !strings.Contains(prompt, "none establishes persistent player identity") {
		t.Fatal("custom prompt lacks attribution semantics")
	}
	persisted, err := store.ListMemories(ctx, profile.ID, 1)
	if err != nil || persisted[0].Subject != "42" {
		t.Fatal("presentation changed historical audit rows")
	}
}

func TestPromptRecallsRecentAttributedSpeakerHistory(t *testing.T) {
	ctx := context.Background()
	store := testSupervisorStore(t)
	profile := testSupervisorProfile("attributed-chat-recall")
	if _, err := store.CreateProfile(ctx, profile); err != nil {
		t.Fatal(err)
	}
	const id = "pc1_0123456789abcdef0123456789abcdef"
	for _, row := range []struct{ key, marker, speaker, context, text string }{
		{"valid", "server_chat_v1", id, "old-session", "remember-this-conversation"},
		{"legacy", "unresolved", id, "old-session", "never-recall-legacy-subject"},
		{"mismatch", "server_chat_v1", "pc1_abcdef0123456789abcdef0123456789", "old-session", "never-recall-mismatch"},
	} {
		if _, err := store.RecordGameObservation(ctx, profile.ID, row.key, "chat.message", id, map[string]any{"speaker_identity": row.marker, "speaker_character_id": row.speaker, "context_id": row.context, "text": row.text}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 40; i++ {
		if _, err := store.RecordGameObservation(ctx, profile.ID, fmt.Sprint(i), "experience", "other", map[string]int{"value": i}); err != nil {
			t.Fatal(err)
		}
	}
	cfg := supervisorTestConfig()
	supervisor, err := New(ctx, store, nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	now := supervisor.cfg.Clock()
	payload, err := json.Marshal(map[string]any{"connected": true, "ready": true, "chat": []map[string]any{{"speaker_character_id": id, "context_id": "new-session", "at": now.Format(time.RFC3339Nano)}}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := Snapshot{GameReady: true, Context: payload}
	for _, custom := range []bool{false, true} {
		if custom {
			supervisor.cfg.Prompt = func(_ airuntime.Profile, _ Snapshot) (string, error) { return "custom prompt", nil }
		}
		prompt, err := supervisor.buildPromptContext(ctx, profile, snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(prompt, "remember-this-conversation") || strings.Contains(prompt, "never-recall-") {
			t.Fatalf("bad chat history recall: %s", prompt)
		}
	}
	if ids := recentChatSubjects(snapshot, now.Add(6*time.Minute)); len(ids) != 0 {
		t.Fatal("stale chat drives recall")
	}
	if ids := recentChatSubjects(snapshot, now.Add(-time.Second)); len(ids) != 0 {
		t.Fatal("future chat drives recall")
	}
	snapshot.GameReady = false
	if ids := recentChatSubjects(snapshot, now); len(ids) != 0 {
		t.Fatal("offline chat drives recall")
	}
}
