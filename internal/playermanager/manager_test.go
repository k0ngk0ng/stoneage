package playermanager

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/gamecatalog"
	"github.com/k0ngk0ng/stoneage/internal/playerbridge"
	"github.com/k0ngk0ng/stoneage/internal/playerdata"
)

type archiveKey struct {
	account string
	slot    int
}

type archiveWrite struct {
	account     string
	slot        int
	expected    []byte
	replacement []byte
}

type testArchives struct {
	data     map[archiveKey][]byte
	reads    []archiveKey
	writes   []archiveWrite
	writeErr error
}

func (a *testArchives) Read(_ context.Context, account string, slot int) ([]byte, error) {
	key := archiveKey{account: account, slot: slot}
	a.reads = append(a.reads, key)
	data, ok := a.data[key]
	if !ok {
		return nil, playerdata.ErrNotFound
	}
	return bytes.Clone(data), nil
}

func (a *testArchives) Write(_ context.Context, account string, slot int, expected, replacement []byte) error {
	key := archiveKey{account: account, slot: slot}
	a.writes = append(a.writes, archiveWrite{
		account:     account,
		slot:        slot,
		expected:    bytes.Clone(expected),
		replacement: bytes.Clone(replacement),
	})
	if a.writeErr != nil {
		return a.writeErr
	}
	current, ok := a.data[key]
	if !ok || !bytes.Equal(current, expected) {
		return playerdata.ErrConflict
	}
	a.data[key] = bytes.Clone(replacement)
	return nil
}

type testGame struct {
	calls []map[string]string
	call  func(map[string]string) (map[string]string, error)
}

func (g *testGame) Call(_ context.Context, values map[string]string) (map[string]string, error) {
	copyValues := make(map[string]string, len(values))
	for key, value := range values {
		copyValues[key] = value
	}
	g.calls = append(g.calls, copyValues)
	if g.call == nil {
		return nil, fmt.Errorf("unexpected game call")
	}
	return g.call(copyValues)
}

func countGameAction(g *testGame, action string) int {
	count := 0
	for _, call := range g.calls {
		if call["action"] == action {
			count++
		}
	}
	return count
}

func offlineGame() *testGame {
	return &testGame{call: func(values map[string]string) (map[string]string, error) {
		switch values["action"] {
		case "snapshot":
			return nil, &playerbridge.Error{Code: "target_not_online", Message: "offline"}
		case "inspect_item":
			return inspectTestItem(values)
		default:
			return nil, fmt.Errorf("unexpected offline action %q", values["action"])
		}
	}}
}

// inspectTestItem is a deterministic stand-in for the native template
// inspector. Its defaults are deliberately independent of the simplified
// archive; fields explicitly present in the archive still override them.
func inspectTestItem(values map[string]string) (map[string]string, error) {
	item, err := playerdata.ParseItem([]byte(values["payload"]))
	if err != nil {
		return nil, err
	}
	id, err := item.Integer("id")
	if err != nil {
		return nil, err
	}
	idText := strconv.FormatInt(id, 10)
	result := map[string]string{
		"kind":                         "item",
		"template_id":                  idText,
		"item.inspected.id":            idText,
		"item.inspected.name":          "Template Item " + idText,
		"item.inspected.field.ma":      "700",
		"item.inspected.field.upin":    "7",
		"item.inspected.field.canpile": "99",
		"item.inspected.field.bi":      "7000",
	}
	for _, key := range []string{"ma", "upin", "bi"} {
		if raw, ok := item.Raw(key); ok {
			result["item.inspected.field."+key] = string(raw)
		}
	}
	if _, ok := item.Raw("na"); ok {
		name, err := item.Text("na")
		if err != nil {
			return nil, err
		}
		result["item.inspected.name"] = name
	}
	return result, nil
}

func testArchive(fields ...string) []byte {
	inner := "name=Hero\n"
	for _, field := range fields {
		inner += field + "\n"
	}
	inner += "DATAEND=1\n"
	return []byte("Hero|lv=1|" + escapeTestSave(inner))
}

func escapeTestSave(value string) string {
	return strings.NewReplacer(
		"\\", "\\y",
		"\n", "\\n",
		",", "\\c",
		"|", "\\z",
	).Replace(value)
}

func newOfflineManager(t *testing.T, archive []byte) (*Manager, *testArchives, *testGame) {
	t.Helper()
	archives := &testArchives{data: map[archiveKey][]byte{{account: "alice", slot: 0}: bytes.Clone(archive)}}
	game := offlineGame()
	return &Manager{Archives: archives, Game: game}, archives, game
}

func attributeValue(snapshot playerdata.Snapshot, key string) (int64, bool) {
	for _, attribute := range snapshot.Attributes {
		if attribute.Key == key {
			return attribute.Value, true
		}
	}
	return 0, false
}

func possessionAttribute(possession playerdata.Possession, key string) (int64, bool) {
	for _, attribute := range possession.Attributes {
		if attribute.Key == key {
			return attribute.Value, true
		}
	}
	return 0, false
}

