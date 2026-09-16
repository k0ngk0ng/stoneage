package aiservice

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/runtimepath"
)

const healingItemCatalogTestFingerprint = "knowledge-v1"

func validHealingItemCatalogDocument() healingItemCatalogDocument {
	return healingItemCatalogDocument{
		Version:              1,
		ItemsetSHA256:        strings.Repeat("a", 64),
		KnowledgeFingerprint: healingItemCatalogTestFingerprint,
		Items: []healingItemCatalogEntry{{
			Alias: "small-meat", TemplateID: 2344, BaseHP: 20, Verified: true,
		}},
	}
}

func writeHealingItemCatalogDocument(t *testing.T, raw []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "healing-items.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func marshalHealingItemCatalogDocument(t *testing.T, document healingItemCatalogDocument) []byte {
	t.Helper()
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestLoadHealingItemsEmptyPathDisablesItemHealing(t *testing.T) {
	contracts, err := LoadHealingItems("", "")
	if err != nil {
		t.Fatal(err)
	}
	if contracts == nil || len(contracts) != 0 {
		t.Fatalf("empty path contracts=%v", contracts)
	}
}

func TestLoadHealingItemsValidatesEnvelopeAndReturnsReviewedContracts(t *testing.T) {
	document := validHealingItemCatalogDocument()
	document.Items = append(document.Items, healingItemCatalogEntry{
		Alias: " Large-Meat ", TemplateID: 2346, BaseHP: 65, Verified: true,
	})
	path := writeHealingItemCatalogDocument(t, marshalHealingItemCatalogDocument(t, document))
	contracts, err := LoadHealingItems(path, healingItemCatalogTestFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	first, ok := contracts["small-meat"]
	if !ok || first.TemplateID != 2344 || first.BaseHP != 20 || !first.Verified || first.SourceFingerprint != healingItemCatalogTestFingerprint {
		t.Fatalf("small-meat contract=%+v ok=%v", first, ok)
	}
	second, ok := contracts["large-meat"]
	if !ok || second.TemplateID != 2346 || second.BaseHP != 65 || !second.Verified || second.SourceFingerprint != healingItemCatalogTestFingerprint {
		t.Fatalf("large-meat contract=%+v ok=%v", second, ok)
	}
	if len(contracts) != 2 {
		t.Fatalf("contracts=%v", contracts)
	}
}

func TestLoadHealingItemsRejectsEnvelopeAndEntryValidationErrors(t *testing.T) {
	tests := []struct {
		name string
		edit func(*healingItemCatalogDocument)
		raw  []byte
	}{
		{name: "missing expected fingerprint", raw: marshalHealingItemCatalogDocument(t, validHealingItemCatalogDocument())},
		{name: "missing itemset hash", edit: func(document *healingItemCatalogDocument) { document.ItemsetSHA256 = "" }},
		{name: "invalid itemset hash", edit: func(document *healingItemCatalogDocument) { document.ItemsetSHA256 = "not-a-hash" }},
		{name: "version", edit: func(document *healingItemCatalogDocument) { document.Version = 2 }},
		{name: "empty knowledge fingerprint", edit: func(document *healingItemCatalogDocument) { document.KnowledgeFingerprint = "   " }},
		{name: "missing items", edit: func(document *healingItemCatalogDocument) { document.Items = nil }},
		{name: "empty alias", edit: func(document *healingItemCatalogDocument) { document.Items[0].Alias = " \t" }},
		{name: "invalid template id", edit: func(document *healingItemCatalogDocument) { document.Items[0].TemplateID = 0 }},
		{name: "negative template id", edit: func(document *healingItemCatalogDocument) { document.Items[0].TemplateID = -1 }},
		{name: "invalid base hp", edit: func(document *healingItemCatalogDocument) { document.Items[0].BaseHP = 0 }},
		{name: "negative base hp", edit: func(document *healingItemCatalogDocument) { document.Items[0].BaseHP = -1 }},
		{name: "unverified entry", edit: func(document *healingItemCatalogDocument) { document.Items[0].Verified = false }},
		{name: "duplicate alias", edit: func(document *healingItemCatalogDocument) { document.Items = append(document.Items, document.Items[0]) }},
		{name: "duplicate normalized alias", edit: func(document *healingItemCatalogDocument) {
			document.Items = append(document.Items, healingItemCatalogEntry{Alias: " Small-Meat ", TemplateID: 2345, BaseHP: 35, Verified: true})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := validHealingItemCatalogDocument()
			if test.edit != nil {
				test.edit(&document)
			}
			raw := test.raw
			if raw == nil {
				raw = marshalHealingItemCatalogDocument(t, document)
			}
			path := writeHealingItemCatalogDocument(t, raw)
			fingerprint := healingItemCatalogTestFingerprint
			if test.name == "missing expected fingerprint" {
				fingerprint = ""
			}
			if contracts, err := LoadHealingItems(path, fingerprint); err == nil || contracts != nil {
				t.Fatalf("accepted invalid catalog: contracts=%v err=%v", contracts, err)
			}
		})
	}

	for _, test := range []struct {
		name string
		raw  []byte
	}{
		{name: "malformed", raw: []byte(`{"version":`)},
		{name: "trailing value", raw: append(marshalHealingItemCatalogDocument(t, validHealingItemCatalogDocument()), []byte(` {}`)...)},
		{name: "trailing malformed", raw: append(marshalHealingItemCatalogDocument(t, validHealingItemCatalogDocument()), []byte(` !`)...)},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := writeHealingItemCatalogDocument(t, test.raw)
			if contracts, err := LoadHealingItems(path, healingItemCatalogTestFingerprint); err == nil || contracts != nil {
				t.Fatalf("accepted malformed or trailing JSON: contracts=%v err=%v", contracts, err)
			}
		})
	}
}

