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

const logoutHelp = "usage: sactl logout [--record-point|--in-place]\nDefault: return to the record point. --in-place: preserve the server position. Both close the session and clear credentials."

// commandLogout closes the session and clears credentials. Only an explicit
// login may authenticate again; observation must not undo a logout.
func (s *Server) commandLogout(ctx context.Context, request Request) Response {
	mode := "record-point"
	if len(request.Args) == 1 && (request.Args[0] == "--help" || request.Args[0] == "-h") {
		return Response{OK: true, Text: logoutHelp}
	}
	if len(request.Args) > 1 {
		return failure(KindUsage, "%s", logoutHelp)
	}
	if len(request.Args) == 1 {
		switch request.Args[0] {
		case "--in-place":
			mode = "in-place"
		case "--record-point":
		default:
			return failure(KindUsage, "%s", logoutHelp)
		}
	}
	s.stopAutoBattle()
	s.connectMu.Lock()
	defer s.connectMu.Unlock()
	s.mu.Lock()
	game := s.game
	s.mu.Unlock()
	var inPlace interface{ LogoutInPlace(context.Context) error }
	var snapshot aigame.Snapshot
	var observeErr error
	if game != nil {
		snapshot, observeErr = game.Observe(ctx)
		if observeErr != nil {
			snapshot = aigame.Snapshot{} // Still clear an already broken session.
		}
		if mode == "in-place" && inWorld(snapshot.Phase) {
			if snapshot.Phase != aigame.PhaseWorld {
				return actionFailure(fmt.Errorf("in-place logout requires leaving battle first"))
			}
			var ok bool
			inPlace, ok = game.(interface{ LogoutInPlace(context.Context) error })
			if !ok {
				return actionFailure(fmt.Errorf("this session does not support in-place logout"))
			}
		}
	}
	s.mu.Lock()
	s.game = nil
	s.generation++
	s.config.Account = ""
	s.config.Password = ""
	s.config.PasswordFile = ""
	s.lastError = ""
	s.mu.Unlock()
	result := struct {
		Mode               string `json:"mode"`
		Confirmed          bool   `json:"confirmed"`
		CredentialsCleared bool   `json:"credentials_cleared"`
	}{mode, true, true}
	if game == nil {
		return Response{OK: true, Text: "already logged out; credentials cleared", Data: replyJSON(result)}
	}
	defer game.Close()
	if observeErr != nil {
		result.Confirmed = false
		return Response{OK: false, Kind: KindUnknown, Error: "session was already disconnected; credentials cleared; log in again to verify position", Data: replyJSON(result)}
	}
	if !inWorld(snapshot.Phase) {
		return Response{OK: true, Text: fmt.Sprintf("logged out (phase=%s); credentials cleared", snapshot.Phase), Data: replyJSON(result)}
	}
	logoutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var err error
	if inPlace != nil {
		err = inPlace.LogoutInPlace(logoutCtx)
	} else {
		err = game.Logout(logoutCtx)
	}
	if err != nil {
		result.Confirmed = false
		return Response{OK: false, Kind: KindUnknown, Error: fmt.Sprintf("%s logout for %q could not be confirmed (%v); session closed and credentials cleared; log in again to verify position", mode, snapshot.Character, err), Data: replyJSON(result)}
	}
	return Response{OK: true, Text: fmt.Sprintf("logged out %q (%s); credentials cleared; use sactl login to log in again", snapshot.Character, mode), Data: replyJSON(result)}
}