func TestApplyOfflineUsesRevisionCASAndReadsSavedArchiveBack(t *testing.T) {
	original := testArchive("gld=45")
	manager, archives, _ := newOfflineManager(t, original)

	before, err := manager.Get(context.Background(), "alice", 0)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if before.Online || before.Name != "Hero" {
		t.Fatalf("unexpected offline snapshot: %+v", before)
	}

	got, err := manager.Apply(context.Background(), "alice", 0, playerdata.Mutation{
		Revision: before.Revision,
		Action:   "set_character",
		Field:    "gld",
		Value:    900,
	})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	value, ok := attributeValue(got, "gld")
	if !ok || value != 900 {
		t.Fatalf("saved snapshot gld = %d, present=%v; want 900", value, ok)
	}
	if got.Revision == before.Revision {
		t.Fatal("saved snapshot revision did not change")
	}
	if len(archives.writes) != 1 {
		t.Fatalf("archive writes = %d, want 1", len(archives.writes))
	}
	write := archives.writes[0]
	if !bytes.Equal(write.expected, original) {
		t.Fatal("write did not use the exact archive read for CAS")
	}
	if !bytes.Equal(archives.data[archiveKey{account: "alice", slot: 0}], write.replacement) {
		t.Fatal("test archive did not retain replacement")
	}
	reloaded, err := playerdata.ParseSave(write.replacement)
	if err != nil {
		t.Fatalf("replacement is not a valid save: %v", err)
	}
	if value, err := reloaded.Character.Integer("gld"); err != nil || value != 900 {
		t.Fatalf("replacement gld = %d, err=%v; want 900", value, err)
	}
}

func TestCatalogLoaderRetriesAndCachesSuccessfulEnrichment(t *testing.T) {
	manager, _, game := newOfflineManager(t, testArchive("item5=id=777|"))
	baseCall := game.call
	game.call = func(values map[string]string) (map[string]string, error) {
		if values["action"] == "inspect_item" {
			inspected, err := inspectTestItem(values)
			if err != nil {
				return nil, err
			}
			// Leave these two fields absent so this test continues to exercise
			// catalog fallback after native inspection.
			delete(inspected, "item.inspected.name")
			delete(inspected, "item.inspected.field.bi")
			return inspected, nil
		}
		return baseCall(values)
	}
	calls := 0
	manager.CatalogLoader = func() (*gamecatalog.Catalog, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("GMSV data is not ready")
		}
		return &gamecatalog.Catalog{Items: []gamecatalog.Item{{Entry: gamecatalog.Entry{
			Kind: gamecatalog.KindItem, ID: 777, Name: "懒加载物品", GraphicID: 123,
		}}}}, nil
	}

	first, err := manager.Get(context.Background(), "alice", 0)
	if err != nil {
		t.Fatalf("first Get() error = %v", err)
	}
	if len(first.Possessions) != 1 || first.Possessions[0].Name != "" {
		t.Fatalf("first snapshot possessions = %+v, want an unenriched item", first.Possessions)
	}
	second, err := manager.Get(context.Background(), "alice", 0)
	if err != nil {
		t.Fatalf("second Get() error = %v", err)
	}
	if len(second.Possessions) != 1 || second.Possessions[0].Name != "懒加载物品" || second.Possessions[0].GraphicID != 123 {
		t.Fatalf("second snapshot possessions = %+v, want catalog enrichment", second.Possessions)
	}
	if _, err = manager.Get(context.Background(), "alice", 0); err != nil {
		t.Fatalf("cached Get() error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("catalog loader calls = %d, want 2", calls)
	}
}

func TestOfflinePetGrowthEditsPreservePackedBytes(t *testing.T) {
	original := testArchive("pet0=lv:1|name:Pet|ownt:|dmswc:777|lvup:-760981768|llt:3|slt:3|")
	manager, archives, _ := newOfflineManager(t, original)

	before, err := manager.Get(context.Background(), "alice", 0)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	wantBefore := map[string]int64{"growth_vi": 0xd2, "growth_str": 0xa4, "growth_tou": 0x56, "growth_dx": 0xf8}
	for key, want := range wantBefore {
		if value, ok := possessionAttribute(before.Possessions[0], key); !ok || value != want {
			t.Fatalf("before %s = %d present=%v, want %d", key, value, ok, want)
		}
	}

	got, err := manager.Apply(context.Background(), "alice", 0, playerdata.Mutation{
		Revision: before.Revision,
		Action:   "set_pet",
		Location: "inventory",
		Slot:     0,
		Field:    "growth_str",
		Value:    9,
	})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	wantAfter := map[string]int64{"growth_vi": 0xd2, "growth_str": 9, "growth_tou": 0x56, "growth_dx": 0xf8}
	for key, want := range wantAfter {
		if value, ok := possessionAttribute(got.Possessions[0], key); !ok || value != want {
			t.Fatalf("after %s = %d present=%v, want %d", key, value, ok, want)
		}
	}
	if len(archives.writes) != 1 {
		t.Fatalf("archive writes = %d, want 1", len(archives.writes))
	}
	replacement, err := playerdata.ParseSave(archives.writes[0].replacement)
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := replacement.Character.Raw("pet0")
	if !ok {
		t.Fatal("updated pet disappeared")
	}
	pet, err := playerdata.ParsePet(raw)
	if err != nil {
		t.Fatal(err)
	}
	packed, err := pet.Integer("lvup")
	if err != nil || packed != -771139848 {
		t.Fatalf("saved packed lvup = %d, err=%v, want -771139848", packed, err)
	}
}

