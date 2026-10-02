package arenaagent

import (
	"context"
	"reflect"
	"testing"
)

func TestCapturedHistoryExcludesFutureCurrentTurnPlan(t *testing.T) {
	f := prospectiveHistory(t)
	current := clone(f.fresh)
	history, cutoff := f.store.HistorySnapshot("match")
	current["history_record_cutoff"] = cutoff
	expected, err := f.model.Decide(context.Background(), current, history)
	if err != nil {
		t.Fatal(err)
	}
	if yes(expected.Diagnostics["reused_plan"]) {
		t.Fatal("fixture unexpectedly reused a plan")
	}
	// This command record was written AFTER current captured its history.
	f.record(current, expected)
	latest := f.store.History("match")
	actual, err := f.model.Decide(context.Background(), current, latest)
	if err != nil || !reflect.DeepEqual(actual, expected) {
		t.Fatal("future current-turn decision crossed the saved command cutoff", err, actual.Diagnostics, expected.Diagnostics)
	}
	// A later real poll has a newer capture and must still reuse the final plan.
	_, after := f.store.HistorySnapshot("match")
	current["history_record_cutoff"] = after
	reused, err := f.model.Decide(context.Background(), current, latest)
	if err != nil || !yes(reused.Diagnostics["reused_plan"]) || !reflect.DeepEqual(reused.Plan, expected.Plan) {
		t.Fatal("known current-turn final plan was not reused", err, reused)
	}
}
