package aiservice

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestPackagedLootHealingMatchesOriginalItemArguments(t *testing.T) {
	const fingerprint = "732242dadf14674ad2dfdfcada6224a86edbde4973a6a9c7df3989fd79f5fbba"
	data := filepath.Join("..", "..", "runtime", "legacy-server", "gmsv", "data", "itemset.txt")
	raw, err := os.ReadFile(data)
	if os.IsNotExist(err) {
		t.Skip("original item table not installed")
	}
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := simplifiedchinese.GB18030.NewDecoder().Bytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	contracts, err := LoadHealingItemsForData(filepath.Join("..", "..", "ai", "catalogs", "healing-items-2.5.json"), fingerprint, data)
	if err != nil {
		t.Fatal(err)
	}
	for alias, id := range map[string]int32{"loot-small-meat": 1234, "loot-wulistan-meat": 1612, "loot-beiendas-meat": 2044} {
		contract, ok := contracts[alias]
		if !ok || contract.TemplateID != id {
			t.Fatalf("missing loot contract %s", alias)
		}
		found := false
		for _, line := range strings.Split(string(decoded), "\n") {
			fields := strings.Split(line, ",")
			if len(fields) <= 19 || fields[16] != strconv.Itoa(int(id)) {
				continue
			}
			if fields[10] != "ITEM_useRecovery" || fields[3] != "体"+strconv.Itoa(int(contract.BaseHP)) || fields[19] != "20" {
				t.Fatalf("loot healing does not match original source: %s", alias)
			}
			found = true
		}
		if !found {
			t.Fatalf("missing source template %d", id)
		}
	}
}

func TestLevelingStockRecognizesReviewedHealingLootInFullBackpack(t *testing.T) {
	for _, verified := range []bool{true, false} {
		stock, session, _ := stockFixture(t)
		session.snapshot.Player.HP, session.snapshot.Player.MaxHP = 100, 100
		for i := 0; i < 14; i++ {
			id := int32(1234)
			if i < 3 {
				id = 2344
			}
			session.bag = append(session.bag, aigame.AIInventoryItem{Slot: int32(5 + i), TemplateID: id})
		}
		supplies := &LevelingStock{Stock: stock, HealingItems: map[string]HealingItemContract{"loot": {TemplateID: 1234, BaseHP: 20, Verified: verified, SourceFingerprint: stock.Backend.Knowledge.Fingerprint()}}}
		order, err := supplies.Quote(context.Background(), session.snapshot, map[string]json.RawMessage{"supply_item": json.RawMessage(`"meat"`)})
		if verified && (err != nil || order != nil) {
			t.Fatalf("ignored usable loot: order=%+v err=%v", order, err)
		}
		if !verified && err == nil {
			t.Fatal("unreviewed loot counted as usable recovery")
		}
		if session.purchases != 0 {
			t.Fatal("quote purchased supplies")
		}
	}
}
