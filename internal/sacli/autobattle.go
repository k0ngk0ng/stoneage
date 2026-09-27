package sacli

import (
	"context"
	"fmt"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/battleauto"
)

// sessionGame adapts the daemon to the battle loop. Every call re-acquires the
// session, so the loop rides the daemon's lazy reconnect instead of holding a
// socket that a reconnect would replace.
type sessionGame struct {
	server *Server
}

func (g sessionGame) Observe(ctx context.Context) (aigame.Snapshot, error) {
	return g.server.snapshot(ctx)
}

func (g sessionGame) ExecuteExpected(ctx context.Context, revision uint64, action aigame.Action) error {
	return g.server.submit(ctx, revision, action)
}

// commandAutoBattle turns the looping battle policy on or off. The loop runs
// in this daemon rather than in the command, because a command's context is
// cancelled as soon as its response is written.
func (s *Server) commandAutoBattle(ctx context.Context, request Request) Response {
	if len(request.Args) == 0 {
		return failure(KindUsage, "usage: auto-battle on [walk|stay]|off|status")
	}
	switch strings.ToLower(strings.TrimSpace(request.Args[0])) {
	case "on":
		walk, err := autoBattleWalk(request.Args[1:])
		if err != nil {
			return failure(KindUsage, "%v", err)
		}
		changed, startErr := s.startAutoBattle(walk)
		if startErr != nil {
			return actionFailure(startErr)
		}
		if !changed {
			return Response{OK: true, Text: "auto battle is already on"}
		}
		if walk {
			return Response{
				OK:   true,
				Text: "auto battle on: the character heals whoever is hurt most, otherwise attacks the first living enemy, and never runs away.\nbetween fights it walks a short back-and-forth pattern so the server keeps rolling encounters.\nstop it with `sactl auto-battle off`",
			}
		}
		return Response{
			OK:   true,
			Text: "auto battle on: the character heals whoever is hurt most, otherwise attacks the first living enemy, and never runs away.\nit answers battles it is in and walks nowhere; add `walk` to look for fights.",
		}
	case "off":
		s.autoMu.Lock()
		s.autoLadderSuppressed = true
		s.autoMu.Unlock()
		stopped := s.stopAutoBattle()
		s.mu.Lock()
		game := s.game
		s.mu.Unlock()
		if game != nil {
			observed, err := game.Observe(ctx)
			if err == nil && observed.Ladder != nil && observed.Ladder.Snapshot.ReservesCharacter() && observed.Ladder.Snapshot.Self.Strategy != "manual" {
				return s.commandLadder(ctx, Request{Args: []string{"strategy", "manual"}})
			}
		}
		if !stopped {
			return Response{OK: true, Text: "auto battle is already off"}
		}
		return Response{OK: true, Text: "auto battle off"}
	case "status":
		return Response{OK: true, Text: s.autoBattleStatus()}
	default:
		return failure(KindUsage, "usage: auto-battle on [walk|stay]|off|status")
	}
}

// autoBattleWalk reads the optional walk opt-in. Answering turns is the mode's
// whole definition; walking after a fight is a choice, because a caller may
// have parked the character deliberately.
func autoBattleWalk(args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "walk", "seek", "encounters":
		return true, nil
	case "stay", "hold":
		return false, nil
	default:
		return false, fmt.Errorf("unknown auto battle option %q (use walk or stay)", args[0])
	}
}

func (s *Server) startAutoBattle(walk bool) (bool, error) {
	return s.startBattleLoop(walk, false)
}

func (s *Server) startBattleLoop(walk, ladderOnly bool) (bool, error) {
	s.autoMu.Lock()
	defer s.autoMu.Unlock()
	return s.startBattleLoopLocked(walk, ladderOnly)
}

