package aiknowledge

import (
	"bytes"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/gamecatalog"
)

const (
	enemyIntFields     = 31
	enemyBaseIntFields = 48
	groupIntFields     = 23
	encountFields      = 33
)

func parseExperience(raw []byte, path string, strict bool) ([]ExperienceEntry, []Issue, error) {
	lines, issues := decodeLines(raw, false)
	result := make([]ExperienceEntry, 0, len(lines))
	row := 0
	for _, line := range lines {
		if commentOrBlank(line.Text) {
			continue
		}
		text := strings.TrimSpace(strings.SplitN(line.Text, "#", 2)[0])
		if text == "" {
			continue
		}
		parts := strings.Fields(strings.ReplaceAll(text, ",", " "))
		if len(parts) == 0 || len(parts) > 2 {
			if err := recordParseIssue(&issues, strict, path, line.Number, "exp_row", "expected one value or level and value"); err != nil {
				return nil, issues, err
			}
			continue
		}
		level := row
		valueToken := parts[0]
		if len(parts) == 2 {
			var err error
			level, err = strconv.Atoi(parts[0])
			if err != nil {
				if parseErr := recordParseIssue(&issues, strict, path, line.Number, "exp_level", fmt.Sprintf("invalid level %q", parts[0])); parseErr != nil {
					return nil, issues, parseErr
				}
				continue
			}
			valueToken = parts[1]
		}
		value, err := strconv.ParseInt(valueToken, 10, 64)
		if err != nil || value < 0 {
			message := fmt.Sprintf("invalid non-negative experience %q", valueToken)
			if err != nil {
				message = fmt.Sprintf("invalid experience %q", valueToken)
			}
			if parseErr := recordParseIssue(&issues, strict, path, line.Number, "exp_value", message); parseErr != nil {
				return nil, issues, parseErr
			}
			continue
		}
		if level < 0 {
			if parseErr := recordParseIssue(&issues, strict, path, line.Number, "exp_level", "level must be non-negative"); parseErr != nil {
				return nil, issues, parseErr
			}
			continue
		}
		result = append(result, ExperienceEntry{Level: level, Required: value, Source: SourceRef{Path: path, Line: line.Number, Encoding: line.Encoding, Extractor: "exp.txt"}})
		row++
	}
	if len(result) == 0 {
		return result, issues, fmt.Errorf("%s: no experience rows", path)
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Level < result[j].Level })
	seen := map[int]bool{}
	for _, item := range result {
		if seen[item.Level] {
			if err := recordParseIssue(&issues, strict, path, item.Source.Line, "exp_duplicate_level", fmt.Sprintf("duplicate level %d", item.Level)); err != nil {
				return nil, issues, err
			}
		}
		seen[item.Level] = true
	}
	return result, issues, nil
}

