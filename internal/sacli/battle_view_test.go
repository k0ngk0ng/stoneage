package sacli

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

type selectionGame struct {
	Game
	snapshot aigame.Snapshot
	writes   int
}

func (g *selectionGame) Observe(context.Context) (aigame.Snapshot, error) { return g.snapshot, nil }
func (g *selectionGame) ExecuteExpected(_ context.Context, revision uint64, a aigame.Action) error {
	if revision != g.snapshot.Revision {
		return aigame.ErrStaleRevision
	}
	g.writes++
	g.snapshot.Battle.PlayerSubmitted = true
	return nil
}
func TestCLISelectionValidationAndAutomationFence(t *testing.T) {
	g := &selectionGame{snapshot: aigame.Snapshot{Revision: 7, Phase: aigame.PhaseBattle, Battle: aigame.BattleSnapshot{
		Active: true, MyNoKnown: true, MyNo: 0, BPReceived: true, BCReceived: true, CommandReady: true}}}
	s := NewServer(DefaultConfig())
	s.game = g
	v := aigame.NewBattleView(g.snapshot)
	selection := aigame.BattleSelection{MatchID: v.MatchID, Turn: v.Turn, ObservationID: v.ID, CandidateID: "player:guard:-1:0"}
	call := func() Response {
		body, _ := json.Marshal(selection)
		return s.commandBattleAct(context.Background(), Request{Args: []string{string(body)}})
	}
	s.autoRunning = true
	if r := call(); r.OK || g.writes != 0 {
		t.Fatal("automation fence", r)
	}
	s.autoRunning = false
	selection.ObservationID = "stale"
	if r := call(); r.OK || g.writes != 0 {
		t.Fatal("stale plan", r)
	}
	selection.ObservationID = v.ID
	if r := call(); !r.OK || g.writes != 1 {
		t.Fatal("valid selection", r)
	}
	if r := call(); r.OK || g.writes != 1 {
		t.Fatal("duplicate selection", r)
	}
}
