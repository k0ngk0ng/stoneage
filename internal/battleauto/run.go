package battleauto

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
)

// Game is the part of a session the loop needs. Both the headless client's
// session and the Web server's session satisfy it, which is what lets one
// implementation serve both.
type Game interface {
	Observe(ctx context.Context) (aigame.Snapshot, error)
	ExecuteExpected(ctx context.Context, expectedRevision uint64, action aigame.Action) error
}

// DefaultInterval is how often the loop looks at the battle. The server does
// not advance a turn until the client answers it, and the native client gives
// the player thirty seconds, so this only decides how quickly the answer
// arrives.
const DefaultInterval = 100 * time.Millisecond

// Runner submits one decision per turn until its context ends.
type Runner struct {
	Game     Game
	Tables   *aiknowledge.RecoveryTables
	Policy   Policy
	Interval time.Duration
	// Log receives a line for every turn submitted and every error that did
	// not end the loop. Nil silences it.
	Log func(format string, args ...any)
}

// Run blocks until ctx is cancelled. A transient failure never ends the loop:
// the next pass observes again, and a submission whose outcome is unknown is
// simply not repeated, because the revision fence and the turn readiness both
// move on without it.
func (r Runner) Run(ctx context.Context) error {
	if r.Game == nil {
		return errors.New("battleauto: no game session")
	}
	interval := r.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	policy := r.Policy
	if policy.HealBelowPercent == 0 && !policy.HealItems && !policy.HealMagic {
		policy = DefaultPolicy()
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}

		if err := r.tick(ctx, policy); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			r.logf("battleauto: %v", err)
			// Do not keep hammering a session that cannot answer. A cancelled
			// context is already handled above, so this only paces retries.
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Second):
			}
		}
	}
}

func (r Runner) tick(ctx context.Context, policy Policy) error {
	snapshot, err := r.Game.Observe(ctx)
	if err != nil {
		return err
	}
	decision, ok := Decide(snapshot, r.Tables, policy)
	if !ok {
		return nil
	}
	err = r.Game.ExecuteExpected(ctx, snapshot.Revision, decision.Action)
	switch {
	case err == nil:
		r.logf("%s", decision.Reason)
		return nil
	case errors.Is(err, aigame.ErrStaleRevision), errors.Is(err, aigame.ErrBattleNotReady):
		// The turn moved under us. The next pass decides again from the state
		// it finds, which is why nothing is retried here.
		return nil
	default:
		return err
	}
}

func (r Runner) logf(format string, args ...any) {
	if r.Log != nil {
		r.Log(format, args...)
	}
}

// Logf is the default line format used by hosts that do not supply one.
func Logf(format string, args ...any) {
	log.Printf(format, args...)
}
