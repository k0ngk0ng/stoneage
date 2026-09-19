package aigame

import (
	"context"
	"fmt"
	"strings"
)

// Observe returns the latest server projection. It does not synthesize a
// position, encounter, balance or command acknowledgement.
func (session *Session) Observe(ctx context.Context) (Snapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if err := session.ensureOpen(); err != nil {
		return Snapshot{}, err
	}
	return session.Snapshot(), nil
}

// Execute is the adapter-facing name for Do. A successful return means the
// legal named packet was written to the socket; callers must observe the
// subsequent server event before treating a non-idempotent action as done.
func (session *Session) Execute(ctx context.Context, action Action) error {
	return session.Do(ctx, action)
}

// ExecuteExpected submits action only when expectedRevision is still the
// session's latest observed revision. The revision check is repeated while
// holding the state read lock immediately before the packet write, so an
// event observed during packet construction cannot turn an observation into
// a stale action.
func (session *Session) ExecuteExpected(ctx context.Context, expectedRevision uint64, action Action) error {
	return session.execute(ctx, action, &expectedRevision)
}

// Act is a short alias retained for simple headless callers.
func (session *Session) Act(ctx context.Context, action Action) error {
	return session.Do(ctx, action)
}

// Do validates one supported client action and writes exactly one named
// protocol request. It never writes a server state packet or an invented
// encounter result.
func (session *Session) Do(ctx context.Context, action Action) error {
	return session.execute(ctx, action, nil)
}

// execute contains the common action validation, packet encoding and write
// path. expectedRevision is nil for the backwards-compatible Do/Execute
// behavior. Versioned submissions perform a final revision check while
// holding stateMu through writePacket, making the check and write one
// linearizable operation with respect to incoming server events.
func (session *Session) execute(ctx context.Context, action Action, expectedRevision *uint64) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := session.ensureOpen(); err != nil {
		return err
	}

	session.requestMu.Lock()
	defer session.requestMu.Unlock()
	if err := session.ensureOpen(); err != nil {
		return err
	}

	// Validate against one projection first. The initial version check also
	// makes a stale expected revision win over action validation errors. The
	// final check below closes the window opened while buildPacket reserves the
	// message ID.
	session.stateMu.RLock()
	if expectedRevision != nil && session.state.snapshot.Revision != *expectedRevision {
		session.stateMu.RUnlock()
		return ErrStaleRevision
	}
	values, function, err := validateActionLocked(&session.state, action)
	statEpoch := session.state.statPointsEpoch
	socialEpoch := session.state.socialFlagsEpoch
	statRevision := session.state.snapshot.Revision
	var submittedWindow *WindowSnapshot
	if action.Kind == ActionWindow {
		submittedWindow = session.state.activeWindow
	}
	session.stateMu.RUnlock()
	if err != nil {
		return err
	}
	packet, err := session.buildPacket(function, values)
	if err != nil {
		return err
	}
	if action.Kind == ActionParty {
		// Invalidate before the write, including uncertain delivery. A later
		// server own-state reply can establish the actual observed mode.
		session.stateMu.Lock()
		session.state.snapshot.AI.PartyModeKnown = false
		session.stateMu.Unlock()
	}
	if action.Kind == ActionTrade {
		// Keep local submission markers ordered before any server reply. The
		// bridge's typed writer does not re-enter the manual packet observer.
		session.stateMu.Lock()
		defer session.stateMu.Unlock()
		if session.state.snapshot.Revision != statRevision {
			return ErrStaleRevision
		}
		// Manual traffic can invalidate a trade without advancing a server
		// revision. Revalidate under the same lock as the write.
		if _, _, err := validateTradeActionLocked(&session.state, action); err != nil {
			return err
		}
		if err := session.writePacket(ctx, packet); err != nil {
			markTradeWriteUncertainLocked(&session.state)
			session.state.snapshot.LastError = err.Error()
			return err
		}
		applyTradeActionLocked(&session.state, action, function)
		applyActionLocked(&session.state, action, function, statRevision)
		return nil
	}
	// submittedAt is the revision the write was issued against. It is read
	// under the same lock as the write so a server event that lands while the
	// write is completing cannot be mistaken for the state this action
	// answered.
	var submittedAt uint64
	if expectedRevision != nil || action.Kind == ActionWindow || action.Kind == ActionAllocateStat || action.Kind == ActionSocialSetting {
		session.stateMu.RLock()
		if (expectedRevision != nil && session.state.snapshot.Revision != *expectedRevision) ||
			(action.Kind == ActionWindow && session.state.activeWindow != submittedWindow) ||
			((action.Kind == ActionAllocateStat || action.Kind == ActionSocialSetting) && session.state.snapshot.Revision != statRevision) {
			session.stateMu.RUnlock()
			return ErrStaleRevision
		}
		submittedAt = session.state.snapshot.Revision
		if err := session.writePacket(ctx, packet); err != nil {
			session.stateMu.RUnlock()
			session.invalidateSubmittedStatPoints(action, statEpoch)
			session.invalidateSubmittedSocialFlags(action, socialEpoch)
			session.recordActionError(err)
			return err
		}
		session.stateMu.RUnlock()
	} else {
		session.stateMu.RLock()
		submittedAt = session.state.snapshot.Revision
		session.stateMu.RUnlock()
		if err := session.writePacket(ctx, packet); err != nil {
			// A cancelled/short write is intentionally not retried. The peer
			// may have received a non-idempotent command before the error
			// surfaced.
			session.recordActionError(err)
			return err
		}
	}
	session.invalidateSubmittedStatPoints(action, statEpoch)
	session.invalidateSubmittedSocialFlags(action, socialEpoch)
	session.stateMu.Lock()
	applyActionLocked(&session.state, action, function, submittedAt)
	markWindowSubmittedLocked(&session.state, submittedWindow)
	session.stateMu.Unlock()
	return nil
}