func parseEnemyBases(raw []byte, path string, strict bool) ([]EnemyBase, []Issue, error) {
	lines, issues := decodeLines(raw, false)
	result := make([]EnemyBase, 0, len(lines))
	// Use gamecatalog's canonical decoder for the fields it exposes.  The
	// complete parser below retains the server-specific numeric fields.
	catalogPets, catalogErr := gamecatalog.ParseEnemyBase(bytes.NewReader(raw))
	if catalogErr != nil {
		issues = append(issues, Issue{Severity: SeverityWarning, Code: "catalog_parse", Message: fmt.Sprintf("enemybase catalog fields unavailable: %v", catalogErr), Source: &SourceRef{Path: path}})
	}
	catalogByRecord := make(map[int]gamecatalog.Pet, len(catalogPets))
	for _, pet := range catalogPets {
		catalogByRecord[pet.RecordIndex] = pet
	}
	recordIndex := 0
	for _, line := range lines {
		if commentOrBlank(line.Text) {
			continue
		}
		currentRecord := recordIndex
		recordIndex++
		fields := strings.Split(line.Text, ",")
		if len(fields) < 6+enemyBaseIntFields {
			if err := recordParseIssue(&issues, strict, path, line.Number, "enemybase_fields", fmt.Sprintf("expected at least %d fields, got %d", 6+enemyBaseIntFields, len(fields))); err != nil {
				return nil, issues, err
			}
			continue
		}
		name := cleanField(fields[0])
		if name == "" {
			if err := recordParseIssue(&issues, strict, path, line.Number, "enemybase_name", "empty template name"); err != nil {
				return nil, issues, err
			}
			continue
		}
		n := make([]int, enemyBaseIntFields)
		valid := true
		for i := range n {
			value, err := requiredOrZero(fields, 6+i)
			if err != nil {
				valid = false
				if parseErr := recordParseIssue(&issues, strict, path, line.Number, "enemybase_number", fmt.Sprintf("field %d: %v", 6+i, err)); parseErr != nil {
					return nil, issues, parseErr
				}
				break
			}
			n[i] = value
		}
		if !valid {
			continue
		}
		if n[0] < 0 {
			if err := recordParseIssue(&issues, strict, path, line.Number, "enemybase_id", "template ID must be non-negative"); err != nil {
				return nil, issues, err
			}
			continue
		}
		base := EnemyBase{
			TemplateID: n[0], Name: name, InitNum: n[1], LevelUpPoint: n[2],
			BaseVital: n[3], BaseStr: n[4], BaseTough: n[5], BaseDex: n[6],
			ModAI: n[7], Get: n[8], Elements: [4]int{n[9], n[10], n[11], n[12]},
			Resistances: [6]int{n[13], n[14], n[15], n[16], n[17], n[18]},
			Rare:        n[26], Critical: n[27], Counter: n[28], Slot: n[29], ImageID: n[30],
			PetFlag: n[31], Size: n[32], LimitLevel: n[47],
			Source: SourceRef{Path: path, Line: line.Number, Encoding: line.Encoding, Extractor: "enemybase.txt"},
		}
		for i := 19; i <= 25; i++ {
			base.PetSkillSlots[i-19] = -1
			if strings.TrimSpace(fields[6+i]) != "" {
				base.PetSkillSlots[i-19] = n[i]
			}
			if n[i] >= 0 {
				base.PetSkillIDs = append(base.PetSkillIDs, n[i])
			}
		}
		if pet, ok := catalogByRecord[currentRecord]; ok {
			base.Pet = pet
		}
		result = append(result, base)
	}
	if len(result) == 0 {
		return result, issues, fmt.Errorf("%s: no enemybase rows", path)
	}
	if err := duplicateIDs(result, path, strict, func(item EnemyBase) int { return item.TemplateID }, "enemybase_id", &issues); err != nil {
		return nil, issues, err
	}
	return result, issues, nil
}

func parseEnemies(raw []byte, path string, strict bool) ([]Enemy, []Issue, error) {
	lines, issues := decodeLines(raw, false)
	result := make([]Enemy, 0, len(lines))
	for _, line := range lines {
		if commentOrBlank(line.Text) {
			continue
		}
		fields := strings.Split(line.Text, ",")
		if len(fields) < 2+1+enemyIntFields { // name, tactics, act condition, numeric fields
			if err := recordParseIssue(&issues, strict, path, line.Number, "enemy_fields", fmt.Sprintf("expected at least %d fields, got %d", 2+1+enemyIntFields, len(fields))); err != nil {
				return nil, issues, err
			}
			continue
		}
		n := make([]int, enemyIntFields)
		valid := true
		for i := range n {
			value, err := requiredOrZero(fields, 3+i)
			if err != nil {
				valid = false
				if parseErr := recordParseIssue(&issues, strict, path, line.Number, "enemy_number", fmt.Sprintf("field %d: %v", 3+i, err)); parseErr != nil {
					return nil, issues, parseErr
				}
				break
			}
			n[i] = value
		}
		if !valid {
			continue
		}
		if n[0] < 0 || n[1] < 0 {
			if err := recordParseIssue(&issues, strict, path, line.Number, "enemy_id", "enemy ID and template ID must be non-negative"); err != nil {
				return nil, issues, err
			}
			continue
		}
		lvMin, lvMax := n[2], n[3]
		if lvMin == 0 {
			lvMin = lvMax
		}
		if lvMin > lvMax {
			lvMin, lvMax = lvMax, lvMin
		}
		enemy := Enemy{
			ID: n[0], TemplateID: n[1], Levels: Range{Min: lvMin, Max: lvMax},
			Create: Range{Min: n[5], Max: n[4]}, Tactics: n[6], Experience: n[7],
			DuelPoint: n[8], Style: n[9], PetFlag: n[10], Name: cleanField(fields[0]),
			TacticsOption: cleanField(fields[1]), ActCondition: cleanField(fields[2]),
			Source: SourceRef{Path: path, Line: line.Number, Encoding: line.Encoding, Extractor: "enemy.txt"},
		}
		for i := 0; i < 10; i++ {
			itemID, probability := n[11+i], n[21+i]
			if itemID > 0 && probability > 0 {
				enemy.Drops = append(enemy.Drops, EnemyDrop{ItemID: itemID, Probability: probability})
			}
		}
		result = append(result, enemy)
	}
	if len(result) == 0 {
		return result, issues, fmt.Errorf("%s: no enemy rows", path)
	}
	if err := duplicateIDs(result, path, strict, func(item Enemy) int { return item.ID }, "enemy_id", &issues); err != nil {
		return nil, issues, err
	}
	return result, issues, nil
}

