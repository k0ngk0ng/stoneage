package sacli

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

// commandTrade covers the native player-to-player trade. The server validates
// every step again (the opponent must be the one the character faces, the
// offers must come from the observed inventory and purse), so this command
// only supplies what the observation already showed.
func (s *Server) commandTrade(ctx context.Context, request Request) Response {
	if len(request.Args) == 0 {
		return failure(KindUsage, "usage: sactl trade request <actor-id> | offer-item <slot 0|1> <inventory 5..19> | offer-gold <slot 0|1> <amount> | offer-pet <pet-slot> | lock | confirm | cancel")
	}
	snapshot, err := s.snapshot(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	if err := requireWorld(snapshot); err != nil {
		return actionFailure(err)
	}
	number := func(index int, label string) (int32, error) {
		if index >= len(request.Args) {
			return 0, fmt.Errorf("%s requires a number", label)
		}
		value, err := strconv.Atoi(request.Args[index])
		if err != nil {
			return 0, fmt.Errorf("%s must be a number, got %q", label, request.Args[index])
		}
		return int32(value), nil
	}
	switch request.Args[0] {
	case "request":
		target, err := number(1, "trade request <actor-id>")
		if err != nil {
			return failure(KindUsage, "%v", err)
		}
		actor, ok := snapshotActorByID(snapshot, target)
		if !ok {
			return actionFailure(fmt.Errorf("trade request: id %d is not a visible actor; use `sactl observe`", target))
		}
		// The server only accepts a request toward the single character the
		// player faces, so face it first.
		if err := s.faceActor(ctx, snapshot, actor); err != nil {
			return actionFailure(err)
		}
		current, err := s.snapshot(ctx)
		if err != nil {
			return sessionFailure(err)
		}
		action := aigame.Action{Kind: aigame.ActionTrade, Command: "request", TargetID: target}
		if err := s.submit(ctx, current.Revision, action); err != nil {
			return executeFailure(err)
		}
		return Response{OK: true, Text: fmt.Sprintf("requested a trade with %q (id %d)\n%s", actor.Name, target, s.renderObservation(current)),
			Data: replyJSON(current)}
	case "offer-item":
		slot, err := number(1, "trade offer-item <offer-slot> <inventory-slot>")
		if err != nil {
			return failure(KindUsage, "%v", err)
		}
		inventory, err := number(2, "trade offer-item <offer-slot> <inventory-slot>")
		if err != nil {
			return failure(KindUsage, "%v", err)
		}
		action := aigame.Action{Kind: aigame.ActionTrade, Command: "offer-item", Index: slot, Value: inventory}
		return s.submitSimple(ctx, action, fmt.Sprintf("offered inventory slot %d in trade slot %d", inventory, slot))
	case "offer-gold":
		slot, err := number(1, "trade offer-gold <offer-slot> <amount>")
		if err != nil {
			return failure(KindUsage, "%v", err)
		}
		amount, err := number(2, "trade offer-gold <offer-slot> <amount>")
		if err != nil {
			return failure(KindUsage, "%v", err)
		}
		action := aigame.Action{Kind: aigame.ActionTrade, Command: "offer-gold", Index: slot, Value: amount}
		return s.submitSimple(ctx, action, fmt.Sprintf("offered %d gold in trade slot %d", amount, slot))
	case "offer-pet":
		slot, err := number(1, "trade offer-pet <pet-slot>")
		if err != nil {
			return failure(KindUsage, "%v", err)
		}
		action := aigame.Action{Kind: aigame.ActionTrade, Command: "offer-pet", PetSlot: slot}
		return s.submitSimple(ctx, action, fmt.Sprintf("offered pet slot %d", slot))
	case "lock":
		return s.submitSimple(ctx, aigame.Action{Kind: aigame.ActionTrade, Command: "lock"}, "locked the offer")
	case "confirm":
		return s.submitSimple(ctx, aigame.Action{Kind: aigame.ActionTrade, Command: "confirm"}, "confirmed the trade")
	case "cancel":
		return s.submitSimple(ctx, aigame.Action{Kind: aigame.ActionTrade, Command: "cancel"}, "cancelled the trade")
	default:
		return failure(KindUsage, "trade: unknown operation %q (request, offer-item, offer-gold, offer-pet, lock, confirm, cancel)", request.Args[0])
	}
}

// snapshotActorByID finds one visible actor by its server object id.
func snapshotActorByID(snapshot aigame.Snapshot, id int32) (aigame.ActorSnapshot, bool) {
	for _, actor := range snapshot.Actors {
		if actor.ID == id {
			return actor, true
		}
	}
	return aigame.ActorSnapshot{}, false
}

// commandLogout leaves the world and closes the session. The daemon keeps the
// character remembered, so the next command reconnects and re-enters.
func (s *Server) commandLogout(ctx context.Context, request Request) Response {
	game, err := s.session(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	snapshot, err := game.Observe(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	if !inWorld(snapshot.Phase) {
		return Response{OK: true, Text: fmt.Sprintf("not in the world (phase=%s)", snapshot.Phase)}
	}
	character := snapshot.Character
	// The native session closes the socket unconditionally after writing
	// CharLogout, so a failure here means the server's acknowledgement could
	// not be read — not that the character stayed in the world. Report what
	// actually happened instead of guessing.
	// The reply may never resolve (aigame cannot decode this server's Ringo
	// acknowledgement), so bound the wait instead of holding the command open
	// until its own timeout.
	logoutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	logoutErr := game.Logout(logoutCtx)
	if logoutErr != nil {
		return Response{
			OK:   true,
			Kind: KindUnknown,
			Text: fmt.Sprintf("sent CharLogout for %q and closed the session, but the server's acknowledgement could not be read (%v); the daemon reconnects and re-enters on the next command",
				character, logoutErr),
		}
	}
	return Response{
		OK:   true,
		Text: fmt.Sprintf("logged out %q; the daemon will reconnect and re-enter on the next command", character),
	}
}
