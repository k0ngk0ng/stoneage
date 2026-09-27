package sacli

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/battleauto"
	"github.com/k0ngk0ng/stoneage/internal/ladder"
)

type ladderAutoGame struct {
	Game
	mu            sync.Mutex
	state         ladder.Snapshot
	battle        aigame.BattleSnapshot
	actions       chan aigame.Action
	beforeObserve func()
}

func (g *ladderAutoGame) Observe(context.Context) (aigame.Snapshot, error) {
	if g.beforeObserve != nil {
		g.beforeObserve()
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	e := ladder.Envelope{Revision: 10, Snapshot: g.state}
	e = e.Clone()
	return aigame.Snapshot{Revision: 10, Phase: aigame.PhaseBattle, Battle: g.battle, Ladder: &e}, nil
}
func (g *ladderAutoGame) RequestLadder(_ context.Context, r ladder.Request) (ladder.Envelope, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch r.Operation {
	case "strategy":
		g.state.Self.Strategy = r.Argument
	case "ready":
		g.state.Self.Ready = true
	case "unready":
		g.state.Self.Ready = false
	case "ack":
		g.state.Phase = "lobby"
		g.state.Self.Ready = false
	}
	e := ladder.Envelope{RequestID: r.ID, OK: true, Revision: 10, Snapshot: g.state}
	return e.Clone(), nil
}
func (g *ladderAutoGame) ExecuteExpected(_ context.Context, _ uint64, a aigame.Action) error {
	g.mu.Lock()
	g.battle.CommandReady = false
	g.mu.Unlock()
	g.actions <- a
	return nil
}
func (g *ladderAutoGame) set(phase, strategy string, ready bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.state = ladder.Snapshot{Phase: phase, Self: ladder.Player{Strategy: strategy, Ready: ready}}
}
func assertLadderLoop(t *testing.T, s *Server, running bool) uint64 {
	t.Helper()
	s.autoMu.Lock()
	defer s.autoMu.Unlock()
	if s.autoRunning != running || (running && (!s.autoLadderOnly || s.autoWalk)) {
		t.Fatalf("loop running=%t ladderOnly=%t walk=%t", s.autoRunning, s.autoLadderOnly, s.autoWalk)
	}
	return s.autoGeneration
}

type cliLocalLadderStrategy struct{ tier string }

func (s cliLocalLadderStrategy) Info() battleauto.StrategyInfo {
	return battleauto.StrategyInfo{ID: "local_guard", Name: "Local guard", Tier: s.tier}
}

func (s cliLocalLadderStrategy) Decide(ctx context.Context, snapshot aigame.Snapshot) (battleauto.Decision, bool, error) {
	if err := ctx.Err(); err != nil {
		return battleauto.Decision{}, false, err
	}
	return battleauto.Decision{Action: aigame.Action{Kind: aigame.ActionBattle, Command: "G"}}, snapshot.Battle.CommandReady, nil
}

func TestLocalLadderStrategyDaemonHostAndManualTakeover(t *testing.T) {
	for _, tier := range []string{"intermediate", "advanced"} {
		t.Run(tier, func(t *testing.T) {
			g := &ladderAutoGame{actions: make(chan aigame.Action, 4)}
			g.set("battle", "manual", false)
			g.state.Match = &ladder.Match{ID: "local_match"}
			g.battle = aigame.BattleSnapshot{Active: true, CommandReady: true, LadderID: "local_match"}
			s := &Server{game: g, config: Config{LadderStrategies: []battleauto.Strategy{cliLocalLadderStrategy{tier: tier}}}}
			t.Cleanup(func() { s.stopAutoBattle() })
			if reply := s.commandLadder(context.Background(), Request{Args: []string{"strategy", "local_guard"}}); !reply.OK {
				t.Fatal(reply)
			}
			assertLadderLoop(t, s, true)
			select {
			case action := <-g.actions:
				if action.Kind != aigame.ActionBattle || action.Command != "G" {
					t.Fatalf("extension emitted %+v", action)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("daemon did not run registered extension")
			}
			if reply := s.commandLadder(context.Background(), Request{Args: []string{"strategy", "manual"}}); !reply.OK {
				t.Fatal(reply)
			}
			assertLadderLoop(t, s, false)
			g.mu.Lock()
			g.battle.CommandReady = true
			g.mu.Unlock()
			select {
			case action := <-g.actions:
				t.Fatalf("extension continued after takeover: %+v", action)
			case <-time.After(250 * time.Millisecond):
			}
		})
	}
}

func TestLadderAutoLaunchStopAndRestoreFromCurrentSession(t *testing.T) {
	g := &ladderAutoGame{actions: make(chan aigame.Action, 4)}
	g.set("lobby", "basic", false)
	s := &Server{game: g}
	t.Cleanup(func() { s.stopAutoBattle() })
	s.ensureLadderBattle(g)
	assertLadderLoop(t, s, false)
	if r := s.commandLadder(context.Background(), Request{Args: []string{"ready"}}); !r.OK {
		t.Fatal(r)
	}
	generation := assertLadderLoop(t, s, true)
	s.ensureLadderBattle(g)
	if assertLadderLoop(t, s, true) != generation {
		t.Fatal("duplicate loop on repeated event")
	}
	if r := s.commandLadder(context.Background(), Request{Args: []string{"strategy", "manual"}}); !r.OK {
		t.Fatal(r)
	}
	assertLadderLoop(t, s, false)
	s.ensureLadderBattle(g)
	assertLadderLoop(t, s, false)
	g.set("battle", "basic", false)
	s.ensureLadderBattle(g)
	assertLadderLoop(t, s, true)
	if r := s.commandAutoBattle(context.Background(), Request{Args: []string{"off"}}); !r.OK {
		t.Fatal(r)
	}
	assertLadderLoop(t, s, false)
	g.mu.Lock()
	strategy := g.state.Self.Strategy
	g.mu.Unlock()
	if strategy != "manual" {
		t.Fatal("off did not persist manual selection")
	}
	if r := s.commandLadder(context.Background(), Request{Args: []string{"strategy", "basic"}}); !r.OK {
		t.Fatal(r)
	}
	assertLadderLoop(t, s, true)
	g.set("result", "basic", true)
	s.ensureLadderBattle(g)
	assertLadderLoop(t, s, false)
	// A newly authenticated connection restores the selected strategy even
	// when no daemon-local ready command preceded the battle.
	next := &ladderAutoGame{actions: make(chan aigame.Action, 4)}
	next.set("battle", "basic", false)
	s.mu.Lock()
	s.game = next
	s.mu.Unlock()
	g.set("battle", "basic", true)
	s.ensureLadderBattle(g)
	assertLadderLoop(t, s, false)
	s.ensureLadderBattle(next)
	assertLadderLoop(t, s, true)
	select {
	case a := <-g.actions:
		t.Fatalf("ordinary action while no battle projection: %+v", a)
	default:
	}
}

func TestLadderAutoPendingObservationCannotUndoOff(t *testing.T) {
	observing, release := make(chan struct{}), make(chan struct{})
	g := &ladderAutoGame{beforeObserve: func() { close(observing); <-release }}
	g.set("battle", "basic", true)
	s := &Server{game: g}
	t.Cleanup(func() { s.stopAutoBattle() })
	done := make(chan struct{})
	go func() { s.ensureLadderBattle(g); close(done) }()
	<-observing
	s.autoMu.Lock()
	s.autoLadderSuppressed = true
	s.autoMu.Unlock()
	s.stopAutoBattle()
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("observation did not return")
	}
	assertLadderLoop(t, s, false)
}
