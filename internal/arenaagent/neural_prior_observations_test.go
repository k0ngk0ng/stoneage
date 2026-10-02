package arenaagent

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type prospectiveHistoryFixture struct {
	store                        *Store
	model                        *Learned
	stale, fresh, next           Object
	staleDecision, freshDecision Decision
	events                       []Object
}

func prospectiveHistory(t *testing.T) prospectiveHistoryFixture {
	t.Helper()
	model, _ := neuralFixtureModel(t, 1)
	zero, events := neuralFixtureTeam(t, 1, 0)
	stale, fresh := prospectiveRefreshOwnStats(t, zero, false), prospectiveRefreshOwnStats(t, zero, true)
	staleDecision, e := model.Decide(context.Background(), stale, events)
	if e != nil {
		t.Fatal(e)
	}
	freshDecision, e := model.Decide(context.Background(), fresh, append(events, neuralTurnRecord(stale, staleDecision)))
	if e != nil {
		t.Fatal(e)
	}
	next, later := neuralFixtureTeam(t, 1, 1)
	store, e := OpenStore(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { store.DB.Close() })
	for _, entry := range append(append([]Object{}, events...), later...) {
		event := obj(entry["event"])
		store.Ingest("member-0", clone(Object{"stream": "stream-0", "cursor": event["sequence"], "events": []Object{event}}))
	}
	return prospectiveHistoryFixture{store, model, stale, fresh, next, staleDecision, freshDecision, events}
}
func prospectiveOrder(t *testing.T, team Object, d Decision, actor string) Object {
	t.Helper()
	for _, o := range d.Plan.Orders {
		if o.Actor == actor {
			return Object{"match_id": "match", "turn": 0, "observation_id": obj(obj(team["members"])[o.Member])["observation_id"], "candidate_id": o.Candidate}
		}
	}
	t.Fatal("missing order", actor)
	return nil
}
func (f prospectiveHistoryFixture) record(team Object, d Decision) {
	f.store.Record("turn", neuralTurnRecord(team, d), "match", "")
}
func (f prospectiveHistoryFixture) submit(t *testing.T, team Object, d Decision, actor, status string) {
	t.Helper()
	selection := prospectiveOrder(t, team, d, actor)
	if !f.store.Reserve("member-0", selection, actor) {
		t.Fatal("reserve", f.store.Err())
	}
	if status == "uncertain" {
		return
	}
	response := Object{"ok": true}
	if status != "written" {
		response = Object{"ok": false, "data": Object{"code": status}}
	}
	f.store.Finish("member-0", selection, actor, response)
	if f.store.Err() != nil {
		t.Fatal(f.store.Err())
	}
}
func prospectiveExpectFrame(t *testing.T, f prospectiveHistoryFixture, current Object, want Object) {
	t.Helper()
	h := f.store.History("match")
	if f.store.Err() != nil {
		t.Fatal(f.store.Err())
	}
	got, e := neuralPriorObservations(current, h)
	if e != nil || !reflect.DeepEqual(got[0], want) {
		t.Fatal("reconstructed wrong original frame", e)
	}
	clean := []Object{}
	for _, entry := range h {
		if obj(entry["team"]) == nil && obj(entry["submission"]) == nil && obj(entry["submission_intent"]) == nil {
			clean = append(clean, entry)
		}
	}
	// Continuous-control inference contains exactly the selected full frame.
	d := f.freshDecision
	if str(want["observation_id"]) == str(f.stale["observation_id"]) {
		d = f.staleDecision
	}
	clean = append(clean, neuralTurnRecord(want, d))
	control := clone(current)
	delete(control, "history_record_cutoff")
	expected, e := f.model.Decide(context.Background(), control, clean)
	if e != nil {
		t.Fatal(e)
	}
	actual, e := f.model.Decide(context.Background(), current, h)
	if e != nil || !reflect.DeepEqual(actual, expected) {
		t.Fatal("continuous/recovered numerical decisions differ", e, actual.Diagnostics, expected.Diagnostics)
	}
}

func TestProspectiveHistoryPartialReusePreservesFullOriginal(t *testing.T) {
	f := prospectiveHistory(t)
	f.record(f.stale, f.staleDecision)
	f.submit(t, f.stale, f.staleDecision, "player", "stale_observation")
	f.record(f.fresh, f.freshDecision)
	f.submit(t, f.fresh, f.freshDecision, "player", "written")
	partial := clone(f.fresh)
	member := obj(obj(partial["members"])["member-0"])
	member["reserved_actors"] = Object{"player": "written"}
	obj(member["battle"])["PlayerSubmitted"] = true
	remaining, e := f.model.Decide(context.Background(), partial, f.store.History("match"))
	if e != nil || !yes(remaining.Diagnostics["reused_plan"]) || len(remaining.Plan.Orders) != 1 {
		t.Fatal("partial-plan reuse control", e, remaining)
	}
	f.record(partial, remaining)
	f.submit(t, partial, remaining, "pet", "written")
	prospectiveExpectFrame(t, f, f.next, f.fresh)
}

