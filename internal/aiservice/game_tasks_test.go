package aiservice

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aileveling"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

func TestGameTasksAcceptsAndPersistsSupplySelectors(t *testing.T) {
	b, session := gameFixture(t)
	session.snapshot.Player.Level = 5
	store, err := automation.OpenStore(filepath.Join(t.TempDir(), "plans.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	coordinator := &aileveling.Coordinator{Game: session, Store: store, Gate: b.Gate, Owner: aicontrol.Agent,
		CharacterID: b.Binding.CharacterID, CharacterName: b.Binding.CharacterName, Supplies: &LevelingStock{}}
	tasks := &GameTasks{Leveling: coordinator, Lease: context.Background()}
	defer tasks.Close()
	receipt, err := tasks.StartLeveling(context.Background(), aimcp.LevelingRequest{TargetKind: "character", TargetLevel: 5,
		Parameters: map[string]json.RawMessage{"area_id": json.RawMessage(`28`), "supply_item": json.RawMessage(`"meat"`), "supply_target_count": json.RawMessage(`10`), "supply_reorder_count": json.RawMessage(`3`)}})
	if err != nil || receipt.Status != aimcp.ReceiptConfirmed {
		t.Fatalf("supply selectors rejected: %+v %v", receipt, err)
	}
	checkpoint, err := store.Load(context.Background(), receipt.Handle)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		AreaID     int                        `json:"area_id"`
		Parameters map[string]json.RawMessage `json:"parameters"`
	}
	if err := json.Unmarshal(checkpoint.Plan.Steps[0].Action.Arguments, &saved); err != nil || saved.AreaID != 28 || string(saved.Parameters["supply_item"]) != `"meat"` {
		t.Fatalf("supply configuration lost: %+v %v", saved, err)
	}
}

func TestGameTasksPreservesLevelingFailureEvidence(t *testing.T) {
	for _, submitted := range []bool{false, true} {
		name := "navigation"
		if submitted {
			name = "uncertain_write"
		}
		t.Run(name, func(t *testing.T) {
			b, session := gameFixture(t)
			session.snapshot.Player.Level = 1
			// This test reaches navigation; provide a complete healthy status.
			session.snapshot.Player.MaxHP = session.snapshot.Player.HP
			session.snapshot.Player.RidePet = -1
			session.snapshot.Player.RidePetKnown = true
			store, err := automation.OpenStore(filepath.Join(t.TempDir(), "plans.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			navigator := aileveling.NavigatorFunc(func(context.Context, aigame.Snapshot, aileveling.NavigationRequest) (aileveling.Navigation, error) {
				if submitted {
					return aileveling.Navigation{Route: "a", Destination: aigame.Point{Floor: 0, X: 0, Y: 1}, CostKnown: true}, nil
				}
				return aileveling.Navigation{}, errors.New("missing verified route")
			})
			if submitted {
				session.writeError = errors.New("uncertain socket write")
			}
			coordinator := &aileveling.Coordinator{Game: session, Navigator: navigator, Store: store, Gate: b.Gate, Owner: aicontrol.Agent, CharacterID: b.Binding.CharacterID, CharacterName: b.Binding.CharacterName}
			tasks := &GameTasks{Leveling: coordinator, Lease: context.Background()}
			defer tasks.Close()
			receipt, err := tasks.StartLeveling(context.Background(), aimcp.LevelingRequest{TargetKind: "character", TargetLevel: 2, MaximumSeconds: 30})
			if err != nil {
				t.Fatal(err)
			}
			tasks.mu.Lock()
			done := tasks.levelDone
			tasks.mu.Unlock()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("leveling worker did not stop")
			}
			checkpoint, err := store.Load(context.Background(), receipt.Handle)
			if err != nil {
				t.Fatal(err)
			}
			wantPhase, wantReason := "ready", "练级导航失败，任务已暂停"
			if submitted {
				wantPhase, wantReason = "prepared", "操作结果未确认，任务已暂停；禁止重试"
			}
			if checkpoint.Status != automation.Paused || checkpoint.Phase != wantPhase || checkpoint.Reason != wantReason {
				t.Fatalf("lost authoritative failure: %+v", checkpoint)
			}
		})
	}
}