// A successful socket write is not a stat-allocation acknowledgement. Fence
// further allocations until SKUP or S:AI refreshes remaining points; preserve a
// newer response that raced with completion of the write. An uncertain write
// has the same fence and is never automatically retried.
func (session *Session) invalidateSubmittedStatPoints(action Action, epoch uint64) {
	if action.Kind != ActionAllocateStat {
		return
	}
	session.stateMu.Lock()
	defer session.stateMu.Unlock()
	if session.state.statPointsEpoch == epoch {
		session.state.snapshot.Player.StatPointsKnown = false
	}
}

// A new WN may arrive after writePacket returns and before stateMu is
// reacquired. Pointer identity distinguishes even two identical WN envelopes;
// finishing an old write must never mark the newly received window submitted.
func markWindowSubmittedLocked(state *gameState, window *WindowSnapshot) {
	if window != nil && state.activeWindow == window {
		window.Submitted = true
		if len(state.windows) > 0 {
			state.windows[len(state.windows)-1].Submitted = true
		}
	}
}

func (session *Session) recordActionError(err error) {
	if err == nil {
		return
	}
	session.stateMu.Lock()
	session.state.snapshot.LastError = err.Error()
	session.stateMu.Unlock()
}

func validateActionLocked(state *gameState, action Action) ([]wireValue, string, error) {
	if state == nil {
		return nil, "", ErrClosed
	}
	phase := state.snapshot.Phase
	if state.trade.snapshot.Active || state.trade.snapshot.Pending {
		switch action.Kind {
		case ActionTrade, ActionStatus, ActionChat, ActionMail:
		default:
			return nil, "", fmt.Errorf("%w: finish or cancel the pending trade before other gameplay actions", ErrInvalidAction)
		}
	}
	world := phase == PhaseWorld
	battle := phase == PhaseBattle && state.snapshot.Battle.Active && !state.snapshot.Battle.Ended
	characterPhaseError := func() error {
		if phase == PhaseAuthenticated || phase == PhaseCharacterList {
			return ErrCharacterRequired
		}
		return fmt.Errorf("%w: current phase is %s", ErrWrongPhase, phase)
	}
	requireWorld := func() error {
		if !world {
			return characterPhaseError()
		}
		return nil
	}
	requirePosition := func(x, y int32) error {
		if state.snapshot.Position.Floor < 0 {
			return fmt.Errorf("%w: server position is unknown", ErrInvalidAction)
		}
		if x != state.snapshot.Position.X || y != state.snapshot.Position.Y {
			return fmt.Errorf("%w: action position %d,%d is not current position %d,%d", ErrInvalidAction, x, y, state.snapshot.Position.X, state.snapshot.Position.Y)
		}
		return nil
	}
	textBytes := func(text, utf8Text string, bytes []byte, max int) ([]byte, error) {
		if bytes != nil {
			bytes = append([]byte(nil), bytes...)
		} else {
			if utf8Text != "" {
				text = utf8Text
			}
			encoded, err := encodeLegacyUTF8(text)
			if err != nil {
				return nil, fmt.Errorf("%w: %v", ErrTextEncoding, err)
			}
			bytes = encoded
		}
		if len(bytes) == 0 {
			return nil, fmt.Errorf("%w: text is empty", ErrInvalidAction)
		}
		if max > 0 && len(bytes) > max {
			return nil, fmt.Errorf("%w: text exceeds %d bytes", ErrInvalidAction, max)
		}
		return bytes, nil
	}

	switch action.Kind {
	case ActionMove:
		if err := requireWorld(); err != nil {
			return nil, "", err
		}
		if err := requirePosition(action.X, action.Y); err != nil {
			return nil, "", err
		}
		if len(action.Route) == 0 || len(action.Route) > maxRouteLength {
			return nil, "", fmt.Errorf("%w: route must contain 1..%d steps", ErrInvalidAction, maxRouteLength)
		}
		for _, step := range action.Route {
			if step < 'a' || step > 'h' {
				return nil, "", fmt.Errorf("%w: route contains invalid direction %q", ErrInvalidAction, step)
			}
		}
		return []wireValue{{kind: wireInt, integer: action.X}, {kind: wireInt, integer: action.Y}, {kind: wireString, text: []byte(action.Route)}}, "W", nil

	case ActionLook:
		if err := requireWorld(); err != nil {
			return nil, "", err
		}
		if action.Direction < 0 || action.Direction > 7 {
			return nil, "", fmt.Errorf("%w: look direction must be 0..7", ErrInvalidAction)
		}
		return []wireValue{{kind: wireInt, integer: action.Direction}}, "L", nil

	case ActionMapEvent:
		if err := requireWorld(); err != nil {
			return nil, "", err
		}
		if !validMapEvent(action.Event) {
			return nil, "", fmt.Errorf("%w: unsupported map event %d", ErrInvalidAction, action.Event)
		}
		if action.EventSequence <= 0 {
			return nil, "", fmt.Errorf("%w: map event sequence must be positive", ErrInvalidAction)
		}
		// lssproto_EV_recv accepts a nearby warp coordinate as a legacy
		// compatibility convenience, but that path lets a caller reposition
		// the character without proving it reached the source tile. Headless
		// automation must send EV only for its exact authoritative position.
		if err := requirePosition(action.X, action.Y); err != nil {
			return nil, "", err
		}
		if action.Direction < -1 || action.Direction > 7 {
			return nil, "", fmt.Errorf("%w: map event direction must be -1..7", ErrInvalidAction)
		}
		return []wireValue{{kind: wireInt, integer: action.Event}, {kind: wireInt, integer: action.EventSequence}, {kind: wireInt, integer: action.X}, {kind: wireInt, integer: action.Y}, {kind: wireInt, integer: action.Direction}}, "EV", nil

	case ActionTalk:
		if err := requireWorld(); err != nil {
			return nil, "", err
		}
		if err := requirePosition(action.X, action.Y); err != nil {
			return nil, "", err
		}
		if action.Color < 0 || action.Color > 255 || action.Range < 0 || action.Range > 255 {
			return nil, "", fmt.Errorf("%w: invalid talk color or range", ErrInvalidAction)
		}
		command, err := textBytes(action.Command, "", action.TextBytes, maxChatBytes)
		if err != nil {
			return nil, "", err
		}
		if !strings.HasPrefix(string(command), "P|") {
			return nil, "", fmt.Errorf("%w: talk command must start with P|", ErrInvalidAction)
		}
		return []wireValue{{kind: wireInt, integer: action.X}, {kind: wireInt, integer: action.Y}, {kind: wireString, text: command}, {kind: wireInt, integer: action.Color}, {kind: wireInt, integer: action.Range}}, "TK", nil

	case ActionChat:
		if err := requireWorld(); err != nil {
			return nil, "", err
		}
		if state.snapshot.Position.Floor < 0 {
			return nil, "", fmt.Errorf("%w: server position is unknown", ErrInvalidAction)
		}
		if action.Color < 0 || action.Color > 255 || action.Range < 0 || action.Range > 255 {
			return nil, "", fmt.Errorf("%w: invalid chat color or range", ErrInvalidAction)
		}
		body, err := textBytes(action.Text, action.TextUTF8, action.TextBytes, maxChatBytes-2)
		if err != nil {
			return nil, "", err
		}
		command := append([]byte("P|"), body...)
		return []wireValue{{kind: wireInt, integer: state.snapshot.Position.X}, {kind: wireInt, integer: state.snapshot.Position.Y}, {kind: wireString, text: command}, {kind: wireInt, integer: action.Color}, {kind: wireInt, integer: action.Range}}, "TK", nil

	case ActionMail:
		if err := requireWorld(); err != nil {
			return nil, "", err
		}
		switch action.Command {
		case "list":
			if action.Index != 0 || action.X != 0 || action.Y != 0 || action.Text != "" || action.TextUTF8 != "" || len(action.TextBytes) != 0 || action.Color != 0 {
				return nil, "", fmt.Errorf("%w: mail list does not accept a recipient or message", ErrInvalidAction)
			}
			return nil, "AB", nil
		case "add":
			if err := requirePosition(action.X, action.Y); err != nil {
				return nil, "", err
			}
			return []wireValue{{kind: wireInt, integer: action.X}, {kind: wireInt, integer: action.Y}}, "AAB", nil
		case "send":
			if _, ok := state.addressBookEntry(action.Index); !ok {
				return nil, "", fmt.Errorf("%w: mail recipient slot %d is not an observed contact", ErrInvalidAction, action.Index)
			}
			if action.Color < 0 || action.Color > 255 {
				return nil, "", fmt.Errorf("%w: invalid mail color", ErrInvalidAction)
			}
			body, err := textBytes(action.Text, action.TextUTF8, action.TextBytes, maxChatBytes)
			if err != nil {
				return nil, "", err
			}
			return []wireValue{{kind: wireInt, integer: action.Index}, {kind: wireString, text: body}, {kind: wireInt, integer: action.Color}}, "MSG", nil
		default:
			return nil, "", fmt.Errorf("%w: unsupported mail operation %q", ErrInvalidAction, action.Command)
		}

	case ActionWindow:
		if err := requireWorld(); err != nil {
			return nil, "", err
		}
		if err := requirePosition(action.X, action.Y); err != nil {
			return nil, "", err
		}
		window := state.activeWindow
		if window == nil || !window.Open || window.Submitted {
			return nil, "", fmt.Errorf("%w: no active server window", ErrInvalidAction)
		}
		if action.WindowSequence != window.Sequence || action.WindowObjectID != window.ObjectID {
			return nil, "", fmt.Errorf("%w: window sequence/object does not match active window", ErrInvalidAction)
		}
		if action.WindowType != 0 && action.WindowType != window.Type {
			return nil, "", fmt.Errorf("%w: window type does not match active window", ErrInvalidAction)
		}
		// A select window is answered with button 0 and the chosen row in the
		// data field: the native hit-test sends the row number and the 2.5 NPC
		// handlers read it with atoi(data) (client/web/index.html:15015-15033).
		// Requiring a non-zero button here would reject every choice selection,
		// so the button may only be empty when no row was supplied either.
		if action.WindowSelect == 0 && action.Text == "" && action.TextUTF8 == "" && len(action.TextBytes) == 0 {
			return nil, "", fmt.Errorf("%w: window button is empty", ErrInvalidAction)
		}
		var data []byte
		var err error
		if action.TextBytes != nil {
			data = append([]byte(nil), action.TextBytes...)
		} else {
			text := action.Text
			if action.TextUTF8 != "" {
				text = action.TextUTF8
			}
			data, err = encodeLegacyUTF8(text)
			if err != nil {
				return nil, "", fmt.Errorf("%w: %v", ErrTextEncoding, err)
			}
		}
		return []wireValue{{kind: wireInt, integer: action.X}, {kind: wireInt, integer: action.Y}, {kind: wireInt, integer: action.WindowSequence}, {kind: wireInt, integer: action.WindowObjectID}, {kind: wireInt, integer: action.WindowSelect}, {kind: wireString, text: data}}, "WN", nil

	case ActionBattle:
		if !battle {
			if phase != PhaseBattle {
				return nil, "", characterPhaseError()
			}
			return nil, "", ErrBattleNotReady
		}
		if !state.snapshot.Battle.BPReceived || !state.snapshot.Battle.BCReceived || !state.snapshot.Battle.CommandReady || state.snapshot.Battle.Movie {
			return nil, "", ErrBattleNotReady
		}
		command, err := validBattleCommand(action.Command)
		if err != nil {
			return nil, "", err
		}
		if strings.HasPrefix(command, "W|") {
			if !state.snapshot.Battle.PetCommandReady() || (state.snapshot.Battle.BPFlags&(BattlePetMenuOff|BattleEnemySurprise) != 0 && command != "W|FF|FF") {
				return nil, "", ErrBattleNotReady
			}
		} else if !state.snapshot.Battle.PlayerCommandReady() || (state.snapshot.Battle.BPFlags&(BattlePlayerMenuOff|BattleEnemySurprise) != 0 && command != "N") {
			return nil, "", ErrBattleNotReady
		}
		return []wireValue{{kind: wireString, text: []byte(command)}}, "B", nil

	case ActionBattleEnd:
		if phase != PhaseWorld && phase != PhaseBattle {
			return nil, "", characterPhaseError()
		}
		if !battle && !state.snapshot.Battle.Ended {
			if phase == PhaseAuthenticated || phase == PhaseCharacterList {
				return nil, "", ErrCharacterRequired
			}
			return nil, "", fmt.Errorf("%w: battle is not active", ErrWrongPhase)
		}
		// The native client ends a battle it has lost as well as one the server
		// has concluded: a wiped side is answered with EO immediately rather
		// than waiting for a result that never arrives.
		concluded := state.snapshot.Battle.Ended || state.snapshot.Battle.Result != "" || state.snapshot.Battle.MySideDefeated()
		if state.snapshot.Battle.LastCommand == "EO" || !concluded {
			return nil, "", ErrBattleNotReady
		}
		return []wireValue{{kind: wireInt, integer: 0}}, "EO", nil

	case ActionTrade:
		return validateTradeActionLocked(state, action)

	case ActionSocialSetting:
		if err := requireWorld(); err != nil {
			return nil, "", err
		}
		mask, ok := socialSettingMask(action.Command)
		if !ok || (action.Value != 0 && action.Value != 1) || !state.snapshot.Player.SocialFlagsKnown {
			return nil, "", fmt.Errorf("%w: social setting requires known flags, a supported setting and value 0/1", ErrInvalidAction)
		}
		flags := state.snapshot.Player.SocialFlags &^ mask
		if action.Value == 1 {
			flags |= mask
		}
		return []wireValue{{kind: wireInt, integer: flags}}, "FS", nil

	case ActionParty:
		if err := requireWorld(); err != nil {
			return nil, "", err
		}
		if err := requirePosition(action.X, action.Y); err != nil {
			return nil, "", err
		}
		if action.PartyRequest != 0 && action.PartyRequest != 1 {
			return nil, "", fmt.Errorf("%w: party request must be 0 or 1", ErrInvalidAction)
		}
		return []wireValue{{kind: wireInt, integer: action.X}, {kind: wireInt, integer: action.Y}, {kind: wireInt, integer: action.PartyRequest}}, "PR", nil

	case ActionDuel:
		if err := requireWorld(); err != nil {
			return nil, "", err
		}
		if err := requirePosition(action.X, action.Y); err != nil {
			return nil, "", err
		}
		return []wireValue{{kind: wireInt, integer: action.X}, {kind: wireInt, integer: action.Y}}, "DU", nil

	case ActionItem:
		if err := requireWorld(); err != nil {
			return nil, "", err
		}
		switch action.Command {
		case "move":
			if action.Index < 0 || action.Index >= 20 || action.Value < 0 || action.Value >= 20 {
				return nil, "", fmt.Errorf("%w: inventory index must be 0..19", ErrInvalidAction)
			}
			return []wireValue{{kind: wireInt, integer: action.Index}, {kind: wireInt, integer: action.Value}}, "MI", nil
		case "drop":
			if err := requirePosition(action.X, action.Y); err != nil {
				return nil, "", err
			}
			if action.Index < 0 || action.Index >= 20 {
				return nil, "", fmt.Errorf("%w: inventory index must be 0..19", ErrInvalidAction)
			}
			return []wireValue{{kind: wireInt, integer: action.X}, {kind: wireInt, integer: action.Y}, {kind: wireInt, integer: action.Index}}, "DI", nil
		case "drop-gold", "gold":
			if err := requirePosition(action.X, action.Y); err != nil {
				return nil, "", err
			}
			if action.Value <= 0 {
				return nil, "", fmt.Errorf("%w: dropped gold must be positive", ErrInvalidAction)
			}
			return []wireValue{{kind: wireInt, integer: action.X}, {kind: wireInt, integer: action.Y}, {kind: wireInt, integer: action.Value}}, "DG", nil
		case "magic":
			if err := requirePosition(action.X, action.Y); err != nil {
				return nil, "", err
			}
			if action.Index < 0 || action.TargetID < 0 {
				return nil, "", fmt.Errorf("%w: magic index and target must be non-negative", ErrInvalidAction)
			}
			return []wireValue{{kind: wireInt, integer: action.X}, {kind: wireInt, integer: action.Y}, {kind: wireInt, integer: action.Index}, {kind: wireInt, integer: action.TargetID}}, "MU", nil
		case "pickup":
			if err := requirePosition(action.X, action.Y); err != nil {
				return nil, "", err
			}
			direction := action.Direction
			if direction < 0 {
				direction = state.snapshot.Position.Direction
			}
			if direction < 0 || direction > 7 {
				return nil, "", fmt.Errorf("%w: pickup direction must be 0..7", ErrInvalidAction)
			}
			return []wireValue{{kind: wireInt, integer: action.X}, {kind: wireInt, integer: action.Y}, {kind: wireInt, integer: direction}}, "PI", nil
		}
		if err := requirePosition(action.X, action.Y); err != nil {
			return nil, "", err
		}
		if action.Index < 0 || action.Index >= 20 {
			return nil, "", fmt.Errorf("%w: inventory index must be 0..19", ErrInvalidAction)
		}
		if action.TargetID < 0 {
			return nil, "", fmt.Errorf("%w: item target cannot be negative", ErrInvalidAction)
		}
		return []wireValue{{kind: wireInt, integer: action.X}, {kind: wireInt, integer: action.Y}, {kind: wireInt, integer: action.Index}, {kind: wireInt, integer: action.TargetID}}, "ID", nil

	case ActionPet:
		if err := requireWorld(); err != nil {
			return nil, "", err
		}
		switch action.Command {
		case "", "status":
			if action.PetSlot < 0 || action.PetSlot >= 5 || (action.Value != 0 && action.Value != 1) {
				return nil, "", fmt.Errorf("%w: pet status requires slot 0..4 and value 0/1", ErrInvalidAction)
			}
			return []wireValue{{kind: wireInt, integer: action.PetSlot}, {kind: wireInt, integer: action.Value}}, "PETST", nil
		case "standby":
			if action.Value < 0 || action.Value > 31 {
				return nil, "", fmt.Errorf("%w: standby pet mask must be 0..31", ErrInvalidAction)
			}
			return []wireValue{{kind: wireInt, integer: action.Value}}, "SPET", nil
		default:
			return nil, "", fmt.Errorf("%w: unsupported pet operation %q", ErrInvalidAction, action.Command)
		}

	case ActionAllocateStat:
		if err := requireWorld(); err != nil {
			return nil, "", err
		}
		p := state.snapshot.Player
		if !state.snapshot.Connected || !p.HasStatus || p.HP <= 0 || state.snapshot.Battle.Active ||
			!p.StatPointsKnown || p.UnspentStatPoints <= 0 || action.Index < 0 || action.Index > 3 || action.Value != 0 || action.Value2 != 0 {
			return nil, "", fmt.Errorf("%w: allocating one stat point requires a living idle character, known available points and index 0..3", ErrInvalidAction)
		}
		return []wireValue{{kind: wireInt, integer: action.Index}}, "SKUP", nil

	case ActionStatus:
		if err := requireWorld(); err != nil {
			return nil, "", err
		}
		if !validStatusRequest(action.Command) {
			return nil, "", fmt.Errorf("%w: unsupported status request %q", ErrInvalidAction, action.Command)
		}
		return []wireValue{{kind: wireString, text: []byte(action.Command)}}, "S", nil

	case ActionRaw:
		return validateRawAction(state, action)
	default:
		return nil, "", fmt.Errorf("%w: unsupported action kind %q", ErrInvalidAction, action.Kind)
	}
}

