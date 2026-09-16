package aiservice

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

// This isolates the native L -> server state path without moving or spending.
func TestLiveLookFreshAI(t *testing.T) {
	if os.Getenv("STONEAGE_LOOK_LIVE_TEST") != "1" {
		t.Skip("set STONEAGE_LOOK_LIVE_TEST=1 for local QA facing confirmation")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	f := movementCrossMapLiveProvisionFreshAI(t, ctx, "look", "look-live-test")
	for _, direction := range []int32{2, 6, 0, 1, 3, 4, 5, 7} {
		before, err := f.Lease.Session.Observe(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Lease.Session.ExecuteExpected(ctx, before.Revision, aigame.Look(direction)); err != nil {
			t.Fatal("submit native look:", err)
		}
		waitCtx, waitCancel := context.WithTimeout(ctx, 3*time.Second)
		after, err := movementCrossMapLiveWaitSnapshot(waitCtx, f.Lease.Session, func(s aigame.Snapshot) bool {
			return s.Revision > before.Revision && s.Position.Direction == direction &&
				s.Position.Floor == before.Position.Floor && s.Position.X == before.Position.X && s.Position.Y == before.Position.Y
		})
		waitCancel()
		if err != nil {
			after, _ = f.Lease.Session.Observe(context.Background())
			selfActors := 0
			for _, actor := range after.Actors {
				if actor.Name == after.Character {
					selfActors++
					t.Logf("matching self actor direction=%d action=%d", actor.Direction, actor.Action)
				}
			}
			t.Fatalf("native L(%d) not confirmed: before=%+v after=%+v self_actors=%d: %v", direction, before.Position, after.Position, selfActors, err)
		}
		t.Logf("native L(%d) confirmed at unchanged tile", direction)
	}
}
