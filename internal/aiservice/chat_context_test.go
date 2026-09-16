package aiservice

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"

	"github.com/k0ngk0ng/stoneage/internal/aimcp"
)

func TestChatMemoryKeepsContextWithoutInventingPersistentSpeaker(t *testing.T) {
	store := memoryTestStore(t)
	profile := memoryTestProfile("chat-context")
	ctx := context.Background()
	if _, err := store.CreateProfile(ctx, profile); err != nil {
		t.Fatal(err)
	}
	recorder := NewMemoryRecorder(store)
	message := aimcp.ChatMessage{At: "2026-09-16T10:00:00Z", Channel: "say", FromID: 42, Text: "hello", ContextID: "connection-one"}
	observe := func(m aimcp.ChatMessage) {
		t.Helper()
		if err := recorder.Observe(ctx, profile.ID, aimcp.Observation{Chat: []aimcp.ChatMessage{m}}); err != nil {
			t.Fatal(err)
		}
	}
	observe(message)
	observe(message)
	recorder.Forget(profile.ID)
	observe(message)
	message.ContextID = "connection-two"
	observe(message)
	memories, err := store.ListMemories(ctx, profile.ID, 32)
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 2 {
		t.Fatalf("context dedupe produced %d memories", len(memories))
	}
	for _, m := range memories {
		if m.Kind != MemoryKindChatMessage || m.Subject != "" {
			t.Fatalf("transient ID became durable subject: %+v", m)
		}
		var c chatMemoryContent
		if err := json.Unmarshal(m.Content, &c); err != nil {
			t.Fatal(err)
		}
		if c.SpeakerIdentity != "unresolved" || c.ContextID == "" || c.FromID != 42 {
			t.Fatalf("missing provenance: %+v", c)
		}
	}
	history, err := store.ListConfirmedSubjectMemories(ctx, profile.ID, "42", 32)
	if err != nil || len(history) != 0 {
		t.Fatalf("numeric object ID recalled as person: %+v %v", history, err)
	}
}

func TestChatContextSurvivesGameObservationProjection(t *testing.T) {
	source := aigame.Snapshot{Chat: []aigame.ChatMessage{{ContextID: "opaque-session", At: time.Date(2026, 9, 16, 1, 2, 3, 0, time.UTC), FromID: 42, Channel: "say", Text: "hello"}}}
	projected := ProjectObservation(aimcp.Binding{}, source)
	if len(projected.Chat) != 1 || projected.Chat[0].ContextID != "opaque-session" || projected.Chat[0].FromID != 42 {
		t.Fatalf("chat provenance lost: %+v", projected.Chat)
	}
}

func TestChatTimeSpeakerSurvivesProjectionAndRecorderRestart(t *testing.T) {
	const id = "pc1_0123456789abcdef0123456789abcdef"
	store := memoryTestStore(t)
	ctx := context.Background()
	profile := memoryTestProfile("chat-time-speaker")
	if _, err := store.CreateProfile(ctx, profile); err != nil {
		t.Fatal(err)
	}
	source := aigame.Snapshot{Chat: []aigame.ChatMessage{{SpeakerCharacterID: id, ContextID: "session-one", At: time.Now(), FromID: 42, Channel: "P", Text: "hello again"}}}
	observation := ProjectObservation(aimcp.Binding{}, source)
	if observation.Chat[0].SpeakerCharacterID != id {
		t.Fatal("projection lost speaker identity")
	}
	recorder := NewMemoryRecorder(store)
	if err := recorder.Observe(ctx, profile.ID, observation); err != nil {
		t.Fatal(err)
	}
	recorder = NewMemoryRecorder(store)
	if err := recorder.Observe(ctx, profile.ID, observation); err != nil {
		t.Fatal(err)
	}
	// Reusing the same object index in another session does not merge people.
	observation.Chat[0].ContextID = "session-two"
	observation.Chat[0].SpeakerCharacterID = "pc1_abcdef0123456789abcdef0123456789"
	if err := recorder.Observe(ctx, profile.ID, observation); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListConfirmedSubjectMemories(ctx, profile.ID, id, 32)
	if err != nil || len(rows) != 1 {
		t.Fatalf("attributed history: %+v %v", rows, err)
	}
	var content chatMemoryContent
	if err := json.Unmarshal(rows[0].Content, &content); err != nil {
		t.Fatal(err)
	}
	if content.SpeakerIdentity != "server_chat_v1" || content.SpeakerCharacterID != id {
		t.Fatalf("bad provenance: %+v", content)
	}
	for _, msg := range []aimcp.ChatMessage{
		{SpeakerCharacterID: "42", ContextID: "session"},
		{SpeakerCharacterID: id},
	} {
		if c := chatObservationContent(msg); c.SpeakerCharacterID != "" || c.SpeakerIdentity != "unresolved" {
			t.Fatalf("unproven identity: %+v", c)
		}
	}
}
