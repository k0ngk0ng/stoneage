package arenaagent

import (
	"context"
	"reflect"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

// A legitimate same-turn refresh can reveal previously unknown own stats.
// Rebuild the public view and identity using the real shared projection.
func prospectiveRefreshOwnStats(t *testing.T, team Object, known bool) Object {
	t.Helper()
	var old aigame.BattleView
	original := obj(obj(team["members"])["member-0"])
	if err := decode(enc(original), &old); err != nil {
		t.Fatal(err)
	}
	old.Own.CombatStatsKnown = known
	fresh := aigame.NewBattleView(aigame.Snapshot{Phase: aigame.PhaseBattle, Player: old.Own, Battle: old.Battle, Pets: old.Pets, Magic: old.Magic, Inventory: old.Inventory})
	fresh.CharacterID, fresh.Mode = old.CharacterID, old.Mode
	var view Object
	if err := decode(enc(fresh), &view); err != nil {
		t.Fatal(err)
	}
	view["event_cutoff"] = original["event_cutoff"]
	result, err := teamObservation(Object{"member-0": view}, map[string]bool{fresh.CharacterID: true}, 1)
	if err != nil {
		t.Fatal(err)
	}
	return clone(result)
}

func TestProspectiveNeuralRejectedAttemptDoesNotReplaceExecutedMemory(t *testing.T) {
	ctx := context.Background()
	model, modelPath := neuralFixtureModel(t, 1)
	zero, events := neuralFixtureTeam(t, 1, 0)
	stale := prospectiveRefreshOwnStats(t, zero, false)
	current := prospectiveRefreshOwnStats(t, zero, true)
	// The refresh arrives as another public status event, before any command.
	obj(obj(obj(current["members"])["member-0"])["event_cutoff"])["cursor"] = 52
	events = append(events, clone(Object{"member": "member-0", "stream": "stream-0", "event": aigame.BattleEvent{Sequence: 52, MatchID: "match", Function: "S", Raw: "P"}}))
	if str(stale["observation_id"]) == str(current["observation_id"]) {
		t.Fatal("fixture must have distinct actual observation identities")
	}
	stalePlan, err := model.Decide(ctx, stale, events)
	if err != nil {
		t.Fatal(err)
	}
	freshPlan, err := model.Decide(ctx, current, append(events, neuralTurnRecord(stale, stalePlan)))
	if err != nil || validatePlan(current, freshPlan.Plan) != nil {
		t.Fatal("legitimate unsubmitted refresh must replan", err)
	}
	one, laterEvents := neuralFixtureTeam(t, 1, 1)
	obj(obj(obj(one["members"])["member-0"])["event_cutoff"])["cursor"] = 54
	for _, entry := range laterEvents {
		event := obj(entry["event"])
		event["sequence"] = integer(event["sequence"]) + 1
	}
	cleanHistory := append(append([]Object{}, events...), neuralTurnRecord(current, freshPlan))
	cleanHistory = append(cleanHistory, laterEvents...)
	want, err := model.Decide(ctx, one, cleanHistory)
	if err != nil {
		t.Fatal(err)
	}

	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range append(append([]Object{}, events...), laterEvents...) {
		event := obj(entry["event"])
		store.Ingest("member-0", clone(Object{"stream": "stream-0", "cursor": event["sequence"], "events": []Object{event}}))
	}
	store.Record("turn", neuralTurnRecord(stale, stalePlan), "match", "")
	for _, order := range stalePlan.Plan.Orders {
		selection := Object{"match_id": "match", "turn": 0, "observation_id": obj(obj(stale["members"])[order.Member])["observation_id"], "candidate_id": order.Candidate}
		if !store.Reserve(order.Member, selection, order.Actor) {
			t.Fatal("reserve", store.Err())
		}
		store.Finish(order.Member, selection, order.Actor, Object{"ok": false, "data": Object{"code": "stale_observation"}})
	}
	store.Record("turn", neuralTurnRecord(current, freshPlan), "match", "")
	for _, order := range freshPlan.Plan.Orders {
		selection := Object{"match_id": "match", "turn": 0, "observation_id": obj(obj(current["members"])[order.Member])["observation_id"], "candidate_id": order.Candidate}
		if !store.Reserve(order.Member, selection, order.Actor) {
			t.Fatal("reserve after definite refusal", store.Err())
		}
		store.Finish(order.Member, selection, order.Actor, Object{"ok": true})
	}
	if store.Err() != nil {
		t.Fatal(store.Err())
	}
	directory := store.Directory
	if err := store.DB.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	reloaded, err := NewLearned(modelPath, 1)
	if err != nil {
		t.Fatal(err)
	}
	history := store.History("match")
	if store.Err() != nil {
		t.Fatal(store.Err())
	}
	got, err := reloaded.Decide(ctx, one, history)
	if err != nil {
		t.Fatal("replayed decision failed", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rejected old observation replaced the successful refresh in recurrent memory: got return=%v logp=%v, want return=%v logp=%v; got plan=%v want plan=%v", got.Diagnostics["estimated_return"], got.Diagnostics["policy_log_probability"], want.Diagnostics["estimated_return"], want.Diagnostics["policy_log_probability"], got.Plan, want.Plan)
	}
}
