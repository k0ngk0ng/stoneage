package main

import (
	"bufio"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aiservice"
)

func configureHumanSupply(t *testing.T, f automationExecutorFixture) {
	t.Helper()
	f.executor.config.StockItems = map[string]aiservice.StockContract{"small-meat": {
		NPC:        aiservice.NPCSpec{Alias: "meat-shop", Floor: 100, X: 5, Y: 5, Name: "MeatShop", TalkRange: 1, Verified: true, SourceFingerprint: f.executor.config.Knowledge.Fingerprint()},
		TemplateID: 2344, ShopIndex: 1, UnitPrice: 12, X: 4, Y: 5,
	}}
}

func TestHumanSupplyCatalogAndReadOnlyPreflight(t *testing.T) {
	for _, mode := range []string{"ready", "budget", "reserve", "unknown-inventory", "full-backpack"} {
		t.Run(mode, func(t *testing.T) {
			f := newAutomationExecutorFixture(t, 10, 200)
			configureHumanSupply(t, f)
			directory, err := f.executor.TaskDirectory(context.Background())
			if err != nil || len(directory.Supplies) != 1 || directory.Supplies[0].UnitPrice != 12 {
				t.Fatalf("catalog: %+v %v", directory, err)
			}
			request := levelingAutomationRequest(f.session.State().Generation, 11, 0)
			request.Config.Supply = &AutomationSupply{Item: "small-meat", TargetCount: 10, ReorderCount: 3}
			request.Config.Budget.MaximumSpend = 120
			items := "none"
			if mode == "full-backpack" {
				items = ""
				for i := 5; i < 20; i++ {
					items += fmtSupplyItem(i)
				}
			}
			if mode != "unknown-inventory" {
				f.tcp.applyAuthoritativePacket(webServerPacket(t, 9, "S", "AI|v=1|chara=17|end=0,0,0,0,0,0|now=0,0,0,0,0,0|ride=0|items="+strings.TrimSuffix(items, ";")))
			}
			if mode == "budget" {
				request.Config.Budget.MaximumSpend = 119
			}
			if mode == "reserve" {
				request.Config.Budget.Reserve = 100
			}
			before := f.tcp.authoritativeSnapshot()
			if (mode != "unknown-inventory") != before.AI.ItemsKnown {
				t.Fatalf("invalid test inventory: %+v", before.AI)
			}
			preview, err := f.executor.Preview(context.Background(), f.session, request)
			if err != nil {
				t.Fatal(err)
			}
			if preview.Ready != (mode == "ready") {
				t.Fatalf("readiness: %+v inventory=%+v", preview, before.AI)
			}
			if f.tcp.authoritativeSnapshot().Revision != before.Revision {
				t.Fatal("preflight changed game state")
			}
			f.peer.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
			if _, err := bufio.NewReader(f.peer).ReadBytes('\n'); err == nil {
				t.Fatal("supply preview sent game packet")
			}
		})
	}
}

func fmtSupplyItem(slot int) string { // Native observation items use slot,template pairs.
	encoded, _ := json.Marshal(slot)
	return string(encoded) + ",99;"
}

func TestHumanSupplySelectorsReachDurableLevelingPlan(t *testing.T) {
	f := newAutomationExecutorFixture(t, 10, 200)
	f.tcp.serverID = "line-a"
	f.tcp.applyAuthoritativePacket(webServerPacket(t, 6, "CharList", "successful", `AutomationHero|0\z0\z1\z10\z100\z20\z30\z4\z0\z50\z50\z50\z50\z0\zAutomationHero\zhome`))
	f.tcp.applyAuthoritativePacket(webServerPacket(t, 7, "CharLogin", "successful", ""))
	seedDurableRecoveryIdentity(t, f.tcp, durableRecoveryPlayerID)
	configureHumanSupply(t, f)
	request := levelingAutomationRequest(f.session.State().Generation, 11, 0)
	request.Config.Supply = &AutomationSupply{Item: "small-meat", TargetCount: 10, ReorderCount: 3}
	request.Config.Budget.MaximumSpend = 120
	handle, err := f.executor.Start(f.lease, f.session, request)
	if err != nil {
		t.Fatal(err)
	}
	running := handle.(*webAutomationHandle)
	defer running.Stop(context.Background())
	checkpoint, err := f.plans.Load(context.Background(), running.taskHandle)
	if err != nil {
		t.Fatal(err)
	}
	var metadata durableAutomationRecovery
	if err := json.Unmarshal(checkpoint.OwnerContext, &metadata); err != nil || metadata.Config.Supply == nil || metadata.Config.Supply.Item != "small-meat" || metadata.Config.Supply.TargetCount != 10 {
		t.Fatalf("durable recovery omitted supply: %+v %v", metadata, err)
	}
	raw := string(checkpoint.Plan.Steps[0].Action.Arguments)
	if !strings.Contains(raw, `"supply_item":"small-meat"`) || !strings.Contains(raw, `"supply_target_count":10`) || !strings.Contains(raw, `"supply_reorder_count":3`) {
		t.Fatalf("supply selectors not persisted: %s", raw)
	}
	copied := cloneAutomationConfig(request.Config)
	request.Config.Supply.TargetCount = 2
	if copied.Supply.TargetCount != 10 {
		t.Fatal("recovery config aliases mutable supply")
	}
}

func TestHumanSupplyRejectsUnknownOrStaleOfferAndQuestMode(t *testing.T) {
	f := newAutomationExecutorFixture(t, 10, 200)
	configureHumanSupply(t, f)
	supply := &AutomationSupply{Item: "small-meat", TargetCount: 10, ReorderCount: 3}
	if validateWebSupply(aicontrol.Quest, supply) == nil {
		t.Fatal("quest accepted automatic restocking")
	}
	for _, mutate := range []func(){func() { supply.Item = "uninstalled" }, func() {
		supply.Item = "small-meat"
		c := f.executor.config.StockItems[supply.Item]
		c.NPC.SourceFingerprint = "stale"
		f.executor.config.StockItems[supply.Item] = c
	}} {
		mutate()
		request := levelingAutomationRequest(f.session.State().Generation, 11, 0)
		request.Config.Supply = supply
		if _, err := f.executor.Start(f.lease, f.session, request); err == nil {
			t.Fatal("unreviewed supply started")
		}
	}
	for _, pair := range [][2]int{{1, 1}, {14, 1}, {10, 0}, {10, 10}} {
		if validateWebSupply(aicontrol.Leveling, &AutomationSupply{Item: "small-meat", TargetCount: pair[0], ReorderCount: pair[1]}) == nil {
			t.Fatalf("invalid thresholds accepted: %v", pair)
		}
	}
}