func TestOnlineSnapshotExpandsPackedPetGrowth(t *testing.T) {
	snapshot, err := onlineSnapshot(map[string]string{
		"account":                    "alice",
		"character":                  "Hero",
		"character_slot":             "0",
		"sequence":                   "42",
		"revision":                   "0123456789abcdef",
		"pet.inventory.0.id":         "777",
		"pet.inventory.0.name":       "Pet",
		"pet.inventory.0.user_name":  "",
		"pet.inventory.0.graphic_id": "123",
		"character.graphic_id":       "100020",
		"pet.inventory.0.field.lvup": "-760981768",
		"pet.inventory.0.field.llt":  "5",
		"pet.inventory.0.field.slt":  "3",
		"pet.inventory.0.skill.0":    "-1",
		"pet.inventory.0.skill.1":    "-1",
		"pet.inventory.0.skill.2":    "-1",
		"pet.inventory.0.skill.3":    "-1",
		"pet.inventory.0.skill.4":    "-1",
		"pet.inventory.0.skill.5":    "-1",
		"pet.inventory.0.skill.6":    "-1",
	}, "alice", 0)
	if err != nil {
		t.Fatalf("onlineSnapshot() error = %v", err)
	}
	if snapshot.GraphicID != 100020 {
		t.Fatalf("online character graphic = %d", snapshot.GraphicID)
	}
	if len(snapshot.Possessions) != 1 {
		t.Fatalf("possessions = %d, want 1", len(snapshot.Possessions))
	}
	for key, want := range map[string]int64{"growth_vi": 0xd2, "growth_str": 0xa4, "growth_tou": 0x56, "growth_dx": 0xf8} {
		if value, ok := possessionAttribute(snapshot.Possessions[0], key); !ok || value != want {
			t.Fatalf("online %s = %d present=%v, want %d", key, value, ok, want)
		}
	}
}

func TestOfflinePetWarehouseGrantUsesTransmigrationCapacity(t *testing.T) {
	pet := "lv:1|name:Pet|ownt:|dmswc:777|lvup:0|llt:1|slt:1|"
	fields := []string{"trn=0"}
	for slot := 0; slot < 6; slot++ {
		fields = append(fields, "poolpet"+strconv.Itoa(slot)+"="+pet)
	}
	original := testArchive(fields...)
	manager, archives, game := newOfflineManager(t, original)
	manager.Catalog = &gamecatalog.Catalog{Pets: []gamecatalog.Pet{{Entry: gamecatalog.Entry{
		Kind: gamecatalog.KindPet, ID: 777, TemplateID: 777,
	}}}}

	before, err := manager.Get(context.Background(), "alice", 0)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if before.Capacities["pet_warehouse"] != 5 {
		t.Fatalf("pet warehouse capacity = %d, want 5", before.Capacities["pet_warehouse"])
	}
	if len(before.Possessions) != 6 {
		t.Fatalf("existing pets = %d, want 6 including one beyond capacity", len(before.Possessions))
	}
	_, err = manager.Apply(context.Background(), "alice", 0, playerdata.Mutation{
		Revision:   before.Revision,
		Action:     "grant_pet",
		Location:   "warehouse",
		TemplateID: 777,
		Quantity:   1,
	})
	if err == nil || !strings.Contains(err.Error(), "空位不足") {
		t.Fatalf("grant beyond capacity error = %v, want capacity error", err)
	}
	if len(archives.writes) != 0 || countGameAction(game, "export_pet") != 0 {
		t.Fatalf("capacity rejection writes=%d exports=%d", len(archives.writes), countGameAction(game, "export_pet"))
	}
}

