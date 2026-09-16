package aiservice

import (
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
)

func TestPersistentIdentityIsSeparateFromExecutionBinding(t *testing.T) {
	const id = "pc1_0123456789abcdef0123456789abcdef"
	binding := aimcp.Binding{CharacterID: "account:0"}
	snapshot := aigame.Snapshot{Connected: true, Phase: aigame.PhaseWorld, AI: aigame.AIObservation{Received: true, PersistentCharacterID: id}}
	snapshot.Player.HasStatus = true
	for _, tc := range []struct {
		name string
		edit func(*aigame.Snapshot)
		want string
	}{
		{"loaded", func(s *aigame.Snapshot) {}, id},
		{"disconnected", func(s *aigame.Snapshot) { s.Connected = false }, ""},
		{"not ready", func(s *aigame.Snapshot) { s.Player.HasStatus = false }, ""},
		{"no native response", func(s *aigame.Snapshot) { s.AI.Received = false }, ""},
		{"invalid", func(s *aigame.Snapshot) { s.AI.PersistentCharacterID = "account:0" }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := snapshot
			tc.edit(&s)
			o := ProjectObservation(binding, s)
			if o.PersistentCharacterID != tc.want {
				t.Fatalf("wrong identity: %+v", o)
			}
			if o.CharacterID != binding.CharacterID || o.Character.ID != binding.CharacterID {
				t.Fatal("stable identity replaced execution binding")
			}
		})
	}
}
