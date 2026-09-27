package battleauto

import (
	"context"
	"errors"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/ladder"
)

type testStrategy struct {
	info   StrategyInfo
	decide func(context.Context, aigame.Snapshot) (Decision, bool, error)
}

func (s testStrategy) Info() StrategyInfo { return s.info }
func (s testStrategy) Decide(ctx context.Context, snapshot aigame.Snapshot) (Decision, bool, error) {
	return s.decide(ctx, snapshot)
}
func ladderObservation(strategy string) aigame.Snapshot {
	b := readyBattle()
	b.LadderID = "match_a"
	return aigame.Snapshot{Revision: 42, Phase: aigame.PhaseBattle, Battle: b,
		Ladder: &ladder.Envelope{Snapshot: ladder.Snapshot{Phase: "battle",
			Self: ladder.Player{ID: "self", Strategy: strategy}, Match: &ladder.Match{ID: "match_a"}}}}
}

func TestStrategiesRequireCurrentMatchAndExplicitInstalledSelection(t *testing.T) {
	r, err := NewStrategies(nil)
	if err != nil {
		t.Fatal(err)
	}
	s := ladderObservation("basic")
	d, ok, err := r.Decide(context.Background(), s)
	if err != nil || !ok || d.Action.Command != "H|A" {
		t.Fatalf("basic: %+v %t %v", d, ok, err)
	}
	for _, mutate := range []func(*aigame.Snapshot){
		func(s *aigame.Snapshot) { s.Ladder.Snapshot.Self.Strategy = "manual" },
		func(s *aigame.Snapshot) { s.Ladder.Snapshot.Self.Abandoned = true },
		func(s *aigame.Snapshot) { s.Ladder.Snapshot.Match.ID = "old_match" },
		func(s *aigame.Snapshot) { s.Ladder.Snapshot.Phase = "result" },
		func(s *aigame.Snapshot) { s.Battle.Ended = true },
		func(s *aigame.Snapshot) { s.Battle.PlayerSubmitted = true; s.Battle.PetSubmitted = true },
	} {
		s = ladderObservation("basic")
		mutate(&s)
		if d, ok, err := r.Decide(context.Background(), s); err != nil || ok {
			t.Fatalf("unexpected action: %+v %v", d, err)
		}
	}
	s = ladderObservation("uninstalled")
	if _, ok, err := r.Decide(context.Background(), s); ok || err == nil {
		t.Fatal("missing extension silently fell back")
	}
}

func TestStrategyExtensionsCannotCrossWorldOrCancellationBoundary(t *testing.T) {
	for _, kind := range []string{"world", "cancel", "guard"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ext := testStrategy{info: StrategyInfo{ID: "local", Name: "Local", Tier: "advanced"}, decide: func(ctx context.Context, s aigame.Snapshot) (Decision, bool, error) {
				if _, ok := ctx.Deadline(); !ok {
					t.Error("extension lacks a decision deadline")
				}
				if s.Revision != 42 {
					t.Error("extension did not receive the observation")
				}
				a := aigame.Action{Kind: aigame.ActionBattle, Command: "G"}
				if kind == "world" {
					a.Kind = aigame.ActionMove
				}
				if kind == "cancel" {
					cancel()
				}
				return Decision{Action: a}, true, nil
			}}
			r, err := NewStrategies(nil, ext)
			if err != nil {
				t.Fatal(err)
			}
			_, ok, err := r.Decide(ctx, ladderObservation("local"))
			if kind == "guard" && (!ok || err != nil) {
				t.Fatalf("valid extension: %t %v", ok, err)
			}
			if kind != "guard" && (ok || err == nil) {
				t.Fatal("unsafe extension decision accepted")
			}
			if kind == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}

type recordingStrategyGame struct {
	snapshot aigame.Snapshot
	actions  []aigame.Action
	revision uint64
}

func (g *recordingStrategyGame) Observe(context.Context) (aigame.Snapshot, error) {
	return g.snapshot, nil
}
func (g *recordingStrategyGame) ExecuteExpected(_ context.Context, revision uint64, action aigame.Action) error {
	g.actions = append(g.actions, action)
	g.revision = revision
	return nil
}

func TestLadderOnlyRunnerNeverActsInOrdinaryBattle(t *testing.T) {
	g := &recordingStrategyGame{snapshot: ladderObservation("basic")}
	r := Runner{Game: g, LadderOnly: true}
	if err := r.tick(context.Background(), DefaultPolicy()); err != nil {
		t.Fatal(err)
	}
	if len(g.actions) != 1 || g.revision != 42 {
		t.Fatalf("not revision fenced: %+v", g)
	}
	g.snapshot.Battle.LadderID = ""
	if err := r.tick(context.Background(), DefaultPolicy()); err != nil {
		t.Fatal(err)
	}
	if len(g.actions) != 1 {
		t.Fatal("ladder loop acted in ordinary battle")
	}
}

func TestLadderCountersWaitForAuthoritativeResultAndCountOnce(t *testing.T) {
	for _, winner := range []int{-1, 0, 1} {
		s := ladderObservation("basic")
		l := &live{}
		s.Battle.Participants[0].Dead = true
		s.Battle.Participants[1].Dead = true
		l.observe(s)
		s.Battle.Active = false
		s.Battle.Result = "ordinary RS"
		l.observe(s)
		if l.wins != 0 || l.losses != 0 {
			t.Fatal("settled from ordinary battle state")
		}
		s.Ladder.Snapshot.Result = &ladder.Result{ID: "match_a", Rated: winner >= 0, WinnerSide: winner, Members: []ladder.ResultPlayer{{Player: ladder.Player{ID: "self"}, Side: 0}}}
		l.observe(s)
		l.observe(s)
		wins, losses := 0, 0
		if winner == 0 {
			wins = 1
		}
		if winner == 1 {
			losses = 1
		}
		if l.battles != 1 || l.wins != wins || l.losses != losses {
			t.Fatalf("winner=%d totals=%+v", winner, l)
		}
	}
}
