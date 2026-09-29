package sacli

import (
	"context"
	"strings"
)

// Credentials arrive only over the private local socket, never CLI arguments.
func (s *Server) commandLogin(ctx context.Context, request Request) Response {
	if len(request.Args) != 2 || strings.TrimSpace(request.Args[0]) == "" || request.Args[1] == "" {
		return failure(KindUsage, "login requires interactive credentials")
	}
	s.connectMu.Lock()
	defer s.connectMu.Unlock()
	s.mu.Lock()
	if s.game != nil {
		s.mu.Unlock()
		return failure(KindSession, "already logged in; use another profile or logout first")
	}
	s.config.Account = request.Args[0]
	s.config.Password = request.Args[1]
	s.config.PasswordFile = ""
	// Interactive login authenticates an account only. A stale configured or
	// remembered character must never turn valid credentials into a failure.
	s.config.Character = ""
	s.character = ""
	s.mu.Unlock()
	game, err := s.connectSession(ctx)
	if err != nil {
		s.mu.Lock()
		s.config.Account = ""
		s.config.Password = ""
		s.mu.Unlock()
		return sessionFailure(err)
	}
	snapshot, err := game.Observe(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	return Response{OK: true, Text: "logged in; use `sactl chars` and `sactl enter <name|slot>`", Data: replyJSON(snapshot)}
}
