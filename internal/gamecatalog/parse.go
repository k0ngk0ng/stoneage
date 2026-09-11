package gamecatalog

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

const (
	// enemybase.txt is parsed by ENEMYTEMP_initEnemy.  The first six columns
	// are strings; the integer columns start at fields[6].
	petTemplateField = 6  // E_T_TEMPNO
	petImageField    = 36 // E_T_IMGNUMBER
	petEarthField    = 15 // E_T_EARTHAT
	petWaterField    = 16 // E_T_WATERAT
	petFireField     = 17 // E_T_FIREAT
	petWindField     = 18 // E_T_WINDAT
	petSkillsFirst   = 25 // E_T_PETSKILL1
	petSkillsLast    = 31 // E_T_PETSKILL7
	petSlotField     = 35 // E_T_SLOT
	petFlagField     = 37 // E_T_PETFLG
	petSizeField     = 38 // E_T_SIZE

	// itemset.txt is parsed by ITEM_readItemConfFile.  These are the two
	// fields which are easy to confuse: ITEM_ID is field 16 and the logical
	// bitmap number is field 17 (both zero-based).
	itemIDField     = 16
	itemImageField  = 17
	itemCostField   = 18
	itemTypeField   = 19
	itemFieldField  = 20
	itemTargetField = 21
	itemLevelField  = 22

	// petskill.txt's deployed _CFREE_petskill format stores six character
	// columns followed by the numeric ID/field/target/use-type/cost columns.
	petSkillIDField      = 6
	petSkillField        = 7
	petSkillTargetField  = 8
	petSkillUseTypeField = 9
	petSkillCostField    = 10
	petSkillCodeField    = 11
)

// Load finds the legacy gmsv data directory below root and loads the three
// tables used by the directory.  root can be the repository root, the gmsv
// directory, or the data directory itself.
func Load(root string) (*Catalog, error) {
	dataDir, err := findDataDir(root)
	if err != nil {
		return nil, err
	}
	return LoadDataDir(dataDir)
}

// LoadDataDir loads itemset.txt, enemybase.txt, and petskill.txt from a
// directory containing the gmsv data files.
func LoadDataDir(dataDir string) (*Catalog, error) {
	if strings.TrimSpace(dataDir) == "" {
		dataDir = "."
	}

	items, err := parseFile(filepath.Join(dataDir, "itemset.txt"), ParseItems)
	if err != nil {
		return nil, err
	}
	pets, err := parseFile(filepath.Join(dataDir, "enemybase.txt"), ParsePets)
	if err != nil {
		return nil, err
	}
	petSkills, err := parseFile(filepath.Join(dataDir, "petskill.txt"), ParsePetSkills)
	if err != nil {
		return nil, err
	}
	catalog := &Catalog{Pets: pets, Items: items, PetSkills: petSkills}
	if err := validateUnique(KindPet, catalog.Pets, func(value Pet) int { return value.ID }); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(dataDir, "enemybase.txt"), err)
	}
	if err := validateUnique(KindItem, catalog.Items, func(value Item) int { return value.ID }); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(dataDir, "itemset.txt"), err)
	}
	if err := validateUnique(KindPetSkill, catalog.PetSkills, func(value PetSkill) int { return value.ID }); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(dataDir, "petskill.txt"), err)
	}
	catalog.Sort()
	return catalog, nil
}

// ParseItems parses the server's itemset.txt format from r.
func ParseItems(r io.Reader) ([]Item, error) {
	return parseRows(r, "itemset.txt", func(fields []string, record int) (Item, error) {
		if len(fields) <= itemImageField {
			return Item{}, fmt.Errorf("itemset record %d has %d fields; need image field %d", record, len(fields), itemImageField)
		}
		id, err := requiredInt(fields, itemIDField, "item id")
		if err != nil {
			return Item{}, fmt.Errorf("itemset record %d: %w", record, err)
		}
		imageID, err := requiredInt(fields, itemImageField, "item image id")
		if err != nil {
			return Item{}, fmt.Errorf("itemset record %d: %w", record, err)
		}
		name := fieldText(fields, 0)
		if name == "" {
			return Item{}, fmt.Errorf("itemset record %d has empty name", record)
		}
		entry := Entry{
			Kind:        KindItem,
			ID:          id,
			TemplateID:  id,
			Name:        name,
			GraphicID:   imageID,
			Description: fieldText(fields, 2),
			Source:      "itemset.txt",
			RecordIndex: record,
		}
		return Item{
			Entry:      entry,
			SecretName: fieldText(fields, 1),
			Cost:       optionalInt(fields, itemCostField),
			Type:       optionalInt(fields, itemTypeField),
			Field:      optionalInt(fields, itemFieldField),
			Target:     optionalInt(fields, itemTargetField),
			Level:      optionalInt(fields, itemLevelField),
		}, nil
	})
}

