package arenaagent

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func fixtureArchive(index int, allocation [4]int) []byte {
	return []byte(fmt.Sprintf(`LadderQA%02d|original-summary|pn=0\nlv=1\nhp=20\nvi=%d\nstr=%d\ntou=%d\ndx=%d\nwht=1\nskup=0\narg=ladderqa%02d\ncharid=fixture-identity\npet0=lv:1\zvi:480\zname:pet\n`, index, allocation[0]*100, allocation[1]*100, allocation[2]*100, allocation[3]*100, index))
}

func TestSimulationSavedRosterChangesOnlyRequestedNumericFields(t *testing.T) {
	for _, input := range [][4]int{{0, 20, 0, 0}, {14, 4, 2, 0}, {5, 5, 5, 5}, {1, 1, 1, 17}} {
		for _, level := range []int{2, 35, 140} {
			s := Simulation{FixtureLevel: level}
			scaled := scaledFixtureBuild(input, s.fixtureBudget())
			total := 0
			for i, n := range scaled {
				total += n
				if n < 0 || input[i] == 0 && n != 0 {
					t.Fatal("changed zero/valid allocation", input, scaled)
				}
			}
			if total != s.fixtureBudget() {
				t.Fatal("lost integer budget", input, scaled)
			}
			raw := fixtureArchive(2, input)
			original := bytes.Clone(raw)
			prepared, err := preparedFixtureArchive(raw, 2, level, input)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(raw, original) {
				t.Fatal("mutated original bytes")
			}
			// Independently construct the expected entire archive: pets, identity,
			// native byte strings and summary must remain untouched.
			want := bytes.Replace(fixtureArchive(2, scaled), []byte(`\nlv=1\n`), []byte(fmt.Sprintf(`\nlv=%d\n`, level)), 1)
			if !bytes.Equal(prepared, want) {
				t.Fatal("changed unrelated saved profile fields")
			}
		}
	}
	if got := scaledFixtureBuild([4]int{14, 4, 2, 0}, 122); got != [4]int{86, 24, 12, 0} {
		t.Fatal("wrong largest-remainder allocation", got)
	}
	// Do not decode/reencode legacy non-UTF8 names or nested escaped values.
	raw := bytes.Replace(fixtureArchive(0, [4]int{0, 20, 0, 0}), []byte("name:pet"), []byte{'n', 'a', 'm', 'e', ':', 0xff, 0xfe}, 1)
	prepared, err := preparedFixtureArchive(raw, 0, 35, [4]int{0, 20, 0, 0})
	if err != nil || !bytes.Contains(prepared, []byte{0xff, 0xfe}) {
		t.Fatal("legacy bytes lost", err)
	}
}