func validMapEvent(event int32) bool {
	switch event {
	case MapEventEnemy, MapEventWarp, MapEventWarpMorning, MapEventWarpNoon, MapEventWarpNight:
		return true
	default:
		return false
	}
}

// applyActionLocked records the client's own action in the projection.
//
// submittedAt is the revision the write was issued against. A server event that
// lands while the write completes already describes a newer turn, and claiming
// that newer turn is answered would leave the caller waiting for a command it
// never sends. The battle markers are therefore only applied while the
// projection is still the one the action was submitted to. The error and
// function bookkeeping stays unconditional, because it reports what this client
// sent either way.
func applyActionLocked(state *gameState, action Action, function string, submittedAt uint64) {
	if state == nil {
		return
	}
	state.snapshot.LastError = ""
	current := state.snapshot.Revision == submittedAt
	if action.Kind == ActionBattle && current {
		if strings.HasPrefix(strings.ToUpper(action.Command), "W|") {
			state.snapshot.Battle.PetSubmitted = true
		} else {
			state.snapshot.Battle.PlayerSubmitted = true
		}
		state.snapshot.Battle.LastCommand = action.Command
		state.snapshot.Battle.updateCommandReadiness()
	}
	if action.Kind == ActionBattleEnd && current {
		// The terminal server result was required before this write. EO
		// completes that exit handshake; it is not a request to abort an
		// unfinished battle or a substitute for a successful escape.
		state.endBattle()
		state.snapshot.Battle.LastCommand = "EO"
	}
	state.snapshot.LastFunction = function
}

