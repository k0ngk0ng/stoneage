package aiknowledge

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Recovery tables describe what can restore HP, read from the tables the game
// server itself loads. They exist so a caller can decide "who is hurt and what
// do I have for them" without a hand-written catalog: the deployed tables
// already say which spells and which items recover HP, and how much.
//
// Two column positions are load-bearing and were verified against the data:
//
//   - magic.txt: field 2 names the effect function (MAGIC_Recovery,
//     MAGIC_OtherRecovery), field 3 is the recovered amount, field 4 is the
//     magic id, field 6 is MAGIC_TARGETTYPE. The id column matches the server's
//     own reader, which takes the token at MAGIC_DATACHARNUM+MAGIC_ID (see
//     MAGIC_initMagic): all 181 rows carry a distinct value there.
//   - itemset.txt: a row is a usable HP recovery item when it carries the
//     ITEM_useRecovery argument, and field 18 is its base amount. Item names
//     repeat across rows, so an item is only usable here when every row
//     sharing its name agrees about what it is.
//
// Neither table is part of the core knowledge digest: joining them would move
// Knowledge.Fingerprint and invalidate every reviewed contract bound to it.
type RecoveryTables struct {
	// Magic holds HP recovery spells, cheapest amount first.
	Magic []MagicRecovery
	// Items maps an item name to what it recovers. Only unambiguous names are
	// present: a name whose rows disagree is deliberately absent.
	Items map[string]ItemRecovery
}

// MagicRecovery is one HP recovery spell from magic.txt.
type MagicRecovery struct {
	ID     int32
	Amount int32
	// Target is the MAGIC_TARGETTYPE column: 0 is the caster, 1 and 6 are a
	// single chosen ally, 2 is the caster's whole side, 8 is one whole side.
	Target int32
	Name   string
}

// ItemRecovery is one usable HP recovery item from itemset.txt.
type ItemRecovery struct {
	Name   string
	Amount int32
}

// The magic.txt effect functions that restore an ally's HP.
const (
	magicRecoverySelf  = "MAGIC_Recovery"
	magicRecoveryOther = "MAGIC_OtherRecovery"
)

// MAGIC_TARGETTYPE values worth naming here: the two that matter are whether a
// spell can reach a character other than its caster.
const (
	magicTargetMyself = 0
	magicTargetOther  = 1
	// magicTargetOtherWithoutMyself is the same reach as magicTargetOther, but
	// refuses to land on the caster.
	magicTargetOtherWithoutMyself = 6
	// magicTargetAllMySide and magicTargetWholeOtherSide heal every row on one
	// side at once.
	magicTargetAllMySide      = 2
	magicTargetWholeOtherSide = 8
)

// Field positions in magic.txt, per the MAGIC_DATAINT/MAGIC_DATACHAR order.
const (
	magicFieldFunc   = 2
	magicFieldAmount = 3
	magicFieldID     = 4
	magicFieldTarget = 6
	magicColumnCount = 7
)

// Field position in magic.txt and itemset.txt.
const (
	// The item id lives at itemsetFieldID; the base amount at
	// itemsetFieldAmount. Both were read out of the deployed table.
	itemsetFieldID       = 16
	itemsetFieldAmount   = 18
	itemsetRecoveryToken = "ITEM_useRecovery"
)

// LoadRecoveryTables reads magic.txt and itemset.txt from a gmsv data
// directory. It follows the same encoding policy as the rest of the knowledge
// loader: UTF-8 fixtures first, then the deployed GBK.
func LoadRecoveryTables(dataDir string) (*RecoveryTables, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, fmt.Errorf("aiknowledge: recovery tables need a data directory")
	}
	// Accept the same roots the rest of the loader accepts: a data directory,
	// a gmsv directory, or a checkout that contains one.
	if resolved, err := findDataDir(dataDir); err == nil {
		dataDir = resolved
	}
	tables := &RecoveryTables{Items: make(map[string]ItemRecovery)}

	magicLines, err := readRecoveryLines(filepath.Join(dataDir, "magic.txt"))
	if err != nil {
		return nil, err
	}
	tables.Magic = parseRecoveryMagic(magicLines)

	itemLines, err := readRecoveryLines(filepath.Join(dataDir, "itemset.txt"))
	if err != nil {
		return nil, err
	}
	tables.Items = parseRecoveryItems(itemLines)

	if len(tables.Magic) == 0 && len(tables.Items) == 0 {
		return nil, fmt.Errorf("aiknowledge: %s holds no recovery tables", dataDir)
	}
	return tables, nil
}

