package aiservice

import (
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/automation"
	"testing"
)

func TestSoloTaskConditionRequiresAuthoritativePartyMode(t *testing.T) {
	condition := automation.Condition{Kind: "flag_set", ID: "party:solo"}
	for _, tc := range []struct {
		name             string
		known, connected bool
		mode             int32
		phase            aigame.Phase
		want             bool
	}{
		{"empty rows unknown", false, true, 0, aigame.PhaseWorld, false},
		{"solo", true, true, 0, aigame.PhaseWorld, true},
		{"leader", true, true, 1, aigame.PhaseWorld, false},
		{"member", true, true, 2, aigame.PhaseWorld, false},
		{"disconnected", true, false, 0, aigame.PhaseWorld, false},
		{"battle", true, true, 0, aigame.PhaseBattle, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := aigame.Snapshot{Connected: tc.connected, Phase: tc.phase, Player: aigame.PlayerSnapshot{HasStatus: true}, AI: aigame.AIObservation{Received: true, PartyModeKnown: tc.known, PartyMode: tc.mode}}
			o := ProjectObservation(aimcp.Binding{}, s)
			if got := condition.Match(projectAutomationObservation(o)); got != tc.want {
				t.Fatalf("solo match=%v want=%v flags=%v", got, tc.want, o.Flags)
			}
		})
	}
}