func TestOfflineHydrationUsesTemplateDefaultsWithoutPersistingDisplayFields(t *testing.T) {
	original := testArchive("item5=id=777|")
	manager, archives, game := newOfflineManager(t, original)

	before, err := manager.Get(context.Background(), "alice", 0)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if len(before.Possessions) != 1 {
		t.Fatalf("possessions = %d, want one item", len(before.Possessions))
	}
	item := before.Possessions[0]
	if item.Name != "Template Item 777" || item.GraphicID != 7000 {
		t.Fatalf("hydrated item identity = name %q graphic %d", item.Name, item.GraphicID)
	}
	if value, ok := possessionAttribute(item, "ma"); !ok || value != 700 {
		t.Fatalf("hydrated ma = %d, present=%v; want 700", value, ok)
	}
	if value, ok := possessionAttribute(item, "upin"); !ok || value != 7 {
		t.Fatalf("hydrated upin = %d, present=%v; want template default 7", value, ok)
	}
	if !bytes.Equal(archives.data[archiveKey{account: "alice", slot: 0}], original) {
		t.Fatal("display hydration changed the source archive")
	}
	originalDoc, err := playerdata.ParseSave(original)
	if err != nil {
		t.Fatal(err)
	}
	originalRaw, _ := originalDoc.Character.Raw("item5")
	if string(originalRaw) != "id=777|" {
		t.Fatalf("source item after display hydration = %q", originalRaw)
	}

	got, err := manager.Apply(context.Background(), "alice", 0, playerdata.Mutation{
		Revision: before.Revision,
		Action:   "set_item",
		Location: "inventory",
		Slot:     5,
		Field:    "upin",
		Value:    3,
	})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(archives.writes) != 1 {
		t.Fatalf("archive writes = %d, want one", len(archives.writes))
	}
	if !bytes.Equal(archives.writes[0].expected, original) {
		t.Fatal("save CAS expected hydrated display bytes instead of source bytes")
	}
	replacementDoc, err := playerdata.ParseSave(archives.writes[0].replacement)
	if err != nil {
		t.Fatal(err)
	}
	replacementRaw, _ := replacementDoc.Character.Raw("item5")
	if string(replacementRaw) != "id=777|upin=3|" {
		t.Fatalf("saved item = %q, want only requested upin change", replacementRaw)
	}
	parsedReplacement, err := playerdata.ParseItem(replacementRaw)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"ma", "canpile", "bi"} {
		if _, ok := replacementDoc.Character.Raw("item5"); !ok {
			t.Fatal("saved item disappeared")
		}
		if _, ok := parsedReplacement.Raw(key); ok {
			t.Fatalf("template-only field %s was persisted", key)
		}
	}
	if len(got.Possessions) != 1 {
		t.Fatalf("saved possessions = %d, want one", len(got.Possessions))
	}
	if value, ok := possessionAttribute(got.Possessions[0], "upin"); !ok || value != 3 {
		t.Fatalf("saved display upin = %d, present=%v; want 3", value, ok)
	}
	if value, ok := possessionAttribute(got.Possessions[0], "ma"); !ok || value != 700 {
		t.Fatalf("saved display ma = %d, present=%v; want hydrated default 700", value, ok)
	}
	if countGameAction(game, "snapshot") != 3 || countGameAction(game, "inspect_item") != 4 {
		t.Fatalf("game actions = snapshot:%d inspect:%d, want 3/4", countGameAction(game, "snapshot"), countGameAction(game, "inspect_item"))
	}
}

func TestApplyOfflineRejectsStaleRevisionWithoutWriting(t *testing.T) {
	manager, archives, _ := newOfflineManager(t, testArchive("gld=45"))
	current, err := manager.Get(context.Background(), "alice", 0)
	if err != nil {
		t.Fatal(err)
	}

	_, err = manager.Apply(context.Background(), "alice", 0, playerdata.Mutation{
		Revision: strings.Repeat("0", len(current.Revision)),
		Action:   "set_character",
		Field:    "gld",
		Value:    901,
	})
	if !errors.Is(err, playerdata.ErrConflict) {
		t.Fatalf("Apply() error = %v, want ErrConflict", err)
	}
	if len(archives.writes) != 0 {
		t.Fatalf("stale Apply() wrote %d archives", len(archives.writes))
	}
	data, err := archives.Read(context.Background(), "alice", 0)
	if err != nil || !bytes.Equal(data, testArchive("gld=45")) {
		t.Fatalf("archive changed after stale Apply(): err=%v data=%q", err, data)
	}
}

func TestApplyOfflinePropagatesArchiveCASConflict(t *testing.T) {
	manager, archives, _ := newOfflineManager(t, testArchive("gld=45"))
	current, err := manager.Get(context.Background(), "alice", 0)
	if err != nil {
		t.Fatal(err)
	}
	archives.writeErr = playerdata.ErrConflict

	_, err = manager.Apply(context.Background(), "alice", 0, playerdata.Mutation{
		Revision: current.Revision,
		Action:   "set_character",
		Field:    "gld",
		Value:    902,
	})
	if !errors.Is(err, playerdata.ErrConflict) {
		t.Fatalf("Apply() error = %v, want ErrConflict", err)
	}
	if len(archives.writes) != 1 {
		t.Fatalf("archive writes = %d, want one attempted CAS", len(archives.writes))
	}
	if !bytes.Equal(archives.data[archiveKey{account: "alice", slot: 0}], testArchive("gld=45")) {
		t.Fatal("archive changed after failed CAS")
	}
}