func readRecoveryLines(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("aiknowledge: read %s: %w", filepath.Base(path), err)
	}
	lines, issues := decodeLines(raw, true)
	text := make([]string, 0, len(lines))
	for _, line := range lines {
		if line.Text == "" || strings.HasPrefix(strings.TrimSpace(line.Text), "#") {
			continue
		}
		text = append(text, line.Text)
	}
	// A malformed line is a warning everywhere else in this loader; here the
	// only consequence is a missing recovery option, so the issues are dropped
	// rather than turned into a failure.
	_ = issues
	return text, nil
}

func parseRecoveryMagic(lines []string) []MagicRecovery {
	spells := make([]MagicRecovery, 0, 16)
	for _, line := range lines {
		fields := strings.Split(line, ",")
		if len(fields) < magicColumnCount {
			continue
		}
		effect := strings.TrimSpace(fields[magicFieldFunc])
		if effect != magicRecoverySelf && effect != magicRecoveryOther {
			continue
		}
		id, ok := recoveryInt(fields[magicFieldID])
		if !ok || id < 0 {
			continue
		}
		amount, ok := recoveryInt(fields[magicFieldAmount])
		if !ok || amount <= 0 {
			continue
		}
		target, _ := recoveryInt(fields[magicFieldTarget])
		spells = append(spells, MagicRecovery{
			ID:     int32(id),
			Amount: int32(amount),
			Target: int32(target),
			Name:   strings.TrimSpace(fields[0]),
		})
	}
	sort.Slice(spells, func(i, j int) bool {
		if spells[i].Amount != spells[j].Amount {
			return spells[i].Amount < spells[j].Amount
		}
		return spells[i].ID < spells[j].ID
	})
	return spells
}

type recoveryItemRow struct {
	name       string
	isRecovery bool
	amount     int32
}

func parseRecoveryItems(lines []string) map[string]ItemRecovery {
	byName := make(map[string][]recoveryItemRow)
	for _, line := range lines {
		fields := strings.Split(line, ",")
		if len(fields) <= itemsetFieldAmount {
			continue
		}
		name := strings.TrimSpace(fields[0])
		if name == "" {
			continue
		}
		row := recoveryItemRow{name: name}
		for _, field := range fields {
			if strings.Contains(field, itemsetRecoveryToken) {
				row.isRecovery = true
				break
			}
		}
		if row.isRecovery {
			if amount, ok := recoveryInt(fields[itemsetFieldAmount]); ok && amount > 0 {
				row.amount = int32(amount)
			} else {
				// A recovery row without a readable amount cannot be ranked
				// against the others, and guessing zero would make it look
				// useless. Hold the name out entirely instead.
				row.amount = -1
			}
		}
		byName[name] = append(byName[name], row)
	}

	items := make(map[string]ItemRecovery)
	for name, rows := range byName {
		if len(rows) != 1 {
			// Names repeat in this table (1636 of them do). Keep a name only
			// when every row carrying it is the same recovery item; anything
			// else is left out rather than resolved to a coin flip.
			agree := true
			amount := int32(-1)
			for _, row := range rows {
				if !row.isRecovery || row.amount <= 0 || (amount >= 0 && row.amount != amount) {
					agree = false
					break
				}
				amount = row.amount
			}
			if !agree {
				continue
			}
			items[name] = ItemRecovery{Name: name, Amount: amount}
			continue
		}
		row := rows[0]
		if !row.isRecovery || row.amount <= 0 {
			continue
		}
		items[name] = ItemRecovery{Name: name, Amount: row.amount}
	}
	return items
}

func recoveryInt(field string) (int, bool) {
	value, err := strconv.Atoi(strings.TrimSpace(field))
	if err != nil {
		return 0, false
	}
	return value, true
}
