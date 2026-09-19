package sacli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/ainavigation"
)

// DefaultChatColor and DefaultChatRange match the preserved client's chat
// defaults (color 0, range 3) so sactl messages look like ordinary player
// chat instead of a special case.
const (
	DefaultChatColor = 0
	DefaultChatRange = 3
)

// maxChatBytes is the CP936 size of the public-chat field after the "P|"
// prefix, matching aigame's own limit (internal/aigame/protocol.go:26).
const maxChatBytes = 68

// moveRouteChunk is the largest route the 2.5 server accepts in one W packet
// (CHAR_walk_init), matching ainavigation.Route.Chunks.
const moveRouteChunk = 32

// actionTimeout bounds how long a command waits for the server to reflect
// one submitted action before reporting the observation as-is.
const actionTimeout = 8 * time.Second

// CommandHelp is the command summary shared by `sactl help` and the CLI
// usage text, so both always describe the same command set.
const CommandHelp = `commands:
  status [--json]                     session, connection and character state
  observe [--json]                    full observation of the current world
  chars                               list the account's characters
  create-character <name> [flags]     create a character with server defaults
  delete-character <name> <name>      delete a character (the name is repeated on purpose)
  enter <character|slot>              enter a character (remembered across reconnects)
  walk <direction>                    one step: up/down/left/right or n/ne/e/se/s/sw/w/nw
  goto <x> <y>                        route to a tile on the current floor
  exits                               list the current floor's warps
  warp [floor [x y]] [--time M|A|N]   take the warp under you, or travel across maps
  encounters                          list the current floor's encounter areas
  seek-encounter                      walk into an encounter area and try to start a battle
  say <text> [--range N] [--color N]  public chat (CP936, 68 bytes max)
  talk <name> [text]                  face a nearby character and speak to it
  choose <row>                        answer a choice window by the row number shown
  reply <ok|cancel|yes|no|prev|next> [text]  answer a message window
  look <direction>                    turn without moving
  auto-battle on [walk|stay]|off|status  heal the most hurt, otherwise attack in order; walk also looks for fights
  battle <command>                    battle turn (H|FF attack, W|FF|FF pet, T|FF defend, S|01|FF skill, E escape, N wait, G give up, HELP)
  battle-end                          acknowledge the battle animation and leave the battle (EO)
  battle-help <0|1>                   toggle the native battle help flag
  item use|drop|drop-gold|move|magic|pickup ...   inventory and field item actions
  mail list|add|send|remove-contact ...           address book and mail
  pet status|standby|battle|rename|drop ...       pet operations
  party invite|leave / duel           social invitations
  trade request|offer-item|offer-gold|offer-pet|lock|confirm|cancel
  logout                              leave the world and close the session
  alloc <0-3>                         spend one stat point
  social <setting> <0|1>              toggle party/duel/trade switches
  ride <slot>|off / title equip|text  riding and titles
  click <object-id> / probe <name>    click a map object; send idle probes
  query <c|i|w|j|n|t|g|AI|k0..k9>     request one status stream
  send <FUNC> [args...] / functions   raw escape hatch with schema validation
  log [count]                         recent server events
  wait [duration]                     block until the next server event
  ping                                check the daemon
  stop                                shut the daemon down
  help                                print this list
  version                             print the client version (local, no daemon needed)`