func TestOfflineGrantRejectsInsufficientCapacityAtomically(t *testing.T) {
	fields := make([]string, 0, 14)
	for slot := 5; slot < 19; slot++ {
		fields = append(fields, fmt.Sprintf("item%d=id=100|", slot))
	}
	original := testArchive(fields...)
	manager, archives, game := newOfflineManager(t, original)
	manager.Catalog = &gamecatalog.Catalog{Items: []gamecatalog.Item{{Entry: gamecatalog.Entry{
		Kind: gamecatalog.KindItem, ID: 777, TemplateID: 777,
	}}}}

	current, err := manager.Get(context.Background(), "alice", 0)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if len(current.Possessions) != 14 {
		t.Fatalf("occupied possessions = %d, want 14", len(current.Possessions))
	}
	_, err = manager.Apply(context.Background(), "alice", 0, playerdata.Mutation{
		Revision:   current.Revision,
		Action:     "grant_item",
		Location:   "inventory",
		TemplateID: 777,
		Quantity:   2,
	})
	if err == nil || !strings.Contains(err.Error(), "空位不足") {
		t.Fatalf("Apply() error = %v, want capacity error", err)
	}
	if len(archives.writes) != 0 {
		t.Fatalf("insufficient grant wrote %d archives", len(archives.writes))
	}
	if countGameAction(game, "snapshot") != 2 || countGameAction(game, "inspect_item") != 28 || countGameAction(game, "export_item") != 0 {
		t.Fatalf("game actions = snapshot:%d inspect:%d export:%d, want 2/28/0", countGameAction(game, "snapshot"), countGameAction(game, "inspect_item"), countGameAction(game, "export_item"))
	}
	if !bytes.Equal(archives.data[archiveKey{account: "alice", slot: 0}], original) {
		t.Fatal("archive changed after rejected grant")
	}
}

func onlineRevision(account string, slot int, sequence, revision string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(account+"\x00"+strconv.Itoa(slot)+"\x00"+sequence+"\x00"+revision)))
}

func saveDataHash(t *testing.T, archive []byte) string {
	t.Helper()
	doc, err := playerdata.ParseSave(archive)
	if err != nil {
		t.Fatal(err)
	}
	hash := fnv.New64a()
	_, _ = hash.Write(doc.Character.Bytes())
	return fmt.Sprintf("%016x", hash.Sum64())
}

func TestApplyOnlineSeparatesCharacterSlotFromAssetSlotAndReadsBackSave(t *testing.T) {
	oldArchive := testArchive("gld=45")
	newArchive := testArchive("gld=900")
	archives := &testArchives{data: map[archiveKey][]byte{{account: "alice", slot: 1}: bytes.Clone(oldArchive)}}
	sequence, revision := "42", "0123456789abcdef"
	saved := false
	onlineResponse := func() map[string]string {
		response := map[string]string{
			"account":        "alice",
			"character":      "Hero",
			"character_slot": "1",
			"sequence":       sequence,
			"revision":       revision,
		}
		if saved {
			response["attribute.gld"] = "900"
		} else {
			response["attribute.gld"] = "45"
		}
		return response
	}
	newArchiveHash := saveDataHash(t, newArchive)
	game := &testGame{}
	game.call = func(values map[string]string) (map[string]string, error) {
		switch values["action"] {
		case "snapshot":
			return onlineResponse(), nil
		case "set_item":
			if values["character_slot"] != "1" || values["slot"] != "7" {
				return nil, fmt.Errorf("character_slot=%q slot=%q", values["character_slot"], values["slot"])
			}
			if values["location"] != "inventory" || values["field"] != "upin" || values["value"] != "2" {
				return nil, fmt.Errorf("unexpected asset mutation values: %#v", values)
			}
			// The game makes the new archive visible before acknowledging the
			// mutation. waitForSave must verify this new content, then Get must
			// expose the post-save attribute.
			archives.data[archiveKey{account: "alice", slot: 1}] = bytes.Clone(newArchive)
			saved = true
			return map[string]string{"save_data_hash": newArchiveHash}, nil
		default:
			return nil, fmt.Errorf("unexpected game action %q", values["action"])
		}
	}
	manager := &Manager{Archives: archives, Game: game}

	before, err := manager.Get(context.Background(), "alice", 1)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	wantRevision := onlineRevision("alice", 1, sequence, revision)
	if before.Revision != wantRevision || !before.Online {
		t.Fatalf("online snapshot revision/flag = %q/%v, want %q/true", before.Revision, before.Online, wantRevision)
	}
	if value, ok := attributeValue(before, "gld"); !ok || value != 45 {
		t.Fatalf("initial online gld = %d, present=%v; want 45", value, ok)
	}

	got, err := manager.Apply(context.Background(), "alice", 1, playerdata.Mutation{
		Revision: before.Revision,
		Action:   "set_item",
		Location: "inventory",
		Slot:     7,
		Field:    "upin",
		Value:    2,
	})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if !got.Online || got.Name != "Hero" {
		t.Fatalf("readback snapshot = %+v", got)
	}
	if value, ok := attributeValue(got, "gld"); !ok || value != 900 {
		t.Fatalf("readback online gld = %d, present=%v; want 900", value, ok)
	}
	if !saved || !bytes.Equal(archives.data[archiveKey{account: "alice", slot: 1}], newArchive) {
		t.Fatal("new archive was not made visible before save confirmation")
	}
	if len(archives.writes) != 0 {
		t.Fatalf("online mutation wrote %d archives directly", len(archives.writes))
	}
	if len(archives.reads) < 3 {
		t.Fatalf("archive reads = %d, want current, save confirmation, and readback", len(archives.reads))
	}
	if len(game.calls) != 4 {
		t.Fatalf("game calls = %d, want snapshot, mutation, readback snapshot plus initial snapshot", len(game.calls))
	}
	mutationCall := game.calls[2]
	if mutationCall["character_slot"] != "1" || mutationCall["slot"] != "7" {
		t.Fatalf("mutation call mixed character and asset slots: %#v", mutationCall)
	}
}

