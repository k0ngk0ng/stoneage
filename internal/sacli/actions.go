package sacli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

// submitSimple submits one action against the freshest revision and reports
// the observation the server produced, waiting briefly for it to change.
func (s *Server) submitSimple(ctx context.Context, action aigame.Action, describe string) Response {
	snapshot, err := s.snapshot(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	if err := requireWorld(snapshot); err != nil {
		return actionFailure(err)
	}
	if err := s.submit(ctx, snapshot.Revision, action); err != nil {
		return executeFailure(err)
	}
	after, err := s.settle(ctx, snapshot.Revision, 1500*time.Millisecond)
	if err != nil {
		return sessionFailure(err)
	}
	return Response{
		OK:   true,
		Text: fmt.Sprintf("%s\n%s", describe, s.renderObservation(after)),
		Data: replyJSON(after),
	}
}

// settle observes until the revision moves past the submitted one, or the
// timeout expires, so a command reports what its action caused.
func (s *Server) settle(ctx context.Context, revision uint64, timeout time.Duration) (aigame.Snapshot, error) {
	deadline := time.Now().Add(timeout)
	for {
		snapshot, err := s.snapshot(ctx)
		if err != nil {
			return aigame.Snapshot{}, err
		}
		if snapshot.Revision > revision || time.Now().After(deadline) {
			return snapshot, nil
		}
		select {
		case <-ctx.Done():
			return snapshot, ctx.Err()
		case <-time.After(120 * time.Millisecond):
		}
	}
}

// commandLook turns the character without moving.
func (s *Server) commandLook(ctx context.Context, request Request) Response {
	if len(request.Args) != 1 {
		return failure(KindUsage, "usage: sactl look <direction>")
	}
	letter, err := directionLetter(request.Args[0])
	if err != nil {
		return failure(KindUsage, "%v", err)
	}
	deltaX, deltaY := directionDelta(letter)
	clientDirection, ok := clientDirectionFor(deltaX, deltaY)
	if !ok {
		return failure(KindUsage, "look: %q is not a legal direction", request.Args[0])
	}
	return s.submitSimple(ctx, aigame.Look(wireDirection(clientDirection)), fmt.Sprintf("looked %s", request.Args[0]))
}

// commandBattle submits one battle command. The vocabulary is the native one
// (H|FF attack, T|FF defend, S|nn|FF skill, C|.. capture, I|.. item, E|.. escape,
// N wait, G give up, HELP), and the server validates it again.
func (s *Server) commandBattle(ctx context.Context, request Request) Response {
	if len(request.Args) == 0 {
		return failure(KindUsage, "usage: sactl battle <command> (e.g. H|FF attack, T|FF defend, S|01|FF skill, N wait, G give up, HELP)")
	}
	command := strings.Join(request.Args, "")
	return s.submitSimple(ctx, aigame.Battle(command), fmt.Sprintf("battle command %q", command))
}

// commandBattleEnd acknowledges a finished battle (native EO).
func (s *Server) commandBattleEnd(ctx context.Context, request Request) Response {
	return s.submitSimple(ctx, aigame.EndBattle(), "battle ended")
}

// commandItem covers the inventory and field item operations.
func (s *Server) commandItem(ctx context.Context, request Request) Response {
	if len(request.Args) == 0 {
		return failure(KindUsage, "usage: sactl item use|drop|drop-gold|move|magic|pickup ...")
	}
	snapshot, err := s.snapshot(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	if err := requireWorld(snapshot); err != nil {
		return actionFailure(err)
	}
	x, y := snapshot.Position.X, snapshot.Position.Y
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
	var action aigame.Action
	var describe string
	switch request.Args[0] {
	case "use":
		slot, err := number(1, "item use <slot>")
		if err != nil {
			return failure(KindUsage, "%v", err)
		}
		target := int32(-1)
		if len(request.Args) > 2 {
			if target, err = number(2, "target"); err != nil {
				return failure(KindUsage, "%v", err)
			}
		}
		action, describe = aigame.UseItem(x, y, slot, target), fmt.Sprintf("used item in slot %d", slot)
	case "drop":
		slot, err := number(1, "item drop <slot>")
		if err != nil {
			return failure(KindUsage, "%v", err)
		}
		action = aigame.Action{Kind: aigame.ActionItem, Command: "drop", X: x, Y: y, Index: slot}
		describe = fmt.Sprintf("dropped item in slot %d", slot)
	case "drop-gold", "gold":
		amount, err := number(1, "item drop-gold <amount>")
		if err != nil {
			return failure(KindUsage, "%v", err)
		}
		action = aigame.Action{Kind: aigame.ActionItem, Command: "drop-gold", X: x, Y: y, Value: amount}
		describe = fmt.Sprintf("dropped %d gold", amount)
	case "move":
		from, err := number(1, "item move <from> <to>")
		if err != nil {
			return failure(KindUsage, "%v", err)
		}
		to, err := number(2, "item move <from> <to>")
		if err != nil {
			return failure(KindUsage, "%v", err)
		}
		action, describe = aigame.ItemMove(from, to), fmt.Sprintf("moved slot %d to %d", from, to)
	case "magic":
		index, err := number(1, "item magic <index> <target>")
		if err != nil {
			return failure(KindUsage, "%v", err)
		}
		target, err := number(2, "item magic <index> <target>")
		if err != nil {
			return failure(KindUsage, "%v", err)
		}
		action = aigame.Action{Kind: aigame.ActionItem, Command: "magic", X: x, Y: y, Index: index, TargetID: target}
		describe = fmt.Sprintf("used magic %d on %d", index, target)
	case "pickup":
		action = aigame.Action{Kind: aigame.ActionItem, Command: "pickup", X: x, Y: y, Direction: -1}
		describe = "picked up what is here"
	default:
		return failure(KindUsage, "item: unknown operation %q (use, drop, drop-gold, move, magic, pickup)", request.Args[0])
	}
	return s.submitSimple(ctx, action, describe)
}

// commandMail covers the native address book and mail.
func (s *Server) commandMail(ctx context.Context, request Request) Response {
	if len(request.Args) == 0 {
		return failure(KindUsage, "usage: sactl mail list | mail add | mail send <slot> <text> | mail remove-contact <slot>")
	}
	snapshot, err := s.snapshot(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	if err := requireWorld(snapshot); err != nil {
		return actionFailure(err)
	}
	switch request.Args[0] {
	case "list":
		return s.submitSimple(ctx, aigame.Mailbox(), "requested the address book")
	case "add":
		return s.submitSimple(ctx, aigame.AddMailContact(snapshot.Position.X, snapshot.Position.Y),
			"asked the character in front to exchange cards")
	case "send":
		if len(request.Args) < 3 {
			return failure(KindUsage, "usage: sactl mail send <slot> <text>")
		}
		slot, err := strconv.Atoi(request.Args[1])
		if err != nil {
			return failure(KindUsage, "mail send: slot must be a number")
		}
		text := strings.Join(request.Args[2:], " ")
		if encoded, err := encodeLegacy(text); err != nil {
			return failure(KindUsage, "mail send: %v", err)
		} else if len(encoded) > 70 {
			return failure(KindUsage, "mail send: text is %d bytes but the mail field allows 70", len(encoded))
		}
		return s.submitSimple(ctx, aigame.Mail(int32(slot), text, DefaultChatColor),
			fmt.Sprintf("sent mail to address-book slot %d", slot))
	case "remove-contact":
		if len(request.Args) != 2 {
			return failure(KindUsage, "usage: sactl mail remove-contact <slot>")
		}
		slot, err := strconv.Atoi(request.Args[1])
		if err != nil {
			return failure(KindUsage, "mail remove-contact: slot must be a number")
		}
		return s.rawSend(ctx, "DAB", []string{request.Args[1]}, fmt.Sprintf("removed address-book slot %d", slot))
	default:
		return failure(KindUsage, "mail: unknown operation %q (list, add, send, remove-contact)", request.Args[0])
	}
}

// commandPet covers pet status, standby masks and the raw-backed pet actions.
func (s *Server) commandPet(ctx context.Context, request Request) Response {
	if len(request.Args) < 2 {
		return failure(KindUsage, "usage: sactl pet status <slot> <0|1> | standby <mask> | battle <slot> | rename <slot> <name> | drop <slot>")
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
	case "status":
		slot, err := number(1, "pet status <slot> <0|1>")
		if err != nil {
			return failure(KindUsage, "%v", err)
		}
		value, err := number(2, "pet status <slot> <0|1>")
		if err != nil {
			return failure(KindUsage, "%v", err)
		}
		return s.submitSimple(ctx, aigame.SetPetStatus(slot, value), fmt.Sprintf("pet slot %d status -> %d", slot, value))
	case "standby":
		mask, err := number(1, "pet standby <mask 0..31>")
		if err != nil {
			return failure(KindUsage, "%v", err)
		}
		action := aigame.Action{Kind: aigame.ActionPet, Command: "standby", Value: mask}
		return s.submitSimple(ctx, action, fmt.Sprintf("pet standby mask -> %d", mask))
	case "battle":
		slot, err := number(1, "pet battle <slot>")
		if err != nil {
			return failure(KindUsage, "%v", err)
		}
		return s.rawSend(ctx, "KS", []string{request.Args[1]}, fmt.Sprintf("chose battle pet slot %d", slot))
	case "rename":
		if len(request.Args) < 3 {
			return failure(KindUsage, "usage: sactl pet rename <slot> <name>")
		}
		slot, err := number(1, "pet rename <slot> <name>")
		if err != nil {
			return failure(KindUsage, "%v", err)
		}
		name := strings.Join(request.Args[2:], " ")
		return s.rawSend(ctx, "KN", []string{strconv.Itoa(int(slot)), name}, fmt.Sprintf("renamed pet slot %d to %q", slot, name))
	case "drop":
		slot, err := number(1, "pet drop <slot>")
		if err != nil {
			return failure(KindUsage, "%v", err)
		}
		return s.rawSend(ctx, "DP", []string{request.Args[1], "0", "0"}, fmt.Sprintf("dropped pet slot %d", slot))
	default:
		return failure(KindUsage, "pet: unknown operation %q (status, standby, battle, rename, drop)", request.Args[0])
	}
}

// commandParty invites the character in front, or accepts/leaves.
func (s *Server) commandParty(ctx context.Context, request Request) Response {
	if len(request.Args) != 1 {
		return failure(KindUsage, "usage: sactl party invite|leave")
	}
	var value int32
	switch request.Args[0] {
	case "invite", "accept", "1":
		value = 1
	case "leave", "decline", "0":
		value = 0
	default:
		return failure(KindUsage, "party: unknown action %q (invite, leave)", request.Args[0])
	}
	snapshot, err := s.snapshot(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	if err := requireWorld(snapshot); err != nil {
		return actionFailure(err)
	}
	action := aigame.Party(snapshot.Position.X, snapshot.Position.Y, value)
	return s.submitSimple(ctx, action, fmt.Sprintf("party request %d", value))
}

// commandDuel challenges the character in front.
func (s *Server) commandDuel(ctx context.Context, request Request) Response {
	snapshot, err := s.snapshot(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	if err := requireWorld(snapshot); err != nil {
		return actionFailure(err)
	}
	return s.submitSimple(ctx, aigame.Duel(snapshot.Position.X, snapshot.Position.Y), "challenged the character in front to a duel")
}

// commandAllocate spends one available stat point on the given attribute.
func (s *Server) commandAllocate(ctx context.Context, request Request) Response {
	if len(request.Args) != 1 {
		return failure(KindUsage, "usage: sactl alloc <0 vital|1 strength|2 toughness|3 dexterity>")
	}
	index, err := strconv.Atoi(request.Args[0])
	if err != nil || index < 0 || index > 3 {
		return failure(KindUsage, "alloc: index must be 0 (vital), 1 (strength), 2 (toughness) or 3 (dexterity)")
	}
	action := aigame.Action{Kind: aigame.ActionAllocateStat, Index: int32(index)}
	return s.submitSimple(ctx, action, fmt.Sprintf("allocated one point to attribute %d", index))
}

// commandSocial toggles one native social switch.
func (s *Server) commandSocial(ctx context.Context, request Request) Response {
	if len(request.Args) != 2 {
		return failure(KindUsage, "usage: sactl social <party|duel|party-chat|trade-card|trade> <0|1>")
	}
	value, err := strconv.Atoi(request.Args[1])
	if err != nil || (value != 0 && value != 1) {
		return failure(KindUsage, "social: value must be 0 or 1")
	}
	action := aigame.Action{Kind: aigame.ActionSocialSetting, Command: request.Args[0], Value: int32(value)}
	return s.submitSimple(ctx, action, fmt.Sprintf("social setting %s -> %d", request.Args[0], value))
}

// commandQuery asks the server for one status/projection stream.
func (s *Server) commandQuery(ctx context.Context, request Request) Response {
	if len(request.Args) != 1 {
		return failure(KindUsage, "usage: sactl query <c|i|w|j|n|t|g|AI|k0..k9>")
	}
	action := aigame.Action{Kind: aigame.ActionStatus, Command: request.Args[0]}
	return s.submitSimple(ctx, action, fmt.Sprintf("requested status %q", request.Args[0]))
}

// commandRide mounts or dismounts a pet slot (native FM, -1 dismounts).
func (s *Server) commandRide(ctx context.Context, request Request) Response {
	if len(request.Args) != 1 {
		return failure(KindUsage, "usage: sactl ride <slot> | sactl ride off")
	}
	slot := -1
	if request.Args[0] != "off" {
		parsed, err := strconv.Atoi(request.Args[0])
		if err != nil {
			return failure(KindUsage, "ride: slot must be a number or 'off'")
		}
		slot = parsed
	}
	return s.rawSend(ctx, "FM", []string{fmt.Sprintf("R|P|%d", slot)}, fmt.Sprintf("ride slot %d", slot))
}

// commandTitle equips a title or renames it.
func (s *Server) commandTitle(ctx context.Context, request Request) Response {
	if len(request.Args) < 1 {
		return failure(KindUsage, "usage: sactl title equip <index> | sactl title text <title>")
	}
	switch request.Args[0] {
	case "equip":
		if len(request.Args) != 2 {
			return failure(KindUsage, "usage: sactl title equip <index>")
		}
		if _, err := strconv.Atoi(request.Args[1]); err != nil {
			return failure(KindUsage, "title equip: index must be a number")
		}
		return s.rawSend(ctx, "ST", []string{request.Args[1]}, "equipped title "+request.Args[1])
	case "text":
		if len(request.Args) < 2 {
			return failure(KindUsage, "usage: sactl title text <title>")
		}
		text := strings.Join(request.Args[1:], " ")
		return s.rawSend(ctx, "FT", []string{text}, fmt.Sprintf("requested title text %q", text))
	default:
		return failure(KindUsage, "title: unknown operation %q (equip, text)", request.Args[0])
	}
}

// commandBattleHelp toggles the native battle help flag.
func (s *Server) commandBattleHelp(ctx context.Context, request Request) Response {
	if len(request.Args) != 1 {
		return failure(KindUsage, "usage: sactl battle-help <0|1>")
	}
	value, err := strconv.Atoi(request.Args[0])
	if err != nil || (value != 0 && value != 1) {
		return failure(KindUsage, "battle-help: value must be 0 or 1")
	}
	return s.rawSend(ctx, "HL", []string{request.Args[0]}, fmt.Sprintf("battle help -> %d", value))
}

// commandClick clicks a map object by its observed id (native C).
func (s *Server) commandClick(ctx context.Context, request Request) Response {
	if len(request.Args) != 1 {
		return failure(KindUsage, "usage: sactl click <object-id>")
	}
	if _, err := strconv.Atoi(request.Args[0]); err != nil {
		return failure(KindUsage, "click: object id must be a number")
	}
	return s.rawSend(ctx, "C", []string{request.Args[0]}, "clicked object "+request.Args[0])
}

// commandProbe sends the client's idle probes, which are how the preserved
// client keeps the session warm and asks how many players are online.
func (s *Server) commandProbe(ctx context.Context, request Request) Response {
	if len(request.Args) == 0 {
		return failure(KindUsage, "usage: sactl probe <procget|playernumget|echo>")
	}
	switch request.Args[0] {
	case "procget":
		return s.rawSend(ctx, "ProcGet", nil, "sent ProcGet")
	case "playernumget":
		return s.rawSend(ctx, "PlayerNumGet", nil, "sent PlayerNumGet")
	case "echo":
		text := "sactl"
		if len(request.Args) > 1 {
			text = strings.Join(request.Args[1:], " ")
		}
		return s.rawSend(ctx, "Echo", []string{text}, "sent Echo")
	default:
		return failure(KindUsage, "probe: unknown probe %q (procget, playernumget, echo)", request.Args[0])
	}
}