func validBattleCommand(command string) (string, error) {
	if command == "" || len(command) > 64 || strings.ContainsAny(command, " \r\n") {
		return "", fmt.Errorf("%w: malformed battle command", ErrInvalidAction)
	}
	parts := strings.Split(strings.ToUpper(command), "|")
	if len(parts) == 0 || parts[0] == "" {
		return "", fmt.Errorf("%w: malformed battle command", ErrInvalidAction)
	}
	token := func(value string) bool {
		if value == "" || len(value) > 2 {
			return false
		}
		if value == "A" || value == "FF" {
			return true
		}
		for _, character := range value {
			if !((character >= '0' && character <= '9') || (character >= 'A' && character <= 'F')) {
				return false
			}
		}
		return true
	}
	validNoArgs := map[string]bool{"G": true, "E": true, "N": true, "U": true, "@": true, "HELP": true}
	if validNoArgs[parts[0]] && len(parts) == 1 {
		return strings.Join(parts, "|"), nil
	}
	switch parts[0] {
	case "H", "T", "C", "S":
		if len(parts) != 2 || !token(parts[1]) {
			return "", fmt.Errorf("%w: invalid %s battle target", ErrInvalidAction, parts[0])
		}
	case "J", "I", "W":
		if len(parts) != 3 || !token(parts[1]) || !token(parts[2]) {
			return "", fmt.Errorf("%w: invalid %s battle arguments", ErrInvalidAction, parts[0])
		}
	default:
		return "", fmt.Errorf("%w: unsupported battle command %q", ErrInvalidAction, parts[0])
	}
	return strings.Join(parts, "|"), nil
}