func TestApplyOnlineRejectsMissingOrInvalidSaveHash(t *testing.T) {
	tests := []struct {
		name        string
		hash        string
		withTimeout bool
	}{
		{name: "missing", hash: ""},
		{name: "malformed", hash: "bad-hash"},
		{name: "wrong", hash: strings.Repeat("f", 16), withTimeout: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			archive := testArchive("gld=45")
			archives := &testArchives{data: map[archiveKey][]byte{{account: "alice", slot: 1}: bytes.Clone(archive)}}
			game := &testGame{}
			game.call = func(values map[string]string) (map[string]string, error) {
				switch values["action"] {
				case "snapshot":
					return map[string]string{
						"account":        "alice",
						"character":      "Hero",
						"character_slot": "1",
						"sequence":       "42",
						"revision":       "0123456789abcdef",
					}, nil
				case "set_item":
					return map[string]string{"save_data_hash": test.hash}, nil
				default:
					return nil, fmt.Errorf("unexpected game action %q", values["action"])
				}
			}
			manager := &Manager{Archives: archives, Game: game}
			before, err := manager.Get(context.Background(), "alice", 1)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			var cancel context.CancelFunc
			if test.withTimeout {
				ctx, cancel = context.WithTimeout(ctx, 40*time.Millisecond)
				defer cancel()
			}
			_, err = manager.Apply(ctx, "alice", 1, playerdata.Mutation{
				Revision: before.Revision,
				Action:   "set_item",
				Location: "inventory",
				Slot:     7,
				Field:    "upin",
				Value:    2,
			})
			if !errors.Is(err, playerdata.ErrUnavailable) {
				t.Fatalf("Apply() error = %v, want ErrUnavailable", err)
			}
			if len(archives.writes) != 0 {
				t.Fatalf("invalid save hash caused %d archive writes", len(archives.writes))
			}
		})
	}
}

