// Package gamecatalog exposes the searchable parts of the data tables used by
// the 2.5 game server.
//
// The server's data files are legacy CP936 comma-delimited files.  The web
// client already uses the same files when it builds its bitmap manifest, so
// this package keeps the table offsets in one place for callers that need to
// show a pet, item, or pet skill outside the browser client.
package gamecatalog

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Kind identifies the table from which an entry came.
type Kind string

const (
	KindPet      Kind = "pet"
	KindItem     Kind = "item"
	KindPetSkill Kind = "pet_skill"
)

// Entry contains the fields common to all searchable catalog records.
//
// ID is the identifier used by the game table.  TemplateID is kept explicit
// because it is the identifier used by the original template table; for
// current item, pet, and pet-skill files it has the same value as ID.  Keeping
// both names makes it clear that a pet's ID comes from enemybase.txt rather
// than from enemy.txt's encounter table.
type Entry struct {
	Kind        Kind   `json:"kind"`
	ID          int    `json:"id"`
	TemplateID  int    `json:"template_id"`
	Name        string `json:"name"`
	GraphicID   int    `json:"graphic_id"`
	Description string `json:"description"`
	Source      string `json:"source"`
	RecordIndex int    `json:"record_index"`
}

// ImageID returns the logical bitmap number used by the source table.  The
// JSON/API spelling is graphic_id because the game protocol calls this field a
// graphic number.
func (e Entry) ImageID() int { return e.GraphicID }

// Pet is one row from enemybase.txt.  enemy.txt contains encounter variants
// and is deliberately not included in this catalog.
type Pet struct {
	Entry
	Elements    [4]int `json:"elements"`
	Slot        int    `json:"slot"`
	PetFlag     int    `json:"pet_flag"`
	Size        int    `json:"size"`
	PetSkillIDs []int  `json:"pet_skill_ids"`
}

// Item is one row from itemset.txt.
type Item struct {
	Entry
	SecretName string `json:"secret_name"`
	Cost       int    `json:"cost"`
	Type       int    `json:"type"`
	Field      int    `json:"field"`
	Target     int    `json:"target"`
	Level      int    `json:"level"`
}

// PetSkill is one row from petskill.txt.  Code is the final symbolic token
// present in the deployed free-skill table (for example PETSKILL_NORMALATTACK).
type PetSkill struct {
	Entry
	Function string `json:"function"`
	Code     string `json:"code"`
	Field    int    `json:"field"`
	Target   int    `json:"target"`
	UseType  int    `json:"use_type"`
	Cost     int    `json:"cost"`
}

// Catalog is the complete searchable game directory.
type Catalog struct {
	Pets      []Pet      `json:"pets"`
	Items     []Item     `json:"items"`
	PetSkills []PetSkill `json:"pet_skills"`
}

// Entries returns all records as the common API shape in deterministic order:
// pets, items, then pet skills, each sorted by ID.
func (c *Catalog) Entries() []Entry {
	if c == nil {
		return nil
	}
	entries := make([]Entry, 0, len(c.Pets)+len(c.Items)+len(c.PetSkills))
	for _, pet := range c.Pets {
		entries = append(entries, pet.Entry)
	}
	for _, item := range c.Items {
		entries = append(entries, item.Entry)
	}
	for _, skill := range c.PetSkills {
		entries = append(entries, skill.Entry)
	}
	return entries
}

// Search returns records whose name, description, source, or source ID
// contains query.  Matching is case-insensitive for Latin text and naturally
// supports Chinese text.  An empty query returns the complete directory.
func (c *Catalog) Search(query string) []Entry {
	needle := normalize(query)
	if needle == "" {
		return c.Entries()
	}
	result := make([]Entry, 0)
	for _, pet := range c.Pets {
		if matches(needle, pet.Entry) {
			result = append(result, pet.Entry)
		}
	}
	for _, item := range c.Items {
		if matches(needle, item.Entry, item.SecretName) {
			result = append(result, item.Entry)
		}
	}
	for _, skill := range c.PetSkills {
		if matches(needle, skill.Entry, skill.Function, skill.Code) {
			result = append(result, skill.Entry)
		}
	}
	return result
}

func matches(needle string, entry Entry, extra ...string) bool {
	fields := []string{
		entry.Name,
		entry.Description,
		entry.Source,
		strconv.Itoa(entry.ID),
		strconv.Itoa(entry.TemplateID),
		strconv.Itoa(entry.GraphicID),
	}
	fields = append(fields, extra...)
	for _, field := range fields {
		if strings.Contains(normalize(field), needle) {
			return true
		}
	}
	return false
}

// Find returns the common entry with the requested table kind and ID.
func (c *Catalog) Find(kind Kind, id int) (Entry, bool) {
	if c == nil {
		return Entry{}, false
	}
	switch kind {
	case KindPet:
		for _, pet := range c.Pets {
			if pet.ID == id {
				return pet.Entry, true
			}
		}
	case KindItem:
		for _, item := range c.Items {
			if item.ID == id {
				return item.Entry, true
			}
		}
	case KindPetSkill:
		for _, skill := range c.PetSkills {
			if skill.ID == id {
				return skill.Entry, true
			}
		}
	}
	return Entry{}, false
}

// Sort normalizes caller-provided catalog slices.  Load already returns a
// sorted catalog; this helper is useful when callers append server-provided
// records before exposing them to a UI.
func (c *Catalog) Sort() {
	if c == nil {
		return
	}
	sort.SliceStable(c.Pets, func(i, j int) bool { return c.Pets[i].ID < c.Pets[j].ID })
	sort.SliceStable(c.Items, func(i, j int) bool { return c.Items[i].ID < c.Items[j].ID })
	sort.SliceStable(c.PetSkills, func(i, j int) bool { return c.PetSkills[i].ID < c.PetSkills[j].ID })
}

func normalize(value string) string {
	// Collapse only ASCII whitespace.  This keeps Chinese names intact while
	// making a query copied from a padded legacy description predictable.
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}

func validateUnique[T any](kind Kind, values []T, id func(T) int) error {
	seen := make(map[int]int, len(values))
	for index, value := range values {
		valueID := id(value)
		if previous, ok := seen[valueID]; ok {
			return fmt.Errorf("duplicate %s id %d at records %d and %d", kind, valueID, previous, index)
		}
		seen[valueID] = index
	}
	return nil
}
