package aiservice

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestPackagedHometownStockOffers(t *testing.T) {
	const fingerprint = "2d007194c2557a7a7c52445bd2dc91eb0df8a2db917c6c842ef6289edbaeac3d"
	catalog := filepath.Join("..", "..", "ai", "catalogs")
	healing, err := LoadHealingItems(filepath.Join(catalog, "healing-items-2.5.json"), fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	npcs, err := LoadNPCRegistry(filepath.Join(catalog, "shops-2.5.json"), fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	offers, err := LoadStockItems(filepath.Join(catalog, "stock-items-2.5.json"), fingerprint, npcs, healing)
	if err != nil {
		t.Fatal(err)
	}
	for alias, floor := range map[string]int{"small-meat": 1004, "hometown-1-small-meat": 2004} {
		offer, ok := offers[alias]
		if !ok || offer.NPC.Floor != floor || offer.NPC.X != 17 || offer.NPC.Y != 13 || offer.X != 17 || offer.Y != 15 || offer.TemplateID != 2344 || offer.UnitPrice != 12 || offer.ShopIndex != 1 {
			t.Fatalf("invalid packaged offer %s: %+v", alias, offer)
		}
		if _, err := offer.registry(5); err != nil {
			t.Fatal(err)
		}
	}
	if offers["hometown-1-small-meat"].NPC.Name != "玛丽那丝的肉店" {
		t.Fatal("wrong hometown 1 shop identity")
	}
}

func TestHometownOneStockMatchesOriginalSource(t *testing.T) {
	data := filepath.Join("..", "..", "runtime", "legacy-server", "gmsv", "data")
	read := func(name string) string {
		raw, err := os.ReadFile(filepath.Join(data, name))
		if os.IsNotExist(err) {
			t.Skip("original server data not installed")
		}
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := simplifiedchinese.GB18030.NewDecoder().Bytes(raw)
		if err != nil {
			t.Fatal(err)
		}
		return strings.ReplaceAll(string(decoded), "\r", "")
	}
	create := read("npc/genout/shop_m.create")
	block := regexp.MustCompile(`(?s)\{[^{}]*floorid=2004\s+borncorner=17,13,17,13[^{}]*\}`).FindString(create)
	if !strings.Contains(block, "name=玛丽那丝的肉店\n") || !strings.Contains(block, "enemy=npcgen_shop|file:genout/ss_2004_17_13") {
		t.Fatal("source NPC identity changed")
	}
	shop := read("npc/genout/ss_2004_17_13")
	if !strings.HasPrefix(shop, "buy_rate:1.0\n") || !strings.Contains(shop, "\nItemList:2344-2347\n") {
		t.Fatal("source shop offer order or purchase rate changed")
	}
	found := false
	for _, line := range strings.Split(read("itemset.txt"), "\n") {
		fields := strings.Split(line, ",")
		if len(fields) > 19 && fields[16] == "2344" {
			price, err := strconv.Atoi(fields[18])
			if err != nil || price != 12 || fields[10] != "ITEM_useRecovery" || fields[19] != "20" {
				t.Fatal("source item price or healing behavior changed")
			}
			found = true
		}
	}
	if !found {
		t.Fatal("source recovery meat missing")
	}
	warps := read("map/mapwarp.txt")
	for _, edge := range []string{"NONE:NULL:2000,92,77:2004,15,21:NULL", "NONE:NULL:2004,15,21:2000,92,77:NULL", "NONE:NULL:2006,20,21:2000,56,48:NULL"} {
		if !strings.Contains(warps, edge+"\n") {
			t.Fatalf("source shop approach changed: %s", edge)
		}
	}
}
