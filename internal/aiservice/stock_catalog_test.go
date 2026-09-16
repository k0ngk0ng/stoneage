package aiservice

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/runtimepath"
)

const stockCatalogTestFingerprint = "knowledge-v1"

func validStockCatalogDocument() stockCatalogDocument {
	return stockCatalogDocument{
		Version:              1,
		KnowledgeFingerprint: stockCatalogTestFingerprint,
		Offers: []stockCatalogOffer{{
			Alias: "small-meat-shop", Item: "small-meat", NPC: "trainer",
			ShopIndex: 1, UnitPrice: 12, X: 5, Y: 7,
		}},
	}
}

func stockCatalogDependencies(t *testing.T) (NPCRegistry, map[string]HealingItemContract) {
	t.Helper()
	registry, err := NewNPCRegistry([]NPCSpec{{
		Alias: "trainer", Floor: 10, X: 5, Y: 6, Name: "Trainer", TalkRange: 2,
		SourceFingerprint: stockCatalogTestFingerprint, Verified: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return registry, map[string]HealingItemContract{
		"small-meat": {
			TemplateID: 2344, BaseHP: 20, Verified: true,
			SourceFingerprint: stockCatalogTestFingerprint,
			ItemsetSHA256:     strings.Repeat("a", 64),
		},
	}
}

func writeStockCatalogDocument(t *testing.T, raw []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stock-items.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func marshalStockCatalogDocument(t *testing.T, document stockCatalogDocument) []byte {
	t.Helper()
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestLoadStockItemsEmptyPathDisablesStock(t *testing.T) {
	items, err := LoadStockItems("", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if items == nil || len(items) != 0 {
		t.Fatalf("empty path contracts=%v", items)
	}
}

func TestLoadStockItemsComposesReviewedReferences(t *testing.T) {
	npcs, healingItems := stockCatalogDependencies(t)
	path := writeStockCatalogDocument(t, marshalStockCatalogDocument(t, validStockCatalogDocument()))
	items, err := LoadStockItems(path, stockCatalogTestFingerprint, npcs, healingItems)
	if err != nil {
		t.Fatal(err)
	}
	offer, ok := items["small-meat-shop"]
	if !ok {
		t.Fatalf("loaded stock items=%v", items)
	}
	if offer.TemplateID != 2344 || offer.NPC.Alias != "trainer" || offer.ShopIndex != 1 || offer.UnitPrice != 12 || offer.X != 5 || offer.Y != 7 {
		t.Fatalf("offer=%+v", offer)
	}
	if _, err := offer.registry(1); err != nil {
		t.Fatalf("returned offer failed registry validation: %v", err)
	}

	// Both the offer alias and references are normalized at the file boundary.
	document := validStockCatalogDocument()
	document.Offers[0].Alias = " Small-Meat-Shop "
	document.Offers[0].Item = " SMALL-MEAT "
	document.Offers[0].NPC = " TRAINER "
	path = writeStockCatalogDocument(t, marshalStockCatalogDocument(t, document))
	items, err = LoadStockItems(path, stockCatalogTestFingerprint, npcs, healingItems)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := items["small-meat-shop"]; !ok {
		t.Fatalf("normalized stock items=%v", items)
	}
}

func TestLoadStockItemsRejectsEnvelopeAndReferenceErrors(t *testing.T) {
	tests := []struct {
		name string
		edit func(*stockCatalogDocument, NPCRegistry, map[string]HealingItemContract)
	}{
		{name: "missing expected fingerprint"},
		{name: "version", edit: func(document *stockCatalogDocument, _ NPCRegistry, _ map[string]HealingItemContract) {
			document.Version = 2
		}},
		{name: "knowledge fingerprint mismatch", edit: func(document *stockCatalogDocument, _ NPCRegistry, _ map[string]HealingItemContract) {
			document.KnowledgeFingerprint = "other"
		}},
		{name: "missing offers", edit: func(document *stockCatalogDocument, _ NPCRegistry, _ map[string]HealingItemContract) {
			document.Offers = nil
		}},
		{name: "empty alias", edit: func(document *stockCatalogDocument, _ NPCRegistry, _ map[string]HealingItemContract) {
			document.Offers[0].Alias = " \t"
		}},
		{name: "duplicate normalized alias", edit: func(document *stockCatalogDocument, _ NPCRegistry, _ map[string]HealingItemContract) {
			document.Offers = append(document.Offers, stockCatalogOffer{Alias: " Small-Meat-Shop ", Item: "small-meat", NPC: "trainer", ShopIndex: 2, UnitPrice: 12, X: 5, Y: 7})
		}},
		{name: "unknown item", edit: func(document *stockCatalogDocument, _ NPCRegistry, _ map[string]HealingItemContract) {
			document.Offers[0].Item = "large-meat"
		}},
		{name: "unverified item", edit: func(_ *stockCatalogDocument, _ NPCRegistry, healingItems map[string]HealingItemContract) {
			item := healingItems["small-meat"]
			item.Verified = false
			healingItems["small-meat"] = item
		}},
		{name: "item fingerprint mismatch", edit: func(_ *stockCatalogDocument, _ NPCRegistry, healingItems map[string]HealingItemContract) {
			item := healingItems["small-meat"]
			item.SourceFingerprint = "other"
			healingItems["small-meat"] = item
		}},
		{name: "itemset hash missing", edit: func(_ *stockCatalogDocument, _ NPCRegistry, healingItems map[string]HealingItemContract) {
			item := healingItems["small-meat"]
			item.ItemsetSHA256 = " "
			healingItems["small-meat"] = item
		}},
		{name: "unknown NPC", edit: func(document *stockCatalogDocument, _ NPCRegistry, _ map[string]HealingItemContract) {
			document.Offers[0].NPC = "nurse"
		}},
		{name: "NPC fingerprint mismatch", edit: func(_ *stockCatalogDocument, npcs NPCRegistry, _ map[string]HealingItemContract) {
			spec := npcs["trainer"]
			spec.SourceFingerprint = "other"
			npcs["trainer"] = spec
		}},
		{name: "invalid shop index", edit: func(document *stockCatalogDocument, _ NPCRegistry, _ map[string]HealingItemContract) {
			document.Offers[0].ShopIndex = 0
		}},
		{name: "invalid unit price", edit: func(document *stockCatalogDocument, _ NPCRegistry, _ map[string]HealingItemContract) {
			document.Offers[0].UnitPrice = 0
		}},
		{name: "invalid interaction point", edit: func(document *stockCatalogDocument, _ NPCRegistry, _ map[string]HealingItemContract) {
			document.Offers[0].X = 20
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := validStockCatalogDocument()
			npcs, healingItems := stockCatalogDependencies(t)
			if test.edit != nil {
				test.edit(&document, npcs, healingItems)
			}
			path := writeStockCatalogDocument(t, marshalStockCatalogDocument(t, document))
			fingerprint := stockCatalogTestFingerprint
			if test.name == "missing expected fingerprint" {
				fingerprint = ""
			}
			if items, err := LoadStockItems(path, fingerprint, npcs, healingItems); err == nil || items != nil {
				t.Fatalf("accepted invalid catalog: items=%v err=%v", items, err)
			}
		})
	}
}

func TestLoadStockItemsRejectsStrictJSONAndNumericMismatches(t *testing.T) {
	npcs, healingItems := stockCatalogDependencies(t)
	valid := marshalStockCatalogDocument(t, validStockCatalogDocument())
	for _, test := range []struct {
		name string
		raw  []byte
	}{
		{name: "malformed", raw: []byte(`{"version":`)},
		{name: "trailing value", raw: append(valid, []byte(` {}`)...)},
		{name: "trailing malformed", raw: append(valid, []byte(` !`)...)},
		{name: "unknown envelope field", raw: []byte(`{"version":1,"knowledge_fingerprint":"knowledge-v1","offers":[],"extra":true}`)},
		{name: "unknown offer field", raw: []byte(`{"version":1,"knowledge_fingerprint":"knowledge-v1","offers":[{"alias":"meat","item":"small-meat","npc":"trainer","shop_index":1,"unit_price":12,"x":5,"y":7,"extra":true}]}`)},
		{name: "float shop index", raw: []byte(`{"version":1,"knowledge_fingerprint":"knowledge-v1","offers":[{"alias":"meat","item":"small-meat","npc":"trainer","shop_index":1.0,"unit_price":12,"x":5,"y":7}]}`)},
		{name: "string unit price", raw: []byte(`{"version":1,"knowledge_fingerprint":"knowledge-v1","offers":[{"alias":"meat","item":"small-meat","npc":"trainer","shop_index":1,"unit_price":"12","x":5,"y":7}]}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := writeStockCatalogDocument(t, test.raw)
			if items, err := LoadStockItems(path, stockCatalogTestFingerprint, npcs, healingItems); err == nil || items != nil {
				t.Fatalf("accepted invalid JSON: items=%v err=%v", items, err)
			}
		})
	}
}

func TestLoadStockItemsRejectsOversizedProtectedAndNonRegularPaths(t *testing.T) {
	npcs, healingItems := stockCatalogDependencies(t)
	var over bytes.Buffer
	over.WriteByte('{')
	over.WriteString(`"version":1,"knowledge_fingerprint":"knowledge-v1","offers":[],"padding":"`)
	over.WriteString(strings.Repeat("x", maxStockCatalogBytes))
	over.WriteString(`"}`)
	if over.Len() <= maxStockCatalogBytes {
		t.Fatalf("oversize fixture is only %d bytes", over.Len())
	}
	path := writeStockCatalogDocument(t, over.Bytes())
	if items, err := LoadStockItems(path, stockCatalogTestFingerprint, npcs, healingItems); err == nil || items != nil {
		t.Fatalf("accepted oversized catalog: items=%v err=%v", items, err)
	}

	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	protected := filepath.Join(workingDirectory, ".codex", "stock-items.json")
	if items, err := LoadStockItems(protected, stockCatalogTestFingerprint, npcs, healingItems); !errors.Is(err, runtimepath.ErrForbiddenPath) || items != nil {
		t.Fatalf("accepted protected catalog path: items=%v err=%v", items, err)
	}

	directory := t.TempDir()
	for _, test := range []struct {
		name string
		make func(string) error
	}{
		{name: "directory", make: func(path string) error { return os.Mkdir(path, 0700) }},
		{name: "symlink", make: func(path string) error {
			target := filepath.Join(directory, "target.json")
			if err := os.WriteFile(target, marshalStockCatalogDocument(t, validStockCatalogDocument()), 0600); err != nil {
				return err
			}
			return os.Symlink(target, path)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(directory, test.name)
			if err := test.make(path); err != nil {
				t.Fatal(err)
			}
			if items, err := LoadStockItems(path, stockCatalogTestFingerprint, npcs, healingItems); err == nil || items != nil {
				t.Fatalf("accepted non-regular catalog path: items=%v err=%v", items, err)
			}
		})
	}
}
