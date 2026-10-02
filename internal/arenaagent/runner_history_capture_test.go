package arenaagent

import (
	"context"
	"reflect"
	"testing"
)

func TestProspectiveRunnerPersistsCommandHistoryCapture(t *testing.T) {
	r, statuses := timingRunner(t, false)
	r.store.Record("event_gap", Object{"stream": "before", "cursor": 1}, "match", "member-0")
	var captured int
	r.strategy = turnDecisionFunc(func(ctx context.Context, team Object, history []Object) (Decision, error) {
		value, ok := team["history_record_cutoff"]
		captured = integer(value)
		if !ok || captured <= 0 {
			t.Fatal("runner did not capture append-only command boundary")
		}
		original := string(enc(history))
		for _, row := range history {
			if integer(row["record_id"]) > captured {
				t.Fatal("future command leaked into initial capture")
			}
		}
		// This is a local fixture write while inference is running, after capture.
		r.store.Record("event_gap", Object{"stream": "after", "cursor": 2}, "match", "member-0")
		if string(enc(history)) != original {
			t.Fatal("later write mutated the captured history")
		}
		return (Basic{}).Decide(ctx, team, history)
	})
	if e := r.battle(context.Background(), statuses); e != nil {
		t.Fatal(e)
	}
	records, e := readObjects(r.store.DB, "SELECT body FROM records WHERE kind='turn'")
	if e != nil || len(records) != 1 {
		t.Fatal(e, len(records))
	}
	team := obj(records[0]["team"])
	if integer(team["history_record_cutoff"]) != captured {
		t.Fatal("persisted decision lost original cutoff")
	}
	history, latest := r.store.HistorySnapshot("match")
	if latest <= int64(captured) || r.store.Err() != nil {
		t.Fatal("later history not retained", latest, r.store.Err())
	}
	intents, submissions, laterGaps := 0, 0, 0
	var full Plan
	if decode(enc(records[0]["plan"]), &full) != nil {
		t.Fatal("saved plan")
	}
	selections := map[Slot]Object{}
	for _, row := range history {
		id := integer(row["record_id"])
		if receipt := obj(row["submission_intent"]); receipt != nil {
			intents++
			if id <= captured {
				t.Fatal("intent before capture")
			}
			selections[Slot{str(row["member"]), str(receipt["actor"])}] = obj(receipt["selection"])
		}
		if receipt := obj(row["submission"]); receipt != nil {
			submissions++
			if id <= captured || !yes(obj(receipt["response"])["ok"]) {
				t.Fatal("bad real fixture dispatch receipt")
			}
		}
		if gap := obj(row["event_gap"]); gap != nil && str(gap["stream"]) == "after" {
			laterGaps++
			if id <= captured {
				t.Fatal("late gap lost append order")
			}
		}
	}
	if intents != 4 || submissions != 4 || laterGaps != 1 {
		t.Fatal("incomplete actual runner history", intents, submissions, laterGaps)
	}
	for slot, selection := range selections {
		if r.store.Reserve(slot.Member, selection, slot.Actor) {
			t.Fatal("duplicate successful command became retryable")
		}
	}
	again, after := r.store.HistorySnapshot("match")
	if after != latest || !reflect.DeepEqual(again, history) {
		t.Fatal("failed duplicate reservations fabricated intent records")
	}
}