// ParseItemSet is an explicit alias for ParseItems for callers that use the
// source file's name.
func ParseItemSet(r io.Reader) ([]Item, error) { return ParseItems(r) }

// ParsePets parses the server's enemybase.txt template table.  enemy.txt is a
// separate encounter table and is intentionally not accepted here.
func ParsePets(r io.Reader) ([]Pet, error) {
	return parseRows(r, "enemybase.txt", func(fields []string, record int) (Pet, error) {
		if len(fields) <= petImageField {
			return Pet{}, fmt.Errorf("enemybase record %d has %d fields; need image field %d", record, len(fields), petImageField)
		}
		templateID, err := requiredInt(fields, petTemplateField, "pet template id")
		if err != nil {
			return Pet{}, fmt.Errorf("enemybase record %d: %w", record, err)
		}
		imageID, err := requiredInt(fields, petImageField, "pet image id")
		if err != nil {
			return Pet{}, fmt.Errorf("enemybase record %d: %w", record, err)
		}
		name := fieldText(fields, 0)
		if name == "" {
			return Pet{}, fmt.Errorf("enemybase record %d has empty name", record)
		}
		var elements [4]int
		for index, field := range []int{petEarthField, petWaterField, petFireField, petWindField} {
			elements[index] = optionalInt(fields, field)
		}
		skills := make([]int, 0, petSkillsLast-petSkillsFirst+1)
		for field := petSkillsFirst; field <= petSkillsLast; field++ {
			if value, ok := optionalIntOK(fields, field); ok && value >= 0 {
				skills = append(skills, value)
			}
		}
		entry := Entry{
			Kind:        KindPet,
			ID:          templateID,
			TemplateID:  templateID,
			Name:        name,
			GraphicID:   imageID,
			Description: "",
			Source:      "enemybase.txt",
			RecordIndex: record,
		}
		return Pet{
			Entry:       entry,
			Elements:    elements,
			Slot:        optionalInt(fields, petSlotField),
			PetFlag:     optionalInt(fields, petFlagField),
			Size:        optionalInt(fields, petSizeField),
			PetSkillIDs: skills,
		}, nil
	})
}

// ParseEnemyBase is an explicit alias for ParsePets for callers that want to
// make the source table distinction visible at the call site.
func ParseEnemyBase(r io.Reader) ([]Pet, error) { return ParsePets(r) }

// ParsePetSkills parses the deployed petskill.txt format.
func ParsePetSkills(r io.Reader) ([]PetSkill, error) {
	return parseRows(r, "petskill.txt", func(fields []string, record int) (PetSkill, error) {
		if len(fields) <= petSkillCostField {
			return PetSkill{}, fmt.Errorf("petskill record %d has %d fields; need cost field %d", record, len(fields), petSkillCostField)
		}
		id, err := requiredInt(fields, petSkillIDField, "pet skill id")
		if err != nil {
			return PetSkill{}, fmt.Errorf("petskill record %d: %w", record, err)
		}
		name := fieldText(fields, 0)
		if name == "" {
			return PetSkill{}, fmt.Errorf("petskill record %d has empty name", record)
		}
		entry := Entry{
			Kind:        KindPetSkill,
			ID:          id,
			TemplateID:  id,
			Name:        name,
			GraphicID:   0,
			Description: fieldText(fields, 1),
			Source:      "petskill.txt",
			RecordIndex: record,
		}
		return PetSkill{
			Entry:    entry,
			Function: fieldText(fields, 2),
			Code:     fieldText(fields, petSkillCodeField),
			Field:    optionalInt(fields, petSkillField),
			Target:   optionalInt(fields, petSkillTargetField),
			UseType:  optionalInt(fields, petSkillUseTypeField),
			Cost:     optionalInt(fields, petSkillCostField),
		}, nil
	})
}

