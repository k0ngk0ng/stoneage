package aiservice

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStockMaterialsRequireReviewedBoundTemplates(t *testing.T) {
	row := []string{"Voucher", "", ""}
	for len(row) < 23 {
		row = append(row, "0")
	}
	row[16] = "20031"
	row[17] = "24000"
	row[18] = "50"
	raw := []byte(strings.Join(row, ",") + "\n")
	path := filepath.Join(t.TempDir(), "itemset.txt")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	npcs, healing := stockCatalogDependencies(t)
	valid := func() stockCatalogDocument {
		return stockCatalogDocument{Version: 2, KnowledgeFingerprint: stockCatalogTestFingerprint, ItemsetSHA256: fmt.Sprintf("%x", sha256.Sum256(raw)), Materials: []stockMaterial{{Alias: "voucher", TemplateID: 20031, Verified: true}}, Offers: []stockCatalogOffer{{Alias: "voucher-shop", Item: "voucher", NPC: "trainer", ShopIndex: 1, UnitPrice: 50, X: 5, Y: 7}}}
	}
	for _, test := range []struct {
		name string
		edit func(*stockCatalogDocument)
	}{
		{"valid", func(d *stockCatalogDocument) {}},
		{"unreviewed", func(d *stockCatalogDocument) { d.Materials[0].Verified = false }},
		{"missing template", func(d *stockCatalogDocument) { d.Materials[0].TemplateID = 9999 }},
		{"duplicate alias", func(d *stockCatalogDocument) {
			d.Materials = append(d.Materials, stockMaterial{Alias: " VOUCHER ", TemplateID: 20031, Verified: true})
		}},
		{"healing alias collision", func(d *stockCatalogDocument) { d.Materials[0].Alias = " SMALL-MEAT " }},
		{"hash mismatch", func(d *stockCatalogDocument) { d.ItemsetSHA256 = strings.Repeat("a", 64) }},
		{"version 1 material", func(d *stockCatalogDocument) { d.Version = 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := valid()
			test.edit(&d)
			got, err := decodeStockItemsForData(marshalStockCatalogDocument(t, d), stockCatalogTestFingerprint, npcs, healing, path)
			if test.name == "valid" {
				if err != nil || got["voucher-shop"].TemplateID != 20031 {
					t.Fatalf("got=%v err=%v", got, err)
				}
			} else if err == nil {
				t.Fatal("unsafe material accepted")
			}
		})
	}
	d := valid()
	catalog := writeStockCatalogDocument(t, marshalStockCatalogDocument(t, d))
	if _, err := LoadStockItems(catalog, stockCatalogTestFingerprint, npcs, healing); err == nil {
		t.Fatal("v2 loaded without item table")
	}
	if _, err := LoadStockItemsForData(catalog, stockCatalogTestFingerprint, npcs, healing, path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, raw...), 0600); err != nil {
		t.Fatal(err)
	}
	d.ItemsetSHA256 = fmt.Sprintf("%x", sha256.Sum256(append(raw, raw...)))
	if _, err := decodeStockItemsForData(marshalStockCatalogDocument(t, d), stockCatalogTestFingerprint, npcs, healing, path); err == nil {
		t.Fatal("ambiguous duplicate template accepted")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadStockItemsForData(catalog, stockCatalogTestFingerprint, npcs, healing, link); err == nil {
		t.Fatal("symlink table accepted")
	}
}
