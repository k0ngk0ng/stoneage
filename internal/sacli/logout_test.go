package sacli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

type logoutGame struct {
	Game
	phase                 aigame.Phase
	record, place, closed bool
	err                   error
}

func (g *logoutGame) Observe(context.Context) (aigame.Snapshot, error) {
	return aigame.Snapshot{Phase: g.phase, Character: "Fixture"}, nil
}
func (g *logoutGame) Logout(context.Context) error        { g.record = true; return g.err }
func (g *logoutGame) LogoutInPlace(context.Context) error { g.place = true; return g.err }
func (g *logoutGame) Close() error                        { g.closed = true; return nil }
func TestLogoutModesAndFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		phase  aigame.Phase
		err    error
		mode   string
		reject bool
	}{
		{name: "default", phase: aigame.PhaseWorld, mode: "in-place"},
		{name: "explicit record", args: []string{"--record-point"}, phase: aigame.PhaseWorld, mode: "record-point"},
		{name: "in place", args: []string{"--in-place"}, phase: aigame.PhaseWorld, mode: "in-place"},
		{name: "unknown result", args: []string{"--in-place"}, phase: aigame.PhaseWorld, mode: "in-place", err: errors.New("timeout")},
		{name: "default in battle", phase: aigame.PhaseBattle, reject: true},
		{name: "battle", args: []string{"--in-place"}, phase: aigame.PhaseBattle, reject: true},
		{name: "invalid", args: []string{"--typo"}, phase: aigame.PhaseWorld, reject: true},
		{name: "conflict", args: []string{"--in-place", "--record-point"}, phase: aigame.PhaseWorld, reject: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewServer(DefaultConfig())
			g := &logoutGame{phase: tc.phase, err: tc.err}
			s.game = g
			s.config.Password = "fixture"
			r := s.Dispatch(context.Background(), Request{Command: "logout", Args: tc.args})
			if tc.reject {
				if r.OK || g.closed || g.record || g.place || s.config.Password == "" {
					t.Fatalf("rejected logout changed session: %+v", r)
				}
				return
			}
			if !g.closed || s.game != nil || s.config.Password != "" || g.place != (tc.mode == "in-place") || g.record != (tc.mode == "record-point") {
				t.Fatalf("wrong logout path: %+v", g)
			}
			if tc.err != nil {
				if r.OK || r.Kind != KindUnknown || !strings.Contains(string(r.Data), `"confirmed":false`) {
					t.Fatalf("unconfirmed logout reported success: %+v", r)
				}
			} else if !r.OK || !strings.Contains(string(r.Data), `"mode":"`+tc.mode+`"`) {
				t.Fatalf("bad result: %+v", r)
			}
		})
	}
}