func TestProspectiveHistoryOrderingCutoffAndUncertainIntent(t *testing.T) {
	f := prospectiveHistory(t)
	f.record(f.stale, f.staleDecision)
	f.submit(t, f.stale, f.staleDecision, "player", "stale_observation")
	f.record(f.fresh, f.freshDecision)
	// A crash after Reserve must preserve uncertain intent, without Finish.
	f.submit(t, f.fresh, f.freshDecision, "player", "uncertain")
	before, cutoff := f.store.HistorySnapshot("match")
	if f.store.Err() != nil || cutoff <= 0 {
		t.Fatal(f.store.Err())
	}
	intents := 0
	for _, r := range before {
		if obj(r["submission_intent"]) != nil {
			intents++
		}
	}
	if intents != 2 {
		t.Fatal("lost reservation history", intents)
	}
	bounded := clone(f.next)
	bounded["history_record_cutoff"] = cutoff
	// Later records from a replay must not replace the old captured history.
	f.record(f.stale, f.staleDecision)
	f.submit(t, f.stale, f.staleDecision, "pet", "written")
	if _, e := f.store.DB.Exec("UPDATE records SET at_ms=100000-id"); e != nil {
		t.Fatal(e)
	}
	prospectiveExpectFrame(t, f, bounded, f.fresh)
	_, e := neuralPriorObservations(f.next, f.store.History("match"))
	var failure *neuralFailure
	if !errors.As(e, &failure) || failure.code != "mixed_executed_observations" {
		t.Fatal("unbounded conflicting execution not rejected", e)
	}
	again, _ := f.store.HistorySnapshot("match")
	if len(again) <= len(before) {
		t.Fatal("fixture did not add future records")
	}
}

func TestProspectiveHistoryRefusesAmbiguousOrConflictingEvidence(t *testing.T) {
	for _, kind := range []string{"no_receipts", "mixed_origins", "orphan", "rejection_after_written", "overlapping_uncertain", "missing_full_reuse", "duplicate_record_id", "missing_captured_id"} {
		t.Run(kind, func(t *testing.T) {
			f := prospectiveHistory(t)
			expected := "ambiguous_history"
			switch kind {
			case "no_receipts":
				f.record(f.stale, f.staleDecision)
				f.record(f.fresh, f.freshDecision)
			case "mixed_origins":
				f.record(f.stale, f.staleDecision)
				f.submit(t, f.stale, f.staleDecision, "player", "written")
				f.record(f.fresh, f.freshDecision)
				f.submit(t, f.fresh, f.freshDecision, "pet", "written")
				expected = "mixed_executed_observations"
			case "orphan":
				f.submit(t, f.fresh, f.freshDecision, "player", "uncertain")
				expected = "orphan_submission"
			case "rejection_after_written":
				f.record(f.fresh, f.freshDecision)
				f.submit(t, f.fresh, f.freshDecision, "player", "written")
				f.store.Finish("member-0", prospectiveOrder(t, f.fresh, f.freshDecision, "player"), "player", Object{"ok": false, "data": Object{"code": "stale_observation"}})
				expected = "conflicting_submission"
			case "overlapping_uncertain":
				f.record(f.stale, f.staleDecision)
				f.submit(t, f.stale, f.staleDecision, "player", "uncertain")
				f.record(f.fresh, f.freshDecision)
				// Inject contradictory receipt to test that it cannot erase the old intent.
				f.store.Record("submission", Object{"actor": "player", "selection": prospectiveOrder(t, f.fresh, f.freshDecision, "player"), "response": Object{"ok": false, "data": Object{"code": "stale_observation"}}}, "match", "member-0")
				expected = "conflicting_submission"
			case "missing_full_reuse":
				d := f.freshDecision
				d.Diagnostics = Object{"reused_plan": true}
				f.record(f.fresh, d)
				f.submit(t, f.fresh, d, "player", "written")
				expected = "missing_full_observation"
			case "duplicate_record_id", "missing_captured_id":
				f.record(f.fresh, f.freshDecision)
			}
			h := f.store.History("match")
			current := clone(f.next)
			if kind == "duplicate_record_id" {
				for _, r := range h {
					if obj(r["team"]) != nil {
						h = append(h, r)
						break
					}
				}
				expected = "duplicate_history_record"
			}
			if kind == "missing_captured_id" {
				current["history_record_cutoff"] = 100000
				for _, r := range h {
					delete(r, "record_id")
				}
				expected = "missing_history_order"
			}
			_, e := neuralPriorObservations(current, h)
			var failure *neuralFailure
			if !errors.As(e, &failure) || failure.code != expected {
				t.Fatal("missing precise refusal", expected, e)
			}
		})
	}
}

func TestProspectiveHistorySingleLegacyFrameRemainsUsable(t *testing.T) {
	f := prospectiveHistory(t)
	// Old in-memory history has no record IDs or command receipts, but only one
	// original frame; recurrence needs no assumption about command success.
	h := append(f.events, neuralTurnRecord(f.fresh, f.freshDecision))
	frames, e := neuralPriorObservations(f.next, h)
	if e != nil || !reflect.DeepEqual(frames[0], f.fresh) {
		t.Fatal("unique legacy observation rejected", e)
	}
}