func parseGroups(raw []byte, path string, strict bool) ([]EncounterGroup, []Issue, error) {
	lines, issues := decodeLines(raw, false)
	result := make([]EncounterGroup, 0, len(lines))
	for _, line := range lines {
		if commentOrBlank(line.Text) {
			continue
		}
		fields := strings.Split(line.Text, ",")
		if len(fields) < 1+groupIntFields {
			if err := recordParseIssue(&issues, strict, path, line.Number, "group_fields", fmt.Sprintf("expected at least %d fields, got %d", 1+groupIntFields, len(fields))); err != nil {
				return nil, issues, err
			}
			continue
		}
		n := make([]int, groupIntFields)
		for i := range n {
			value, err := optionalInt(fields, 1+i, -1)
			if err != nil {
				if parseErr := recordParseIssue(&issues, strict, path, line.Number, "group_number", fmt.Sprintf("field %d: %v", 1+i, err)); parseErr != nil {
					return nil, issues, parseErr
				}
				n[i] = -1
				continue
			}
			n[i] = value
		}
		if n[0] < 0 {
			if err := recordParseIssue(&issues, strict, path, line.Number, "group_id", "group ID must be non-negative"); err != nil {
				return nil, issues, err
			}
			continue
		}
		group := EncounterGroup{ID: n[0], Name: cleanField(fields[0]), AppearByItemID: n[1], NotAppearByItemID: n[2], Source: SourceRef{Path: path, Line: line.Number, Encoding: line.Encoding, Extractor: "group.txt"}}
		group.EnemyIDs = append([]int(nil), n[3:13]...)
		group.CreateProbabilities = append([]int(nil), n[13:23]...)
		seen := map[int]bool{}
		for _, id := range group.EnemyIDs {
			if id < 0 {
				continue
			}
			if seen[id] {
				if err := recordParseIssue(&issues, strict, path, line.Number, "group_duplicate_enemy", fmt.Sprintf("enemy ID %d appears more than once", id)); err != nil {
					return nil, issues, err
				}
			}
			seen[id] = true
		}
		result = append(result, group)
	}
	if len(result) == 0 {
		return result, issues, fmt.Errorf("%s: no group rows", path)
	}
	if err := duplicateIDs(result, path, strict, func(item EncounterGroup) int { return item.ID }, "group_id", &issues); err != nil {
		return nil, issues, err
	}
	return result, issues, nil
}

