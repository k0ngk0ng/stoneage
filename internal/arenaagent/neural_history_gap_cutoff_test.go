package arenaagent

import (
	"context"
	"reflect"
	"testing"
)

func TestProspectiveRecordedCutoffExcludesLaterGap(t *testing.T) {
	f := prospectiveHistory(t)
	f.record(f.fresh, f.freshDecision)
	before, cutoff := f.store.HistorySnapshot("match")
	current := clone(f.next)
	current["history_record_cutoff"] = cutoff
	want, err := f.model.Decide(context.Background(), current, before)
	if err != nil {
		t.Fatal("causal initial control", err)
	}
	// The fact that a later retrieval reports a gap must not be injected into a
	// previously captured decision, even if it refers to an old event cursor.
	f.store.Record("event_gap", Object{"stream": "stream-0", "cursor": 53}, "match", "member-0")
	after := f.store.History("match")
	got, err := f.model.Decide(context.Background(), current, after)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("future gap changed an earlier captured decision: %v", err)
	}
}

func TestProspectiveKnownGapAndMalformedCutoffsRemainRejected(t *testing.T) {
	for _, kind := range []string{"known_gap", "missing_gap_id", "negative_cutoff", "string_cutoff", "fractional_cutoff", "null_cutoff"} {
		t.Run(kind, func(t *testing.T) {
			f := prospectiveHistory(t)
			f.record(f.fresh, f.freshDecision)
			if kind == "known_gap" || kind == "missing_gap_id" {
				f.store.Record("event_gap", Object{"stream": "stream-0", "cursor": 53}, "match", "member-0")
			}
			history, cutoff := f.store.HistorySnapshot("match")
			current := clone(f.next)
			current["history_record_cutoff"] = cutoff
			switch kind {
			case "missing_gap_id":
				for _, entry := range history {
					if obj(entry["event_gap"]) != nil {
						delete(entry, "record_id")
					}
				}
			case "negative_cutoff":
				current["history_record_cutoff"] = -1
			case "string_cutoff":
				current["history_record_cutoff"] = "4"
			case "fractional_cutoff":
				current["history_record_cutoff"] = .5
			case "null_cutoff":
				current["history_record_cutoff"] = nil
			}
			_, err := f.model.Decide(context.Background(), current, history)
			if err == nil {
				t.Fatal("invalid/incomplete history accepted", kind)
			}
		})
	}
}
