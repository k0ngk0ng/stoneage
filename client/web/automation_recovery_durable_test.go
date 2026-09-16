package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aileveling"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

const durableRecoveryPlayerID = "pc1_0123456789abcdef0123456789abcdef"

func seedDurableRecoveryIdentity(t *testing.T, tcp *tcpSession, id string) automationIdentity {
	t.Helper()
	tcp.applyAuthoritativePacket(webServerPacket(t, 8, "S", "AI|v=1|chara=12|end=0,0,0,0,0,0|now=0,0,0,0,0,0|ride=0|character_id="+id))
	snapshot, err := tcp.observeAuthoritative(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	identity, ok := automationIdentityFromSnapshot(snapshot, tcp.serverLineID())
	if !ok || identity.PersistentCharacterID != id {
		t.Fatalf("persistent identity missing: %+v", identity)
	}
	return identity
}

func TestAutomationRecoverySurvivesDatabaseReopenWithoutReplay(t *testing.T) {
	ctx := context.Background()
	for _, phase := range []string{"ready", "submitted"} {
		t.Run(phase, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "plans.db")
			db, err := automation.OpenStore(path)
			if err != nil {
				t.Fatal(err)
			}
			base, _ := newRecoveryExecutor(t, &recoveryCheckpointStore{})
			base.config.Plans = db
			tcp, _, _ := newRecoveryWebSession(t, "line-a", 1)
			identity := seedDurableRecoveryIdentity(t, tcp, durableRecoveryPlayerID)
			config := AutomationConfig{Targets: []AutomationTarget{{Kind: "character", Level: 99}}, TargetPolicy: "all", MaximumSeconds: 60}
			checkpoint := automation.Checkpoint{Plan: recoveryLevelPlan("restart-"+phase, identity.CharacterID, 1), Revision: 1, Status: automation.Running, Phase: phase, StartedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
			if phase == "submitted" {
				checkpoint.Plan.Targets[0].Level = 99
			}
			if err := base.recoveryPlanStore(identity, config).Create(ctx, checkpoint); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = automation.OpenStore(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			config2 := base.config
			config2.Plans = db
			fresh, err := NewAutomationExecutor(config2)
			if err != nil {
				t.Fatal(err)
			}
			rows, err := db.ListRecoveryHistory(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := fresh.restoreDurableRecoveries(ctx, rows); err != nil {
				t.Fatal(err)
			}
			saved, err := db.Load(ctx, checkpoint.Plan.ID)
			if err != nil || saved.Status != automation.Paused || saved.Phase != phase || saved.Revision != 2 {
				t.Fatalf("startup changed delivery phase: %+v %v", saved, err)
			}
			reconnected, peer, pendingSnapshot := newRecoveryWebSession(t, "line-a", 1)
			pendingIdentity, _ := automationIdentityFromSnapshot(pendingSnapshot, reconnected.serverLineID())
			if !fresh.recoveryBusy(pendingIdentity) {
				t.Fatal("missing persistent observation bypassed durable offer")
			}
			if offer, err := fresh.Recovery(ctx, &AutomationSession{ID: reconnected.id, session: reconnected}); !errors.Is(err, errAutomationRecoveryNotReady) || offer != nil {
				t.Fatalf("unconfirmed identity exposed recovery: %+v %v", offer, err)
			}
			seedDurableRecoveryIdentity(t, reconnected, durableRecoveryPlayerID)
			session := &AutomationSession{ID: reconnected.id, session: reconnected}
			offer, err := fresh.Recovery(ctx, session)
			if err != nil || offer == nil || offer.Handle != checkpoint.Plan.ID {
				t.Fatalf("missing restart offer: %+v %v", offer, err)
			}
			if !fresh.recoveryBusy(identity) {
				t.Fatal("restart offer did not block new Start")
			}
			// Same name/account/slot/line with a replaced character must never claim it.
			seedDurableRecoveryIdentity(t, reconnected, "pc1_abcdef0123456789abcdef0123456789")
			if offer, err := fresh.Recovery(ctx, session); err != nil || offer != nil {
				t.Fatalf("replacement offered old task: %+v %v", offer, err)
			}
			seedDurableRecoveryIdentity(t, reconnected, durableRecoveryPlayerID)
			if phase == "submitted" {
				state, lease, err := reconnected.gate.Switch(1, aicontrol.Leveling, "unknown restart recovery")
				if err != nil {
					t.Fatal(err)
				}
				session.mode, session.generation = aicontrol.Leveling, state.Generation
				if _, err := fresh.Recover(lease, session, checkpoint.Plan.ID); !errors.Is(err, aileveling.ErrUnknownDelivery) {
					t.Fatalf("unknown delivery replayed: %v", err)
				}
				if err := peer.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
					t.Fatal(err)
				}
				var packet [1]byte
				n, readErr := peer.Read(packet[:])
				var timeout net.Error
				if n != 0 || !errors.As(readErr, &timeout) || !timeout.Timeout() {
					t.Fatalf("restart recovery sent game packet: %d %v", n, readErr)
				}
				if offer, err := fresh.Recovery(ctx, session); err != nil || offer == nil {
					t.Fatalf("unknown recovery lost offer: %+v %v", offer, err)
				}
				if err := fresh.DiscardRecovery(ctx, session, checkpoint.Plan.ID); err != nil {
					t.Fatal(err)
				}
				saved, _ = db.Load(ctx, checkpoint.Plan.ID)
				if saved.Phase != "cancelled" {
					t.Fatal("explicit discard was not durable")
				}
				return
			}
			state, lease, err := reconnected.gate.Switch(1, aicontrol.Leveling, "restart recovery")
			if err != nil {
				t.Fatal(err)
			}
			session.mode, session.generation = aicontrol.Leveling, state.Generation
			recovered, err := fresh.Recover(lease, session, checkpoint.Plan.ID)
			if err != nil {
				t.Fatal(err)
			}
			handle := recovered.(*webAutomationHandle)
			handle.Activate()
			saved, err = db.Load(ctx, checkpoint.Plan.ID)
			if err != nil || saved.Status != automation.Completed || saved.Plan.Targets[0].Level != 1 {
				t.Fatalf("original plan not resumed: %+v %v", saved, err)
			}
			if unfinished, err := db.ListRecoveryHistory(ctx); err != nil || len(unfinished) != 1 || unfinished[0].Status != automation.Completed {
				t.Fatalf("completion left recoverable rows: %+v %v", unfinished, err)
			}
		})
	}
}

func TestAutomationRestartDoesNotRenewExpiryOrClaimLegacyWork(t *testing.T) {
	ctx := context.Background()
	store := &recoveryCheckpointStore{}
	executor, _ := newRecoveryExecutor(t, store)
	identity := automationIdentity{AccountID: "a", CharacterID: "a:0", CharacterName: "Hero", ServerID: "line", PersistentCharacterID: durableRecoveryPlayerID}
	old := time.Now().UTC().Add(-25 * time.Hour)
	checkpoint := automation.Checkpoint{Plan: recoveryLevelPlan("expired", "a:0", 30), Revision: 1, Status: automation.Running, Phase: "submitted", UpdatedAt: old, StartedAt: old}
	if err := executor.recoveryPlanStore(identity, AutomationConfig{}).Create(ctx, checkpoint); err != nil {
		t.Fatal(err)
	}
	checkpoint, _ = store.Load(ctx, "expired")
	legacy := checkpoint
	legacy.Plan.ID = "legacy"
	legacy.OwnerContext = nil
	if err := store.Create(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	if err := executor.restoreDurableRecoveries(ctx, []automation.Checkpoint{checkpoint, legacy}); err != nil {
		t.Fatal(err)
	}
	saved, _ := store.Load(ctx, "expired")
	var metadata durableAutomationRecovery
	if err := json.Unmarshal(saved.OwnerContext, &metadata); err != nil {
		t.Fatal(err)
	}
	if !metadata.ActivityAt.Equal(old) || saved.Status != automation.Paused || len(executor.recoveries) != 0 {
		t.Fatal("expired recovery renewed or offered")
	}
	legacy, _ = store.Load(ctx, "legacy")
	if legacy.Status != automation.Running || legacy.Revision != 1 {
		t.Fatal("unowned legacy work mutated")
	}
	if err := executor.restoreDurableRecoveries(ctx, []automation.Checkpoint{saved}); err != nil {
		t.Fatal(err)
	}
	if len(executor.recoveries) != 0 {
		t.Fatal("second startup renewed expiry")
	}
}

func TestTerminalRecoveryHistoryShadowsOlderPausedRun(t *testing.T) {
	ctx := context.Background()
	store := &recoveryCheckpointStore{}
	executor, _ := newRecoveryExecutor(t, store)
	identity := automationIdentity{AccountID: "a", CharacterID: "a:0", CharacterName: "Hero", ServerID: "line", PersistentCharacterID: durableRecoveryPlayerID}
	writer := executor.recoveryPlanStore(identity, AutomationConfig{})
	old := automation.Checkpoint{Plan: recoveryLevelPlan("old", "a:0", 30), Revision: 1, Status: automation.Paused, Phase: "submitted", UpdatedAt: time.Now().UTC()}
	if err := writer.Create(ctx, old); err != nil {
		t.Fatal(err)
	}
	newest := old
	newest.Plan.ID = "newest"
	newest.Phase = "cancelled"
	if err := writer.Create(ctx, newest); err != nil {
		t.Fatal(err)
	}
	old, _ = store.Load(ctx, "old")
	newest, _ = store.Load(ctx, "newest")
	if err := executor.restoreDurableRecoveries(ctx, []automation.Checkpoint{newest, old}); err != nil {
		t.Fatal(err)
	}
	if len(executor.recoveries) != 0 {
		t.Fatal("cancelled latest run resurrected old offer")
	}
}

func TestAutomationStartPersistsOwnerBeforeReturningHandle(t *testing.T) {
	fixture := newAutomationExecutorFixture(t, 5, 0)
	fixture.tcp.serverID = "line-a"
	fixture.tcp.applyAuthoritativePacket(webServerPacket(t, 6, "CharList", "successful", `AutomationHero|0\z0\z1\z10\z100\z20\z30\z4\z0\z50\z50\z50\z50\z0\zAutomationHero\zhome`))
	fixture.tcp.applyAuthoritativePacket(webServerPacket(t, 7, "CharLogin", "successful", ""))
	seedDurableRecoveryIdentity(t, fixture.tcp, durableRecoveryPlayerID)
	request := levelingAutomationRequest(fixture.session.State().Generation, 5, 0)
	value, err := fixture.executor.Start(fixture.lease, fixture.session, request)
	if err != nil {
		t.Fatal(err)
	}
	handle := value.(*webAutomationHandle)
	defer handle.Activate()
	saved, err := fixture.plans.Load(context.Background(), handle.taskHandle)
	if err != nil {
		t.Fatal(err)
	}
	var metadata durableAutomationRecovery
	if err := json.Unmarshal(saved.OwnerContext, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Owner != "web-player" || metadata.Identity.PersistentCharacterID != durableRecoveryPlayerID || metadata.Identity.ServerID != "line-a" || len(metadata.Config.Targets) != 1 || metadata.Config.Targets[0].Level != 5 {
		t.Fatalf("Start failed to stamp recovery: %+v", metadata)
	}
}

func TestConfiguredAutomationRuntimeRestoresBeforeAcceptingSessions(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "plans.db")
	db, err := automation.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	identity := automationIdentity{AccountID: "a", CharacterID: "a:0", CharacterName: "Hero", ServerID: "line", PersistentCharacterID: durableRecoveryPlayerID}
	executor := &AutomationExecutor{config: AutomationExecutorConfig{Plans: db}}
	checkpoint := automation.Checkpoint{Plan: recoveryLevelPlan("runtime-restart", "a:0", 30), Revision: 1, Status: automation.Running, Phase: "submitted", UpdatedAt: time.Now().UTC(), StartedAt: time.Now().UTC()}
	if err := executor.recoveryPlanStore(identity, AutomationConfig{MaximumSeconds: 60}).Create(context.Background(), checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join("..", "..", "runtime", "legacy-server", "gmsv", "data")
	runtime, closeRuntime, err := configureAutomationRuntime(Config{AutomationKnowledgeDataDir: data, AutomationMapDataDir: data, AutomationDB: path, ReceiptDB: filepath.Join(root, "receipts.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer closeRuntime()
	fresh := runtime.(*AutomationExecutor)
	if !fresh.recoveryBusy(identity) {
		t.Fatal("startup did not register durable offer")
	}
	saved, err := fresh.config.Plans.Load(context.Background(), checkpoint.Plan.ID)
	if err != nil || saved.Status != automation.Paused || saved.Phase != "submitted" {
		t.Fatalf("startup reconciliation missing: %+v %v", saved, err)
	}
}
