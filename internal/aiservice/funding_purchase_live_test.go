package aiservice

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aifunding"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/ainavigation"
	"github.com/k0ngk0ng/stoneage/internal/aiplanner"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

// This is a real NPC purchase, with a fresh isolated QA identity. It requires
// qa-funding.override.yml and never changes character gold or item tables.
func TestLiveFundingPurchaseFreshAI(t *testing.T) {
	if os.Getenv("STONEAGE_FUNDING_PURCHASE_LIVE_TEST") != "1" {
		t.Skip("opt in with STONEAGE_FUNDING_PURCHASE_LIVE_TEST=1 and the local QA funding override")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	const qaBinaryHash = "95fd6cb85005061cc3427fa84b5d1606d162e8e5c9494f0659aa55c49e001ab1"
	binary, err := exec.CommandContext(ctx, "docker", "exec", "stoneage-player-qa-gmsv-1", "sha256sum", "/proc/1/exe").Output()
	if err != nil || len(strings.Fields(string(binary))) != 2 || strings.Fields(string(binary))[0] != qaBinaryHash {
		t.Fatal("running QA binary must match the reviewed funding/observation build")
	}
	f := movementCrossMapLiveProvisionFreshAI(t, ctx, "funding-purchase", "funding-purchase-live-test")
	dataRoot := filepath.Join(f.RepoRoot, "runtime", "legacy-server", "gmsv", "data")
	effectiveRoot := filepath.Join(f.RepoRoot, "build", "player-integration", "game", "gmsv", "data")
	sources := []string{"npc/genout/shop_m.create", "npc/genout/npcgen.template", "npc/genout/ss_1001_17_13", "itemset.txt"}
	hashes := map[string]string{}
	fingerprint := sha256.New()
	for _, path := range sources {
		hash, err := movementCrossMapLiveCompareFile(dataRoot, effectiveRoot, path)
		if err != nil {
			t.Fatal(err)
		}
		hashes[path] = hash
		raw, err := os.ReadFile(filepath.Join(dataRoot, path))
		if err != nil {
			t.Fatal(err)
		}
		fingerprint.Write([]byte(path + "\x00"))
		fingerprint.Write(raw)
		fingerprint.Write([]byte{0})
	}
	const reviewed = "452c31145ab04b4f59974b5a556d2951b9ccccb3a53c4d33f0df8d8bee05a74c"
	if hex.EncodeToString(fingerprint.Sum(nil)) != reviewed {
		t.Fatal("shop source contract changed; review it before purchasing")
	}
	knowledge, err := aiknowledge.LoadDataDir(ctx, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	navigator, err := ainavigation.LoadDataDir(ctx, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := movementCrossMapLiveVerifyEffectiveData(ctx, f.RepoRoot, navigator); err != nil {
		t.Fatal(err)
	}
	floor, ok := navigator.Floor(1001)
	if !ok {
		t.Fatal("shop floor is missing")
	}
	if _, err := movementCrossMapLiveCompareFile(dataRoot, effectiveRoot, filepath.Join("map", floor.Source)); err != nil {
		t.Fatal(err)
	}
	manager, err := aifunding.NewManager(filepath.Join(f.RepoRoot, "build", "player-integration", "tmp", "ai-funding", "policies"))
	if err != nil {
		t.Fatal(err)
	}
	account, slot := f.Created.Account.Username, f.Created.Binding.CharacterSlot
	t.Cleanup(func() {
		if err := manager.Revoke(account, slot); err != nil {
			t.Error("revoke isolated funding policy:", err)
		}
		path, err := manager.PolicyPath(account, slot)
		if err != nil {
			t.Error(err)
		} else if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) || manager.Allowed(account, slot) {
			t.Error("isolated funding capability was not removed")
		}
	})
	gate := aicontrol.New()
	defer gate.Close()
	state, _, err := gate.Switch(gate.State().Generation, aicontrol.Agent, "isolated funding purchase")
	if err != nil {
		t.Fatal(err)
	}
	binding := aimcp.Binding{AccountID: account, CharacterID: f.Created.Binding.CharacterID,
		CharacterName: f.Created.Binding.CharacterName, Generation: state.Generation}
	backend := &GameBackend{Binding: binding, Gate: gate, Owner: aicontrol.Agent, Session: f.Lease.Session,
		Knowledge: knowledge, OwnStateRefresh: &OwnStateRefresher{},
		Funding: func(context.Context) (bool, error) { return manager.Allowed(account, slot), nil }}
	initial, err := movementCrossMapLiveWaitObservation(ctx, backend, binding, func(o aimcp.Observation) bool {
		return o.Ready && o.Phase == string(aigame.PhaseWorld) && o.Floor == 1006 && o.Flags["inventory:known"]
	})
	if err != nil {
		t.Fatal(err)
	}
	const unitPrice = int64(105)
	quantity := int(initial.Gold/unitPrice) + 1
	if initial.Gold < 0 || quantity < 1 || quantity > 15 || len(initial.Inventory) != 0 {
		t.Fatalf("fresh inventory/balance unsuitable for bounded purchase: gold=%d quantity=%d inventory=%v", initial.Gold, quantity, initial.Inventory)
	}
	cost := int64(quantity) * unitPrice
	t.Logf("fresh visible gold=%d; purchase %d x item 4 at 105, total=%d", initial.Gold, quantity, cost)
	movement := &MovementSkill{Backend: backend, Navigator: navigator, WarpGraph: aiplanner.NewWarpGraph(knowledge), SegmentTimeout: 5 * time.Second, WarpConfirmationTimeout: 8 * time.Second}
	raw, _ := json.Marshal(movementArguments{Floor: 1001, X: 15, Y: 13})
	if err := movement.Execute(ctx, automation.Action{Skill: "move", ExpectedRevision: initial.Revision, Arguments: raw}); err != nil {
		last, _ := f.Lease.Session.Observe(context.Background())
		t.Fatalf("move to village weapon shop: %v; position=%+v phase=%s", err, last.Position, last.Phase)
	}
	t.Log("reached weapon shop interaction tile")
	spec := NPCSpec{Alias: "qa-weapon-shop", Floor: 1001, X: 17, Y: 13, Name: "萨姆吉尔的武器店", TalkRange: 2,
		Verified: true, SourceFingerprint: reviewed, Windows: []NPCWindowSpec{
			{Type: 6, Sequence: 240, WindowObjectFromActor: true, Choices: map[int]NPCChoice{1: {Button: 1, Data: "1"}}},
			{Type: 7, Sequence: 242, WindowObjectFromActor: true, Choices: map[int]NPCChoice{1: {Button: 1, Data: fmt.Sprintf("2|%d", quantity), MaximumCost: cost}}},
		}}
	registry, err := NewNPCRegistry([]NPCSpec{spec})
	if err != nil {
		t.Fatal(err)
	}
	skill := NewNPCSkill(backend, registry)
	act := func(name string, arguments any, maximum int64) {
		t.Helper()
		raw, _ := json.Marshal(arguments)
		for attempt := 0; attempt < 3; attempt++ {
			o, err := backend.Observe(ctx, binding)
			if err != nil {
				t.Fatal(err)
			}
			err = skill.Execute(ctx, automation.Action{Skill: name, ExpectedRevision: o.Revision, Arguments: raw, MaximumCost: maximum})
			if !errors.Is(err, aigame.ErrStaleRevision) {
				if err != nil {
					t.Fatal(name, err)
				}
				return
			}
		}
		t.Fatal("NPC action remained stale before submission")
	}
	act("npc.talk", map[string]any{"npc": spec.Alias, "floor": 1001, "x": 17, "y": 13, "command": "talk"}, 0)
	waitWindow := func(sequence int32) {
		t.Helper()
		windowCtx, windowCancel := context.WithTimeout(ctx, 15*time.Second)
		defer windowCancel()
		if _, err := movementCrossMapLiveWaitSnapshot(windowCtx, f.Lease.Session, func(s aigame.Snapshot) bool {
			return s.ActiveWindow != nil && s.ActiveWindow.Open && s.ActiveWindow.Sequence == sequence
		}); err != nil {
			last, _ := f.Lease.Session.Observe(context.Background())
			t.Fatalf("wait for reviewed shop window %d: %v; last=%+v", sequence, err, last.ActiveWindow)
		}
		t.Logf("received shop window %d", sequence)
	}
	waitWindow(240)
	act("npc.window", npcWindowArgumentsForLive(spec.Alias, 240, 1), 0)
	waitWindow(242)
	ledgerPath := filepath.Join(f.RepoRoot, "build", "player-integration", "tmp", "ai-funding", "ledger.log")
	baseline, err := os.ReadFile(ledgerPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read funding ledger baseline:", err)
	}
	if bytes.Contains(baseline, []byte("|"+account+"|")) {
		t.Fatal("fresh account unexpectedly has prior funding charges")
	}
	started := time.Now().Unix()
	if err := manager.Provision(account, slot, 0); err != nil {
		t.Fatal("provision server-owned unlimited funding:", err)
	}
	act("npc.window", npcWindowArgumentsForLive(spec.Alias, 242, 1), cost)
	finalCtx, finalCancel := context.WithTimeout(ctx, 15*time.Second)
	defer finalCancel()
	final, err := movementCrossMapLiveWaitObservation(finalCtx, backend, binding, func(o aimcp.Observation) bool {
		return o.Flags["inventory:known"] && o.Inventory["item:4"] == quantity
	})
	if err != nil {
		t.Fatal("server did not confirm purchased item count:", err)
	}
	if final.Gold != initial.Gold || !final.UnlimitedFunds || cost <= initial.Gold {
		t.Fatalf("unexpected funding result: initial=%d final=%d cost=%d unlimited=%v", initial.Gold, final.Gold, cost, final.UnlimitedFunds)
	}
	ledger, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal("read QA funding ledger:", err)
	}
	if !bytes.HasPrefix(ledger, baseline) {
		t.Fatal("funding ledger was replaced during the purchase")
	}
	charges := 0
	for _, line := range strings.Split(string(ledger[len(baseline):]), "\n") {
		fields := strings.Split(line, "|")
		if len(fields) != 6 || fields[1] != account || fields[2] != strconv.Itoa(slot) {
			continue
		}
		if fields[0] != "1" || fields[3] != "npc" || fields[4] != strconv.FormatInt(cost, 10) {
			t.Fatal("unexpected charge for isolated account")
		}
		stamp, err := strconv.ParseInt(fields[5], 10, 64)
		if err != nil || stamp < started || stamp > time.Now().Unix()+1 {
			t.Fatal("charge timestamp is outside the purchase interval")
		}
		charges++
	}
	if charges != 1 {
		t.Fatalf("expected exactly one authoritative NPC charge, got %d", charges)
	}
	if err := manager.Revoke(account, slot); err != nil || manager.Allowed(account, slot) {
		t.Fatal("revoke funding after purchase:", err)
	}
	// Re-enter the saved character with the grant revoked. This obtains fresh
	// inventory and gold from the server, rather than relying on a pre-purchase
	// gold field retained in the first session's snapshot.
	f.Lease.Close()
	reloginCtx, reloginCancel := context.WithTimeout(ctx, 20*time.Second)
	defer reloginCancel()
	for {
		lease, err := f.Provider.Open(reloginCtx, f.Profile)
		if err == nil {
			if lease.Binding.CharacterID != f.Created.Binding.CharacterID || lease.Binding.CharacterName != f.Created.Binding.CharacterName {
				lease.Close()
				t.Fatal("relogin changed the isolated character binding")
			}
			f.Lease = lease
			backend.Session = lease.Session
			backend.OwnStateRefresh = &OwnStateRefresher{}
			break
		}
		select {
		case <-reloginCtx.Done():
			t.Fatal("relogin after purchase:", err)
		case <-time.After(250 * time.Millisecond):
		}
	}
	relogged, err := movementCrossMapLiveWaitObservation(reloginCtx, backend, binding, func(o aimcp.Observation) bool {
		return o.Connected && o.Ready && o.Phase == string(aigame.PhaseWorld) && o.Flags["inventory:known"] && o.Inventory["item:4"] == quantity
	})
	if err != nil || relogged.Gold != initial.Gold || relogged.UnlimitedFunds {
		t.Fatalf("purchase did not persist after revocation/relogin: gold=%d unlimited=%v err=%v", relogged.Gold, relogged.UnlimitedFunds, err)
	}
	evidence := map[string]any{"test": t.Name(), "status": "passed", "initial_gold": initial.Gold, "final_gold": final.Gold,
		"cost": cost, "item_id": 4, "quantity": quantity, "ledger_charges": charges, "source_hashes": hashes, "qa_binary_sha256": qaBinaryHash,
		"insufficient_balance_purchase_verified": true, "purchase_relogin_verified": true, "funding_revoked": true, "model_invoked": false}
	raw, _ = json.MarshalIndent(evidence, "", "  ")
	if err := os.WriteFile(filepath.Join(f.RepoRoot, "build", "ai", "funding-purchase-live-evidence.json"), append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}
