package aigame

import (
	"context"
	"time"
)

// All freshness markers are advanced by the shared state reducer. A valid AI
// response acknowledges even genuinely unknown identifiers and legacy schemas,
// so those do not create a permanent query loop. Only subsequent inventory or
// pet identity changes dirty the projection again.
func (session *Session) identityRefreshState() (bool, uint64) {
	session.stateMu.RLock()
	defer session.stateMu.RUnlock()
	state := &session.state
	active := state.snapshot.Phase == PhaseWorld || state.snapshot.Phase == PhaseBattle
	return active && state.identityDirty != state.identityConfirmed, state.snapshot.AIObservationRevision
}

func (session *Session) startIdentityRefresh() {
	session.wg.Add(1)
	go func() {
		defer session.wg.Done()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		var attempted time.Time
		var responseRevision uint64
		for {
			select {
			case <-session.done:
				return
			case <-ticker.C:
				needed, revision := session.identityRefreshState()
				if !needed {
					continue
				}
				interval := 5 * time.Second // Missing/malformed responses must not flood legacy servers.
				if revision != responseRevision {
					interval = 250 * time.Millisecond
				}
				if time.Since(attempted) < interval {
					continue
				}
				attempted, responseRevision = time.Now(), revision
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				// This is a read-only status query, never a gameplay action or an LLM call.
				_ = session.Do(ctx, Action{Kind: ActionStatus, Command: "AI"})
				cancel()
			}
		}
	}()
}

// Give an ordinary status/observe call a bounded chance to include the pending
// identifiers. Unsupported servers remain usable and unknown stays explicit.
func (session *Session) waitForIdentityRefresh(ctx context.Context) {
	needed, _ := session.identityRefreshState()
	if !needed {
		return
	}
	deadline := time.NewTimer(750 * time.Millisecond)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-session.done:
			return
		case <-ctx.Done():
			return
		case <-deadline.C:
			return
		case <-ticker.C:
			needed, _ = session.identityRefreshState()
			if !needed {
				return
			}
		}
	}
}
