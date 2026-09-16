package aiservice

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aileveling"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

type buildGame struct {
	*fakeGame
	actions []aigame.Action
}

func (g *buildGame) ExecuteExpected(ctx context.Context, rev uint64, a aigame.Action) error {
	if rev != g.snapshot.Revision {
		return aigame.ErrStaleRevision
	}
	g.actions = append(g.actions, a)
	return g.fakeGame.ExecuteExpected(ctx, rev, a)
}

func buildFixture(t *testing.T) (*GameBackend, *buildGame, *LevelingCharacterBuild) {
	t.Helper()
	b, f := gameFixture(t)
	g := &buildGame{fakeGame: f}
	b.Session = g
	b.OwnStateRefresh = &OwnStateRefresher{}
	b.CharacterBuild = &airuntime.CharacterBuild{Weights: airuntime.AttributeWeights{Strength: 1, Dexterity: 1}, ReservePoints: 1}
	p := &g.snapshot.Player
	p.Vital, p.Strength, p.Toughness, p.Dexterity = 5, 5, 5, 5
	p.StatPointsKnown, p.UnspentStatPoints, p.Level, p.MaxHP = true, 3, 5, 10
	return b, g, &LevelingCharacterBuild{Backend: b}
}

func confirmBuildPoint(g *buildGame, index int) {
	p := &g.snapshot.Player
	attributes := []*int32{&p.Vital, &p.Strength, &p.Toughness, &p.Dexterity}
	*attributes[index]++
	p.UnspentStatPoints--
	p.StatPointsKnown = true
	g.snapshot.Revision++
	g.snapshot.AIObservationRevision = g.snapshot.Revision
}

func TestLevelingCharacterBuildWaitsForEachPointAndPreservesReserve(t *testing.T) {
	b, g, prep := buildFixture(t)
	ctx := context.Background()
	ready, err := prep.PrepareCharacter(ctx, g.snapshot)
	if err != nil || ready || len(g.actions) != 1 || g.actions[0].Kind != aigame.ActionAllocateStat || g.actions[0].Index != 1 {
		t.Fatalf("first point: ready=%t actions=%+v err=%v", ready, g.actions, err)
	}
	ready, err = prep.PrepareCharacter(ctx, g.snapshot)
	if err != nil || ready || len(g.actions) != 2 || g.actions[1].Kind != aigame.ActionStatus || g.actions[1].Command != "AI" {
		t.Fatalf("unconfirmed point replayed: ready=%t actions=%+v err=%v", ready, g.actions, err)
	}
	confirmBuildPoint(g, 1)
	ready, err = prep.PrepareCharacter(ctx, g.snapshot)
	if err != nil || ready || len(g.actions) != 3 || g.actions[2].Kind != aigame.ActionAllocateStat || g.actions[2].Index != 3 {
		t.Fatalf("second point: ready=%t actions=%+v err=%v", ready, g.actions, err)
	}
	confirmBuildPoint(g, 3)
	ready, err = prep.PrepareCharacter(ctx, g.snapshot)
	if err != nil || !ready || len(g.actions) != 3 {
		t.Fatalf("reserve not ready: ready=%t actions=%+v err=%v", ready, g.actions, err)
	}
	if unknown, err := b.Receipts.HasUnknown(ctx, b.Binding, ""); err != nil || unknown {
		t.Fatalf("confirmed points left unknown: %t %v", unknown, err)
	}
}

func TestLevelingCharacterBuildRefreshesUnknownAndRejectsStaleWrites(t *testing.T) {
	_, g, prep := buildFixture(t)
	ctx := context.Background()
	g.snapshot.Player.StatPointsKnown = false
	ready, err := prep.PrepareCharacter(ctx, g.snapshot)
	if err != nil || ready || len(g.actions) != 1 || g.actions[0].Kind != aigame.ActionStatus {
		t.Fatalf("unknown points: %t %+v %v", ready, g.actions, err)
	}
	g.snapshot.Player.StatPointsKnown = true
	old := g.snapshot
	g.snapshot.Revision++
	ready, err = prep.PrepareCharacter(ctx, old)
	if err != nil || ready || len(g.actions) != 1 {
		t.Fatalf("stale snapshot wrote: %t %+v %v", ready, g.actions, err)
	}
}