// Dispatch routes one request to its handler.
func (s *Server) Dispatch(ctx context.Context, request Request) Response {
	switch request.Command {
	case "ping":
		return Response{OK: true, Text: "pong"}
	case "help", "commands":
		return Response{OK: true, Text: CommandHelp}
	case "status":
		return s.commandStatus(ctx, request)
	case "observe":
		return s.commandObserve(ctx, request)
	case "chars":
		return s.commandChars(ctx, request)
	case "create-character":
		return s.commandCreateCharacter(ctx, request)
	case "delete-character":
		return s.commandDeleteCharacter(ctx, request)
	case "enter":
		return s.commandEnter(ctx, request)
	case "walk":
		return s.commandWalk(ctx, request)
	case "goto":
		return s.commandGoto(ctx, request)
	case "exits":
		return s.commandExits(ctx, request)
	case "warp":
		return s.commandWarp(ctx, request)
	case "encounters":
		return s.commandEncounters(ctx, request)
	case "seek-encounter":
		return s.commandSeekEncounter(ctx, request)
	case "say":
		return s.commandSay(ctx, request)
	case "look":
		return s.commandLook(ctx, request)
	case "battle":
		return s.commandBattle(ctx, request)
	case "auto-battle":
		return s.commandAutoBattle(ctx, request)
	case "battle-end":
		return s.commandBattleEnd(ctx, request)
	case "battle-help":
		return s.commandBattleHelp(ctx, request)
	case "item":
		return s.commandItem(ctx, request)
	case "mail":
		return s.commandMail(ctx, request)
	case "pet":
		return s.commandPet(ctx, request)
	case "party":
		return s.commandParty(ctx, request)
	case "duel":
		return s.commandDuel(ctx, request)
	case "trade":
		return s.commandTrade(ctx, request)
	case "logout":
		return s.commandLogout(ctx, request)
	case "alloc":
		return s.commandAllocate(ctx, request)
	case "social":
		return s.commandSocial(ctx, request)
	case "query":
		return s.commandQuery(ctx, request)
	case "ride":
		return s.commandRide(ctx, request)
	case "title":
		return s.commandTitle(ctx, request)
	case "click":
		return s.commandClick(ctx, request)
	case "probe":
		return s.commandProbe(ctx, request)
	case "send":
		return s.commandSend(ctx, request)
	case "functions":
		return s.commandFunctions(ctx, request)
	case "talk":
		return s.commandTalk(ctx, request)
	case "choose":
		return s.commandChoose(ctx, request)
	case "reply":
		return s.commandReply(ctx, request)
	case "log":
		return s.commandLog(ctx, request)
	case "wait":
		return s.commandWait(ctx, request)
	case "stop":
		defer s.Stop()
		return Response{OK: true, Text: "sactl daemon stopping"}
	default:
		return failure(KindUsage, "unknown command %q; run `sactl --help` for the command list", request.Command)
	}
}

func (s *Server) commandStatus(ctx context.Context, request Request) Response {
	timeoutCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// Observe first: the session connects lazily, so a header built before the
	// attempt would report "not connected" for a daemon that is about to
	// connect.
	snapshot, err := s.snapshot(timeoutCtx)
	lines := []string{s.statusReport()}
	if err != nil {
		lines = append(lines, "game: "+err.Error())
		return failureWithText(KindSession, strings.Join(lines, "\n"), "%v", err)
	}
	lines = append(lines, s.renderObservation(snapshot))
	text := strings.Join(lines, "\n")
	if request.JSON {
		return Response{OK: true, Text: text, Data: replyJSON(snapshot)}
	}
	return Response{OK: true, Text: text}
}

