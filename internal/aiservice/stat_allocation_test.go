package aiservice

import (
	"context"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

func TestStatPointsProjectionAndSingleSubmissionReceipt(t *testing.T) {
	b, game := gameFixture(t)
	o, err := b.Observe(context.Background(), b.Binding)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := o.OwnProgress["stat_points"]; present {
		t.Fatal("unknown points projected as zero")
	}
	game.snapshot.Player.StatPointsKnown = true
	game.snapshot.Player.UnspentStatPoints = 0
	game.snapshot.Player.Vital = 5
	o, err = b.Observe(context.Background(), b.Binding)
	if err != nil || !o.Flags["stat_points:known"] || o.OwnProgress["attribute_vital"] != 5 {
		t.Fatalf("projection: %+v %v", o, err)
	}
	if value, present := o.OwnProgress["stat_points"]; !present || value != 0 {
		t.Fatal("authoritative zero omitted")
	}
	game.snapshot.Player.UnspentStatPoints = 2
	game.snapshot.Player.Strength, game.snapshot.Player.Toughness, game.snapshot.Player.Dexterity = 5, 5, 5
	b.CharacterBuild = &airuntime.CharacterBuild{Weights: airuntime.AttributeWeights{Vital: 1}}
	a := aimcp.TypedAction{Kind: "allocate-stat", Index: 0, ExpectedRevision: game.snapshot.Revision}
	first, err := b.GameAction(context.Background(), b.Binding, a)
	if err != nil || first.Status != aimcp.ReceiptUnknown {
		t.Fatalf("write claimed allocation success: %+v %v", first, err)
	}
	second, err := b.GameAction(context.Background(), b.Binding, a)
	if err != nil || first.Handle != second.Handle || game.writes != 1 {
		t.Fatalf("repeated allocation: %+v %v writes=%d", second, err, game.writes)
	}
}

func TestConfiguredBuildSinglePointConfirmationAndReserve(t *testing.T) {
	b, game := gameFixture(t)
	b.CharacterBuild = &airuntime.CharacterBuild{Weights: airuntime.AttributeWeights{Strength: 1, Dexterity: 1}, ReservePoints: 1}
	p := &game.snapshot.Player
	p.Vital, p.Strength, p.Toughness, p.Dexterity = 5, 5, 5, 5
	p.StatPointsKnown, p.UnspentStatPoints = true, 2
	ctx := context.Background()
	o, err := b.Observe(ctx, b.Binding)
	if err != nil || !o.Flags["build:allocation_allowed"] || o.OwnProgress["build_next_stat"] != 1 {
		t.Fatalf("build suggestion: %+v %v", o, err)
	}
	if _, err := b.GameAction(ctx, b.Binding, aimcp.TypedAction{Kind: "allocate-stat", Index: 0, ExpectedRevision: o.Revision}); err == nil || game.writes != 0 {
		t.Fatal("model overrode configured build")
	}
	r, err := b.GameAction(ctx, b.Binding, aimcp.TypedAction{Kind: "allocate-stat", Index: 1, ExpectedRevision: o.Revision})
	if err != nil || r.Status != aimcp.ReceiptUnknown || game.writes != 1 {
		t.Fatalf("submission: %+v %v", r, err)
	}
	p.Strength++
	game.snapshot.Revision++
	got, err := b.TaskStatus(ctx, b.Binding, r.Handle)
	if err != nil || got.Status != aimcp.ReceiptUnknown {
		t.Fatalf("attribute alone confirmed: %+v %v", got, err)
	}
	p.UnspentStatPoints--
	got, err = b.TaskStatus(ctx, b.Binding, r.Handle)
	if err != nil || got.Status != aimcp.ReceiptUnknown {
		t.Fatalf("missing fresh own-state reply confirmed: %+v %v", got, err)
	}
	game.snapshot.AIObservationRevision = game.snapshot.Revision
	p.Dexterity++
	got, err = b.TaskStatus(ctx, b.Binding, r.Handle)
	if err != nil || got.Status != aimcp.ReceiptUnknown {
		t.Fatalf("ambiguous second attribute change confirmed: %+v %v", got, err)
	}
	p.Dexterity--
	got, err = b.TaskStatus(ctx, b.Binding, r.Handle)
	if err != nil || got.Status != aimcp.ReceiptConfirmed || game.writes != 1 {
		t.Fatalf("confirmed receipt: %+v %v writes=%d", got, err, game.writes)
	}
	o, err = b.Observe(ctx, b.Binding)
	if err != nil || o.Flags["build:allocation_allowed"] {
		t.Fatalf("reserve spent: %+v %v", o, err)
	}
	if _, err := b.GameAction(ctx, b.Binding, aimcp.TypedAction{Kind: "allocate-stat", Index: 3, ExpectedRevision: o.Revision}); err == nil || game.writes != 1 {
		t.Fatal("reserve bypassed")
	}
}

func TestAllocationDoesNotReconcileAfterBackendRecreation(t *testing.T) {
	b, game := gameFixture(t)
	b.CharacterBuild = &airuntime.CharacterBuild{Weights: airuntime.AttributeWeights{Vital: 1}}
	p := &game.snapshot.Player
	p.Vital, p.Strength, p.Toughness, p.Dexterity = 5, 5, 5, 5
	p.StatPointsKnown, p.UnspentStatPoints = true, 3
	ctx := context.Background()
	r, err := b.GameAction(ctx, b.Binding, aimcp.TypedAction{Kind: "allocate-stat", Index: 0, ExpectedRevision: game.snapshot.Revision})
	if err != nil {
		t.Fatal(err)
	}
	p.Vital++
	p.UnspentStatPoints--
	game.snapshot.Revision++
	game.snapshot.AIObservationRevision = game.snapshot.Revision
	restarted := &GameBackend{Binding: b.Binding, Gate: b.Gate, Owner: b.Owner, Session: game, Receipts: b.Receipts, CharacterBuild: b.CharacterBuild.Clone()}
	got, err := restarted.TaskStatus(ctx, b.Binding, r.Handle)
	if err != nil || got.Status != aimcp.ReceiptUnknown {
		t.Fatalf("new backend inferred old delivery: %+v %v", got, err)
	}
	o, err := restarted.Observe(ctx, b.Binding)
	if err != nil || o.Flags["build:allocation_allowed"] {
		t.Fatalf("unknown receipt permitted new point: %+v %v", o, err)
	}
	if _, err := restarted.GameAction(ctx, b.Binding, aimcp.TypedAction{Kind: "allocate-stat", Index: 0, ExpectedRevision: game.snapshot.Revision}); err == nil || game.writes != 1 {
		t.Fatal("new backend replayed while previous allocation unknown")
	}
}

func TestUnconfiguredBuildLeavesPointsUnspent(t *testing.T) {
	b, game := gameFixture(t)
	p := &game.snapshot.Player
	p.Vital, p.Strength, p.Toughness, p.Dexterity = 5, 5, 5, 5
	p.StatPointsKnown, p.UnspentStatPoints = true, 3
	if _, err := b.GameAction(context.Background(), b.Binding, aimcp.TypedAction{Kind: "allocate-stat", Index: 0, ExpectedRevision: game.snapshot.Revision}); err == nil || game.writes != 0 {
		t.Fatal("unconfigured AI allocated points")
	}
}

func TestAllocationReusedBackendDoesNotConfirmPriorGeneration(t *testing.T) {
	b, game := gameFixture(t)
	b.CharacterBuild = &airuntime.CharacterBuild{Weights: airuntime.AttributeWeights{Vital: 1}}
	p := &game.snapshot.Player
	p.Vital, p.Strength, p.Toughness, p.Dexterity = 5, 5, 5, 5
	p.StatPointsKnown, p.UnspentStatPoints = true, 3
	ctx := context.Background()
	r, err := b.GameAction(ctx, b.Binding, aimcp.TypedAction{Kind: "allocate-stat", Index: 0, ExpectedRevision: game.snapshot.Revision})
	if err != nil {
		t.Fatal(err)
	}
	next, _, err := b.Gate.Switch(b.Binding.Generation, aicontrol.Agent, "new lease")
	if err != nil {
		t.Fatal(err)
	}
	b.Binding.Generation = next.Generation
	p.Vital++
	p.UnspentStatPoints--
	game.snapshot.Revision++
	game.snapshot.AIObservationRevision = game.snapshot.Revision
	got, err := b.TaskStatus(ctx, b.Binding, r.Handle)
	if err != nil || got.Status != aimcp.ReceiptUnknown || len(b.statAllocations) != 0 {
		t.Fatalf("prior generation evidence reused: %+v %v", got, err)
	}
	if _, err := b.Observe(ctx, b.Binding); err != nil {
		t.Fatalf("old receipt broke observation: %v", err)
	}
}

func TestAllocationBlockedByLongUnknownHistory(t *testing.T) {
	b, game := gameFixture(t)
	b.CharacterBuild = &airuntime.CharacterBuild{Weights: airuntime.AttributeWeights{Vital: 1}}
	p := &game.snapshot.Player
	p.Vital, p.Strength, p.Toughness, p.Dexterity = 5, 5, 5, 5
	p.StatPointsKnown, p.UnspentStatPoints = true, 3
	ctx := context.Background()
	if _, err := b.Receipts.Prepare(ctx, b.Binding, aimcp.TypedAction{Kind: "item", Command: "use", ExpectedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 130; i++ {
		if _, err := b.Receipts.Prepare(ctx, b.Binding, aimcp.TypedAction{Kind: "chat", Text: "history", ExpectedRevision: uint64(i + 100)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.GameAction(ctx, b.Binding, aimcp.TypedAction{Kind: "allocate-stat", Index: 0, ExpectedRevision: game.snapshot.Revision}); err == nil || game.writes != 0 {
		t.Fatal("unknown history did not block point allocation")
	}
}
