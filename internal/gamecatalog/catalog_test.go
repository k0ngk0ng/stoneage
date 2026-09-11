package gamecatalog

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func TestLoadRealCatalogFromTrackedServerSource(t *testing.T) {
	root := repositoryRoot(t)
	catalog, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Pets) != 952 {
		t.Fatalf("pets=%d, want 952", len(catalog.Pets))
	}
	if len(catalog.Items) != 12620 {
		t.Fatalf("items=%d, want 12620", len(catalog.Items))
	}
	if len(catalog.PetSkills) != 32 {
		t.Fatalf("pet skills=%d, want 32", len(catalog.PetSkills))
	}

	pet := catalog.Pets[0]
	if pet.ID != 1 || pet.TemplateID != 1 || pet.Name != "乌力" || pet.GraphicID != 100250 {
		t.Fatalf("first pet=%+v", pet)
	}
	if pet.RecordIndex != 0 || pet.Elements != [4]int{80, 20, 0, 0} {
		t.Fatalf("first pet source fields=%+v", pet)
	}

	item := catalog.Items[0]
	if item.ID != 0 || item.TemplateID != 0 || item.Name != "小斧头" || item.GraphicID != 20033 {
		t.Fatalf("first item=%+v", item)
	}
	if item.Description != "攻 +9 防 -3 敏 -3" || item.RecordIndex != 0 {
		t.Fatalf("first item description/source=%q/%d", item.Description, item.RecordIndex)
	}

	skill := catalog.PetSkills[0]
	if skill.ID != 0 || skill.Name != "待机" || skill.Description != "什么也不做" || skill.Function != "PETSKILL_None" {
		t.Fatalf("first skill=%+v", skill)
	}
	attack, ok := catalog.Find(KindPetSkill, 1)
	if !ok || attack.Name != "攻击" || attack.Description != "通常攻击" || attack.GraphicID != 0 {
		t.Fatalf("attack=%+v found=%v", attack, ok)
	}

	if pet.GraphicID != 100250 || item.GraphicID != 20033 {
		t.Fatalf("real source graphics changed: pet=%d item=%d", pet.GraphicID, item.GraphicID)
	}
}

func TestResolveRealBrowserManifestWhenPresent(t *testing.T) {
	root := repositoryRoot(t)
	dataDir := filepath.Join(root, "server", "legacy", "source", "2.5", "gmsv", "data")
	catalog, err := Load(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	pet := catalog.Pets[0]

	// Pet previews come from the native SPR stand row.  actor_bitmaps contains
	// a colliding physical bitmap number for 100250 and is not the pet's
	// direction-1/action-3 appearance.
	spritesPath := filepath.Join(root, "client", "web", "assets", "original", "sprites.json")
	spritesFile, err := os.Open(spritesPath)
	if err != nil {
		t.Fatal(err)
	}
	petPreviews, err := ParsePetPreviews(spritesFile)
	_ = spritesFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	petAsset, ok := petPreviews[pet.GraphicID]
	if !ok || petAsset.File == "" {
		t.Fatalf("pet graphic %d not available in SPR preview manifest: %+v", pet.GraphicID, petAsset)
	}
	if petAsset.File != "bitmaps/bitmap_95929.png" || petAsset.XOffset != -27 || petAsset.YOffset != -34 {
		t.Fatalf("pet graphic %d resolved to non-native stand frame: %+v", pet.GraphicID, petAsset)
	}

	manifestPath := filepath.Join(root, "client", "web", "assets", "original", "manifest.json")
	if _, err := os.Stat(manifestPath); err != nil {
		t.Skipf("optional generated Web manifest is unavailable: %v", err)
	}
	item := catalog.Items[0]
	manifest, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	itemAsset, ok := manifest.ResolveItem(item.GraphicID)
	if !ok || itemAsset.File == "" {
		t.Fatalf("item graphic %d not available through bitmap alias: %+v", item.GraphicID, itemAsset)
	}
	if itemAsset.PhysicalID == item.GraphicID {
		t.Fatalf("item graphic %d unexpectedly skipped logical alias: %+v", item.GraphicID, itemAsset)
	}
}

func TestLoadAcceptsTrackedDataDirectoryVariants(t *testing.T) {
	root := repositoryRoot(t)
	for _, path := range []string{
		root,
		filepath.Join(root, "server", "legacy", "source", "2.5", "gmsv"),
		filepath.Join(root, "server", "legacy", "source", "2.5", "gmsv", "data"),
	} {
		catalog, err := Load(path)
		if err != nil {
			t.Fatalf("Load(%q): %v", path, err)
		}
		if len(catalog.Pets) != 952 {
			t.Fatalf("Load(%q) pets=%d", path, len(catalog.Pets))
		}
	}
}

func TestParsersDecodeRealCP936RowsAndKeepTableDistinction(t *testing.T) {
	root := repositoryRoot(t)
	dataDir := filepath.Join(root, "server", "legacy", "source", "2.5", "gmsv", "data")
	read := func(name string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dataDir, name))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}

	pets, err := ParseEnemyBase(bytes.NewReader(read("enemybase.txt")))
	if err != nil {
		t.Fatal(err)
	}
	if pets[1].Name != "乌力乌力" || pets[1].GraphicID != 100251 || pets[1].ID != 2 {
		t.Fatalf("second pet=%+v", pets[1])
	}
	// enemy.txt uses encounter IDs and names.  It must never silently become a
	// pet template catalog.
	if _, err := ParseEnemyBase(bytes.NewReader(read("enemy.txt"))); err == nil {
		t.Fatal("accepted enemy encounter table as enemybase templates")
	}

	items, err := ParseItemSet(bytes.NewReader(read("itemset.txt")))
	if err != nil {
		t.Fatal(err)
	}
	if items[1].ID != 1 || items[1].GraphicID != 20032 || items[1].Description != "攻 +7 防 -2 敏 -2 猛毒的精灵 Lv1" {
		t.Fatalf("second item=%+v", items[1])
	}

	skills, err := ParsePetSkill(bytes.NewReader(read("petskill.txt")))
	if err != nil {
		t.Fatal(err)
	}
	if skills[1].ID != 1 || skills[1].Name != "攻击" || skills[1].Description != "通常攻击" || skills[1].Field != 1 || skills[1].Target != 6 || skills[1].Cost != 1000 {
		t.Fatalf("second skill=%+v", skills[1])
	}
}