func validStatusRequest(value string) bool {
	if strings.HasPrefix(value, "AI:") {
		return validAIRequestID(strings.TrimPrefix(value, "AI:"))
	}
	if value == "" || len(value) > 8 || strings.ContainsAny(value, "| \r\n") {
		return false
	}
	if value == "c" || value == "i" || value == "w" || value == "j" || value == "n" || value == "t" || value == "g" || value == "AI" {
		return true
	}
	if len(value) == 2 && (value[0] == 'k' || value[0] == 'w' || value[0] == 'j' || value[0] == 'n' || value[0] == 'i') && value[1] >= '0' && value[1] <= '9' {
		return true
	}
	return false
}

func validAIRequestID(value string) bool {
	if len(value) != 16 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func validateRawAction(state *gameState, action Action) ([]wireValue, string, error) {
	function := action.Function
	if function == "" {
		return nil, "", fmt.Errorf("%w: raw function is empty", ErrInvalidAction)
	}
	switch function {
	case "ClientLogin", "CreateNewChar", "CharDelete", "CharLogin", "CharList", "CharLogout":
		return nil, "", fmt.Errorf("%w: raw lifecycle function is not an action", ErrInvalidAction)
	case "W", "w", "L", "TK", "WN", "B", "EO", "PR", "DU", "ID", "MI", "PETST", "SPET", "SKUP", "FS", "TD":
		return nil, "", fmt.Errorf("%w: use the typed constructor for %s", ErrInvalidAction, function)
	}
	if state.snapshot.Phase != PhaseWorld && state.snapshot.Phase != PhaseBattle {
		return nil, "", fmt.Errorf("%w: current phase is %s", ErrWrongPhase, state.snapshot.Phase)
	}
	schema, ok := clientSchemas[function]
	if !ok || len(schema) != len(action.Fields) {
		return nil, "", fmt.Errorf("%w: raw function %q has invalid field count", ErrInvalidAction, function)
	}
	values := make([]wireValue, len(schema))
	for index, kind := range schema {
		field := action.Fields[index]
		if kind == wireInt {
			if field.Int == nil {
				return nil, "", fmt.Errorf("%w: raw field %d must be integer", ErrInvalidAction, index)
			}
			values[index] = wireValue{kind: wireInt, integer: *field.Int}
		} else {
			values[index] = wireValue{kind: wireString, text: append([]byte(nil), field.Text...)}
		}
	}
	return values, function, nil
}
