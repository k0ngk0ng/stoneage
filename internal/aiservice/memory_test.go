package aiservice

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

func memoryTestStore(t *testing.T) *airuntime.Store {
	t.Helper()
	store, err := airuntime.OpenStore(filepath.Join(t.TempDir(), "ai.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func memoryTestProfile(id string) airuntime.Profile {
	return airuntime.Profile{
		ID:        id,
		Account:   airuntime.AccountIdentity{ID: id + "-account", Username: id + "-user"},
		Character: airuntime.CharacterIdentity{ID: id + "-character", Name: "MemoryHero"},
	}
}

func TestMemoryRecorderRecordsFactsAndStableRetries(t *testing.T) {
	store := memoryTestStore(t)
	profile := memoryTestProfile("memory-recorder-profile")
	if _, err := store.CreateProfile(context.Background(), profile); err != nil {
		t.Fatal(err)
	}
	recorder := NewMemoryRecorder(store)
	first := aimcp.Observation{
		CharacterID: "character-1",
		Ready:       true,
		Character:   aimcp.Entity{ID: "character-1", Level: 10},
		Pets: []aimcp.Entity{
			{ID: "pet-b", Level: 4},
			{Level: 8},
			{ID: "pet-a", Level: 2},
		},
		Chat:  []aimcp.ChatMessage{{At: "2026-09-15T10:00:00Z", Channel: "say", FromID: 42, Text: "hello"}},
		Party: []aimcp.PartyMember{{ID: "20", Name: "B", Level: 9}, {ID: "10", Name: "A", Level: 10}},
	}
	if err := recorder.Observe(context.Background(), profile.ID, first); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Observe(context.Background(), profile.ID, first); err != nil {
		t.Fatal(err)
	}
	memories, err := store.ListMemories(context.Background(), profile.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 5 { // character, two stable pets, chat, party
		t.Fatalf("initial memories = %#v", memories)
	}
	for _, memory := range memories {
		if memory.Kind == "chat.message" {
			var chat map[string]any
			if err := json.Unmarshal(memory.Content, &chat); err != nil {
				t.Fatal(err)
			}
			if _, hasRelation := chat["relationship"]; hasRelation {
				t.Fatalf("chat memory inferred a relationship: %s", memory.Content)
			}
		}
	}

	second := first
	second.Character.Level = 11
	second.Pets = []aimcp.Entity{{ID: "pet-a", Level: 3}, {ID: "pet-b", Level: 4}}
	second.Chat = []aimcp.ChatMessage{
		first.Chat[0],
		{At: "2026-09-15T10:00:01Z", Channel: "party", FromID: 10, Text: "ready"},
	}
	second.Party = []aimcp.PartyMember{{ID: "10", Name: "A", Level: 11}, {ID: "20", Name: "B", Level: 9}}
	if err := recorder.Observe(context.Background(), profile.ID, second); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Observe(context.Background(), profile.ID, second); err != nil {
		t.Fatal(err)
	}
	memories, err = store.ListMemories(context.Background(), profile.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 9 { // character, pet, party changes, and one new chat
		t.Fatalf("changed memories = %d (%#v)", len(memories), memories)
	}

	// Party order is presentation detail. Reordering it must not create a
	// second snapshot or a false change.
	second.Party = []aimcp.PartyMember{{ID: "20", Name: "B", Level: 9}, {ID: "10", Name: "A", Level: 11}}
	if err := recorder.Observe(context.Background(), profile.ID, second); err != nil {
		t.Fatal(err)
	}
	memories, err = store.ListMemories(context.Background(), profile.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 9 {
		t.Fatalf("reordered party changed memory count = %d", len(memories))
	}
}

func TestMemoryRecorderSerializesConcurrentObservations(t *testing.T) {
	store := memoryTestStore(t)
	profile := memoryTestProfile("memory-recorder-race")
	if _, err := store.CreateProfile(context.Background(), profile); err != nil {
		t.Fatal(err)
	}
	recorder := NewMemoryRecorder(store)
	observation := aimcp.Observation{CharacterID: "character-race", Ready: true, Character: aimcp.Entity{ID: "character-race", Level: 1}}
	var group sync.WaitGroup
	for i := 0; i < 16; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if err := recorder.Observe(context.Background(), profile.ID, observation); err != nil {
				t.Errorf("concurrent observation: %v", err)
			}
		}()
	}
	group.Wait()
	memories, err := store.ListMemories(context.Background(), profile.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 2 { // initial character level and empty ready party
		t.Fatalf("concurrent memories = %#v", memories)
	}
}

func TestMemoryRecorderSkipsRestrictedChatAndContinuesFacts(t *testing.T) {
	store := memoryTestStore(t)
	profile := memoryTestProfile("memory-recorder-chat-noise")
	if _, err := store.CreateProfile(context.Background(), profile); err != nil {
		t.Fatal(err)
	}
	recorder := NewMemoryRecorder(store)
	observation := aimcp.Observation{
		CharacterID: "chat-noise-character",
		Ready:       true,
		Character:   aimcp.Entity{ID: "chat-noise-character", Level: 3},
		Chat: []aimcp.ChatMessage{
			{At: "2026-09-15T10:00:00Z", Channel: "say", FromID: 1, Text: "password=do-not-store"},
			{At: "2026-09-15T10:00:01Z", Channel: "say", FromID: 2, Text: strings.Repeat("x", airuntime.MaxObservedContentBytes)},
		},
	}
	if err := recorder.Observe(context.Background(), profile.ID, observation); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Observe(context.Background(), profile.ID, observation); err != nil {
		t.Fatal(err)
	}
	memories, err := store.ListMemories(context.Background(), profile.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 2 { // character level and the ready empty-party snapshot
		t.Fatalf("memories after restricted chat = %#v", memories)
	}
	for _, memory := range memories {
		if memory.Kind == MemoryKindChatMessage {
			t.Fatalf("restricted chat was persisted: %#v", memory)
		}
	}
}

func TestMemoryRecorderRestoresStateAfterForget(t *testing.T) {
	store := memoryTestStore(t)
	profile := memoryTestProfile("memory-recorder-forget")
	if _, err := store.CreateProfile(context.Background(), profile); err != nil {
		t.Fatal(err)
	}
	recorder := NewMemoryRecorder(store)
	first := aimcp.Observation{CharacterID: "forget-character", Character: aimcp.Entity{ID: "forget-character", Level: 4}}
	if err := recorder.Observe(context.Background(), profile.ID, first); err != nil {
		t.Fatal(err)
	}
	recorder.Forget(profile.ID)
	second := first
	second.Character.Level = 5
	if err := recorder.Observe(context.Background(), profile.ID, second); err != nil {
		t.Fatal(err)
	}
	memories, err := store.ListMemories(context.Background(), profile.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 2 || memories[0].Kind != MemoryKindCharacterLevel {
		t.Fatalf("restored memories = %#v", memories)
	}
	var change map[string]int
	if err := json.Unmarshal(memories[0].Content, &change); err != nil || change["from_level"] != 4 || change["level"] != 5 {
		t.Fatalf("restored change = %s, %v", memories[0].Content, err)
	}
}
