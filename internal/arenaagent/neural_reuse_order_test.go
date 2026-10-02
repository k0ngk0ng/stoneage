package arenaagent

import (
	"context"
	"testing"
)

// A persisted current final plan is already authoritative for its remaining
// orders. Upgrading replay validation must not replan it because earlier legacy
// attempts are ambiguous, or force Basic to replace remaining committed orders.
func TestProspectiveCurrentFinalPlanReusePrecedesPriorReplayValidation(t *testing.T) {
	f := prospectiveHistory(t)
	events := f.store.History("match")
	validHistory := append(append([]Object{}, events...), neuralTurnRecord(f.fresh, f.freshDecision))
	currentDecision, err := f.model.Decide(context.Background(), f.next, validHistory)
	if err != nil {
		t.Fatal("current final-plan control", err)
	}
	ambiguousHistory := append(append([]Object{}, events...), neuralTurnRecord(f.stale, f.staleDecision), neuralTurnRecord(f.fresh, f.freshDecision), neuralTurnRecord(f.next, currentDecision))
	partial := clone(f.next)
	member := obj(obj(partial["members"])["member-0"])
	member["reserved_actors"] = Object{"player": "written"}
	obj(member["battle"])["PlayerSubmitted"] = true
	got, err := f.model.Decide(context.Background(), partial, ambiguousHistory)
	if err != nil {
		t.Fatalf("earlier ambiguous legacy attempts blocked reuse of persisted current final plan: %v", err)
	}
	if !yes(got.Diagnostics["reused_plan"]) || len(got.Plan.Orders) != 1 || got.Plan.Orders[0].Actor != "pet" {
		t.Fatal("must reuse exactly the remaining final order", got)
	}
	want := ""
	for _, o := range currentDecision.Plan.Orders {
		if o.Actor == "pet" {
			want = o.Candidate
		}
	}
	if got.Plan.Orders[0].Candidate != want {
		t.Fatal("reused a different order")
	}
}