func parseEncounters(raw []byte, path string, strict bool) ([]EncounterArea, []Issue, error) {
	lines, issues := decodeLines(raw, false)
	result := make([]EncounterArea, 0, len(lines))
	for _, line := range lines {
		if commentOrBlank(line.Text) {
			continue
		}
		fields := strings.Split(line.Text, ",")
		if len(fields) < encountFields {
			if err := recordParseIssue(&issues, strict, path, line.Number, "encount_fields", fmt.Sprintf("expected at least %d fields, got %d", encountFields, len(fields))); err != nil {
				return nil, issues, err
			}
			continue
		}
		n := make([]int, encountFields)
		valid := true
		for i := 0; i < 10; i++ {
			value, err := requiredOrZero(fields, i)
			if err != nil {
				valid = false
				if parseErr := recordParseIssue(&issues, strict, path, line.Number, "encount_number", fmt.Sprintf("field %d: %v", i, err)); parseErr != nil {
					return nil, issues, parseErr
				}
				break
			}
			n[i] = value
		}
		if !valid {
			continue
		}
		for i := 10; i < encountFields; i++ {
			value, err := optionalInt(fields, i, -1)
			if err != nil {
				if parseErr := recordParseIssue(&issues, strict, path, line.Number, "encount_number", fmt.Sprintf("field %d: %v", i, err)); parseErr != nil {
					return nil, issues, parseErr
				}
				n[i] = -1
				continue
			}
			n[i] = value
		}
		x1, y1, x2, y2 := n[2], n[3], n[4], n[5]
		bounds := normalizeRectangle(x1, y1, x2, y2)
		prob := Range{Min: n[6], Max: n[7]}
		if prob.Min > prob.Max {
			prob.Min, prob.Max = prob.Max, prob.Min
		}
		if n[8] < 1 || n[8] > 10 {
			if err := recordParseIssue(&issues, strict, path, line.Number, "encount_max_enemies", fmt.Sprintf("max enemies %d is outside server range 1..10", n[8])); err != nil {
				return nil, issues, err
			}
			continue
		}
		area := EncounterArea{ID: n[0], Floor: n[1], Bounds: bounds, EncounterProbability: prob, MaxEnemies: n[8], ZOrder: n[9], EventNow: n[30], EventEnd: n[31], EnemyGroup: n[32], Source: SourceRef{Path: path, Line: line.Number, Encoding: line.Encoding, Extractor: "encount.txt"}}
		area.GroupIDs = append([]int(nil), n[10:20]...)
		area.GroupProbabilities = append([]int(nil), n[20:30]...)
		result = append(result, area)
	}
	if len(result) == 0 {
		return result, issues, fmt.Errorf("%s: no encounter rows", path)
	}
	if err := duplicateIDs(result, path, strict, func(item EncounterArea) int { return item.ID }, "encount_id", &issues); err != nil {
		return nil, issues, err
	}
	return result, issues, nil
}

func parseMapWarps(raw []byte, path string, strict bool) ([]MapWarp, []Issue, error) {
	lines, issues := decodeLines(raw, false)
	result := make([]MapWarp, 0, len(lines))
	for _, line := range lines {
		if commentOrBlank(line.Text) {
			continue
		}
		fields := strings.Split(line.Text, ":")
		if len(fields) != 5 {
			if err := recordParseIssue(&issues, strict, path, line.Number, "mapwarp_fields", fmt.Sprintf("expected five colon-delimited fields, got %d", len(fields))); err != nil {
				return nil, issues, err
			}
			continue
		}
		typeName := strings.TrimSpace(fields[0])
		if typeName != "NONE" && typeName != "FREE" && typeName != "ERROR" {
			if err := recordParseIssue(&issues, strict, path, line.Number, "mapwarp_type", fmt.Sprintf("unknown warp type %q", typeName)); err != nil {
				return nil, issues, err
			}
			continue
		}
		timeName := strings.TrimSpace(fields[1])
		if timeName != "NULL" && timeName != "M" && timeName != "N" && timeName != "A" {
			if err := recordParseIssue(&issues, strict, path, line.Number, "mapwarp_time", fmt.Sprintf("unknown time selector %q", timeName)); err != nil {
				return nil, issues, err
			}
			continue
		}
		from, err := parsePoint(fields[2])
		if err != nil {
			if parseErr := recordParseIssue(&issues, strict, path, line.Number, "mapwarp_from", err.Error()); parseErr != nil {
				return nil, issues, parseErr
			}
			continue
		}
		to, err := parsePoint(fields[3])
		if err != nil {
			if parseErr := recordParseIssue(&issues, strict, path, line.Number, "mapwarp_to", err.Error()); parseErr != nil {
				return nil, issues, parseErr
			}
			continue
		}
		result = append(result, MapWarp{Type: typeName, Time: timeName, From: from, To: to, Attribute: strings.TrimSpace(fields[4]), Source: SourceRef{Path: path, Line: line.Number, Encoding: line.Encoding, Extractor: "mapwarp.txt"}})
	}
	if len(result) == 0 {
		return result, issues, fmt.Errorf("%s: no map warp rows", path)
	}
	return result, issues, nil
}

