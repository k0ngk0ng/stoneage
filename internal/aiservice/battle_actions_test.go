package aiservice

import (
	"context"
	"errors"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
)

type battleActionGame struct {
	*fakeGame
	actions []aigame.Action
}

func (g *battleActionGame) ExecuteExpected(ctx context.Context, revision uint64, action aigame.Action) error {
	if err := g.fakeGame.ExecuteExpected(ctx, revision, action); err != nil {
		return err
	}
	g.actions = append(g.actions, action)
	return nil
}

func TestGameActionTranslatesSemanticBattleCommands(t *testing.T) {
	for _, tc := range []struct {
		command       string
		index, target int32
		wire          string
	}{
		{"escape", 0, 0, "E"}, {"defend", 0, 0, "G"}, {"guard", 0, 0, "G"},
		{"wait", 0, 0, "N"}, {"attack", 0, 10, "H|A"},
		{"pet", 2, 10, "W|2|A"}, {"pet", 255, 255, "W|FF|FF"},
		{"item", 12, 0, "I|C|0"}, {"skill", 3, 19, "J|3|13"},
	} {
		t.Run(tc.command+tc.wire, func(t *testing.T) {
			backend, fake := gameFixture(t)
			game := &battleActionGame{fakeGame: fake}
			backend.Session = game
			action := aimcp.TypedAction{Kind: "battle", ExpectedRevision: fake.snapshot.Revision, Command: tc.command, Index: tc.index, TargetID: tc.target}
			receipt, err := backend.GameAction(context.Background(), backend.Binding, action)
			if err != nil || len(game.actions) != 1 || game.actions[0].Command != tc.wire {
				t.Fatalf("actions=%+v receipt=%+v err=%v", game.actions, receipt, err)
			}
			// Translation must not bypass the write-ahead receipt or turn a
			// successful socket write into an invented game outcome.
			if receipt.Status != aimcp.ReceiptUnknown {
				t.Fatalf("write became confirmed game result: %+v", receipt)
			}
			again, err := backend.GameAction(context.Background(), backend.Binding, action)
			if err != nil || again.Handle != receipt.Handle || len(game.actions) != 1 {
				t.Fatalf("receipt retry resubmitted the battle command: %+v %v", again, err)
			}
		})
	}
}

func TestGameActionRejectsRawAndMalformedBattleCommandsBeforeWrite(t *testing.T) {
	for _, action := range []aimcp.TypedAction{
		{Command: "E"}, {Command: "H|A"}, {Command: "escape|E"},
		{Command: "attack", TargetID: -1}, {Command: "attack", TargetID: 20},
		{Command: "pet", Index: -1}, {Command: "pet", Index: 256},
		{Command: "skill", TargetID: -1}, {Command: "item", TargetID: 256},
	} {
		backend, fake := gameFixture(t)
		action.Kind, action.ExpectedRevision = "battle", fake.snapshot.Revision
		_, err := backend.GameAction(context.Background(), backend.Binding, action)
		if !errors.Is(err, aimcp.ErrInvalidParams) || fake.writes != 0 {
			t.Fatalf("invalid semantic action %+v wrote %d packets: %v", action, fake.writes, err)
		}
	}
}
