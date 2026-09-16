package aiservice

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

// Record only observations already requested by gameplay. Credentials, names,
// raw packets and account/session configuration are never included.
type travelDiagnosticSession struct {
	GameSession
	mu          sync.Mutex
	rows        []json.RawMessage
	last        string
	seenBattle  bool
	actions     map[aigame.ActionKind]int
	writeErrors []string
}

func (s *travelDiagnosticSession) ExecuteExpected(ctx context.Context, revision uint64, action aigame.Action) error {
	err := s.GameSession.ExecuteExpected(ctx, revision, action)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.actions == nil {
		s.actions = map[aigame.ActionKind]int{}
	}
	if err == nil {
		s.actions[action.Kind]++
	} else if len(s.writeErrors) < 64 {
		category := "execution_error"
		if errors.Is(err, aigame.ErrStaleRevision) {
			category = "stale_revision"
		}
		s.writeErrors = append(s.writeErrors, string(action.Kind)+":"+category)
	}
	return err
}

func (s *travelDiagnosticSession) WaitForMapEvent(ctx context.Context, sequence int32) (aigame.Event, error) {
	acker, ok := s.GameSession.(interface {
		WaitForMapEvent(context.Context, int32) (aigame.Event, error)
	})
	if !ok {
		return aigame.Event{}, errors.New("diagnostic session requires map event acknowledgements")
	}
	return acker.WaitForMapEvent(ctx, sequence)
}

func (s *travelDiagnosticSession) Observe(ctx context.Context) (aigame.Snapshot, error) {
	o, err := s.GameSession.Observe(ctx)
	if err != nil {
		return o, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !o.Battle.Active && !s.seenBattle {
		return o, nil
	}
	s.seenBattle = o.Battle.Active
	actors := make([]map[string]any, 0, len(o.Battle.Participants))
	for _, a := range o.Battle.Participants {
		actors = append(actors, map[string]any{"battle_id": a.BattleID, "level": a.Level, "hp": a.HP, "max_hp": a.MaxHP, "player": a.Player, "dead": a.Dead, "graphic": a.Graphic})
	}
	row, _ := json.Marshal(map[string]any{
		"position": o.Position, "hp": o.Player.HP, "max_hp": o.Player.MaxHP,
		"active": o.Battle.Active, "turn": o.Battle.Turn, "my_no": o.Battle.MyNo,
		"bp_flags": o.Battle.BPFlags, "ready": o.Battle.CommandReady,
		"last_command": o.Battle.LastCommand, "ended": o.Battle.Ended,
		"actors": actors,
	})
	if string(row) != s.last && len(s.rows) < 512 {
		s.rows = append(s.rows, row)
		s.last = string(row)
	}
	return o, nil
}

func (s *travelDiagnosticSession) persist(t *testing.T, root string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := json.MarshalIndent(map[string]any{"test": t.Name(), "failed": t.Failed(), "battle_observations": s.rows, "limit": 512, "protocol_submissions": s.actions, "protocol_errors": s.writeErrors}, "", "  ")
	if err != nil {
		t.Error(err)
		return
	}
	f, err := os.CreateTemp(filepath.Join(root, "build", "ai"), "travel-diagnostic-*.json")
	if err != nil {
		t.Error(err)
		return
	}
	_, err = f.Write(append(raw, '\n'))
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		t.Errorf("persist travel diagnostic: %v %v", err, closeErr)
	}
	t.Logf("battle diagnostic: %s", f.Name())
}