func TestSearchAndJSONUseStableCommonFields(t *testing.T) {
	catalog, err := Load(repositoryRoot(t))
	if err != nil {
		t.Fatal(err)
	}

	results := catalog.Search("猛毒的精灵")
	if len(results) == 0 || results[0].Kind != KindItem || results[0].Name != "贝诺美斯Lv1斧头" {
		t.Fatalf("description search=%+v", results[:min(3, len(results))])
	}
	results = catalog.Search("100250")
	if len(results) == 0 || results[0].Kind != KindPet || results[0].GraphicID != 100250 {
		t.Fatalf("pet image search=%+v", results[:min(3, len(results))])
	}
	results = catalog.Search("petskill_normalattack")
	if len(results) == 0 || results[0].Kind != KindPetSkill || results[0].Name != "攻击" {
		t.Fatalf("skill code search=%+v", results[:min(3, len(results))])
	}

	payload, err := json.Marshal(results[0])
	if err != nil {
		t.Fatal(err)
	}
	jsonText := string(payload)
	for _, key := range []string{`"kind"`, `"id"`, `"template_id"`, `"graphic_id"`, `"description"`} {
		if !strings.Contains(jsonText, key) {
			t.Fatalf("JSON %q missing %s", jsonText, key)
		}
	}
	if strings.Contains(jsonText, "ImageID") || strings.Contains(jsonText, "TemplateID") {
		t.Fatalf("JSON used Go field names: %s", jsonText)
	}
}

func TestParsersRejectMalformedRowsAndAllowUTF8Fixtures(t *testing.T) {
	if _, err := ParseItems(strings.NewReader("名字,,,,\n")); err == nil {
		t.Fatal("accepted short item row")
	}
	if _, err := ParsePets(strings.NewReader("宠物,,,,,,x,,,,,,,,,,,,,,,,,,,,,,,,,,,,,,,,,,,,\n")); err == nil {
		t.Fatal("accepted invalid pet template ID")
	}
	items, err := ParseItems(strings.NewReader("测试,暗名,描述,,,,,,,,,,,,,,0,123,4,1,0,0,0,-1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Name != "测试" || items[0].GraphicID != 123 {
		t.Fatalf("UTF-8 fixture=%+v", items)
	}
}

func TestParseManifestAcceptsStringAndNumericAliases(t *testing.T) {
	manifest, err := ParseManifest(strings.NewReader(`{
		"bitmaps": {
			"42": {"file":"bitmaps/bitmap_42.png", "physical":42, "width":1, "height":2},
			"99": {"file":"bitmaps/bitmap_99.png", "physical":99, "width":3, "height":4}
		},
		"bitmap_aliases": {"100": "42", "101": 99},
		"actor_bitmaps": {"200": "bitmaps/bitmap_200.png"}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	item, ok := manifest.ResolveItem(100)
	if !ok || item.PhysicalID != 42 || item.File != "bitmaps/bitmap_42.png" {
		t.Fatalf("string alias=%+v found=%v", item, ok)
	}
	item, ok = manifest.ResolveItem(101)
	if !ok || item.PhysicalID != 99 || item.File != "bitmaps/bitmap_99.png" {
		t.Fatalf("numeric alias=%+v found=%v", item, ok)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