func (s *Server) startBattleLoopLocked(walk, ladderOnly bool) (bool, error) {
	if ladderOnly && s.autoLadderSuppressed {
		return false, nil
	}
	if s.isStopping() {
		return false, fmt.Errorf("daemon is stopping")
	}
	if s.autoRunning {
		if ladderOnly || !s.autoLadderOnly {
			return false, nil
		}
	}
	tables, err := s.recoveryTables()
	if err != nil && !ladderOnly {
		return false, err
	}
	strategies, err := battleauto.NewStrategies(tables, s.config.LadderStrategies...)
	if err != nil {
		return false, err
	}
	if s.autoRunning {
		s.autoCancel()
	}
	// The loop outlives the request, so it must not inherit the request
	// context: handleConn cancels that when the response is written.
	ctx, cancel := context.WithCancel(context.Background())
	s.autoCancel = cancel
	s.autoRunning = true
	s.autoStateKept = false
	s.autoLastLine = ""
	s.autoWalk, s.autoLadderOnly = walk, ladderOnly
	s.autoGeneration++
	generation := s.autoGeneration
	go func() {
		policy := battleauto.DefaultPolicy()
		policy.SeekEncounters = walk
		runner := battleauto.Runner{
			Game:       sessionGame{server: s},
			Tables:     tables,
			Policy:     policy,
			LadderOnly: ladderOnly,
			Strategies: strategies,
			Log: func(format string, args ...any) {
				s.autoMu.Lock()
				if s.autoGeneration == generation {
					s.autoLastLine = fmt.Sprintf(format, args...)
				}
				s.autoMu.Unlock()
			},
			State: func(state battleauto.State) {
				s.autoMu.Lock()
				if s.autoGeneration == generation {
					s.autoState = state
					s.autoStateKept = true
				}
				s.autoMu.Unlock()
			},
		}
		_ = runner.Run(ctx)
		s.autoMu.Lock()
		if s.autoGeneration == generation {
			s.autoRunning = false
			s.autoCancel = nil
			s.autoStateKept = false
		}
		s.autoMu.Unlock()
	}()
	return true, nil
}

// stopAutoBattle cancels the loop and reports whether one was running.
func (s *Server) stopAutoBattle() bool {
	s.autoMu.Lock()
	defer s.autoMu.Unlock()
	return s.stopAutoBattleLocked()
}

func (s *Server) stopAutoBattleLocked() bool {
	running := s.autoRunning
	if s.autoCancel != nil {
		s.autoCancel()
	}
	s.autoRunning = false
	s.autoCancel = nil
	s.autoStateKept = false
	s.autoGeneration++
	return running
}

// A received ladder snapshot restores the default policy after login or
// reconnect. Only this daemon runs it; its HTTP bridge never starts another
// policy merely because it observed a raw ladder packet.
func (s *Server) ensureLadderBattle(game Game) {
	snapshot, err := game.Observe(context.Background())
	if err != nil || snapshot.Ladder == nil {
		return
	}
	state := snapshot.Ladder.Snapshot
	registry, err := battleauto.NewStrategies(nil, s.config.LadderStrategies...)
	s.autoMu.Lock()
	defer s.autoMu.Unlock()
	s.mu.Lock()
	current := s.game == game && !s.stopping
	s.mu.Unlock()
	if !current {
		return
	}
	eligible := state.AutoBattleEligible()
	if s.autoRunning && s.autoLadderOnly && (!eligible || state.Self.Strategy == "manual") {
		s.stopAutoBattleLocked()
	}
	if err != nil || !eligible || !registry.Has(state.Self.Strategy) {
		return
	}
	_, _ = s.startBattleLoopLocked(false, true)
}

func (s *Server) autoBattleStatus() string {
	s.autoMu.Lock()
	defer s.autoMu.Unlock()
	if !s.autoRunning {
		return "auto battle: off"
	}
	status := "auto battle: on"
	if s.autoLadderOnly {
		status += " (ladder only)"
	}
	if s.autoStateKept {
		status += "\n" + describeAutoState(s.autoState)
	}
	if s.autoLastLine != "" {
		status += "\nlast action: " + s.autoLastLine
	}
	return status
}

// describeAutoState is the line `auto-battle status` shows for a running loop:
// what it is doing now, and how the fights have gone.
func describeAutoState(state battleauto.State) string {
	doing := "idle"
	switch {
	case state.InBattle:
		doing = fmt.Sprintf("in battle (turn %d, %d enemies left)", state.Turn, state.Enemies)
	case state.Seeking && state.Blocked == "walk":
		/* Every direction is refused: the character is standing somewhere it
		   cannot step off, and no amount of waiting will change that. */
		doing = "cannot walk (every direction is refused; step the character somewhere else)"
	case state.Seeking:
		doing = "looking for a fight (walking in place)"
	}
	return fmt.Sprintf("%s\nfights %d (won %d, lost %d)", doing, state.Battles, state.Wins, state.Losses)
}

// recoveryTables loads the game's own recovery tables once. They are static
// data, so a daemon keeps them for its lifetime.
func (s *Server) recoveryTables() (*aiknowledge.RecoveryTables, error) {
	s.recoveryOnce.Do(func() {
		directory := strings.TrimSpace(s.config.MapDirectory)
		if directory == "" {
			s.recoveryErr = fmt.Errorf("auto battle needs map_directory to point at the server data directory")
			return
		}
		s.recoveryData, s.recoveryErr = aiknowledge.LoadRecoveryTables(directory)
	})
	return s.recoveryData, s.recoveryErr
}
