package aiservice

import (
	"context"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
)

func TestPersonObservationProjectionAndDurableMemory(t *testing.T) {
	ctx := context.Background()
	store := memoryTestStore(t)
	profile := memoryTestProfile("person-memory")
	if _, err := store.CreateProfile(ctx, profile); err != nil {
		t.Fatal(err)
	}
	const id = "pc1_0123456789abcdef0123456789abcdef"
	snapshot := aigame.Snapshot{Connected: true, Phase: aigame.PhaseWorld, Player: aigame.PlayerSnapshot{HasStatus: true}, Actors: []aigame.ActorSnapshot{{ID: 42, Name: "Friend", PersistentCharacterID: id}}, Party: []aigame.PartyMember{{ID: 42, Name: "Friend", PersistentCharacterID: id}}}
	o := ProjectObservation(aimcp.Binding{CharacterID: profile.Character.ID}, snapshot)
	if o.Actors[0].PersistentCharacterID != id || o.Party[0].PersistentCharacterID != id {
		t.Fatal("identity lost in MCP projection")
	}
	recorder := NewMemoryRecorder(store)
	for i := 0; i < 3; i++ {
		if i == 1 {
			recorder = NewMemoryRecorder(store)
		} // Restart keeps deduplication.
		if err := recorder.Observe(ctx, profile.ID, o); err != nil {
			t.Fatal(err)
		}
	}
	memories, err := store.ListConfirmedSubjectMemories(ctx, profile.ID, id, 10)
	if err != nil || len(memories) != 2 {
		t.Fatalf("first sighting/shared party: %d %v", len(memories), err)
	}
	// Reused object handle and same name cannot associate a new person.
	snapshot.Actors[0].PersistentCharacterID = ""
	snapshot.Party[0].PersistentCharacterID = ""
	o = ProjectObservation(aimcp.Binding{CharacterID: profile.Character.ID}, snapshot)
	if err := recorder.Observe(ctx, profile.ID, o); err != nil {
		t.Fatal(err)
	}
	transient, err := store.ListConfirmedSubjectMemories(ctx, profile.ID, "42", 10)
	if err != nil || len(transient) != 0 {
		t.Fatalf("object ID used as person: %+v %v", transient, err)
	}
}