func (s *Server) commandObserve(ctx context.Context, request Request) Response {
	snapshot, err := s.snapshot(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	text := s.renderObservation(snapshot)
	if request.JSON {
		return Response{OK: true, Text: text, Data: replyJSON(snapshot)}
	}
	return Response{OK: true, Text: text}
}

func (s *Server) commandWalk(ctx context.Context, request Request) Response {
	if len(request.Args) != 1 {
		return failure(KindUsage, "usage: sactl walk <direction>")
	}
	letter, err := directionLetter(request.Args[0])
	if err != nil {
		return failure(KindUsage, "%v", err)
	}
	snapshot, err := s.snapshot(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	if err := requireWorld(snapshot); err != nil {
		return actionFailure(err)
	}
	before := snapshot.Position
	deltaX, deltaY := directionDelta(letter)
	target := ainavigation.Point{X: int(before.X) + deltaX, Y: int(before.Y) + deltaY}
	if err := s.submit(ctx, snapshot.Revision, aigame.Move(before.X, before.Y, letter)); err != nil {
		return executeFailure(err)
	}
	after, err := s.confirmPosition(ctx, before.Floor, target, actionTimeout)
	if err != nil {
		return sessionFailure(err)
	}
	reached := after.Position.X == int32(target.X) && after.Position.Y == int32(target.Y)
	text := fmt.Sprintf("walk %s: (%d,%d) -> (%d,%d) revision=%d", letter, before.X, before.Y, after.Position.X, after.Position.Y, after.Revision)
	if !reached {
		text += fmt.Sprintf("  (blocked: (%d,%d) is not walkable)", target.X, target.Y)
		return Response{OK: false, Kind: KindAction, Text: text, Data: replyJSON(after),
			Error: fmt.Sprintf("walk %s did not move the character to (%d,%d)", letter, target.X, target.Y)}
	}
	return Response{OK: true, Text: text, Data: replyJSON(after)}
}

// walkTo routes within the current floor and walks there in server-sized
// chunks, verifying the observed position between chunks.
func (s *Server) walkTo(ctx context.Context, target ainavigation.Point) (aigame.Snapshot, error) {
	snapshot, err := s.snapshot(ctx)
	if err != nil {
		return aigame.Snapshot{}, err
	}
	if err := requireWorld(snapshot); err != nil {
		return aigame.Snapshot{}, err
	}
	if snapshot.Position.Floor < 0 {
		return aigame.Snapshot{}, errors.New("server position is unknown")
	}
	navigator, err := s.navigator()
	if err != nil {
		return aigame.Snapshot{}, err
	}
	floor := int(snapshot.Position.Floor)
	from := ainavigation.Point{X: int(snapshot.Position.X), Y: int(snapshot.Position.Y)}
	route, err := navigator.Route(floor, from, target)
	if err != nil {
		return aigame.Snapshot{}, fmt.Errorf("no route from (%d,%d) to (%d,%d) on floor %d: %w",
			from.X, from.Y, target.X, target.Y, floor, err)
	}
	if route.Empty() {
		return snapshot, nil
	}
	directions := route.Directions
	points := route.Points
	for offset := 0; offset < len(directions); {
		end := offset + moveRouteChunk
		if end > len(directions) {
			end = len(directions)
		}
		point := points[end-1]
		current, err := s.snapshot(ctx)
		if err != nil {
			return aigame.Snapshot{}, err
		}
		if int(current.Position.Floor) != route.Floor {
			return aigame.Snapshot{}, fmt.Errorf("floor changed to %d during the route", current.Position.Floor)
		}
		if err := s.submit(ctx, current.Revision, aigame.Move(current.Position.X, current.Position.Y, directions[offset:end])); err != nil {
			return aigame.Snapshot{}, err
		}
		stepped, err := s.confirmPosition(ctx, current.Position.Floor, point, actionTimeout)
		if err != nil {
			return aigame.Snapshot{}, err
		}
		if stepped.Position.X != int32(point.X) || stepped.Position.Y != int32(point.Y) {
			return stepped, fmt.Errorf("movement stalled at (%d,%d) on floor %d; expected (%d,%d)",
				stepped.Position.X, stepped.Position.Y, stepped.Position.Floor, point.X, point.Y)
		}
		offset = end
	}
	return s.snapshot(ctx)
}

// commandGoto routes to a tile on the current floor and walks there.
func (s *Server) commandGoto(ctx context.Context, request Request) Response {
	if len(request.Args) != 2 {
		return failure(KindUsage, "usage: sactl goto <x> <y>")
	}
	x, err := strconv.Atoi(request.Args[0])
	if err != nil {
		return failure(KindUsage, "invalid x %q", request.Args[0])
	}
	y, err := strconv.Atoi(request.Args[1])
	if err != nil {
		return failure(KindUsage, "invalid y %q", request.Args[1])
	}
	snapshot, err := s.snapshot(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	if err := requireWorld(snapshot); err != nil {
		return actionFailure(err)
	}
	before := snapshot.Position
	final, err := s.walkTo(ctx, ainavigation.Point{X: x, Y: y})
	if err != nil {
		return actionFailure(err)
	}
	reached := int(final.Position.X) == x && int(final.Position.Y) == y
	text := fmt.Sprintf("goto (%d,%d): (%d,%d) -> (%d,%d) floor=%d",
		x, y, before.X, before.Y, final.Position.X, final.Position.Y, final.Position.Floor)
	if !reached {
		text += "  (not at the requested tile)"
		return Response{OK: false, Kind: KindAction, Text: text, Data: replyJSON(final),
			Error: "did not reach the requested tile"}
	}
	return Response{OK: true, Text: text, Data: replyJSON(final)}
}

func (s *Server) commandSay(ctx context.Context, request Request) Response {
	if len(request.Args) == 0 {
		return failure(KindUsage, "usage: sactl say <text> [--range N] [--color N]")
	}
	text, color, chatRange := parseSayArgs(request.Args)
	if strings.TrimSpace(text) == "" {
		return failure(KindUsage, "say: text is empty")
	}
	// Chat is one CP936 packet field with a hard server-side limit. Report the
	// limit before submitting so the caller can rewrite instead of
	// discovering it as an opaque protocol error.
	if encoded, err := encodeLegacy(text); err != nil {
		return failure(KindUsage, "say: text cannot be encoded for the legacy client: %v", err)
	} else if len(encoded) > maxChatBytes {
		return failure(KindUsage, "say: text is %d bytes but the 2.5 chat field allows %d; shorten it (each Chinese character is 2 bytes)",
			len(encoded), maxChatBytes)
	}
	snapshot, err := s.snapshot(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	if err := requireWorld(snapshot); err != nil {
		return actionFailure(err)
	}
	if err := s.submit(ctx, snapshot.Revision, aigame.Chat(text, color, chatRange)); err != nil {
		return executeFailure(err)
	}
	// Public chat has no delivery acknowledgement; report submission only.
	return Response{
		OK:   true,
		Text: fmt.Sprintf("say submitted (color=%d range=%d): %s", color, chatRange, text),
		Data: replyJSON(map[string]any{"text": text, "color": color, "range": chatRange, "revision": snapshot.Revision}),
	}
}

func (s *Server) commandLog(ctx context.Context, request Request) Response {
	count := 20
	if len(request.Args) > 0 {
		parsed, err := strconv.Atoi(request.Args[0])
		if err != nil || parsed <= 0 {
			return failure(KindUsage, "usage: sactl log [count]")
		}
		count = parsed
	}
	events, _ := s.events.tail(count)
	if len(events) == 0 {
		return Response{OK: true, Text: "no events recorded yet"}
	}
	lines := make([]string, 0, len(events))
	for _, event := range events {
		lines = append(lines, renderEvent(event))
	}
	return Response{OK: true, Text: strings.Join(lines, "\n"), Data: replyJSON(events)}
}

// commandWait blocks until a new server event arrives or the timeout
// expires. This is what lets a terminal agent react instead of polling.
func (s *Server) commandWait(ctx context.Context, request Request) Response {
	timeout := 30 * time.Second
	if len(request.Args) > 0 {
		parsed, err := time.ParseDuration(request.Args[0])
		if err != nil || parsed <= 0 {
			return failure(KindUsage, "usage: sactl wait [duration, e.g. 30s]")
		}
		timeout = parsed
	}
	startSequence := s.events.lastSequence()
	_, notify := s.events.tail(0)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return sessionFailure(ctx.Err())
	case <-notify:
	case <-timer.C:
		return Response{OK: true, Text: fmt.Sprintf("timeout after %s: no new event", timeout)}
	}
	// Give the pump a moment to record every event of the same burst.
	time.Sleep(50 * time.Millisecond)
	events, _ := s.events.tail(100)
	fresh := make([]aigame.Event, 0, len(events))
	for _, event := range events {
		if event.Sequence > startSequence {
			fresh = append(fresh, event)
		}
	}
	lines := make([]string, 0, len(fresh))
	for _, event := range fresh {
		lines = append(lines, renderEvent(event))
	}
	text := fmt.Sprintf("%d new event(s)", len(fresh))
	if len(lines) > 0 {
		text += "\n" + strings.Join(lines, "\n")
	}
	return Response{OK: true, Text: text, Data: replyJSON(fresh)}
}

// confirmPosition asks the server where the character actually is, until it
// reaches target. The 2.5 server does not report an owner's own walk, so a
// client that only waits for events keeps observing a stale tile; the
// read-only S:c status request is what makes the new position visible
// (internal/aiservice/movement.go:250-281). W is never resent here.
func (s *Server) confirmPosition(ctx context.Context, floor int32, target ainavigation.Point, timeout time.Duration) (aigame.Snapshot, error) {
	deadline := time.Now().Add(timeout)
	var last aigame.Snapshot
	for {
		snapshot, err := s.snapshot(ctx)
		if err != nil {
			return aigame.Snapshot{}, err
		}
		last = snapshot
		if snapshot.Position.Floor == floor &&
			snapshot.Position.X == int32(target.X) && snapshot.Position.Y == int32(target.Y) {
			return snapshot, nil
		}
		if time.Now().After(deadline) {
			return last, nil
		}
		if err := s.submit(ctx, snapshot.Revision, aigame.Action{Kind: aigame.ActionStatus, Command: "c"}); err != nil {
			if !errors.Is(err, aigame.ErrStaleRevision) {
				return last, err
			}
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// submit writes one validated action against the observed revision.
func (s *Server) submit(ctx context.Context, revision uint64, action aigame.Action) error {
	game, err := s.session(ctx)
	if err != nil {
		return err
	}
	if err := game.ExecuteExpected(ctx, revision, action); err != nil {
		s.recordError(err)
		return err
	}
	return nil
}

// requireWorld rejects actions that need an entered character.
func requireWorld(snapshot aigame.Snapshot) error {
	switch snapshot.Phase {
	case aigame.PhaseWorld, aigame.PhaseBattle:
		return nil
	case aigame.PhaseAuthenticated, aigame.PhaseCharacterList:
		return fmt.Errorf("no character entered (phase=%s); set `character` in the sactl config", snapshot.Phase)
	default:
		return fmt.Errorf("session is not in the world (phase=%s)", snapshot.Phase)
	}
}

// parseSayArgs extracts text plus optional --color/--range values.
func parseSayArgs(args []string) (string, int32, int32) {
	color := int32(DefaultChatColor)
	chatRange := int32(DefaultChatRange)
	words := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--color", "--range":
			key := args[index]
			if index+1 >= len(args) {
				words = append(words, key)
				continue
			}
			value, err := strconv.Atoi(args[index+1])
			if err != nil {
				words = append(words, key, args[index+1])
				index++
				continue
			}
			if key == "--color" {
				color = int32(value)
			} else {
				chatRange = int32(value)
			}
			index++
		default:
			words = append(words, args[index])
		}
	}
	return strings.Join(words, " "), color, chatRange
}

func sessionFailure(err error) Response {
	return failureWithText(KindSession, "", "%v", err)
}

func actionFailure(err error) Response {
	return failureWithText(KindAction, "", "%v", err)
}

func executeFailure(err error) Response {
	kind := KindAction
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return failure(KindUnknown, "result unknown: %v (do not blindly repeat the action)", err)
	}
	return failureWithText(kind, "", "%v", err)
}

func failureWithText(kind, text, format string, args ...any) Response {
	response := failure(kind, format, args...)
	if text != "" {
		response.Text = text + "\nerror: " + response.Error
	}
	return response
}