func TestSimulationSavedRosterRejectsUncertainProfilesBeforeWriting(t *testing.T) {
	allocation := [4]int{0, 20, 0, 0}
	raw := fixtureArchive(0, allocation)
	for _, corrupt := range [][]byte{
		[]byte("not an archive"),
		bytes.Replace(raw, []byte("LadderQA00"), []byte("someone-else"), 1),
		bytes.Replace(raw, []byte("arg=ladderqa00"), []byte("arg=ladderqa01"), 1),
		bytes.Replace(raw, []byte("wht=1"), []byte("wht=3"), 1),
		bytes.Replace(raw, []byte(`lv=1\n`), []byte(`lv=35\n`), 1),
		bytes.Replace(raw, []byte(`vi=0\n`), nil, 1),
		append(bytes.Clone(raw), []byte(`vi=0\n`)...),
		bytes.Replace(raw, []byte("str=2000"), []byte("str=1999"), 1),
	} {
		if _, err := preparedFixtureArchive(corrupt, 0, 35, allocation); err == nil {
			t.Fatal("ambiguous archive accepted")
		}
	}
	root := t.TempDir()
	charDir := filepath.Join(root, "saac/char/fixture")
	if err := os.MkdirAll(charDir, 0700); err != nil {
		t.Fatal(err)
	}
	before := map[int]Object{}
	for i := 0; i < 2; i++ {
		body := fixtureArchive(i, allocation)
		if i == 1 {
			body = []byte("bad second archive")
		}
		if err := os.WriteFile(filepath.Join(charDir, fmt.Sprintf("ladderqa%02d.0.char", i)), body, 0600); err != nil {
			t.Fatal(err)
		}
		before[i] = Object{"Player": Object{"Level": 1, "CombatStatsKnown": true, "Vital": 0, "Strength": 20, "Toughness": 0, "Dexterity": 0}}
	}
	t.Setenv("STONEAGE_ARENA_ISOLATED", "1")
	s := Simulation{Work: root, Mode: 1, FixtureLevel: 35}
	if err := s.prepareSavedRoster(before); err == nil {
		t.Fatal("partial invalid roster accepted")
	}
	unchanged, err := os.ReadFile(filepath.Join(charDir, "ladderqa00.0.char"))
	if err != nil || !bytes.Equal(unchanged, raw) {
		t.Fatal("first archive changed before roster validated", err)
	}
	if _, err := os.Stat(filepath.Join(root, "original-profiles")); !os.IsNotExist(err) {
		t.Fatal("failed validation created preparation directory", err)
	}
	if err := os.WriteFile(filepath.Join(charDir, "ladderqa01.0.char"), fixtureArchive(1, allocation), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.prepareSavedRoster(before); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(filepath.Join(root, "original-profiles/ladderqa00.0.char"))
	if err != nil || !bytes.Equal(original, raw) {
		t.Fatal("original archive not retained", err)
	}
	if _, err := os.Stat(filepath.Join(root, "prepared-roster.json")); err != nil {
		t.Fatal(err)
	}
	if err := s.prepareSavedRoster(before); err == nil {
		t.Fatal("overwrote already prepared profile")
	}
}

func TestSimulationPreparedRosterRequiresActualReloginAttributes(t *testing.T) {
	s := Simulation{Mode: 1, Allocation: "14,4,2,0", FixtureLevel: 35}
	before := map[int]Object{}
	for i := 0; i < 2; i++ {
		before[i] = Object{"Player": Object{"Level": 35, "CombatStatsKnown": true, "Vital": 86, "Strength": 24, "Toughness": 12, "Dexterity": 0}}
	}
	if _, err := s.initialRosterEvidence(before); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"Level", "Vital", "Strength"} {
		player := obj(before[1]["Player"])
		old := player[key]
		player[key] = 1
		if _, err := s.initialRosterEvidence(before); err == nil {
			t.Fatal("accepted incorrect relogin field", key)
		}
		player[key] = old
	}
}

func TestSimulationCoverageCannotPassWithoutRequestedBoundary(t *testing.T) {
	s := Simulation{RequireWithdrawal: true, MinBattleTurns: 20}
	for _, tc := range []struct {
		withdrawn, turns int
		pass             bool
	}{{0, 30, false}, {4, 19, false}, {1, 20, true}} {
		err := s.checkScenarioCoverage(tc.withdrawn, []Object{{"decision_turns": tc.turns}})
		if (err == nil) != tc.pass {
			t.Fatal("incorrect scenario coverage", tc, err)
		}
	}
	root := t.TempDir()
	for _, value := range []string{"0", "-1", "141"} {
		var out bytes.Buffer
		err := Main(context.Background(), []string{"simulate", "--root", root, "--work", "build/must-not-start", "--fixture-level", value}, "test", &out)
		if err == nil || !strings.Contains(err.Error(), "--fixture-level must") {
			t.Fatal("explicit invalid level silently defaulted or reached container", value, err)
		}
	}
	s = Simulation{Root: root, Work: "build/test", Mode: 2, Matches: 2, Strategy: "learned", Model: "build/model.json", FixtureLevel: 35, RequireWithdrawal: true, MinBattleTurns: 20}
	valid, err := s.validate()
	if err != nil {
		t.Fatal(err)
	}
	args := valid.containerArguments()
	for flag, want := range map[string]string{"--fixture-level": "35", "--min-battle-turns": "20"} {
		found := false
		for i, arg := range args {
			if arg == flag {
				found = true
				if args[i+1] != want {
					t.Fatal("flag changed across container", args)
				}
			}
		}
		if !found {
			t.Fatal("lost fixture flag", flag)
		}
	}
	if !strings.Contains(strings.Join(args, " "), "--require-withdrawal") {
		t.Fatal("lost withdrawal assertion")
	}
	for _, level := range []int{-1, 141} {
		bad := s
		bad.FixtureLevel = level
		if _, err := bad.validate(); err == nil {
			t.Fatal("invalid level accepted", strconv.Itoa(level))
		}
	}
	s.Mode = 1
	if _, err := s.validate(); err == nil {
		t.Fatal("cannot continue after only member withdraws")
	}
	if !reflect.DeepEqual(scaledFixtureBuild([4]int{5, 5, 5, 5}, 20), [4]int{5, 5, 5, 5}) {
		t.Fatal("default budget changed")
	}
}
