package aiservice

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
)

type OwnStateRefresher struct {
	mu          sync.Mutex
	lastRequest time.Time
}

func (refresher *OwnStateRefresher) Refresh(ctx context.Context, backend *GameBackend, observed aigame.Snapshot) (aigame.Snapshot, error) {
	if !observed.Connected || !observed.Player.HasStatus || observed.Phase != aigame.PhaseWorld {
		return observed, nil
	}
	refresher.mu.Lock()
	defer refresher.mu.Unlock()
	if time.Since(refresher.lastRequest) < time.Second {
		return backend.Session.Observe(ctx)
	}
	dispatch := func(writeCtx context.Context) error {
		bounded, cancel := context.WithTimeout(writeCtx, 3*time.Second)
		defer cancel()
		for attempt := 0; attempt < 3; attempt++ {
			if err := bounded.Err(); err != nil {
				return err
			}
			current, err := backend.Session.Observe(bounded)
			if err != nil {
				return err
			}
			if current.Account != backend.Binding.AccountID || current.Character != backend.Binding.CharacterName {
				return aimcp.ErrInvalidBinding
			}
			if !current.Connected || !current.Player.HasStatus || current.Phase != aigame.PhaseWorld {
				return nil
			}
			err = backend.Session.ExecuteExpected(bounded, current.Revision, aigame.Action{Kind: aigame.ActionStatus, Command: "AI"})
			if errors.Is(err, aigame.ErrStaleRevision) {
				continue
			}
			// Throttle even uncertain writes; they must not be replayed here.
			refresher.lastRequest = time.Now()
			return err
		}
		// This background refresh is opportunistic. Under sustained incoming
		// events return current state, without claiming an AI response arrived.
		return nil
	}
	var err error
	// The Web bridge's AutomationSession already enters Gate.Dispatch in its
	// ExecuteExpected method. Gate.Dispatch holds its mutex while invoking the
	// callback, so wrapping that adapter a second time would deadlock. Headless
	// aigame.Session instances have no such wrapper and retain the outer fence.
	if _, alreadyBound := backend.Session.(interface{ UsesControlGate() }); alreadyBound {
		err = dispatch(ctx)
	} else {
		err = backend.Gate.Dispatch(ctx, backend.Binding.Generation, backend.Owner, dispatch)
	}
	if err != nil {
		return aigame.Snapshot{}, err
	}
	return backend.Session.Observe(ctx)
}
