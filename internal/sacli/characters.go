package sacli

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

// defaultCharacterCreate matches the AI provisioning defaults
// (cmd/stoneage-admin/ai_runtime.go:797), which are known-good against the
// 2.5 server: image 100000, face 30000, 5/5/5/5 status, earth 10.
func defaultCharacterCreate() aigame.CharacterCreate {
	return aigame.CharacterCreate{
		Image: 100000, FaceImage: 30000,
		Vital: 5, Strength: 5, Toughness: 5, Dexterity: 5,
		Earth: 10, Hometown: 0,
	}
}

func (s *Server) commandChars(ctx context.Context, request Request) Response {
	game, err := s.session(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	snapshot, err := game.Observe(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	// CharList is only legal at the character list; inside the world the list
	// from login is what the session already holds.
	characters := snapshot.Characters
	if !inWorld(snapshot.Phase) {
		if refreshed, err := game.RefreshCharacters(ctx); err == nil {
			characters = refreshed
		} else {
			return sessionFailure(err)
		}
	}
	if len(characters) == 0 {
		return Response{OK: true, Text: "account has no characters yet; create one with `sactl create-character <name>`",
			Data: replyJSON(characters)}
	}
	lines := make([]string, 0, len(characters))
	for _, character := range characters {
		lines = append(lines, fmt.Sprintf("slot=%d name=%q", character.Slot, character.Name))
	}
	return Response{OK: true, Text: strings.Join(lines, "\n"), Data: replyJSON(characters)}
}

// commandCreateCharacter creates one legacy character with server-compatible
// defaults, then reports the refreshed list.
func (s *Server) commandCreateCharacter(ctx context.Context, request Request) Response {
	if len(request.Args) == 0 {
		return failure(KindUsage, "usage: sactl create-character <name> [--hometown N] [--slot N] [--image N] [--face N] [--vital N --strength N --toughness N --dexterity N] [--earth N --water N --fire N --wind N]")
	}
	create := defaultCharacterCreate()
	create.Name = request.Args[0]
	numbers := map[string]*int32{
		"--hometown": &create.Hometown, "--slot": &create.DataPlace,
		"--image": &create.Image, "--face": &create.FaceImage,
		"--vital": &create.Vital, "--strength": &create.Strength,
		"--toughness": &create.Toughness, "--dexterity": &create.Dexterity,
		"--earth": &create.Earth, "--water": &create.Water,
		"--fire": &create.Fire, "--wind": &create.Wind,
	}
	for index := 1; index < len(request.Args); index++ {
		arg := request.Args[index]
		target, ok := numbers[arg]
		if !ok {
			return failure(KindUsage, "unknown create-character flag %q", arg)
		}
		if index+1 >= len(request.Args) {
			return failure(KindUsage, "%s requires a value", arg)
		}
		value, err := strconv.Atoi(request.Args[index+1])
		if err != nil {
			return failure(KindUsage, "%s requires a number, got %q", arg, request.Args[index+1])
		}
		*target = int32(value)
		index++
	}
	game, err := s.session(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	if err := game.CreateCharacter(ctx, create); err != nil {
		return actionFailure(fmt.Errorf("create character %q: %w", create.Name, err))
	}
	characters, err := game.RefreshCharacters(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	lines := []string{fmt.Sprintf("created character %q", create.Name)}
	for _, character := range characters {
		lines = append(lines, fmt.Sprintf("slot=%d name=%q", character.Slot, character.Name))
	}
	return Response{
		OK:   true,
		Text: strings.Join(lines, "\n"),
		Data: replyJSON(map[string]any{"created": create.Name, "characters": characters}),
	}
}

// commandDeleteCharacter removes a character from the account. It requires
// the account to be at the character list and the name to be repeated, so a
// mistyped command cannot delete a character.
func (s *Server) commandDeleteCharacter(ctx context.Context, request Request) Response {
	if len(request.Args) != 2 {
		return failure(KindUsage, "usage: sactl delete-character <name> <name>  (the name must be repeated on purpose)")
	}
	if request.Args[0] != request.Args[1] {
		return failure(KindUsage, "delete-character: the repeated name does not match %q", request.Args[0])
	}
	name := request.Args[0]
	game, err := s.session(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	snapshot, err := game.Observe(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	if inWorld(snapshot.Phase) {
		return actionFailure(fmt.Errorf("delete-character: %q is currently logged in; leave the world first", snapshot.Character))
	}
	characters, err := game.RefreshCharacters(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	found := false
	for _, character := range characters {
		if character.Name == name {
			found = true
			break
		}
	}
	if !found {
		return actionFailure(fmt.Errorf("delete-character: this account has no character named %q; see `sactl chars`", name))
	}
	if err := game.DeleteCharacter(ctx, name); err != nil {
		return actionFailure(err)
	}
	remaining, err := game.RefreshCharacters(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	lines := []string{fmt.Sprintf("deleted character %q", name)}
	for _, character := range remaining {
		lines = append(lines, fmt.Sprintf("slot=%d name=%q", character.Slot, character.Name))
	}
	return Response{
		OK:   true,
		Text: strings.Join(lines, "\n"),
		Data: replyJSON(map[string]any{"deleted": name, "characters": remaining}),
	}
}

// commandEnter enters a character, which is also how a running daemon
// switches characters without restarting. The name is remembered so that a
// later reconnect returns to the world instead of stopping at the character
// list.
func (s *Server) commandEnter(ctx context.Context, request Request) Response {
	if len(request.Args) != 1 {
		return failure(KindUsage, "usage: sactl enter <character|slot>")
	}
	game, err := s.session(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	snapshot, err := game.Observe(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	name, err := resolveCharacterArg(game, snapshot, request.Args[0])
	if err != nil {
		return actionFailure(err)
	}
	if inWorld(snapshot.Phase) {
		if snapshot.Character == name {
			return Response{OK: true, Text: fmt.Sprintf("already in the world as %q", name), Data: replyJSON(snapshot)}
		}
		return actionFailure(fmt.Errorf("already in the world as %q; stop the daemon to switch characters", snapshot.Character))
	}
	if err := game.EnterCharacter(ctx, name); err != nil {
		return actionFailure(fmt.Errorf("enter character %q: %w", name, err))
	}
	s.setDesiredCharacter(name)
	after, err := game.Observe(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	return Response{
		OK:   true,
		Text: fmt.Sprintf("entered %q\n%s", name, s.renderObservation(after)),
		Data: replyJSON(after),
	}
}

// resolveCharacterArg accepts either a character name or a character-list
// slot, because both are visible in `chars` output and callers try both.
func resolveCharacterArg(game Game, snapshot aigame.Snapshot, argument string) (string, error) {
	characters := snapshot.Characters
	if len(characters) == 0 {
		if refreshed, err := game.RefreshCharacters(context.Background()); err == nil {
			characters = refreshed
		}
	}
	for _, character := range characters {
		if character.Name == argument {
			return character.Name, nil
		}
	}
	if slot, err := strconv.Atoi(argument); err == nil {
		for _, character := range characters {
			if character.Slot == slot {
				return character.Name, nil
			}
		}
		return "", fmt.Errorf("no character in slot %d; see `sactl chars`", slot)
	}
	if len(characters) == 0 {
		return "", fmt.Errorf("no character named %q on this account; create one with `sactl create-character <name>`", argument)
	}
	names := make([]string, 0, len(characters))
	for _, character := range characters {
		names = append(names, fmt.Sprintf("%q(slot %d)", character.Name, character.Slot))
	}
	return "", fmt.Errorf("no character named %q; available: %s", argument, strings.Join(names, ", "))
}
