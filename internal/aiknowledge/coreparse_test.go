package aiknowledge

import (
	"strings"
	"testing"
)

func TestParseServerIntMatchesEnemybaseAtoiValue(t *testing.T) {
	value, err := parseServerInt("4.50")
	if err != nil || value != 4 {
		t.Fatalf("parseServerInt(4.50) = %d, %v; want 4", value, err)
	}
	if _, err := parseServerInt("4.5x"); err == nil {
		t.Fatal("accepted malformed decimal")
	}
}

func TestParseCoreTablesAndReferences(t *testing.T) {
	experience, issues, err := parseExperience([]byte("0 0\n1 100\n"), "exp.txt", true)
	if err != nil || len(experience) != 2 || len(issues) != 0 {
		t.Fatalf("experience parse: rows=%#v issues=%#v err=%v", experience, issues, err)
	}

	baseFields := make([]string, 56)
	baseFields[0] = "fixture-pet"
	baseFields[6] = "11"
	for i := 7; i < len(baseFields); i++ {
		baseFields[i] = "0"
	}
	baseFields[8] = "4.50"
	bases, issues, err := parseEnemyBases([]byte(strings.Join(baseFields, ",")+"\n"), "enemybase.txt", true)
	if err != nil || len(bases) != 1 || bases[0].TemplateID != 11 || bases[0].LevelUpPoint != 4 || len(issues) != 0 {
		t.Fatalf("enemybase parse: rows=%#v issues=%#v err=%v", bases, issues, err)
	}

	enemyFields := make([]string, 34)
	enemyFields[0] = "fixture-enemy"
	enemyFields[1] = "tactics"
	enemyFields[2] = ""
	for i := 3; i < len(enemyFields); i++ {
		enemyFields[i] = "0"
	}
	enemyFields[3] = "9"
	enemyFields[4] = "11"
	enemyFields[5] = "4"
	enemyFields[6] = "4"
	enemyFields[7] = "3"
	enemyFields[8] = "1"
	enemyFields[9] = "7"
	enemies, issues, err := parseEnemies([]byte(strings.Join(enemyFields, ",")+"\n"), "enemy.txt", true)
	if err != nil || len(enemies) != 1 || enemies[0].ID != 9 || enemies[0].TemplateID != 11 || len(issues) != 0 {
		t.Fatalf("enemy parse: rows=%#v issues=%#v err=%v", enemies, issues, err)
	}

	groupFields := make([]string, 24)
	groupFields[0] = "fixture-group"
	for i := 1; i < len(groupFields); i++ {
		groupFields[i] = "-1"
	}
	groupFields[1] = "5"
	groupFields[4] = "9"
	groupFields[14] = "100"
	groups, issues, err := parseGroups([]byte(strings.Join(groupFields, ",")+"\n"), "group.txt", true)
	if err != nil || len(groups) != 1 || groups[0].ID != 5 || groups[0].EnemyIDs[0] != 9 || len(issues) != 0 {
		t.Fatalf("group parse: rows=%#v issues=%#v err=%v", groups, issues, err)
	}

	encounterFields := make([]string, 33)
	for i := range encounterFields {
		encounterFields[i] = "-1"
	}
	encounterFields[0] = "7"
	encounterFields[1] = "100"
	encounterFields[2] = "1"
	encounterFields[3] = "2"
	encounterFields[4] = "3"
	encounterFields[5] = "4"
	encounterFields[6] = "10"
	encounterFields[7] = "20"
	encounterFields[8] = "2"
	encounterFields[9] = "0"
	encounterFields[10] = "5"
	encounterFields[20] = "100"
	encounterFields[30] = "0"
	encounterFields[31] = "0"
	encounterFields[32] = "5"
	encounters, issues, err := parseEncounters([]byte(strings.Join(encounterFields, ",")+"\n"), "encount.txt", true)
	if err != nil || len(encounters) != 1 || encounters[0].ID != 7 || encounters[0].GroupIDs[0] != 5 || len(issues) != 0 {
		t.Fatalf("encounter parse: rows=%#v issues=%#v err=%v", encounters, issues, err)
	}

	warps, issues, err := parseMapWarps([]byte("NONE:NULL:100,1,2:101,3,4:NULL\n"), "map/mapwarp.txt", true)
	if err != nil || len(warps) != 1 || warps[0].From.Floor != 100 || warps[0].To.X != 3 || len(issues) != 0 {
		t.Fatalf("warp parse: rows=%#v issues=%#v err=%v", warps, issues, err)
	}

	k := &Knowledge{EnemyBases: bases, EnemiesTable: enemies, Groups: groups, Encounters: encounters}
	if err := validateReferences(k, true); err != nil {
		t.Fatal(err)
	}
	areas := deriveLevelingAreas(k)
	if len(areas) != 1 || !areas[0].Verified || areas[0].Levels.Min != 4 || areas[0].Levels.Max != 4 {
		t.Fatalf("derived area: %#v", areas)
	}
}

func TestStrictCoreParserRejectsMalformedRows(t *testing.T) {
	if _, _, err := parseMapWarps([]byte("broken\n"), "map/mapwarp.txt", true); err == nil {
		t.Fatal("strict parser accepted malformed mapwarp")
	}
	warps, issues, err := parseMapWarps([]byte("broken\nNONE:NULL:100,1,2:101,3,4:NULL\n"), "map/mapwarp.txt", false)
	if err != nil || len(warps) != 1 || !hasIssue(issues, "mapwarp_fields") {
		t.Fatalf("non-strict parser: warps=%#v issues=%#v err=%v", warps, issues, err)
	}
}
