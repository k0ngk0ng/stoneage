package aiservice

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
	"github.com/k0ngk0ng/stoneage/internal/automation"
	"github.com/k0ngk0ng/stoneage/internal/characterbuild"
)

func levelingBuilderFixture(t *testing.T) (BackendBuilder, *GameBackend, *buildGame, context.Context) {
	t.Helper()
	b, f := gameFixture(t)
	g := &buildGame{fakeGame: f}
	b.Session = g
	state, lease, err := b.Gate.Switch(b.Binding.Generation, aicontrol.Leveling, "web leveling")
	if err != nil {
		t.Fatal(err)
	}
	b.Binding.Generation = state.Generation
	store, err := automation.OpenStore(filepath.Join(t.TempDir(), "plans.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	builder, err := NewGameplayBuilder(GameplayConfig{Plans: store, Tiles: oneStepNavigator{}})
	if err != nil {
		t.Fatal(err)
	}
	return builder, b, g, lease
}

func TestGameplayBuilderUsesExplicitBuildForLevelingOwner(t *testing.T) {
	builder, b, g, lease := levelingBuilderFixture(t)
	policy := &characterbuild.Policy{Weights: characterbuild.Weights{Strength: 1, Dexterity: 1}, ReservePoints: 1}
	// A profile value must be ignored for Web leveling, even when it is invalid.
	profile := airuntime.Profile{Goal: airuntime.Goal{CharacterBuild: &airuntime.CharacterBuild{Weights: airuntime.AttributeWeights{}}}}
	value, err := builder(context.Background(), BackendInput{Profile: profile, CharacterBuild: policy, Binding: b.Binding, Gate: b.Gate, Session: g, Knowledge: &aiknowledge.Knowledge{Digest: "data"}, Receipts: b.Receipts, Lease: lease})
	if err != nil {
		t.Fatal(err)
	}
	backend := value.(*GameBackend)
	defer backend.Close()
	if backend.Owner != aicontrol.Leveling || backend.CharacterBuild == nil || backend.CharacterBuild.Weights != policy.Weights || backend.CharacterBuild.ReservePoints != policy.ReservePoints {
		t.Fatalf("explicit leveling policy was not wired: owner=%s build=%+v", backend.Owner, backend.CharacterBuild)
	}
	if backend.Tasks.(*GameTasks).Leveling.CharacterPreparation == nil {
		t.Fatal("leveling character preparation was not wired")
	}
	policy.ReservePoints = 99
	if backend.CharacterBuild.ReservePoints != 1 {
		t.Fatal("explicit policy was not cloned")
	}
}

func TestGameplayBuilderLevelingWithoutExplicitBuildIgnoresProfile(t *testing.T) {
	builder, b, g, lease := levelingBuilderFixture(t)
	profile := airuntime.Profile{Goal: airuntime.Goal{CharacterBuild: &airuntime.CharacterBuild{Weights: airuntime.AttributeWeights{Vital: 1}}}}
	value, err := builder(context.Background(), BackendInput{Profile: profile, Binding: b.Binding, Gate: b.Gate, Session: g, Knowledge: &aiknowledge.Knowledge{Digest: "data"}, Receipts: b.Receipts, Lease: lease})
	if err != nil {
		t.Fatal(err)
	}
	backend := value.(*GameBackend)
	defer backend.Close()
	if backend.CharacterBuild != nil || backend.Tasks.(*GameTasks).Leveling.CharacterPreparation != nil {
		t.Fatal("leveling profile accidentally enabled automatic character allocation")
	}
	if _, err := backend.Observe(context.Background(), b.Binding); err != nil {
		t.Fatal(err)
	}
}

func TestGameplayBuilderRejectsInvalidExplicitLevelingBuild(t *testing.T) {
	builder, b, g, lease := levelingBuilderFixture(t)
	value, err := builder(context.Background(), BackendInput{CharacterBuild: &characterbuild.Policy{}, Binding: b.Binding, Gate: b.Gate, Session: g, Knowledge: &aiknowledge.Knowledge{Digest: "data"}, Receipts: b.Receipts, Lease: lease})
	if err == nil || value != nil {
		t.Fatalf("invalid explicit policy accepted: value=%#v err=%v", value, err)
	}
}

func TestGameActionEnforcesConfiguredPolicyForLevelingOwner(t *testing.T) {
	builder, b, g, lease := levelingBuilderFixture(t)
	policy := &characterbuild.Policy{Weights: characterbuild.Weights{Strength: 1, Dexterity: 1}, ReservePoints: 1}
	g.snapshot.Player.Vital, g.snapshot.Player.Strength, g.snapshot.Player.Toughness, g.snapshot.Player.Dexterity = 5, 5, 5, 5
	g.snapshot.Player.StatPointsKnown, g.snapshot.Player.UnspentStatPoints = true, 3
	value, err := builder(context.Background(), BackendInput{CharacterBuild: policy, Binding: b.Binding, Gate: b.Gate, Session: g, Knowledge: &aiknowledge.Knowledge{Digest: "data"}, Receipts: b.Receipts, Lease: lease})
	if err != nil {
		t.Fatal(err)
	}
	backend := value.(*GameBackend)
	defer backend.Close()
	if _, err := backend.GameAction(context.Background(), b.Binding, aimcp.TypedAction{Kind: "allocate-stat", Index: 0, ExpectedRevision: g.snapshot.Revision}); err == nil || g.writes != 0 {
		t.Fatal("leveling owner bypassed configured policy")
	}
	r, err := backend.GameAction(context.Background(), b.Binding, aimcp.TypedAction{Kind: "allocate-stat", Index: 1, ExpectedRevision: g.snapshot.Revision})
	if err != nil || r.Status != aimcp.ReceiptUnknown || g.writes != 1 {
		t.Fatalf("configured leveling allocation failed: receipt=%+v writes=%d err=%v", r, g.writes, err)
	}
}

func TestCharacterPreparationRunsForLevelingOwnerWithoutReplayingUnknown(t *testing.T) {
	b, g, prep := buildFixture(t)
	state, _, err := b.Gate.Switch(b.Binding.Generation, aicontrol.Leveling, "web leveling")
	if err != nil {
		t.Fatal(err)
	}
	b.Binding.Generation = state.Generation
	b.Owner = aicontrol.Leveling
	ctx := context.Background()
	ready, err := prep.PrepareCharacter(ctx, g.snapshot)
	if err != nil || ready || len(g.actions) != 1 || g.actions[0].Kind != aigame.ActionAllocateStat || g.actions[0].Index != 1 {
		t.Fatalf("leveling preparation did not submit first point: ready=%t actions=%+v err=%v", ready, g.actions, err)
	}
	ready, err = prep.PrepareCharacter(ctx, g.snapshot)
	if err != nil || ready || len(g.actions) != 2 || g.actions[1].Kind != aigame.ActionStatus {
		t.Fatalf("unknown leveling allocation was replayed or not refreshed: ready=%t actions=%+v err=%v", ready, g.actions, err)
	}
	confirmBuildPoint(g, 1)
	ready, err = prep.PrepareCharacter(ctx, g.snapshot)
	if err != nil || ready || len(g.actions) != 3 || g.actions[2].Kind != aigame.ActionAllocateStat || g.actions[2].Index != 3 {
		t.Fatalf("leveling preparation did not continue after confirmation: ready=%t actions=%+v err=%v", ready, g.actions, err)
	}
}

func TestCharacterPreparationRejectsUnsupportedOwner(t *testing.T) {
	b, g, prep := buildFixture(t)
	state, _, err := b.Gate.Switch(b.Binding.Generation, aicontrol.Quest, "quest")
	if err != nil {
		t.Fatal(err)
	}
	b.Binding.Generation = state.Generation
	b.Owner = aicontrol.Quest
	if ready, err := prep.PrepareCharacter(context.Background(), g.snapshot); err == nil || ready || len(g.actions) != 0 {
		t.Fatalf("unsupported owner enabled character preparation: ready=%t actions=%+v err=%v", ready, g.actions, err)
	}
}
