package aiservice

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/aiprovision"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

func reloginGiftPickup(t *testing.T, ctx context.Context, fixture *movementCrossMapLiveFreshAI, backend *GameBackend) aimcp.Observation {
	t.Helper()
	return reloginGiftCharacter(t, ctx, fixture, backend, func(o aimcp.Observation) bool {
		return o.Connected && o.Ready && o.Phase == string(aigame.PhaseWorld) &&
			o.Flags["inventory:known"] && o.Inventory["item:2415"] == 1 && o.Flags["now:2"] && o.Flags["savepoint:0"]
	})
}

func reloginGiftCharacter(t *testing.T, ctx context.Context, fixture *movementCrossMapLiveFreshAI, backend *GameBackend, predicate func(aimcp.Observation) bool) aimcp.Observation {
	t.Helper()
	fixture.Lease.Close()
	profile, err := fixture.Profiles.GetProfile(ctx, fixture.Profile.ID)
	if err != nil {
		t.Fatal("reload gift character profile")
	}
	deadline, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var lease aiprovision.SessionLease
	for {
		lease, err = fixture.Provider.Open(deadline, profile)
		if err == nil {
			break
		}
		if deadline.Err() != nil || (!errors.Is(err, aiprovision.ErrGameSession) && !errors.Is(err, aiprovision.ErrCharacterUnavailable) && !errors.Is(err, aiprovision.ErrAlreadyOpen)) {
			t.Fatalf("reopen gift character after server save: %v", err)
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-deadline.Done():
			timer.Stop()
			t.Fatal("gift character relogin timed out")
		case <-timer.C:
		}
	}
	t.Cleanup(lease.Close)
	fixture.Lease = lease
	if lease.Binding.CharacterID != fixture.Created.Binding.CharacterID || lease.Binding.CharacterName != fixture.Created.Binding.CharacterName {
		t.Fatal("relogin changed the bound gift character")
	}
	backend.Session = lease.Session
	backend.OwnStateRefresh = &OwnStateRefresher{}
	observation, err := movementCrossMapLiveWaitObservation(deadline, backend, backend.Binding, predicate)
	if err != nil {
		t.Fatalf("expected gift inventory and event state did not survive character relogin: %v", err)
	}
	return observation
}

var errGiftCheckpointWrite = errors.New("QA injected post-submission checkpoint write failure")

type giftCheckpointStore struct {
	*automation.SQLiteStore
	failed bool
}

func (s *giftCheckpointStore) Save(ctx context.Context, c automation.Checkpoint, expected uint64) error {
	if c.Phase == "submitted" && !s.failed {
		s.failed = true
		return errGiftCheckpointWrite
	}
	return s.SQLiteStore.Save(ctx, c, expected)
}

type giftCheckpointGame struct {
	*AutomationGame
	requests int
}

func (g *giftCheckpointGame) Execute(ctx context.Context, a automation.Action) error {
	g.requests++
	return g.AutomationGame.Execute(ctx, a)
}

// This QA-only plan covers the pickup checkpoint, not the complete exchange.
// It sends a real NPC choice, loses only the subsequent local checkpoint
// write, and returns a verifier that must run after authoritative pickup.
func beginGiftCheckpointRecovery(t *testing.T, ctx context.Context, root string, game *AutomationGame, action automation.Action, fingerprint string) func() {
	t.Helper()
	path := filepath.Join(root, "gift-checkpoint.db")
	store, err := automation.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	tracked := &giftCheckpointGame{AutomationGame: game}
	fault := &giftCheckpointStore{SQLiteStore: store}
	engine := &automation.Engine{Game: tracked, Store: fault}
	success := []automation.Condition{
		{Kind: "flag_set", ID: "inventory:known"},
		{Kind: "item_count", ID: "item:2415", Value: 1},
		{Kind: "flag_set", ID: "now:2"},
	}
	plan := automation.Plan{
		ID: "qa-gift-pickup-recovery", CharacterID: game.Backend.Binding.CharacterID,
		Mode: "quest", KnowledgeRevision: fingerprint, Title: "QA flower pickup recovery",
		MaximumSeconds: 120, Budget: automation.Budget{Known: true},
		Preconditions: []automation.Condition{{Kind: "flag_set", ID: "savepoint:0"}},
		Completion:    success,
		Steps:         []automation.Step{{ID: "request-flower", Action: action, Success: success, TimeoutSeconds: 60, CostKnown: true}},
	}
	started, err := engine.Start(ctx, plan)
	if err != nil || started.Status != automation.Running {
		t.Fatalf("start live pickup checkpoint: status=%s err=%v", started.Status, err)
	}
	if _, err = engine.Tick(ctx, plan.ID); !errors.Is(err, errGiftCheckpointWrite) || !fault.failed {
		t.Fatalf("expected loss of post-submission checkpoint, got %v", err)
	}
	if tracked.requests != 1 {
		t.Fatalf("pickup submissions=%d, want 1", tracked.requests)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		reopened, err := automation.OpenStore(path)
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		pending, err := reopened.Load(ctx, plan.ID)
		if err != nil || pending.Phase != "prepared" || pending.Status != automation.Running {
			t.Fatalf("durable unknown pickup checkpoint: phase=%s status=%s err=%v", pending.Phase, pending.Status, err)
		}
		recovered := &automation.Engine{Game: tracked, Store: reopened}
		completed, err := recovered.Tick(ctx, plan.ID)
		if err != nil || completed.Status != automation.Completed || completed.Confirmation == nil {
			t.Fatalf("reconcile real pickup after database reopen: status=%s err=%v", completed.Status, err)
		}
		if completed.Confirmation.Inventory["item:2415"] != 1 || !completed.Confirmation.Flags["now:2"] {
			t.Fatal("recovery lacks authoritative single-flower and event confirmation")
		}
		if _, err := recovered.Tick(ctx, plan.ID); err != nil || tracked.requests != 1 {
			t.Fatalf("recovery replayed pickup: requests=%d err=%v", tracked.requests, err)
		}
		t.Log("pickup checkpoint recovered from prepared after database reopen; NPC request count=1")
	}
}
