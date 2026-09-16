package aiservice

import (
	"context"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

func TestCommunicationUnknownDoesNotBlockIndependentStatAllocation(t *testing.T) {
	for _, test := range []struct {
		kind, command string
		blocks        bool
	}{
		{"chat", "", false}, {"mail", "list", false}, {"mail", "send", false},
		{"mail", "add", true}, {"item", "use", true}, {"trade", "confirm", true}, {"allocate-stat", "", true},
	} {
		t.Run(test.kind+"-"+test.command, func(t *testing.T) {
			ctx := context.Background()
			b, game := gameFixture(t)
			b.CharacterBuild = &airuntime.CharacterBuild{Weights: airuntime.AttributeWeights{Vital: 1}}
			game.snapshot.Player.StatPointsKnown = true
			game.snapshot.Player.UnspentStatPoints = 3
			game.snapshot.Player.Vital, game.snapshot.Player.Strength = 5, 5
			game.snapshot.Player.Toughness, game.snapshot.Player.Dexterity = 5, 5
			prior, err := b.Receipts.Prepare(ctx, b.Binding, aimcp.TypedAction{Kind: test.kind, Command: test.command, ExpectedRevision: 1})
			if err != nil {
				t.Fatal(err)
			}
			observation, err := b.Observe(ctx, b.Binding)
			if err != nil {
				t.Fatal(err)
			}
			if observation.Flags["build:allocation_allowed"] == test.blocks {
				t.Fatal("allocation projection ignored dependency class")
			}
			result, err := b.GameAction(ctx, b.Binding, aimcp.TypedAction{Kind: "allocate-stat", Index: 0, ExpectedRevision: game.snapshot.Revision})
			if test.blocks {
				if err == nil || game.writes != 0 {
					t.Fatal("unknown state mutation did not block allocation")
				}
			} else if err != nil || game.writes != 1 || result.Status != aimcp.ReceiptUnknown {
				t.Fatalf("communication blocked allocation or fabricated success: %+v %v", result, err)
			}
			stored, err := b.Receipts.Load(ctx, b.Binding, prior.Handle)
			if err != nil || stored.Status != aimcp.ReceiptUnknown {
				t.Fatal("prior unknown outcome was rewritten")
			}
		})
	}
}
