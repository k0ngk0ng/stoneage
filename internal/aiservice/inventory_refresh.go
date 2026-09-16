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

// refreshInventoryIdentity requests one authoritative template/slot snapshot.
// General snapshot revisions also advance for chat and outgoing packets;
// only AIObservationRevision proves that an own-state response was received.
// The request is read-only, but is still ownership and revision fenced.
func refreshInventoryIdentity(ctx context.Context, npc *NPCSkill) (aigame.Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
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
		if !before.Connected || before.Phase != aigame.PhaseWorld || before.Battle.Active {
			return before, errors.New("inventory refresh requires the connected world")
		}
		requestID = fmt.Sprintf("%016x", nextInventoryRequest.Add(1))
		err = npc.submit(ctx, before.Revision, func(s aigame.Snapshot) (aigame.Action, error) {
			if !s.Connected || s.Phase != aigame.PhaseWorld || s.Battle.Active {
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
		break
	}
	if err != nil {
		return before, err
	}
	return waitHealerState(ctx, npc, func(s aigame.Snapshot) (bool, error) {
		if s.AIObservationRevision <= before.Revision || s.AI.RequestID != requestID {
			return false, nil
		}
		if !s.AI.Received || !s.AI.ItemsKnown {
			return false, errors.New("server response does not establish inventory identity")
		}
		return true, nil
	})
}
