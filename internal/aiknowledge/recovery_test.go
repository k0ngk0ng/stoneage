package aiknowledge

import (
	"os"
	"path/filepath"
	"testing"
)

// The deployed tables are checked in with the archived server, so the parser
// is exercised against the data it actually has to read rather than a fixture
// that only resembles it.
func archiveDataDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("..", "..", "server", "legacy", "source", "2.5", "gmsv", "data")
	if _, err := os.Stat(filepath.Join(dir, "magic.txt")); err != nil {
		t.Skipf("archived data tables unavailable: %v", err)
	}
	return dir
}

func TestLoadRecoveryTablesReadsTheDeployedTables(t *testing.T) {
	tables, err := LoadRecoveryTables(archiveDataDir(t))
	if err != nil {
		t.Fatal(err)
	}

	// magic.txt carries nineteen HP recovery rows: six self heals (ids 0-5),
	// six single-target heals (ids 10-15) and the whole-side 恩惠 line
	// (ids 20-26) as archived. Anything else means the column positions moved.
	if len(tables.Magic) != 19 {
		t.Fatalf("recovery spells = %d, want 19", len(tables.Magic))
	}

	byID := make(map[int32]MagicRecovery, len(tables.Magic))
	for _, spell := range tables.Magic {
		byID[spell.ID] = spell
	}
	for _, want := range []MagicRecovery{
		{ID: 0, Amount: 80, Target: 0, Name: "治愈的精灵 Lv1"},
		{ID: 5, Amount: 1160, Target: 0, Name: "治愈的精灵 Lv6"},
		{ID: 10, Amount: 65, Target: 1, Name: "滋润的精灵 Lv1"},
		{ID: 15, Amount: 880, Target: 1, Name: "滋润的精灵 Lv6"},
	} {
		got, ok := byID[want.ID]
		if !ok {
			t.Fatalf("magic id %d missing", want.ID)
		}
		if got.Amount != want.Amount || got.Target != want.Target || got.Name != want.Name {
			t.Fatalf("magic id %d = %+v, want %+v", want.ID, got, want)
		}
	}

	// The self-healing line must not be usable on someone else: its target
	// type is what keeps a pet alive.
	if got := byID[0].Target; got != magicTargetMyself {
		t.Fatalf("治愈的精灵 target = %d, want %d", got, magicTargetMyself)
	}
	if got := byID[10].Target; got != magicTargetOther {
		t.Fatalf("滋润的精灵 target = %d, want %d", got, magicTargetOther)
	}

	// Ordered cheapest first, so a caller can pick the smallest sufficient
	// option without re-sorting.
	for i := 1; i < len(tables.Magic); i++ {
		if tables.Magic[i-1].Amount > tables.Magic[i].Amount {
			t.Fatalf("spells are not ordered by amount at %d", i)
		}
	}
}

func TestLoadRecoveryTablesReadsItems(t *testing.T) {
	tables, err := LoadRecoveryTables(archiveDataDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(tables.Items) < 400 {
		t.Fatalf("recovery items = %d, want the several hundred the table holds", len(tables.Items))
	}
	for name, want := range map[string]int32{
		"小块肉":  20,
		"普通的肉": 40,
		"带骨的肉": 65,
	} {
		got, ok := tables.Items[name]
		if !ok {
			t.Fatalf("%q missing from the recovery items", name)
		}
		if got.Amount != want {
			t.Fatalf("%q amount = %d, want %d", name, got.Amount, want)
		}
	}

	// A weapon is not a recovery item, and a name shared by rows that disagree
	// must be held out rather than guessed at.
	if _, ok := tables.Items["小斧头"]; ok {
		t.Fatal("a plain weapon was classified as a recovery item")
	}
}

func TestRecoveryItemNamesSharedByDisagreeingRowsAreDropped(t *testing.T) {
	lines := []string{
		"歧义药,,,,,,,,,,ITEM_useRecovery,,,,,,900,1,50,20,0,0,0,-1",
		"歧义药,,,,,,,,,,,,,,,,901,2,0,20,0,0,0,-1",
		"一致药,,,,,,,,,,ITEM_useRecovery,,,,,,902,3,30,20,0,0,0,-1",
		"一致药,,,,,,,,,,ITEM_useRecovery,,,,,,903,4,30,20,0,0,0,-1",
		"无害物,,,,,,,,,,,,,,,,904,5,10,20,0,0,0,-1",
	}
	items := parseRecoveryItems(lines)
	if _, ok := items["歧义药"]; ok {
		t.Fatal("a name whose rows disagree was accepted")
	}
	if got, ok := items["一致药"]; !ok || got.Amount != 30 {
		t.Fatalf("agreeing rows = %+v, %t; want amount 30", got, ok)
	}
	if _, ok := items["无害物"]; ok {
		t.Fatal("a non-recovery row was accepted")
	}
}

func TestRecoveryItemsWithoutAnAmountAreHeldOut(t *testing.T) {
	lines := []string{"无数量药,,,,,,,,,,ITEM_useRecovery,,,,,,905,6,,20,0,0,0,-1"}
	if items := parseRecoveryItems(lines); len(items) != 0 {
		t.Fatalf("an unrankable recovery row was accepted: %+v", items)
	}
}

func TestRecoveryMagicSkipsMalformedRows(t *testing.T) {
	lines := []string{
		"短线,描述,MAGIC_Recovery,80",
		"坏标识,描述,MAGIC_Recovery,80,notanumber,0,0",
		"零数量,描述,MAGIC_Recovery,0,7,0,0",
		"攻击,描述,MAGIC_AttMagic,80,8,0,0",
		"好的,描述,MAGIC_OtherRecovery,65,9,0,1",
	}
	spells := parseRecoveryMagic(lines)
	if len(spells) != 1 || spells[0].ID != 9 {
		t.Fatalf("spells = %+v, want only id 9", spells)
	}
}