func TestLoadHealingItemsRejectsUnknownFieldsAndWrongEnvelopeName(t *testing.T) {
	valid := marshalHealingItemCatalogDocument(t, validHealingItemCatalogDocument())
	for _, raw := range [][]byte{
		[]byte(`{"version":1,"knowledge_fingerprint":"knowledge-v1","items":[],"extra":true}`),
		[]byte(`{"version":1,"source_fingerprint":"knowledge-v1","items":[]}`),
		[]byte(`{"version":1,"knowledge_fingerprint":"knowledge-v1","items":[{"alias":"food","template_id":77,"base_hp":20,"verified":true,"extra":1}]}`),
	} {
		path := writeHealingItemCatalogDocument(t, raw)
		if contracts, err := LoadHealingItems(path, healingItemCatalogTestFingerprint); err == nil || contracts != nil {
			t.Fatalf("accepted unknown field JSON: contracts=%v err=%v", contracts, err)
		}
	}
	if len(valid) == 0 {
		t.Fatal("valid catalog unexpectedly empty")
	}
}

func TestLoadHealingItemsRejectsFingerprintAndNumericTypeMismatches(t *testing.T) {
	document := validHealingItemCatalogDocument()
	path := writeHealingItemCatalogDocument(t, marshalHealingItemCatalogDocument(t, document))
	if contracts, err := LoadHealingItems(path, "other"); err == nil || contracts != nil {
		t.Fatalf("accepted mismatched fingerprint: contracts=%v err=%v", contracts, err)
	}
	for _, raw := range [][]byte{
		[]byte(`{"version":1,"knowledge_fingerprint":"knowledge-v1","items":[{"alias":"food","template_id":77.0,"base_hp":20,"verified":true}]}`),
		[]byte(`{"version":1,"knowledge_fingerprint":"knowledge-v1","items":[{"alias":"food","template_id":"77","base_hp":20,"verified":true}]}`),
		[]byte(`{"version":1,"knowledge_fingerprint":"knowledge-v1","items":[{"alias":"food","template_id":77,"base_hp":20.0,"verified":true}]}`),
	} {
		path := writeHealingItemCatalogDocument(t, raw)
		if contracts, err := LoadHealingItems(path, healingItemCatalogTestFingerprint); err == nil || contracts != nil {
			t.Fatalf("accepted invalid numeric type: contracts=%v err=%v", contracts, err)
		}
	}
}

func TestLoadHealingItemsRejectsOversizedCatalog(t *testing.T) {
	var over bytes.Buffer
	over.WriteByte('{')
	over.WriteString(`"version":1,"knowledge_fingerprint":"knowledge-v1","items":[],"padding":"`)
	over.WriteString(strings.Repeat("x", maxHealingItemCatalogBytes))
	over.WriteString(`"}`)
	if over.Len() <= maxHealingItemCatalogBytes {
		t.Fatalf("oversize fixture is only %d bytes", over.Len())
	}
	path := writeHealingItemCatalogDocument(t, over.Bytes())
	if contracts, err := LoadHealingItems(path, healingItemCatalogTestFingerprint); err == nil || contracts != nil {
		t.Fatalf("accepted oversized catalog: contracts=%v err=%v", contracts, err)
	}
}

func TestLoadHealingItemsRejectsProtectedRuntimePath(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	protected := filepath.Join(workingDirectory, ".codex", "healing-items.json")
	_, err = LoadHealingItems(protected, healingItemCatalogTestFingerprint)
	if !errors.Is(err, runtimepath.ErrForbiddenPath) {
		t.Fatalf("protected catalog path err=%v", err)
	}
}

func TestLoadHealingItemsRejectsNonRegularFilesBeforeOpen(t *testing.T) {
	directory := t.TempDir()
	for _, test := range []struct {
		name string
		make func(string) error
	}{
		{name: "directory", make: func(path string) error { return os.Mkdir(path, 0700) }},
		{name: "symlink", make: func(path string) error {
			target := filepath.Join(directory, "target.json")
			if err := os.WriteFile(target, marshalHealingItemCatalogDocument(t, validHealingItemCatalogDocument()), 0600); err != nil {
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
			if contracts, err := LoadHealingItems(path, healingItemCatalogTestFingerprint); err == nil || contracts != nil {
				t.Fatalf("accepted non-regular catalog path: contracts=%v err=%v", contracts, err)
			}
		})
	}
}

func TestLoadHealingItemsForDataRejectsChangedItemTable(t *testing.T) {
	root := t.TempDir()
	table := filepath.Join(root, "itemset.txt")
	raw := []byte("reviewed recovery item table")
	if err := os.WriteFile(table, raw, 0600); err != nil {
		t.Fatal(err)
	}
	doc := validHealingItemCatalogDocument()
	doc.ItemsetSHA256 = fmt.Sprintf("%x", sha256.Sum256(raw))
	catalog := writeHealingItemCatalogDocument(t, marshalHealingItemCatalogDocument(t, doc))
	if _, err := LoadHealingItemsForData(catalog, healingItemCatalogTestFingerprint, table); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(table, []byte("changed item effect"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadHealingItemsForData(catalog, healingItemCatalogTestFingerprint, table); err == nil {
		t.Fatal("accepted changed itemset under unchanged knowledge fingerprint")
	}
	if _, err := LoadHealingItemsForData("", "", filepath.Join(root, "missing")); err != nil {
		t.Fatal("disabled catalog read item data:", err)
	}
}
