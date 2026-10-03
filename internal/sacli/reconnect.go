package sacli

import (
	"context"
	"errors"
	"fmt"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

func (s *Server) commandReconnect(ctx context.Context, request Request) Response {
	if len(request.Args) == 1 && (request.Args[0] == "--help" || request.Args[0] == "-h") {
		return Response{OK: true, Text: "usage: sactl reconnect\nReconnect the same account and selected character using credentials retained in this profile's daemon memory. After logout or a daemon restart, use sactl login. Active battles, trades, Arena reservations and auto-battle must end first."}
	}
	if len(request.Args) != 0 {
		return failure(KindUsage, "usage: sactl reconnect")
	}
	s.connectMu.Lock()
	defer s.connectMu.Unlock()
	s.mu.Lock()
	game := s.game
	hasCredentials := s.config.Account != "" && s.config.Password != ""
	s.mu.Unlock()
	if !hasCredentials {
		return failure(KindSession, "no in-memory login credentials; run sactl login")
	}
	s.autoMu.Lock()
	running := s.autoRunning
	s.autoMu.Unlock()
	if running {
		return failure(KindAction, "stop auto-battle before reconnecting")
	}
	if game != nil {
		snapshot, err := game.Observe(ctx)
		// The reader closes the session before pump finishes draining its
		// events. A reconnect in that interval must handle the closed session
		// exactly like a nil game; other observation failures are not proof
		// that it is safe to replace a live connection.
		if err != nil && !errors.Is(err, aigame.ErrClosed) {
			return sessionFailure(err)
		}
		if err == nil && snapshot.Connected {
			if snapshot.Battle.Active || snapshot.Trade.Active || snapshot.Ladder != nil && snapshot.Ladder.Snapshot.ReservesCharacter() {
				return failure(KindAction, "cannot reconnect during battle, trade or an Arena reservation; reconcile the current activity first")
			}
			if snapshot.Phase == aigame.PhaseWorld {
				closer, ok := game.(interface{ LogoutInPlace(context.Context) error })
				if !ok {
					return failure(KindSession, "session does not support acknowledged in-place reconnect")
				}
				if err := closer.LogoutInPlace(ctx); err != nil {
					return sessionFailure(fmt.Errorf("in-place disconnect not confirmed; no login retried: %w", err))
				}
			} else if err := game.Close(); err != nil {
				return sessionFailure(err)
			}
		} else if err := game.Close(); err != nil {
			return sessionFailure(err)
		}
		s.mu.Lock()
		if s.game == game {
			s.game = nil
			s.generation++
		}
		s.mu.Unlock()
	}
	game, err := s.connectSession(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	snapshot, err := game.Observe(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	return Response{OK: true, Text: "reconnected using in-memory credentials\n" + s.renderObservation(snapshot), Data: replyJSON(snapshot)}
}