// ParsePetSkill is an explicit singular alias for ParsePetSkills.
func ParsePetSkill(r io.Reader) ([]PetSkill, error) { return ParsePetSkills(r) }

func findDataDir(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	root = filepath.Clean(root)
	candidates := []string{
		root,
		filepath.Join(root, "data"),
		filepath.Join(root, "gmsv", "data"),
		filepath.Join(root, "server", "legacy", "source", "2.5", "gmsv", "data"),
		filepath.Join(root, "runtime", "legacy-server", "gmsv", "data"),
	}
	for _, candidate := range candidates {
		if hasCatalogFiles(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("gamecatalog: no gmsv data directory below %s", root)
}

func hasCatalogFiles(directory string) bool {
	for _, name := range []string{"itemset.txt", "enemybase.txt", "petskill.txt"} {
		info, err := os.Stat(filepath.Join(directory, name))
		if err != nil || info.IsDir() {
			return false
		}
	}
	return true
}

func parseFile[T any](path string, parse func(io.Reader) ([]T, error)) ([]T, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("gamecatalog: open %s: %w", path, err)
	}
	defer file.Close()
	values, err := parse(file)
	if err != nil {
		return nil, fmt.Errorf("gamecatalog: parse %s: %w", path, err)
	}
	return values, nil
}

func parseRows[T any](reader io.Reader, source string, parse func([]string, int) (T, error)) ([]T, error) {
	scanner := bufio.NewScanner(reader)
	// A few itemset rows contain long padded descriptions and future columns.
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	values := make([]T, 0)
	record := 0
	line := 0
	for scanner.Scan() {
		line++
		raw := bytes.TrimSuffix(scanner.Bytes(), []byte{'\r'})
		if line == 1 {
			raw = bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf})
			raw = bytes.TrimPrefix(raw, []byte{0xff, 0xfe})
		}
		if len(bytes.TrimSpace(raw)) == 0 || bytes.HasPrefix(bytes.TrimSpace(raw), []byte{'#'}) {
			continue
		}
		decoded, err := decodeCP936(raw)
		if err != nil {
			return nil, fmt.Errorf("%s line %d: %w", source, line, err)
		}
		fields := strings.Split(decoded, ",")
		value, err := parse(fields, record)
		if err != nil {
			return nil, fmt.Errorf("%s line %d: %w", source, line, err)
		}
		values = append(values, value)
		record++
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%s: read: %w", source, err)
	}
	return values, nil
}

func decodeCP936(raw []byte) (string, error) {
	if bytes.HasPrefix(raw, []byte{0xef, 0xbb, 0xbf}) {
		raw = raw[3:]
	}
	// Test fixtures and operator overrides are occasionally UTF-8.  Prefer it
	// when it is unambiguously valid; the deployed legacy files are decoded by
	// the GBK decoder below.
	if utf8.Valid(raw) {
		return string(raw), nil
	}
	decoded, err := simplifiedchinese.GBK.NewDecoder().Bytes(raw)
	if err != nil {
		return "", fmt.Errorf("decode CP936: %w", err)
	}
	if !utf8.Valid(decoded) {
		return "", fmt.Errorf("decode CP936 produced invalid UTF-8")
	}
	return string(decoded), nil
}

func fieldText(fields []string, index int) string {
	if index < 0 || index >= len(fields) {
		return ""
	}
	// itemset descriptions are padded to the old fixed-width display field;
	// remove that padding while keeping one ordinary separator between words.
	return strings.Join(strings.Fields(strings.TrimSpace(fields[index])), " ")
}

func requiredInt(fields []string, index int, label string) (int, error) {
	if index < 0 || index >= len(fields) {
		return 0, fmt.Errorf("missing %s field %d", label, index)
	}
	value := strings.TrimSpace(fields[index])
	if value == "" {
		return 0, fmt.Errorf("empty %s field %d", label, index)
	}
	number, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %w", label, value, err)
	}
	return number, nil
}

func optionalInt(fields []string, index int) int {
	value, ok := optionalIntOK(fields, index)
	if !ok {
		return 0
	}
	return value
}

func optionalIntOK(fields []string, index int) (int, bool) {
	if index < 0 || index >= len(fields) {
		return 0, false
	}
	value := strings.TrimSpace(fields[index])
	if value == "" {
		return 0, false
	}
	number, err := strconv.Atoi(value)
	if err != nil {
		return 0, false
	}
	return number, true
}
