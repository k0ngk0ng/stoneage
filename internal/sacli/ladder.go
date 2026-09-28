package sacli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/battleauto"
	"github.com/k0ngk0ng/stoneage/internal/ladder"
)

type ladderGame interface {
	RequestLadder(context.Context, ladder.Request) (ladder.Envelope, error)
}

func ladderResponse(e ladder.Envelope) Response {
	s := e.Snapshot
	var b strings.Builder
	if e.Event == "contacts_lookup" {
		for _, p := range e.Contacts {
			fmt.Fprintf(&b, "slot=%d id=%s name=%s online=%t\n", p.Slot, p.ID, p.Name, p.Online)
		}
		return Response{OK: e.OK, Text: strings.TrimSpace(b.String()), Data: replyJSON(e)}
	}
	fmt.Fprintf(&b, "arena: %s; result=%s revision=%d\n", s.Phase, e.Code, e.Revision)
	fmt.Fprintf(&b, "strategy=%s\n", s.Self.Strategy)
	if s.Room != nil {
		fmt.Fprintf(&b, "room=%s mode=%dv%d leader=%s\n", s.Room.ID, s.Room.Mode, s.Room.Mode, s.Room.LeaderID)
		for _, p := range s.Room.Members {
			fmt.Fprintf(&b, "  %s (%s) rating=%d power=%.2f ready=%t online=%t\n", p.Name, p.ID, p.Rating, p.Power, p.Ready, p.Online)
		}
	}
	for _, invite := range s.Invitations {
		fmt.Fprintf(&b, "invitation=%s from=%s mode=%dv%d expires_at_ms=%d\n", invite.ID, invite.From.Name, invite.Mode, invite.Mode, invite.ExpiresAtMS)
	}
	if s.Match != nil {
		fmt.Fprintf(&b, "match=%s power_gap=%.2f%% start_at_ms=%d\n", s.Match.ID, s.Match.PowerGap*100, s.Match.StartAtMS)
		for i, team := range s.Match.Teams {
			fmt.Fprintf(&b, "  side=%d rating=%.1f power=%.2f\n", i, team.Rating, team.Power)
		}
	}
	if s.Result != nil {
		r := s.Result
		fmt.Fprintf(&b, "result=%s winner_side=%d rated=%t reason=%s", r.ID, r.WinnerSide, r.Rated, r.Reason)
		if r.StatisticsIncomplete {
			fmt.Fprintln(&b, "\nbattle turns, duration and statistics were not recovered")
		} else {
			fmt.Fprintf(&b, " turns=%d\n", r.Turns)
		}
		for _, p := range r.Members {
			fmt.Fprintf(&b, "  %s rating=%d %+d -> %d", p.Name, p.RatingBefore, p.RatingDelta, p.RatingAfter)
			if !r.StatisticsIncomplete {
				fmt.Fprintf(&b, " kills=%d pet_kills=%d damage=%d received=%d", p.Statistics.PlayerKills, p.Statistics.PetKills, p.Statistics.Damage, p.Statistics.DamageTaken)
			}
			fmt.Fprintln(&b)
		}
	}
	r := Response{OK: e.OK, Text: strings.TrimSpace(b.String()), Data: replyJSON(e)}
	if !e.OK {
		r.Kind = KindAction
	}
	return r
}