func TestApplyOnlineCancellationDuringSaveConfirmationCannotSucceed(t *testing.T) {
	archive := testArchive("gld=45")
	archives := &testArchives{data: map[archiveKey][]byte{{account: "alice", slot: 1}: bytes.Clone(archive)}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	game := &testGame{}
	game.call = func(values map[string]string) (map[string]string, error) {
		switch values["action"] {
		case "snapshot":
			return map[string]string{
				"account":        "alice",
				"character":      "Hero",
				"character_slot": "1",
				"sequence":       "42",
				"revision":       "0123456789abcdef",
			}, nil
		case "set_item":
			cancel()
			return map[string]string{"save_data_hash": saveDataHash(t, archive)}, nil
		default:
			return nil, fmt.Errorf("unexpected game action %q", values["action"])
		}
	}
	manager := &Manager{Archives: archives, Game: game}
	before, err := manager.Get(context.Background(), "alice", 1)
	if err != nil {
		t.Fatal(err)
	}

	_, err = manager.Apply(ctx, "alice", 1, playerdata.Mutation{
		Revision: before.Revision,
		Action:   "set_item",
		Location: "inventory",
		Slot:     7,
		Field:    "upin",
		Value:    2,
	})
	if err == nil {
		t.Fatal("Apply() reported success after context cancellation")
	}
}

func giftMutation(revision string, quantity int) playerdata.Mutation {
	return playerdata.Mutation{
		Revision:   revision,
		Action:     "grant_item",
		Location:   "inventory",
		TemplateID: 777,
		Quantity:   quantity,
	}
}

func giftCatalog() *gamecatalog.Catalog {
	return &gamecatalog.Catalog{Items: []gamecatalog.Item{{Entry: gamecatalog.Entry{
		Kind: gamecatalog.KindItem, ID: 777, TemplateID: 777, Name: "Gift",
	}}}}
}

func TestOfflineGrantWritesMultipleGiftsAtomically(t *testing.T) {
	original := testArchive()
	manager, archives, game := newOfflineManager(t, original)
	manager.Catalog = giftCatalog()
	const payload = "id=777|na=Gift|"
	game.call = func(values map[string]string) (map[string]string, error) {
		switch values["action"] {
		case "snapshot":
			return nil, &playerbridge.Error{Code: "target_not_online", Message: "offline"}
		case "inspect_item":
			return inspectTestItem(values)
		case "export_item":
			if values["template_id"] != "777" {
				return nil, fmt.Errorf("template_id=%q", values["template_id"])
			}
			return map[string]string{"kind": "item", "template_id": "777", "payload": payload}, nil
		default:
			return nil, fmt.Errorf("unexpected game action %q", values["action"])
		}
	}

	before, err := manager.Get(context.Background(), "alice", 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := manager.Apply(context.Background(), "alice", 0, giftMutation(before.Revision, 2))
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(archives.writes) != 1 {
		t.Fatalf("archive writes = %d, want one atomic write", len(archives.writes))
	}
	if len(got.Possessions) != 2 {
		t.Fatalf("gift possessions = %d, want 2", len(got.Possessions))
	}
	replacement := archives.writes[0].replacement
	doc, err := playerdata.ParseSave(replacement)
	if err != nil {
		t.Fatalf("replacement is not a valid save: %v", err)
	}
	for _, slot := range []int{5, 6} {
		raw, ok := doc.Character.Raw("item" + strconv.Itoa(slot))
		if !ok {
			t.Fatalf("missing granted item in slot %d", slot)
		}
		item, err := playerdata.ParseItem(raw)
		if err != nil {
			t.Fatalf("item%d parse: %v", slot, err)
		}
		if id, err := item.Integer("id"); err != nil || id != 777 {
			t.Fatalf("item%d id=%d err=%v, want 777", slot, id, err)
		}
	}
	if countGameAction(game, "snapshot") != 3 || countGameAction(game, "inspect_item") != 2 || countGameAction(game, "export_item") != 2 {
		t.Fatalf("game actions = snapshot:%d inspect:%d export:%d, want 3/2/2", countGameAction(game, "snapshot"), countGameAction(game, "inspect_item"), countGameAction(game, "export_item"))
	}
}

func TestOfflineGrantSecondExportFailureDoesNotWritePartialCAS(t *testing.T) {
	original := testArchive()
	manager, archives, game := newOfflineManager(t, original)
	manager.Catalog = giftCatalog()
	exports := 0
	game.call = func(values map[string]string) (map[string]string, error) {
		switch values["action"] {
		case "snapshot":
			return nil, &playerbridge.Error{Code: "target_not_online", Message: "offline"}
		case "export_item":
			exports++
			if exports == 2 {
				return nil, errors.New("second export failed")
			}
			return map[string]string{"kind": "item", "template_id": "777", "payload": "id=777|na=Gift|"}, nil
		default:
			return nil, fmt.Errorf("unexpected game action %q", values["action"])
		}
	}

	before, err := manager.Get(context.Background(), "alice", 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Apply(context.Background(), "alice", 0, giftMutation(before.Revision, 2))
	if err == nil || !strings.Contains(err.Error(), "second export failed") {
		t.Fatalf("Apply() error = %v, want second export failure", err)
	}
	if exports != 2 {
		t.Fatalf("exports = %d, want 2", exports)
	}
	if len(archives.writes) != 0 {
		t.Fatalf("partial export failure wrote %d archives", len(archives.writes))
	}
	if !bytes.Equal(archives.data[archiveKey{account: "alice", slot: 0}], original) {
		t.Fatal("archive changed after partial export failure")
	}
}

func bundleCatalog() *gamecatalog.Catalog {
	return &gamecatalog.Catalog{
		Items: []gamecatalog.Item{{Entry: gamecatalog.Entry{
			Kind: gamecatalog.KindItem, ID: 777, TemplateID: 777, Name: "Gift Item",
		}}},
		Pets: []gamecatalog.Pet{{Entry: gamecatalog.Entry{
			Kind: gamecatalog.KindPet, ID: 888, TemplateID: 888, Name: "Gift Pet",
		}}},
	}
}

func TestBundleEntryLimitMatchesNativeContract(t *testing.T) {
	entries := func(kind string, count int) []playerdata.BundleEntry {
		bundle := make([]playerdata.BundleEntry, count)
		for index := range bundle {
			bundle[index] = playerdata.BundleEntry{Kind: kind, TemplateID: 777, Quantity: 1}
		}
		return bundle
	}

	if _, _, err := bundleQuantities(entries("item", 20)); err != nil {
		t.Fatalf("20-entry bundle rejected: %v", err)
	}
	if _, _, err := bundleQuantities(entries("item", 21)); err == nil {
		t.Fatal("21-entry bundle unexpectedly accepted")
	}

	mixed := append(entries("item", 15), entries("pet", 5)...)
	items, pets, err := bundleQuantities(mixed)
	if err != nil {
		t.Fatalf("15-item/5-pet bundle rejected: %v", err)
	}
	if items != 15 || pets != 5 {
		t.Fatalf("mixed bundle quantities = items:%d pets:%d, want items:15 pets:5", items, pets)
	}
}

func TestOfflineBundleWritesMixedContentsWithOneCAS(t *testing.T) {
	original := testArchive()
	manager, archives, game := newOfflineManager(t, original)
	manager.Catalog = bundleCatalog()
	game.call = func(values map[string]string) (map[string]string, error) {
		switch values["action"] {
		case "snapshot":
			return nil, &playerbridge.Error{Code: "target_not_online", Message: "offline"}
		case "inspect_item":
			return inspectTestItem(values)
		case "export_item":
			return map[string]string{"kind": "item", "template_id": "777", "payload": "id=777|na=Gift|"}, nil
		case "export_pet":
			return map[string]string{"kind": "pet", "template_id": "888", "payload": "dmswc:888|name:Wolf|ownt:|"}, nil
		default:
			return nil, fmt.Errorf("unexpected game action %q", values["action"])
		}
	}

	before, err := manager.Get(context.Background(), "alice", 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := manager.Apply(context.Background(), "alice", 0, playerdata.Mutation{
		Revision: before.Revision,
		Action:   "grant_bundle",
		Location: "inventory",
		Bundle: []playerdata.BundleEntry{
			{Kind: "item", TemplateID: 777, Quantity: 2, Name: "补给"},
			{Kind: "pet", TemplateID: 888, Quantity: 1, Name: "伙伴"},
		},
	})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(archives.writes) != 1 {
		t.Fatalf("archive writes = %d, want one atomic write", len(archives.writes))
	}
	if len(got.Possessions) != 3 {
		t.Fatalf("bundle possessions = %d, want 3", len(got.Possessions))
	}
	replacement := archives.writes[0].replacement
	doc, err := playerdata.ParseSave(replacement)
	if err != nil {
		t.Fatalf("replacement is not a valid save: %v", err)
	}
	for _, slot := range []int{5, 6} {
		raw, ok := doc.Character.Raw("item" + strconv.Itoa(slot))
		if !ok {
			t.Fatalf("missing bundle item in slot %d", slot)
		}
		item, err := playerdata.ParseItem(raw)
		if err != nil {
			t.Fatalf("item%d parse: %v", slot, err)
		}
		if id, err := item.Integer("id"); err != nil || id != 777 {
			t.Fatalf("item%d id=%d err=%v, want 777", slot, id, err)
		}
		if name, err := item.Text("na"); err != nil || name != "补给" {
			t.Fatalf("item%d name=%q err=%v, want 补给", slot, name, err)
		}
	}
	raw, ok := doc.Character.Raw("pet0")
	if !ok {
		t.Fatal("missing bundle pet in slot 0")
	}
	pet, err := playerdata.ParsePet(raw)
	if err != nil {
		t.Fatalf("pet parse: %v", err)
	}
	if id, err := pet.Integer("dmswc"); err != nil || id != 888 {
		t.Fatalf("pet id=%d err=%v, want 888", id, err)
	}
	if name, err := pet.Text("ownt"); err != nil || name != "伙伴" {
		t.Fatalf("pet name=%q err=%v, want 伙伴", name, err)
	}
	if countGameAction(game, "export_item") != 2 || countGameAction(game, "export_pet") != 1 {
		t.Fatalf("exports = item:%d pet:%d, want 2/1", countGameAction(game, "export_item"), countGameAction(game, "export_pet"))
	}
}

func TestOfflineBundleCapacityFailureDoesNotExportOrWrite(t *testing.T) {
	fields := make([]string, 0, 16)
	for slot := 5; slot < 20; slot++ {
		fields = append(fields, "item"+strconv.Itoa(slot)+"=id=777|")
	}
	original := testArchive(fields...)
	manager, archives, game := newOfflineManager(t, original)
	manager.Catalog = bundleCatalog()
	before, err := manager.Get(context.Background(), "alice", 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Apply(context.Background(), "alice", 0, playerdata.Mutation{
		Revision: before.Revision,
		Action:   "grant_bundle",
		Location: "inventory",
		Bundle: []playerdata.BundleEntry{
			{Kind: "item", TemplateID: 777, Quantity: 1},
			{Kind: "pet", TemplateID: 888, Quantity: 1},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "空位不足") {
		t.Fatalf("capacity error = %v, want capacity rejection", err)
	}
	if len(archives.writes) != 0 || countGameAction(game, "export_item") != 0 || countGameAction(game, "export_pet") != 0 {
		t.Fatalf("capacity rejection writes=%d exports=%d/%d", len(archives.writes), countGameAction(game, "export_item"), countGameAction(game, "export_pet"))
	}
}
