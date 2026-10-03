package aigame

import (
	"context"
	"fmt"

	"github.com/k0ngk0ng/stoneage/internal/ladder"
)

// Echo only proves the native socket is alive: the legacy server echoes even
// when that connection no longer owns a logged-in character. When the server
// has advertised Arena, require a correlated, read-only character reply.
// Never replay a gameplay mutation to test or recover connection health.
func (session *Session) heartbeat(ctx context.Context) error {
	session.stateMu.RLock()
	snapshot := &session.state.snapshot
	inWorld := snapshot.Phase == PhaseWorld || snapshot.Phase == PhaseBattle
	characterID := ""
	if inWorld && snapshot.Ladder != nil {
		characterID = snapshot.Ladder.Snapshot.Self.ID
	}
	session.stateMu.RUnlock()
	if characterID == "" {
		_, err := session.request(ctx, "Echo", []wireValue{{kind: wireString, text: []byte("sactl")}})
		return err
	}
	id, err := ladder.NewRequestID()
	if err != nil {
		return err
	}
	reply, err := session.RequestLadder(ctx, ladder.Request{ID: id, Operation: "status"})
	if err != nil {
		return fmt.Errorf("character status probe failed (socket Echo is insufficient): %w", err)
	}
	if !reply.OK || reply.Snapshot.Self.ID != characterID {
		return fmt.Errorf("character status probe rejected or character changed")
	}
	return nil
}
