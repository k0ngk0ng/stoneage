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
		if !s.stopAutoBattle() {
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
	s.autoMu.Lock()
	defer s.autoMu.Unlock()
	if s.autoRunning {
		return false, nil
	}
	tables, err := s.recoveryTables()
	if err != nil {
		return false, err
	}
	// The loop outlives the request, so it must not inherit the request
	// context: handleConn cancels that when the response is written.
	ctx, cancel := context.WithCancel(context.Background())
	s.autoCancel = cancel
	s.autoRunning = true
	go func() {
		policy := battleauto.DefaultPolicy()
		policy.SeekEncounters = walk
		runner := battleauto.Runner{
			Game:   sessionGame{server: s},
			Tables: tables,
			Policy: policy,
			Log: func(format string, args ...any) {
				s.autoMu.Lock()
				s.autoLastLine = fmt.Sprintf(format, args...)
				s.autoMu.Unlock()
			},
			State: func(state battleauto.State) {
				s.autoMu.Lock()
				s.autoState = state
				s.autoStateKept = true
				s.autoMu.Unlock()
			},
		}
		_ = runner.Run(ctx)
		s.autoMu.Lock()
		s.autoRunning = false
		s.autoCancel = nil
		s.autoStateKept = false
		s.autoMu.Unlock()
	}()
	return true, nil
}

// stopAutoBattle cancels the loop and reports whether one was running.
func (s *Server) stopAutoBattle() bool {
	s.autoMu.Lock()
	cancel := s.autoCancel
	running := s.autoRunning
	s.autoRunning = false
	s.autoCancel = nil
	s.autoMu.Unlock()
	if cancel != nil {
		cancel()
	}
	return running
}

func (s *Server) autoBattleStatus() string {
	s.autoMu.Lock()
	defer s.autoMu.Unlock()
	if !s.autoRunning {
		return "auto battle: off"
	}
	status := "auto battle: on"
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