func (s *Server) commandLadder(ctx context.Context, request Request) Response {
	args := append([]string(nil), request.Args...)
	if len(args) == 0 {
		args = []string{"status"}
	}
	if args[0] == "wait" {
		return s.commandLadderWait(ctx, args[1:])
	}
	if args[0] == "strategies" {
		if len(args) != 1 {
			return failure(KindUsage, "usage: arena strategies")
		}
		registry, err := battleauto.NewStrategies(nil, s.config.LadderStrategies...)
		if err != nil {
			return actionFailure(err)
		}
		return Response{OK: true, Text: "basic: initial deterministic battle policy; manual: normal battle commands or local scripts", Data: replyJSON(registry.List())}
	}
	op := args[0]
	args = args[1:]
	s.autoMu.Lock()
	autoGeneration := s.autoGeneration
	walking := s.autoRunning && s.autoWalk
	s.autoMu.Unlock()
	if op == "ready" || op == "queue" {
		if walking {
			return Response{OK: false, Kind: KindAction, Text: "stop auto-battle walking before arena preparation", Data: replyJSON(map[string]string{"code": "automation_conflict"})}
		}
	}
	var id, arg string
	var revision uint64
	hasRevision := false
	for len(args) > 0 {
		switch args[0] {
		case "--request-id":
			if len(args) < 2 || id != "" {
				return failure(KindUsage, "--request-id requires one unique ID")
			}
			id = args[1]
			args = args[2:]
		case "--revision":
			if len(args) < 2 || hasRevision {
				return failure(KindUsage, "--revision requires one server revision")
			}
			n, err := strconv.ParseUint(args[1], 10, 64)
			if err != nil || n == 0 {
				return failure(KindUsage, "invalid revision")
			}
			revision = n
			hasRevision = true
			args = args[2:]
		default:
			if op == "invite" && arg != "" && !strings.Contains(arg, ":") && !strings.HasPrefix(args[0], "--") {
				arg += ":" + args[0]
				args = args[1:]
				continue
			}
			if arg != "" || strings.HasPrefix(args[0], "--") {
				return failure(KindUsage, "unexpected arena argument %q", args[0])
			}
			arg = args[0]
			args = args[1:]
		}
	}
	if (id != "") != hasRevision {
		return failure(KindUsage, "retry with BOTH --request-id and --revision from the original request")
	}
	if id == "" {
		var err error
		id, err = ladder.NewRequestID()
		if err != nil {
			return actionFailure(err)
		}
	}
	read := op == "status" || op == "result" || op == "contacts"
	// Validate before opening a session or issuing the preliminary observation.
	validate := ladder.Request{ID: id, Revision: 1, Operation: op, Argument: arg}
	if _, err := validate.Wire(); err != nil {
		return failure(KindUsage, "%v", err)
	}
	game, err := s.session(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	client, ok := game.(ladderGame)
	if !ok {
		return failure(KindAction, "session does not support the ladder protocol")
	}
	ctx, cancel := context.WithTimeout(ctx, actionTimeout)
	defer cancel()
	if !read && !hasRevision {
		statusID, err := ladder.NewRequestID()
		if err != nil {
			return actionFailure(err)
		}
		status, err := client.RequestLadder(ctx, ladder.Request{ID: statusID, Operation: "status"})
		if err != nil {
			return actionFailure(fmt.Errorf("arena observation unavailable: %w", err))
		}
		if !status.OK {
			return ladderResponse(status)
		}
		revision = status.Revision
	}
	r := ladder.Request{ID: id, Revision: revision, Operation: op, Argument: arg}
	if op == "strategy" && arg != "manual" {
		registry, err := battleauto.NewStrategies(nil, s.config.LadderStrategies...)
		if err != nil {
			return actionFailure(err)
		}
		if !registry.Has(arg) {
			return failure(KindUsage, "strategy %q is not installed; use arena strategies", arg)
		}
	}
	e, err := client.RequestLadder(ctx, r)
	if err != nil {
		return Response{OK: false, Kind: KindAction, Text: fmt.Sprintf("arena reply unconfirmed: %v; retain request_id=%s revision=%d", err, r.ID, r.Revision),
			Data: replyJSON(struct {
				Code    string         `json:"code"`
				Request ladder.Request `json:"request"`
			}{"outcome_unknown", r})}
	}
	if e.OK && !e.HistoricalReceipt() && (op == "ready" || (op == "strategy" && arg != "manual")) {
		s.autoMu.Lock()
		if s.autoGeneration == autoGeneration {
			s.autoLadderSuppressed = false
		}
		s.autoMu.Unlock()
	}
	if e.OK {
		s.ensureLadderBattle(game)
	}
	return ladderResponse(e)
}

type ladderEventGame interface {
	ladderGame
	WaitLadderEvents(context.Context, string, uint64) (ladder.Events, error)
}

func (s *Server) commandLadderWait(ctx context.Context, args []string) Response {
	if len(args) < 1 {
		return failure(KindUsage, "usage: arena wait <cursor> [duration] [--stream <stream>]")
	}
	cursor, err := strconv.ParseUint(args[0], 10, 64)
	if err != nil {
		return failure(KindUsage, "invalid cursor")
	}
	timeout := 30 * time.Second
	stream, hasDuration := "", false
	for args = args[1:]; len(args) > 0; {
		if args[0] == "--stream" {
			if len(args) < 2 || stream != "" || len(args[1]) > 128 || args[1] == "" {
				return failure(KindUsage, "--stream requires one stream from the previous wait response")
			}
			stream, args = args[1], args[2:]
			continue
		}
		if hasDuration {
			return failure(KindUsage, "unexpected arena wait argument")
		}
		timeout, err = time.ParseDuration(args[0])
		if err != nil || timeout <= 0 || timeout > 5*time.Minute {
			return failure(KindUsage, "duration must be between 0 and 5m")
		}
		hasDuration, args = true, args[1:]
	}
	game, err := s.session(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	client, ok := game.(ladderEventGame)
	if !ok {
		return failure(KindAction, "session does not support arena event recovery")
	}
	// A reconnect starts with no local projection. Ask the authority before
	// waiting, then let the shared journal declare any missing history.
	id, err := ladder.NewRequestID()
	if err != nil {
		return actionFailure(err)
	}
	statusContext, cancelStatus := context.WithTimeout(ctx, actionTimeout)
	status, err := client.RequestLadder(statusContext, ladder.Request{ID: id, Operation: "status"})
	cancelStatus()
	if err != nil {
		return sessionFailure(err)
	}
	if !status.OK {
		return ladderResponse(status)
	}
	waitContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	batch, err := client.WaitLadderEvents(waitContext, stream, cursor)
	if ctx.Err() != nil {
		err = ctx.Err()
		batch.TimedOut = false
	}
	if err != nil {
		return Response{OK: false, Kind: KindSession, Text: err.Error(), Data: replyJSON(batch)}
	}
	return Response{OK: true, Text: fmt.Sprintf("arena events=%d cursor=%d gap=%t timed_out=%t", len(batch.Events), batch.Cursor, batch.Gap, batch.TimedOut), Data: replyJSON(batch)}
}
