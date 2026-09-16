package aiservice

import (
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

func TestSubmittedNPCWindowEvidenceIsCurrentAndBound(t *testing.T) {
	registry, err := NewNPCRegistry([]NPCSpec{verifiedNPCSpec()})
	if err != nil {
		t.Fatal(err)
	}
	fresh := func() aimcp.Observation {
		w := aimcp.WindowState{Type: 7, Sequence: 100, ObjectID: 42, Open: true, Submitted: true}
		return aimcp.Observation{Connected: true, Ready: true, Phase: "world", Floor: 10, ActiveWindow: &w, Windows: []aimcp.WindowState{w}, Actors: []aimcp.VisibleActor{{ID: 42, Name: "Trainer", X: 5, Y: 6}}}
	}
	o := fresh()
	submitted := registry.SubmittedWindows(o)
	if submitted["trainer"] != 100 || len(registry.ObservedWindows(o)) != 0 {
		t.Fatal("submission must remain distinct from actionable window")
	}
	if !(automation.Condition{Kind: "window_submitted", ID: "trainer", Value: 100}).Match(automation.Observation{SubmittedWindows: submitted}) {
		t.Fatal("submission condition not projected")
	}
	cases := map[string]func(*aimcp.Observation){
		"unsubmitted":           func(o *aimcp.Observation) { o.ActiveWindow.Submitted = false },
		"stale_history":         func(o *aimcp.Observation) { o.Windows[0].Sequence = 101 },
		"contradictory_history": func(o *aimcp.Observation) { o.Windows[0].Submitted = false },
		"different_actor":       func(o *aimcp.Observation) { o.ActiveWindow.ObjectID = 99 },
		"different_floor":       func(o *aimcp.Observation) { o.Floor = 11 },
		"closed":                func(o *aimcp.Observation) { o.ActiveWindow.Open = false },
		"disconnected":          func(o *aimcp.Observation) { o.Connected = false },
		"battle":                func(o *aimcp.Observation) { o.Battle.Active = true },
		"missing_actor":         func(o *aimcp.Observation) { o.Actors = nil },
		"ambiguous_actor":       func(o *aimcp.Observation) { o.Actors = append(o.Actors, o.Actors[0]) },
		"relogin":               func(o *aimcp.Observation) { o.ActiveWindow = nil },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			o := fresh()
			change(&o)
			if len(registry.SubmittedWindows(o)) != 0 {
				t.Fatal("unconfirmed window submission exposed")
			}
		})
	}
}

func TestNPCWindowAliasesCannotShareOneActiveIdentity(t *testing.T) {
	first := verifiedNPCSpec()
	second := verifiedNPCSpec()
	second.Alias = "trainer-alias"
	registry, err := NewNPCRegistry([]NPCSpec{first, second})
	if err != nil {
		t.Fatal(err)
	}
	for _, submitted := range []bool{false, true} {
		window := aimcp.WindowState{Type: 7, Sequence: 100, ObjectID: 42, Open: true, Submitted: submitted}
		o := aimcp.Observation{Connected: true, Ready: true, Phase: "world", Floor: 10, ActiveWindow: &window, Windows: []aimcp.WindowState{window}, Actors: []aimcp.VisibleActor{{ID: 42, Name: "Trainer", X: 5, Y: 6}}}
		if len(registry.ObservedWindows(o)) != 0 || len(registry.SubmittedWindows(o)) != 0 {
			t.Fatal("two aliases claimed the same active window")
		}
	}
}
