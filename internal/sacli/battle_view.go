package sacli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

func battleSelectionFailure(err error) Response {
	r := executeFailure(err)
	code := "outcome_unknown"
	switch {
	case errors.Is(err, aigame.ErrStaleRevision):
		code = "stale_observation"
	case errors.Is(err, aigame.ErrBattleNotReady), errors.Is(err, aigame.ErrWrongPhase):
		code = "not_ready"
	case errors.Is(err, aigame.ErrInvalidAction):
		code = "invalid_candidate"
	}
	r.Data = replyJSON(map[string]string{"code": code})
	return r
}

func (s *Server) commandBattleEvents(ctx context.Context, request Request) Response {
	if len(request.Args) < 1 || len(request.Args) > 2 {
		return failure(KindUsage, "usage: battle-events <cursor> [stream]")
	}
	cursor, err := strconv.ParseUint(request.Args[0], 10, 64)
	if err != nil {
		return failure(KindUsage, "invalid battle event cursor")
	}
	stream := ""
	if len(request.Args) == 2 {
		stream = request.Args[1]
	}
	game, err := s.session(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	reader, ok := game.(interface {
		BattleEvents(string, uint64) aigame.BattleEventBatch
	})
	if !ok {
		return failure(KindAction, "session does not support battle events")
	}
	return Response{OK: true, Data: replyJSON(reader.BattleEvents(stream, cursor)), Text: "incremental battle observations"}
}

func (s *Server) commandBattleState(ctx context.Context, request Request) Response {
	if len(request.Args) != 0 {
		return failure(KindUsage, "usage: battle-state")
	}
	snapshot, err := s.snapshot(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	return Response{OK: true, Data: replyJSON(aigame.NewBattleView(snapshot)), Text: "battle observation and client action candidates"}
}

func (s *Server) commandBattleAct(ctx context.Context, request Request) Response {
	if len(request.Args) != 1 || len(request.Args[0]) > 4096 {
		return failure(KindUsage, "usage: battle-act <selection JSON>")
	}
	var selection aigame.BattleSelection
	d := json.NewDecoder(strings.NewReader(request.Args[0]))
	d.DisallowUnknownFields()
	if err := d.Decode(&selection); err != nil {
		return failure(KindUsage, "invalid battle selection")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return failure(KindUsage, "one battle selection required")
	}
	s.autoMu.Lock()
	running := s.autoRunning
	s.autoMu.Unlock()
	if running {
		return Response{OK: false, Kind: KindAction, Data: replyJSON(map[string]string{"code": "automation_conflict"})}
	}
	game, err := s.session(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	before, err := game.Observe(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	if before.Ladder != nil && before.Battle.LadderID != "" && before.Ladder.Snapshot.Self.Strategy != "manual" {
		return Response{OK: false, Kind: KindAction, Data: replyJSON(map[string]string{"code": "automation_conflict"})}
	}
	action, err := aigame.ResolveBattleSelection(before, selection)
	if err != nil {
		return battleSelectionFailure(err)
	}
	// Retain the same game object: a reconnect must not resolve an old plan
	// against a new session that happens to have the same numeric revision.
	if err = game.ExecuteExpected(ctx, before.Revision, action); err != nil {
		return battleSelectionFailure(err)
	}
	return Response{OK: true, Text: "battle command written; await authoritative outcome", Data: replyJSON(map[string]any{
		"status": "written", "selection": selection, "revision": before.Revision,
	})}
}
