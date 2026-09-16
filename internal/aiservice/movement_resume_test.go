package aiservice

import (
	"context"
	"errors"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

// Model ordinary status arrivals immediately after recovery. The old resume
// path sampled twice and compared these distinct revisions before any W.
type recoveryStatusSession struct {
	*moveGame
	updates int
}

func (s *recoveryStatusSession) Observe(ctx context.Context) (aigame.Snapshot, error) {
	if s.updates > 0 {
		s.snapshot.Revision++
		s.updates--
	}
	return s.moveGame.Observe(ctx)
}

func TestMovementReplansFromFreshStatusAfterConfirmedRecovery(t *testing.T) {
	move, game := movementFixture(t, true)
	session := &recoveryStatusSession{moveGame: game}
	move.Backend.Session = session
	game.snapshot.Phase = aigame.PhaseBattle
	game.snapshot.Battle.Active = true
	calls := 0
	move.BattleRecovery = movementBattleRecoveryFunc(func(context.Context) error {
		calls++
		game.snapshot.Phase = aigame.PhaseWorld
		game.snapshot.Battle.Active = false
		session.updates = 2
		return nil
	})
	err := move.Execute(context.Background(), crossMapAction(12, 10, 1, 0))
	if err != nil || calls != 1 || game.moves != 1 || game.snapshot.Position.X != 1 {
		t.Fatalf("recovery resume: err=%v recovery=%d moves=%d position=%+v", err, calls, game.moves, game.snapshot.Position)
	}
}

func TestMovementInitialRevisionRemainsStrictWithRecoveryInstalled(t *testing.T) {
	move, game := movementFixture(t, true)
	calls := 0
	move.BattleRecovery = movementBattleRecoveryFunc(func(context.Context) error { calls++; return nil })
	err := move.Execute(context.Background(), crossMapAction(11, 10, 1, 0))
	if !errors.Is(err, aigame.ErrStaleRevision) || calls != 0 || game.moves != 0 {
		t.Fatalf("initial revision bypassed: err=%v recovery=%d moves=%d", err, calls, game.moves)
	}
}