func TestLevelingCharacterBuildRejectsOldUnknownReceipt(t *testing.T) {
	b, g, _ := buildFixture(t)
	ctx := context.Background()
	if _, err := b.Receipts.Prepare(ctx, b.Binding, aimcp.TypedAction{Kind: "allocate-stat", Index: 1, ExpectedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	prep := &LevelingCharacterBuild{Backend: b}
	if ready, err := prep.PrepareCharacter(ctx, g.snapshot); err == nil || ready || len(g.actions) != 0 {
		t.Fatalf("old unknown allocation replayed: %t %+v %v", ready, g.actions, err)
	}
}

func TestLevelingCharacterBuildOwnsAllocationsDuringActiveTask(t *testing.T) {
	b, g, prep := buildFixture(t)
	b.Tasks = &factoryTestTasks{receipts: []aimcp.TaskReceipt{{Handle: "leveling", Status: aimcp.ReceiptRunning}}}
	ctx := context.Background()
	if _, err := b.GameAction(ctx, b.Binding, aimcp.TypedAction{Kind: "allocate-stat", Index: 1, ExpectedRevision: g.snapshot.Revision}); err == nil || len(g.actions) != 0 {
		t.Fatal("MCP point allocation interfered with active task")
	}
	if ready, err := prep.PrepareCharacter(ctx, g.snapshot); err != nil || ready || len(g.actions) != 1 || g.actions[0].Kind != aigame.ActionAllocateStat {
		t.Fatalf("idle hook could not prepare character: ready=%t actions=%+v err=%v", ready, g.actions, err)
	}
}

func TestGameplayLevelTargetWaitsForConfiguredBuild(t *testing.T) {
	for _, alreadyAtTarget := range []bool{true, false} {
		t.Run(map[bool]string{true: "start-at-target", false: "level-up-during-run"}[alreadyAtTarget], func(t *testing.T) {
			b, g, _ := buildFixture(t)
			g.snapshot.Player.UnspentStatPoints = 2
			if !alreadyAtTarget {
				g.snapshot.Player.Level = 4
			}
			store, err := automation.OpenStore(filepath.Join(t.TempDir(), "build-plans.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			builder, err := NewGameplayBuilder(GameplayConfig{Plans: store, Tiles: oneStepNavigator{}})
			if err != nil {
				t.Fatal(err)
			}
			profile := airuntime.Profile{Goal: airuntime.Goal{CharacterBuild: b.CharacterBuild.Clone()}}
			value, err := builder(context.Background(), BackendInput{Profile: profile, Binding: b.Binding, Gate: b.Gate, Session: g, Knowledge: &aiknowledge.Knowledge{Digest: "data"}, Receipts: b.Receipts, Lease: context.Background()})
			if err != nil {
				t.Fatal(err)
			}
			backend := value.(*GameBackend)
			defer backend.Close()
			profile.Goal.CharacterBuild.ReservePoints = 99
			if backend.CharacterBuild.ReservePoints != 1 {
				t.Fatal("policy was not cloned for the session")
			}
			coordinator := backend.Tasks.(*GameTasks).Leveling
			if coordinator.CharacterPreparation == nil {
				t.Fatal("build preparation not wired")
			}
			r, err := coordinator.Start(context.Background(), aileveling.StartRequest{TargetKind: "character", TargetLevel: 5})
			if err != nil || r.Status != aimcp.ReceiptRunning {
				t.Fatalf("target bypassed preparation: %+v %v", r, err)
			}
			g.snapshot.Player.Level = 5
			checkpoint, err := coordinator.Tick(context.Background(), r.Handle)
			if err != nil || checkpoint.Status != automation.Running || len(g.actions) != 1 || g.actions[0].Kind != aigame.ActionAllocateStat {
				t.Fatalf("target skipped allocation: %+v %+v %v", checkpoint, g.actions, err)
			}
			confirmBuildPoint(g, 1)
			checkpoint, err = coordinator.Tick(context.Background(), r.Handle)
			if err != nil || checkpoint.Status != automation.Completed || len(g.actions) != 1 {
				t.Fatalf("build did not complete target: %+v %+v %v", checkpoint, g.actions, err)
			}
		})
	}
}

func TestGoalReachedWaitsForBuildSettlement(t *testing.T) {
	goal := airuntime.Goal{TargetLevel: 5, StopWhenCompleted: true, CharacterBuild: &airuntime.CharacterBuild{Weights: airuntime.AttributeWeights{Vital: 1}}}
	o := aimcp.Observation{Connected: true, Ready: true, Character: aimcp.Entity{Level: 5}, Flags: map[string]bool{"build:configured": true}}
	if goalReached(o, goal) {
		t.Fatal("supervisor would finish before configured build")
	}
	o.Flags["build:settled"] = true
	if !goalReached(o, goal) {
		t.Fatal("settled build blocked reached target")
	}
}
