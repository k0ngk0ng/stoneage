package aiservice

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

var nextInventoryRequest atomic.Uint64

var errIdentityReplySuperseded = errors.New("identity reply superseded by another status response")

// refreshInventoryIdentity requests one authoritative template/slot snapshot.
// General snapshot revisions also advance for chat and outgoing packets;
// only AIObservationRevision proves that an own-state response was received.
// The request is read-only, but is still ownership and revision fenced.
func refreshInventoryIdentity(ctx context.Context, npc *NPCSkill) (aigame.Snapshot, error) {
	return refreshOwnIdentity(ctx, npc, false)
}

func refreshOwnIdentity(ctx context.Context, npc *NPCSkill, allowBattle bool) (aigame.Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	valid := func(s aigame.Snapshot) bool {
		return s.Connected && (s.Phase == aigame.PhaseWorld && !s.Battle.Active || allowBattle && s.Phase == aigame.PhaseBattle && s.Battle.Active)
	}
	var before aigame.Snapshot
	var requestID string
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return before, err
		}
		before, err = npc.observe(ctx)
		if err != nil {
			return before, err
		}
		if !valid(before) {
			return before, errors.New("inventory refresh requires the connected world")
		}
		requestID = fmt.Sprintf("%016x", nextInventoryRequest.Add(1))
		err = npc.submit(ctx, before.Revision, func(s aigame.Snapshot) (aigame.Action, error) {
			if !valid(s) {
				return aigame.Action{}, errors.New("inventory refresh interrupted")
			}
			return aigame.Action{Kind: aigame.ActionStatus, Command: "AI:" + requestID}, nil
		})
		if errors.Is(err, aigame.ErrStaleRevision) {
			continue // Both revision fences reject before any packet is written.
		}
		if err != nil {
			return before, err
		}
		observed, waitErr := waitIdentityState(ctx, npc, func(s aigame.Snapshot) (bool, error) {
			if !valid(s) {
				return false, errors.New("inventory refresh interrupted by game state")
			}
			if s.AIObservationRevision <= before.Revision {
				return false, nil
			}
			if s.AI.RequestID != requestID {
				return false, errIdentityReplySuperseded
			}
			if !s.AI.Received || !s.AI.ItemsKnown {
				return false, errors.New("server response does not establish inventory identity")
			}
			return true, nil
		})
		if !errors.Is(waitErr, errIdentityReplySuperseded) {
			return observed, waitErr
		}
		// The automatic refresher may replace our correlated response before
		// this poll observes it. Requery within the existing three-attempt/time
		// bounds; never treat the unrelated response as our confirmation.
		before, err = observed, waitErr
	}
	return before, err
}

// Identity queries are also used during capture. Unlike healer confirmation,
// this wait does not impose a world-only phase; each caller checks its phase.
func waitIdentityState(ctx context.Context, npc *NPCSkill, ready func(aigame.Snapshot) (bool, error)) (aigame.Snapshot, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		snapshot, err := npc.observe(ctx)
		if err != nil {
			return snapshot, err
		}
		if !snapshot.Connected {
			return snapshot, errors.New("identity refresh session disconnected")
		}
		if ok, err := ready(snapshot); err != nil || ok {
			return snapshot, err
		}
		select {
		case <-ctx.Done():
			return snapshot, ctx.Err()
		case <-ticker.C:
		}
	}
}
