package sacli

import (
	"context"
	"reflect"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

type itemUseFixture struct {
	Game
	actions []aigame.Action
}

func (g *itemUseFixture) Observe(context.Context) (aigame.Snapshot, error) {
	return aigame.Snapshot{Phase: aigame.PhaseWorld, Revision: uint64(1 + len(g.actions)), Position: aigame.Point{X: 14, Y: 15}}, nil
}

func (g *itemUseFixture) ExecuteExpected(_ context.Context, revision uint64, action aigame.Action) error {
	g.actions = append(g.actions, action)
	return nil
}

func TestItemUseDefaultsToNativeSelfAndPreservesExplicitTarget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		target int32
	}{
		{"default self", []string{"use", "9"}, 0},
		{"explicit self", []string{"use", "9", "0"}, 0},
		{"first pet", []string{"use", "9", "1"}, 1},
		{"last pet", []string{"use", "9", "5"}, 5},
		{"party", []string{"use", "9", "7"}, 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &itemUseFixture{}
			s := &Server{game: g}
			r := s.Dispatch(context.Background(), Request{Command: "item", Args: tc.args})
			if !r.OK || len(g.actions) != 1 {
				t.Fatalf("response=%+v actions=%+v", r, g.actions)
			}
			if got, want := g.actions[0], aigame.UseItem(14, 15, 9, tc.target); !reflect.DeepEqual(got, want) {
				t.Fatalf("action=%+v want=%+v", got, want)
			}
		})
	}
}

func TestItemUseRejectsAmbiguousArgumentsBeforeSubmission(t *testing.T) {
	for _, args := range [][]string{
		{"use"}, {"use", "9", "1", "2"}, {"use", "9", "pet"},
		{"use", "-1"}, {"use", "20"}, {"use", "4294967296"},
		{"use", "9", "-1"}, {"use", "9", "11"}, {"use", "9", "4294967296"},
	} {
		g := &itemUseFixture{}
		s := &Server{game: g}
		r := s.Dispatch(context.Background(), Request{Command: "item", Args: args})
		if r.OK || r.Kind != KindUsage || len(g.actions) != 0 {
			t.Fatalf("args=%v response=%+v actions=%+v", args, r, g.actions)
		}
	}
}
