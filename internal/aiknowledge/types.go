// Package aiknowledge builds a versioned, evidence-backed view of the
// StoneAge 2.5 game data used by the legacy gmsv server.
//
// The package deliberately exposes facts and provenance separately.  A
// caller can use an encounter or task only after checking its Status and
// Evidence; data which could not be parsed or verified is retained as an
// issue instead of being presented as complete game knowledge.
package aiknowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/gamecatalog"
)

// Options controls a knowledge load.
type Options struct {
	// GroupFile selects the deployed enemy-group table (for example group1.txt).
	// Empty preserves the historical group.txt default. Only basenames are accepted.
	GroupFile string
	// DataDir is the gmsv data directory.  A repository root, gmsv directory,
	// or data directory is accepted.  An empty value searches the current
	// working directory using the same repository layouts as gamecatalog.
	DataDir string
	// TaskDir is a directory of JSON task definitions.  An empty value uses
	// the definitions embedded in this package.
	TaskDir string
	// Strict turns structural errors in core tables and invalid task
	// definitions into a load error.  Unsupported optional NPC artefacts are
	// still reported in Issues so that strict mode cannot imply full coverage.
	Strict bool
}

// SourceRef identifies the exact source evidence for a fact.
type SourceRef struct {
	Path      string `json:"path"`
	Line      int    `json:"line,omitempty"`
	LineEnd   int    `json:"line_end,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
	Encoding  string `json:"encoding,omitempty"`
	Extractor string `json:"extractor,omitempty"`
}

// FileDigest is the immutable digest and parse summary for a loaded file.
type FileDigest struct {
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	Bytes     int64  `json:"bytes"`
	Records   int    `json:"records"`
	Encoding  string `json:"encoding,omitempty"`
	Supported bool   `json:"supported"`
	Issue     string `json:"issue,omitempty"`
}

// IssueSeverity describes the effect of a knowledge issue.
type IssueSeverity string

const (
	SeverityWarning IssueSeverity = "warning"
	SeverityError   IssueSeverity = "error"
)

// Issue is a parse, consistency, or coverage problem retained with a
// knowledge snapshot.  An error issue always prevents a strict load.
type Issue struct {
	Severity IssueSeverity `json:"severity"`
	Code     string        `json:"code"`
	Message  string        `json:"message"`
	Source   *SourceRef    `json:"source,omitempty"`
}

// ExperienceEntry is one row of exp.txt.  Required is the value read from
// the file.  The deployed 2.5 table uses one value per line (level is the
// zero-based row number); newer USEREXP variants may include an explicit
// level as the first token.
type ExperienceEntry struct {
	Level    int       `json:"level"`
	Required int64     `json:"required"`
	Source   SourceRef `json:"source"`
}

// Rectangle is an inclusive coordinate area in the server's map coordinate
// system.  Width and Height are retained because they are how the C server
// computes the encounter rectangle; callers should treat X2/Y2 as the
// normalized opposite corner.
type Rectangle struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	X2     int `json:"x2"`
	Y2     int `json:"y2"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Contains reports whether a point is inside the rectangle, including its
// endpoints as the map server's CoordinateInRect helper does.
func (r Rectangle) Contains(x, y int) bool {
	return x >= r.X && x <= r.X2 && y >= r.Y && y <= r.Y2
}

// EncounterArea is one row from encount.txt.  GroupIDs and GroupProbabilities
// preserve the ten fixed slots used by ENCOUNT_Table; empty slots are -1.
type EncounterArea struct {
	ID                   int       `json:"id"`
	Floor                int       `json:"floor"`
	Bounds               Rectangle `json:"bounds"`
	EncounterProbability Range     `json:"encounter_probability"`
	MaxEnemies           int       `json:"max_enemies"`
	ZOrder               int       `json:"z_order"`
	GroupIDs             []int     `json:"group_ids"`
	GroupProbabilities   []int     `json:"group_probabilities"`
	EventNow             int       `json:"event_now"`
	EventEnd             int       `json:"event_end"`
	EnemyGroup           int       `json:"enemy_group"`
	Source               SourceRef `json:"source"`
}

// Range is an inclusive integer range.
type Range struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

// EnemyDrop describes one item/probability pair from enemy.txt.
type EnemyDrop struct {
	ItemID      int `json:"item_id"`
	Probability int `json:"probability"`
}

// Enemy is one encounter enemy row from enemy.txt.  ID is the encounter
// variant ID; TemplateID points into enemybase.txt and must resolve there.
type Enemy struct {
	ID            int         `json:"id"`
	TemplateID    int         `json:"template_id"`
	Levels        Range       `json:"levels"`
	Create        Range       `json:"create"`
	Tactics       int         `json:"tactics"`
	Experience    int         `json:"experience"`
	DuelPoint     int         `json:"duel_point"`
	Style         int         `json:"style"`
	PetFlag       int         `json:"pet_flag"`
	Name          string      `json:"name"`
	TacticsOption string      `json:"tactics_option"`
	ActCondition  string      `json:"act_condition,omitempty"`
	Drops         []EnemyDrop `json:"drops,omitempty"`
	Source        SourceRef   `json:"source"`
}

// EnemyBase is the template row consumed by ENEMYTEMP_initEnemy.  Pet holds
// the already validated gamecatalog representation, while the fields below
// retain the numeric attributes needed by an AI combat/leveling planner.
type EnemyBase struct {
	TemplateID   int    `json:"template_id"`
	Name         string `json:"name"`
	InitNum      int    `json:"init_num"`
	LevelUpPoint int    `json:"level_up_point"`
	BaseVital    int    `json:"base_vital"`
	BaseStr      int    `json:"base_str"`
	BaseTough    int    `json:"base_tough"`
	BaseDex      int    `json:"base_dex"`
	ModAI        int    `json:"mod_ai"`
	Get          int    `json:"get"`
	Elements     [4]int `json:"elements"`
	Resistances  [6]int `json:"resistances"`
	PetSkillIDs  []int  `json:"pet_skill_ids"`
	// PetSkillSlots preserves empty (-1) native slots for runtime overrides.
	PetSkillSlots [7]int          `json:"pet_skill_slots"`
	Rare          int             `json:"rare"`
	Critical      int             `json:"critical"`
	Counter       int             `json:"counter"`
	Slot          int             `json:"slot"`
	ImageID       int             `json:"image_id"`
	PetFlag       int             `json:"pet_flag"`
	Size          int             `json:"size"`
	LimitLevel    int             `json:"limit_level"`
	Pet           gamecatalog.Pet `json:"catalog_pet"`
	Source        SourceRef       `json:"source"`
}

// EncounterGroup is one row from group.txt.  EnemyIDs and CreateProbabilities
// preserve the server's ten fixed slots.
type EncounterGroup struct {
	ID                  int       `json:"id"`
	Name                string    `json:"name"`
	AppearByItemID      int       `json:"appear_by_item_id"`
	NotAppearByItemID   int       `json:"not_appear_by_item_id"`
	EnemyIDs            []int     `json:"enemy_ids"`
	CreateProbabilities []int     `json:"create_probabilities"`
	Source              SourceRef `json:"source"`
}

// MapWarp is one source-to-destination relation from map/mapwarp.txt.
type MapWarp struct {
	Type      string    `json:"type"`
	Time      string    `json:"time"`
	From      Point     `json:"from"`
	To        Point     `json:"to"`
	Attribute string    `json:"attribute"`
	Source    SourceRef `json:"source"`
}

// Point is a map coordinate.
type Point struct {
	Floor int `json:"floor"`
	X     int `json:"x"`
	Y     int `json:"y"`
}

// NPCTemplate is the active key/value block read from a .template (or legacy
// .templete) file.
type NPCTemplate struct {
	Name        string            `json:"template_name"`
	DisplayName string            `json:"display_name,omitempty"`
	Type        string            `json:"type,omitempty"`
	Graphic     string            `json:"graphic,omitempty"`
	FunctionSet string            `json:"function_set,omitempty"`
	Fields      map[string]string `json:"fields"`
	Source      SourceRef         `json:"source"`
}

// NPCEnemyRef is an enemy=template|argument relation in an NPCCREATE block.
type NPCEnemyRef struct {
	Template string `json:"template"`
	Argument string `json:"argument,omitempty"`
	Kind     string `json:"kind,omitempty"`
}

// NPCCreate is one active NPC spawn block.
type NPCCreate struct {
	Floor      int               `json:"floor"`
	Born       Rectangle         `json:"born"`
	Move       Rectangle         `json:"move"`
	Direction  int               `json:"direction"`
	Graphic    string            `json:"graphic,omitempty"`
	Name       string            `json:"name,omitempty"`
	SpawnCount int               `json:"spawn_count"`
	Time       int               `json:"time"`
	Date       int               `json:"date"`
	Family     int               `json:"family"`
	Enemies    []NPCEnemyRef     `json:"enemies"`
	Fields     map[string]string `json:"fields"`
	Source     SourceRef         `json:"source"`
}

// NPCFile is a parse summary for an NPC artefact.  Files with binary or
// generator-only content are listed with Supported=false rather than being
// silently omitted.
type NPCFile struct {
	Path      string              `json:"path"`
	Kind      string              `json:"kind"`
	SHA256    string              `json:"sha256"`
	Bytes     int64               `json:"bytes"`
	Encoding  string              `json:"encoding,omitempty"`
	Supported bool                `json:"supported"`
	Fields    map[string][]string `json:"fields,omitempty"`
	Issue     string              `json:"issue,omitempty"`
	Source    SourceRef           `json:"source"`
}

// NPCKnowledge contains structured NPC data plus auxiliary config summaries.
type NPCKnowledge struct {
	Templates []NPCTemplate `json:"templates"`
	Creates   []NPCCreate   `json:"creates"`
	Files     []NPCFile     `json:"files"`
}

// LevelingArea is a derived, query-friendly view of one encounter area.  It
// contains only facts reachable through encount -> group -> enemy references.
type LevelingArea struct {
	ID                   int         `json:"id"`
	Floor                int         `json:"floor"`
	Bounds               Rectangle   `json:"bounds"`
	EncounterIDs         []int       `json:"encounter_ids"`
	GroupIDs             []int       `json:"group_ids"`
	EnemyIDs             []int       `json:"enemy_ids"`
	TemplateIDs          []int       `json:"template_ids"`
	Levels               Range       `json:"levels"`
	EncounterProbability Range       `json:"encounter_probability"`
	MaxEnemies           int         `json:"max_enemies"`
	Verified             bool        `json:"verified"`
	Evidence             []SourceRef `json:"evidence"`
}

// TaskStatus indicates whether a task is safe to offer to an executor.
type TaskStatus string

const (
	TaskVerified    TaskStatus = "verified"
	TaskUnverified  TaskStatus = "unverified"
	TaskUnsupported TaskStatus = "unsupported"
)

// MachineCondition is the small condition language understood by an
// executor. Its shape mirrors automation.Condition so a task compiler can
// copy the fields without parsing a free-form expression. The
// character_skill_level, window_sequence and window_submitted kinds require
// an executor observation mapping. window_submitted records a local write,
// not server acknowledgement or a resulting game-state mutation.
type MachineCondition struct {
	Kind  string `json:"kind"`
	ID    string `json:"id,omitempty"`
	Value int64  `json:"value,omitempty"`
	X     int    `json:"x,omitempty"`
	Y     int    `json:"y,omitempty"`
}

// TaskCondition is the public name used by task authors. Keep the more
// descriptive MachineCondition name available for callers building plans.
type TaskCondition = MachineCondition

// Validate checks the condition vocabulary shared with the automation
// package. Explicitly named knowledge extensions are accepted as
// separate condition kinds rather than being hidden in an expression.
func (c MachineCondition) Validate() error {
	switch c.Kind {
	case "backpack_free_slots":
		if c.Value < 0 || c.Value > 15 {
			return errors.New("backpack free slots must be 0..15")
		}
	case "character_level":
		if c.Value < 1 {
			return errors.New("minimum character level must be positive")
		}
	case "gold_at_least", "not_battle", "alive":
		// ID is ignored by the current automation.Condition implementation for
		// these kinds. Keep it legal so adapters may retain a source identity.
	case "position":
		if c.Value < 0 || c.X < 0 || c.Y < 0 {
			return errors.New("position condition requires non-negative floor and coordinates")
		}
	case "pet_level", "item_count", "item_absent", "flag_set", "flag_clear", "character_skill_level", "window_sequence", "window_submitted":
		if c.Kind == "pet_level" && c.Value < 1 {
			return errors.New("minimum pet level must be positive")
		}
		if c.ID == "" {
			return fmt.Errorf("condition %s requires a stable identity", c.Kind)
		}
		if c.Kind == "item_absent" && c.Value != 0 {
			return errors.New("item_absent condition value must be zero")
		}
	default:
		return fmt.Errorf("unknown task condition %q", c.Kind)
	}
	if c.Value < 0 {
		return errors.New("negative condition value")
	}
	return nil
}

// AutomationCompatible reports whether the condition can be copied directly
// to automation.Condition. Knowledge extensions need a server observation
// adapter first.
func (c MachineCondition) AutomationCompatible() bool {
	switch c.Kind {
	case "character_level", "gold_at_least", "not_battle", "alive", "position", "pet_level", "item_count", "item_absent", "flag_set", "flag_clear", "backpack_free_slots":
		return true
	default:
		return false
	}
}

// Precondition is an evidence-bearing machine condition. Key and Operator
// are retained as deprecated authoring aliases for old JSON, but executable
// task definitions must use ID and the integer Value directly.
type Precondition struct {
	MachineCondition
	Key      string      `json:"key,omitempty"`
	Operator string      `json:"operator,omitempty"`
	Evidence []SourceRef `json:"evidence"`
}

// Condition returns the canonical executable form of a precondition.
func (p Precondition) Condition() MachineCondition {
	c := p.MachineCondition
	if c.ID == "" {
		c.ID = p.Key
	}
	return c
}

// TaskAction is a versioned executor skill invocation. Arguments must be a
// JSON object containing all values needed to submit the action, including a
// window sequence for NPC/window actions.
type TaskAction struct {
	Skill     string          `json:"skill"`
	Arguments json.RawMessage `json:"arguments"`
}

// TaskStep describes one human-maintained executable step.  Kind is an
// executor skill name such as move, talk, select, battle, give_item, or
// observe; unknown kinds are rejected by Validate.
type TaskStep struct {
	ID          string     `json:"id"`
	Kind        string     `json:"kind"`
	Description string     `json:"description"`
	Action      TaskAction `json:"action"`
	NPC         string     `json:"npc,omitempty"`
	Floor       int        `json:"floor,omitempty"`
	Coordinates *Point     `json:"coordinates,omitempty"`
	Inputs      []string   `json:"inputs,omitempty"`
	Outputs     []string   `json:"outputs,omitempty"`
	// Success is a human annotation kept for compatibility. It is never
	// interpreted as an execution predicate.
	Success           []string           `json:"success,omitempty"`
	Preconditions     []MachineCondition `json:"preconditions,omitempty"`
	SuccessConditions []MachineCondition `json:"success_conditions"`
	TimeoutSeconds    int                `json:"timeout_seconds"`
	CostKnown         bool               `json:"cost_known"`
	MaximumCost       int64              `json:"maximum_cost"`
	Evidence          []SourceRef        `json:"evidence"`
}

// SuccessCondition is a machine-readable and human-readable completion
// condition.  At least one of Expression or Description must be present.
type SuccessCondition struct {
	MachineCondition
	Expression  string      `json:"expression,omitempty"`
	Description string      `json:"description,omitempty"`
	Evidence    []SourceRef `json:"evidence"`
}

// Budget describes deterministic and uncertain costs.  A zero value means
// that the definition has not established a cost, which is different from a
// verified free task and should be called out by its Notes.
type Budget struct {
	GoldMin      int64       `json:"gold_min"`
	GoldExpected int64       `json:"gold_expected"`
	GoldMax      int64       `json:"gold_max"`
	Notes        string      `json:"notes,omitempty"`
	Evidence     []SourceRef `json:"evidence"`
}

// Evidence links a task to source files and explains what was checked.
type Evidence struct {
	Source   SourceRef `json:"source"`
	Claim    string    `json:"claim"`
	Verified bool      `json:"verified"`
}

// TaskDefinition is a manually curated, evidence-backed task.  Definitions
// are loaded from JSON and never inferred from names or model output.
type TaskDefinition struct {
	ID            string             `json:"id"`
	Name          string             `json:"name"`
	Description   string             `json:"description,omitempty"`
	Dependencies  []string           `json:"dependencies,omitempty"`
	Status        TaskStatus         `json:"status"`
	Preconditions []Precondition     `json:"preconditions"`
	Steps         []TaskStep         `json:"steps"`
	Success       []SuccessCondition `json:"success"`
	Budget        Budget             `json:"budget"`
	Evidence      []Evidence         `json:"evidence"`
	Source        SourceRef          `json:"source"`
	// PreparationReviewed is an operator review of guide/version applicability,
	// route hazards, character/pet minimum levels, equipment and supplies. It
	// must be established before live QA; ExecutionVerified is a later stage.
	// Any required levels belong in Preconditions, not only in prose.
	PreparationReviewed bool   `json:"preparation_reviewed"`
	PreparationNotes    string `json:"preparation_notes,omitempty"`
	// EvidenceVerified means every declared data source has a matching
	// SHA-256 and every static evidence claim is marked verified. It is
	// computed while loading and is independent from ExecutionVerified.
	EvidenceVerified bool `json:"evidence_verified"`
	// ExecutionVerified is an operator assertion backed by an external
	// runtime test. Loading source files cannot establish this value.
	ExecutionVerified bool `json:"execution_verified"`
	// DataFingerprint is the digest of the task's evidence files at authoring
	// time. A verified task must provide it and match the current files.
	DataFingerprint string `json:"data_fingerprint,omitempty"`
}

// Coverage summarizes what this snapshot can safely support.
type Coverage struct {
	CoreTables        map[string]bool `json:"core_tables"`
	NPCFiles          int             `json:"npc_files"`
	SupportedNPCFiles int             `json:"supported_npc_files"`
	TasksTotal        int             `json:"tasks_total"`
	TasksVerified     int             `json:"tasks_verified"`
	TasksUnverified   int             `json:"tasks_unverified"`
	TasksUnsupported  int             `json:"tasks_unsupported"`
	LevelingAreas     int             `json:"leveling_areas"`
	Complete          bool            `json:"complete"`
	Gaps              []string        `json:"gaps"`
}

// Knowledge is an immutable data snapshot.  Its slices and maps are copied
// by query methods; callers should use those methods when exposing data to
// other goroutines or an API response.
type Knowledge struct {
	Version         string               `json:"version"`
	Digest          string               `json:"fingerprint"`
	DataDir         string               `json:"data_dir"`
	Files           []FileDigest         `json:"files"`
	Experience      []ExperienceEntry    `json:"experience"`
	Encounters      []EncounterArea      `json:"encounters"`
	EnemiesTable    []Enemy              `json:"enemies"`
	EnemyBases      []EnemyBase          `json:"enemy_bases"`
	Groups          []EncounterGroup     `json:"groups"`
	Warps           []MapWarp            `json:"warps"`
	NPC             NPCKnowledge         `json:"npc"`
	Leveling        []LevelingArea       `json:"leveling_areas"`
	TaskDefinitions []TaskDefinition     `json:"tasks"`
	CoverageReport  Coverage             `json:"coverage"`
	Issues          []Issue              `json:"issues"`
	Catalog         *gamecatalog.Catalog `json:"catalog,omitempty"`
}

// Fingerprint returns the SHA-256 digest of all source files and task
// definitions loaded into this snapshot.
func (k *Knowledge) Fingerprint() string {
	if k == nil {
		return ""
	}
	return k.Digest
}

// Areas returns a copy of the derived leveling areas.
func (k *Knowledge) Areas() []LevelingArea {
	if k == nil {
		return nil
	}
	return cloneSlice(k.Leveling)
}

// Enemies returns a copy of encounter enemy rows.  It intentionally does not
// return enemybase templates; use EnemyBase or Catalog for those.
func (k *Knowledge) Enemies() []Enemy {
	if k == nil {
		return nil
	}
	return cloneSlice(k.EnemiesTable)
}

// Tasks returns a copy of all manually curated task definitions, including
// unverified ones so an operator can display the coverage gap.
func (k *Knowledge) Tasks() []TaskDefinition {
	if k == nil {
		return nil
	}
	result := make([]TaskDefinition, len(k.TaskDefinitions))
	for i, task := range k.TaskDefinitions {
		result[i] = cloneTask(task)
	}
	return result
}

// Coverage returns a copy of the coverage report.
func (k *Knowledge) Coverage() Coverage {
	if k == nil {
		return Coverage{}
	}
	return cloneCoverage(k.CoverageReport)
}

// FindArea finds an area by its encount ID.
func (k *Knowledge) FindArea(id int) (LevelingArea, bool) {
	if k == nil {
		return LevelingArea{}, false
	}
	for _, area := range k.Leveling {
		if area.ID == id {
			return cloneArea(area), true
		}
	}
	return LevelingArea{}, false
}

// FindEnemy finds an encounter enemy by ID.
func (k *Knowledge) FindEnemy(id int) (Enemy, bool) {
	if k == nil {
		return Enemy{}, false
	}
	for _, enemy := range k.EnemiesTable {
		if enemy.ID == id {
			return cloneEnemy(enemy), true
		}
	}
	return Enemy{}, false
}

// FindTask finds a task by its stable definition ID.
func (k *Knowledge) FindTask(id string) (TaskDefinition, bool) {
	if k == nil {
		return TaskDefinition{}, false
	}
	for _, task := range k.TaskDefinitions {
		if task.ID == id {
			return cloneTask(task), true
		}
	}
	return TaskDefinition{}, false
}

// SearchTasks performs a case-insensitive search over task IDs, names, and
// descriptions.
func (k *Knowledge) SearchTasks(query string) []TaskDefinition {
	needle := strings.ToLower(strings.TrimSpace(query))
	if k == nil {
		return nil
	}
	result := make([]TaskDefinition, 0)
	for _, task := range k.TaskDefinitions {
		if needle == "" || strings.Contains(strings.ToLower(task.ID), needle) ||
			strings.Contains(strings.ToLower(task.Name), needle) ||
			strings.Contains(strings.ToLower(task.Description), needle) {
			result = append(result, cloneTask(task))
		}
	}
	return result
}

func cloneSlice[T any](in []T) []T {
	if in == nil {
		return nil
	}
	return append([]T(nil), in...)
}

func cloneArea(in LevelingArea) LevelingArea {
	in.EncounterIDs = cloneSlice(in.EncounterIDs)
	in.GroupIDs = cloneSlice(in.GroupIDs)
	in.EnemyIDs = cloneSlice(in.EnemyIDs)
	in.TemplateIDs = cloneSlice(in.TemplateIDs)
	in.Evidence = cloneSlice(in.Evidence)
	return in
}

func cloneEnemy(in Enemy) Enemy {
	in.Drops = cloneSlice(in.Drops)
	return in
}

func cloneTask(in TaskDefinition) TaskDefinition {
	in.Dependencies = cloneSlice(in.Dependencies)
	in.Preconditions = cloneSlice(in.Preconditions)
	in.Steps = cloneSlice(in.Steps)
	in.Success = cloneSlice(in.Success)
	in.Evidence = cloneSlice(in.Evidence)
	for i := range in.Preconditions {
		in.Preconditions[i].Evidence = cloneSlice(in.Preconditions[i].Evidence)
	}
	for i := range in.Steps {
		in.Steps[i].Inputs = cloneSlice(in.Steps[i].Inputs)
		in.Steps[i].Outputs = cloneSlice(in.Steps[i].Outputs)
		in.Steps[i].Success = cloneSlice(in.Steps[i].Success)
		in.Steps[i].Preconditions = cloneSlice(in.Steps[i].Preconditions)
		in.Steps[i].SuccessConditions = cloneSlice(in.Steps[i].SuccessConditions)
		in.Steps[i].Evidence = cloneSlice(in.Steps[i].Evidence)
		if in.Steps[i].Coordinates != nil {
			coordinates := *in.Steps[i].Coordinates
			in.Steps[i].Coordinates = &coordinates
		}
		in.Steps[i].Action.Arguments = append(json.RawMessage(nil), in.Steps[i].Action.Arguments...)
	}
	for i := range in.Success {
		in.Success[i].Evidence = cloneSlice(in.Success[i].Evidence)
	}
	in.Budget.Evidence = cloneSlice(in.Budget.Evidence)
	return in
}

func cloneCoverage(in Coverage) Coverage {
	if in.CoreTables != nil {
		original := in.CoreTables
		in.CoreTables = make(map[string]bool, len(original))
		for key, value := range original {
			in.CoreTables[key] = value
		}
	}
	in.Gaps = cloneSlice(in.Gaps)
	return in
}

// SHA256Hex returns the lowercase hexadecimal SHA-256 digest of data.  It is
// exported for task tooling and tests that need to construct source evidence.
func SHA256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// SortedSourceRefs returns a deterministic copy, useful when assembling a
// task definition from multiple evidence files.
func SortedSourceRefs(refs []SourceRef) []SourceRef {
	result := cloneSlice(refs)
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Path != result[j].Path {
			return result[i].Path < result[j].Path
		}
		return result[i].Line < result[j].Line
	})
	return result
}
