package arenaagent

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

func (s Simulation) fixtureBudget() int { return 20 + 3*(max(1, s.FixtureLevel)-1) }

// Largest-remainder scaling retains the requested proportions and exact
// integer budget. Ties use attribute order; zero allocations stay zero.
func scaledFixtureBuild(original [4]int, budget int) [4]int {
	var scaled [4]int
	indices := []int{0, 1, 2, 3}
	total := 0
	for i, n := range original {
		scaled[i] = n * budget / 20
		total += scaled[i]
	}
	sort.SliceStable(indices, func(i, j int) bool {
		return original[indices[i]]*budget%20 > original[indices[j]]*budget%20
	})
	for i := 0; total < budget; i++ {
		scaled[indices[i]]++
		total++
	}
	return scaled
}

// SAAC archives are name|summary|escaped-character-data. Only change five
// exact top-level numeric fields. Preserve the native encoding, character ID,
// inventory, pets and summary bytes; the next native save regenerates summary.
// This is a fixture editor, not a game feature or general character importer.
func preparedFixtureArchive(raw []byte, index, level int, original [4]int) ([]byte, error) {
	if level < 2 || level > 140 || index < 0 || index >= 10 {
		return nil, fmt.Errorf("invalid isolated profile fixture")
	}
	total := 0
	for _, n := range original {
		if n < 0 || n > 20 {
			return nil, fmt.Errorf("invalid original fixture attributes")
		}
		total += n
	}
	if total != 20 {
		return nil, fmt.Errorf("fixture must start from the native 20-point creation budget")
	}
	parts := bytes.SplitN(raw, []byte("|"), 3)
	if len(parts) != 3 || string(parts[0]) != fmt.Sprintf("LadderQA%02d", index) {
		return nil, fmt.Errorf("not the requested synthetic character archive")
	}
	keys := []string{"vi", "str", "tou", "dx"}
	wanted := map[string]string{"lv": "1", "wht": "1", "arg": fmt.Sprintf("ladderqa%02d", index), "skup": "0"}
	for i, key := range keys {
		wanted[key] = strconv.Itoa(original[i] * 100)
	}
	scaled := scaledFixtureBuild(original, 20+3*(level-1))
	updates := map[string]string{"lv": strconv.Itoa(level)}
	for i, key := range keys {
		updates[key] = strconv.Itoa(scaled[i] * 100)
	}
	seen := map[string]bool{}
	lines := bytes.Split(parts[2], []byte(`\n`))
	for i, line := range lines {
		pair := bytes.SplitN(line, []byte("="), 2)
		key := string(pair[0])
		want, relevant := wanted[key]
		if !relevant {
			continue
		}
		if len(pair) != 2 || seen[key] || string(pair[1]) != want {
			return nil, fmt.Errorf("saved fixture %s differs from original public observation", key)
		}
		seen[key] = true
		if value, change := updates[key]; change {
			lines[i] = []byte(key + "=" + value)
		}
	}
	if len(seen) != len(wanted) {
		return nil, fmt.Errorf("incomplete original fixture profile")
	}
	parts[2] = bytes.Join(lines, []byte(`\n`))
	return bytes.Join(parts, []byte("|")), nil
}

// Caller has stopped all clients and all three isolated fixture servers.
func (s Simulation) prepareSavedRoster(before map[int]Object) error {
	if os.Getenv("STONEAGE_ARENA_ISOLATED") != "1" || s.FixtureLevel < 2 || len(before) != 2*s.Mode {
		return fmt.Errorf("saved roster preparation requires the isolated complete fixture")
	}
	type change struct {
		path         string
		old, current []byte
	}
	var changes []change
	var manifest []Object
	for index := 0; index < 2*s.Mode; index++ {
		paths, err := filepath.Glob(filepath.Join(s.Work, "saac/char/*", fmt.Sprintf("ladderqa%02d.0.char", index)))
		if err != nil || len(paths) != 1 {
			return fmt.Errorf("missing or ambiguous synthetic saved character %d", index)
		}
		path := paths[0]
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 128<<10 {
			return fmt.Errorf("invalid fixture archive %d", index)
		}
		player := obj(before[index]["Player"])
		if !yes(player["CombatStatsKnown"]) || integer(player["Level"]) != 1 {
			return fmt.Errorf("fixture is not a known new character")
		}
		var original [4]int
		for j, key := range []string{"Vital", "Strength", "Toughness", "Dexterity"} {
			if _, ok := player[key]; !ok {
				return fmt.Errorf("incomplete fixture attribute %s", key)
			}
			original[j] = integer(player[key])
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		updated, err := preparedFixtureArchive(raw, index, s.FixtureLevel, original)
		if err != nil {
			return err
		}
		changes = append(changes, change{path, raw, updated})
		manifest = append(manifest, Object{"member_index": index, "attributes": scaledFixtureBuild(original, s.fixtureBudget()),
			"before_sha256": fmt.Sprintf("%x", sha256.Sum256(raw)), "after_sha256": fmt.Sprintf("%x", sha256.Sum256(updated))})
	}
	dir := filepath.Join(s.Work, "original-profiles")
	if err := os.Mkdir(dir, 0700); err != nil {
		return err
	}
	// Keep all original archives before changing any profile.
	for _, change := range changes {
		if err := writePrivate(filepath.Join(dir, filepath.Base(change.path)), change.old); err != nil {
			return err
		}
	}
	for _, change := range changes {
		if err := writePrivate(change.path, change.current); err != nil {
			return err
		}
	}
	return writePrivate(filepath.Join(s.Work, "prepared-roster.json"), append(enc(Object{
		"schema": "stopped-isolated-roster-v1", "level": s.FixtureLevel, "player_budget": s.fixtureBudget(), "members": manifest,
		"pet_changes": false, "requires_public_relogin_validation": true}), '\n'))
}

func (s Simulation) checkScenarioCoverage(observerWithdrawn int, matches []Object) error {
	if s.RequireWithdrawal && observerWithdrawn == 0 {
		return fmt.Errorf("fixture did not exercise a new policy decision after original observer withdrawal")
	}
	longest := 0
	for _, match := range matches {
		longest = max(longest, integer(match["decision_turns"]))
	}
	if longest < s.MinBattleTurns {
		return fmt.Errorf("fixture reached %d decision turns, requires %d", longest, s.MinBattleTurns)
	}
	return nil
}
