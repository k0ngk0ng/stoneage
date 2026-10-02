package arenaagent

import (
	"context"
	"errors"
	"testing"
	"time"
)

type turnDecisionFunc func(context.Context, Object, []Object) (Decision, error)

func (turnDecisionFunc) ID() string      { return "fixture-policy" }
func (turnDecisionFunc) Version() string { return "fixture-policy-v1" }
func (f turnDecisionFunc) Decide(ctx context.Context, team Object, history []Object) (Decision, error) {
	return f(ctx, team, history)
}

func TestCommanderCancellationDoesNotBecomeFallback(t *testing.T) {
	for _, timing := range []string{"before", "during-error", "during-success"} {
		t.Run(timing, func(t *testing.T) {
			store, err := OpenStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.DB.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			team := fixture(t, 2)
			called := false
			r := &Runner{store: store, strategy: turnDecisionFunc(func(_ context.Context, team Object, history []Object) (Decision, error) {
				called = true
				plan, err := (Basic{}).Decide(context.Background(), team, history)
				if err != nil {
					t.Fatal(err)
				}
				cancel()
				if timing == "during-error" {
					return Decision{}, context.Canceled
				}
				return plan, nil
			})}
			if timing == "before" {
				cancel()
			}
			decision, err := r.decideForTurn(ctx, ctx, team, nil)
			if !errors.Is(err, context.Canceled) || len(decision.Plan.Orders) != 0 || called != (timing != "before") {
				t.Fatal("stopped commander returned a plan or invoked a new decision", decision, called, err)
			}
			var records int
			if err := store.DB.QueryRow("SELECT count(*) FROM records").Scan(&records); err != nil || records != 0 {
				t.Fatal("shutdown was recorded as a strategy failure", records, err)
			}
		})
	}
}

func TestStrategyDeadlineStillFallsBackWithLiveCommander(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	ctx := context.Background()
	decisionCtx, cancel := context.WithDeadline(ctx, time.Time{})
	defer cancel()
	team := fixture(t, 2)
	r := &Runner{store: store, strategy: turnDecisionFunc(func(ctx context.Context, _ Object, _ []Object) (Decision, error) {
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Fatal("fixture did not expire only the strategy deadline")
		}
		return Decision{}, ctx.Err()
	})}
	decision, err := r.decideForTurn(ctx, decisionCtx, team, nil)
	if err != nil || decision.Strategy != "basic" || validatePlan(team, decision.Plan) != nil {
		t.Fatal("strategy timeout disabled the authorized fallback", decision, err)
	}
	fallbacks, err := readObjects(store.DB, "SELECT body FROM records WHERE kind='strategy_fallback'")
	if err != nil || len(fallbacks) != 1 || str(fallbacks[0]["kind"]) != "decision_failed" {
		t.Fatal("missing strategy failure record", fallbacks, err)
	}
}