func parsePoint(value string) (Point, error) {
	parts := strings.Split(strings.TrimSpace(value), ",")
	if len(parts) != 3 {
		return Point{}, fmt.Errorf("expected floor,x,y, got %q", value)
	}
	values := [3]int{}
	for i, part := range parts {
		parsed, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return Point{}, fmt.Errorf("invalid coordinate %q: %w", part, err)
		}
		values[i] = parsed
	}
	return Point{Floor: values[0], X: values[1], Y: values[2]}, nil
}

func normalizeRectangle(x1, y1, x2, y2 int) Rectangle {
	if x1 > x2 {
		x1, x2 = x2, x1
	}
	if y1 > y2 {
		y1, y2 = y2, y1
	}
	return Rectangle{X: x1, Y: y1, X2: x2, Y2: y2, Width: x2 - x1, Height: y2 - y1}
}

func cleanField(value string) string {
	return strings.TrimSpace(value)
}

func requiredOrZero(fields []string, index int) (int, error) {
	if index < 0 || index >= len(fields) {
		return 0, io.ErrUnexpectedEOF
	}
	value := strings.TrimSpace(fields[index])
	if value == "" {
		return 0, nil
	}
	parsed, err := parseServerInt(value)
	if err != nil {
		return 0, fmt.Errorf("invalid integer %q: %w", value, err)
	}
	return parsed, nil
}

// parseServerInt follows the legacy C loader's atoi conversion. The shipped
// enemybase table writes LVUPPOINT as values such as 4.50 even though the
// server stores the integer prefix (4). Keeping that conversion here lets us
// parse the real table while retaining the server's effective value.
func parseServerInt(value string) (int, error) {
	if parsed, err := strconv.Atoi(value); err == nil {
		return parsed, nil
	}
	dot := strings.IndexByte(value, '.')
	if dot <= 0 || strings.IndexByte(value[dot+1:], '.') >= 0 {
		return 0, fmt.Errorf("not an integer")
	}
	whole, fraction := value[:dot], value[dot+1:]
	if fraction == "" {
		return 0, fmt.Errorf("invalid decimal")
	}
	for _, character := range fraction {
		if character < '0' || character > '9' {
			return 0, fmt.Errorf("invalid decimal")
		}
	}
	return strconv.Atoi(whole)
}

func optionalInt(fields []string, index, empty int) (int, error) {
	if index < 0 || index >= len(fields) {
		return empty, io.ErrUnexpectedEOF
	}
	value := strings.TrimSpace(fields[index])
	if value == "" {
		return empty, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return empty, fmt.Errorf("invalid integer %q: %w", value, err)
	}
	return parsed, nil
}

func recordParseIssue(issues *[]Issue, strict bool, path string, line int, code, message string) error {
	issue := Issue{Severity: SeverityError, Code: code, Message: message, Source: &SourceRef{Path: path, Line: line}}
	if strict {
		return fmt.Errorf("%s:%d: %s", path, line, message)
	}
	issue.Severity = SeverityWarning
	*issues = append(*issues, issue)
	return nil
}

func duplicateIDs[T any](values []T, path string, strict bool, id func(T) int, code string, issues *[]Issue) error {
	seen := map[int]int{}
	for index, value := range values {
		valueID := id(value)
		if previous, ok := seen[valueID]; ok {
			if err := recordParseIssue(issues, strict, path, 0, code, fmt.Sprintf("duplicate ID %d at records %d and %d", valueID, previous, index)); err != nil {
				return err
			}
		}
		seen[valueID] = index
	}
	return nil
}

func annotateSource(ref *SourceRef, digest FileDigest) {
	if ref == nil {
		return
	}
	ref.SHA256 = digest.SHA256
	if ref.Encoding == "" {
		ref.Encoding = digest.Encoding
	}
}
