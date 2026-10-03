// Package automation runs deterministic game plans. Game is a stateful,
// authenticated adapter: neither observations nor permissions come from a model.
package automation

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

type Entity struct {
	SpeciesID      int      `json:"species_id,omitempty"`
	SpeciesIDKnown bool     `json:"species_id_known,omitempty"`
	EventFlag      int      `json:"event_flag,omitempty"`
	EventFlagKnown bool     `json:"event_flag_known,omitempty"`
	Slot           *int     `json:"slot,omitempty"`
	ID             string   `json:"id"`
	Name           string   `json:"name,omitempty"`
	Level          int      `json:"level"`
	HP             int      `json:"hp"`
	MaxHP          int      `json:"max_hp"`
	Alive          bool     `json:"alive,omitempty"`
	Skills         []string `json:"skills,omitempty"`
	UseFlag        int      `json:"use_flag,omitempty"`
}

type Observation struct {
	// StepPets is derived by Engine from durable, confirmed collection outputs
	// intersected with this observation's owned stable identities.
	StepPets       map[string][]string `json:"step_pets,omitempty"`
	ConfirmedSteps map[string]bool     `json:"confirmed_steps,omitempty"`
	Revision       uint64              `json:"revision"`
	CharacterID    string              `json:"character_id"`
	Connected      bool                `json:"connected"`
	Ready          bool                `json:"ready"`
	Character      Entity              `json:"character"`
	Pets           []Entity            `json:"pets"`
	Floor          int                 `json:"floor"`
	X              int                 `json:"x"`
	Y              int                 `json:"y"`
	Gold           int64               `json:"gold"`
	// UnlimitedFunds comes only from the authenticated server capability.
	UnlimitedFunds bool `json:"unlimited_funds"`
	// Spent is a monotonically increasing, server-observed expenditure counter.
	// Gold balance deltas alone are not a reliable expense ledger.
	Spent           int64           `json:"spent"`
	SpendingKnown   bool            `json:"spending_known"`
	Battle          bool            `json:"battle"`
	Dead            bool            `json:"dead"`
	Inventory       map[string]int  `json:"inventory"`
	Flags           map[string]bool `json:"flags"`
	Skills          map[string]int  `json:"skills,omitempty"`
	OwnProgress     map[string]int  `json:"own_progress,omitempty"`
	EncounterPolicy string          `json:"encounter_policy,omitempty"`
	Windows         map[string]int  `json:"windows,omitempty"`
	// SubmittedWindows records local successful writes, never server acknowledgements.
	SubmittedWindows map[string]int `json:"submitted_windows,omitempty"`
}

type Action struct {
	Skill            string          `json:"skill"`
	Arguments        json.RawMessage `json:"arguments"`
	ExpectedRevision uint64          `json:"expected_revision"`
	MaximumCost      int64           `json:"maximum_cost"`
}

type Game interface {
	Observe(context.Context) (Observation, error)
	// Execute returns after submission/skill completion; success predicates
	// still must be observed. Uncertain delivery must never be blindly retried.
	// Paid skills must enforce MaximumCost against their current verified quote.
	Execute(context.Context, Action) error
}

// SkillValidator lets the application reject unavailable deterministic
// skills and unsupported arguments before acquiring a running checkpoint.
type SkillValidator interface {
	ValidateSkill(context.Context, Action) error
}

type Condition struct {
	Kind  string `json:"kind"`
	ID    string `json:"id,omitempty"`
	Value int64  `json:"value,omitempty"`
	X     int    `json:"x,omitempty"`
	Y     int    `json:"y,omitempty"`
}

func (c Condition) Validate() error {
	switch c.Kind {
	case "pet_collection":
		if c.ID == "" || c.Value < 1 || c.Value > 5 {
			return errors.New("pet collection requires a step identity and count 1..5")
		}
	case "step_confirmed":
		if c.ID == "" || c.Value != 0 {
			return errors.New("confirmed step requires an identity and zero value")
		}
	case "pet_species_absent":
		if c.Value < 0 {
			return errors.New("pet species must be nonnegative")
		}
	case "pet_free_slots":
		if c.Value < 0 || c.Value > 5 {
			return errors.New("pet free slots must be 0..5")
		}
	case "gold_reward_capacity":
		if c.Value < 1 {
			return errors.New("gold reward must be positive")
		}
	case "backpack_free_slots":
		if c.Value < 0 || c.Value > 15 {
			return errors.New("backpack free slots must be 0..15")
		}
	case "character_hp_percent":
		if c.Value < 1 || c.Value > 100 {
			return errors.New("health percentage must be 1..100")
		}
	case "character_level", "character_hp_full", "gold_at_least", "battle", "not_battle", "alive", "position":
		if c.Kind == "character_level" && c.Value < 1 {
			return errors.New("character level must be at least 1")
		}
	case "pet_level", "item_count", "item_absent", "flag_set", "flag_clear", "character_skill_level", "window_sequence", "window_submitted":
		if c.ID == "" {
			return errors.New("condition requires a stable identity")
		}
		if c.Kind == "pet_level" && c.Value < 1 {
			return errors.New("pet level must be at least 1")
		}
		if c.Kind == "item_absent" && c.Value != 0 {
			return errors.New("item_absent condition value must be zero")
		}
	default:
		return errors.New("unknown game condition")
	}
	if c.Value < 0 {
		return errors.New("negative condition value")
	}
	return nil
}

func (c Condition) Match(o Observation) bool {
	switch c.Kind {
	case "pet_species_absent":
		if c.Value < 0 || !o.Connected || !o.Ready || !o.Flags["pets:known"] {
			return false
		}
		for _, pet := range o.Pets {
			if !pet.SpeciesIDKnown || int64(pet.SpeciesID) == c.Value {
				return false
			}
		}
		return true
	case "pet_free_slots":
		return c.Value >= 0 && c.Value <= 5 && o.Connected && o.Ready && o.Flags["pets:known"] && len(o.Pets) <= 5 && int64(5-len(o.Pets)) >= c.Value
	case "gold_reward_capacity":
		limit, known := o.OwnProgress["gold_limit"]
		return c.Value > 0 && o.Connected && o.Ready && o.Flags["gold_limit:known"] && known && limit > 0 && o.Gold >= 0 && o.Gold < int64(limit) && c.Value < int64(limit)-o.Gold

	case "pet_collection":
		return o.Connected && o.Ready && !o.Battle && c.ID != "" && c.Value > 0 && int64(len(o.StepPets[c.ID])) >= c.Value
	case "step_confirmed":
		return o.Connected && o.Ready && c.ID != "" && o.ConfirmedSteps[c.ID]
	case "battle":
		return o.Connected && o.Battle
	case "backpack_free_slots":
		used, known := o.OwnProgress["backpack_used_slots"]
		return c.Value >= 0 && c.Value <= 15 && o.Flags["inventory:known"] && known && used >= 0 && used <= 15 && int64(15-used) >= c.Value
	case "character_hp_percent":
		return c.Value >= 1 && c.Value <= 100 && o.Connected && o.Ready && !o.Battle && !o.Dead && o.Character.MaxHP > 0 && o.Character.HP > 0 && o.Character.HP <= o.Character.MaxHP && int64(o.Character.HP)*100 >= int64(o.Character.MaxHP)*c.Value
	case "character_level":
		return c.Value >= 1 && o.Character.Level >= 1 && int64(o.Character.Level) >= c.Value
	case "pet_level":
		for _, p := range o.Pets {
			if p.ID == c.ID {
				return c.ID != "" && c.Value >= 1 && p.Level >= 1 && int64(p.Level) >= c.Value
			}
		}
		return false
	case "gold_at_least":
		return o.UnlimitedFunds || o.Gold >= c.Value
	case "not_battle":
		return !o.Battle
	case "alive":
		return !o.Dead && o.Character.HP > 0
	case "character_hp_full":
		return o.Connected && o.Ready && !o.Battle && !o.Dead && o.Character.MaxHP > 0 && o.Character.HP == o.Character.MaxHP
	case "position":
		return int64(o.Floor) == c.Value && o.X == c.X && o.Y == c.Y
	case "item_count":
		n, known := o.Inventory[c.ID]
		return known && int64(n) >= c.Value
	case "item_absent":
		// Absence is only meaningful after a connected, ready session has
		// received an authoritative inventory snapshot. A missing map entry and
		// an explicit zero count both mean that the item is absent; an absent or
		// untrusted map must never satisfy the condition.
		if c.ID == "" || c.Value != 0 || !o.Connected || !o.Ready || !o.Flags["inventory:known"] || o.Inventory == nil {
			return false
		}
		n, present := o.Inventory[c.ID]
		return !present || n == 0
	case "flag_set":
		v, known := o.Flags[c.ID]
		return known && v
	case "flag_clear":
		v, known := o.Flags[c.ID]
		return known && !v
	case "character_skill_level":
		v, known := o.Skills[c.ID]
		return known && int64(v) >= c.Value
	case "window_submitted":
		v, known := o.SubmittedWindows[c.ID]
		return known && int64(v) == c.Value
	case "window_sequence":
		v, known := o.Windows[c.ID]
		return known && int64(v) == c.Value
	}
	return false
}

func conditionsMatch(conditions []Condition, o Observation) bool {
	for _, c := range conditions {
		if !c.Match(o) {
			return false
		}
	}
	return true
}

type Step struct {
	ID             string      `json:"id"`
	Description    string      `json:"description"`
	Action         Action      `json:"action"`
	Preconditions  []Condition `json:"preconditions"`
	Success        []Condition `json:"success"`
	TimeoutSeconds int         `json:"timeout_seconds"`
	MaximumCost    int64       `json:"maximum_cost"`
	CostKnown      bool        `json:"cost_known"`
}

type Budget struct {
	Minimum      int64 `json:"minimum"`
	ExpectedLow  int64 `json:"expected_low"`
	ExpectedHigh int64 `json:"expected_high"`
	Reserve      int64 `json:"reserve"`
	MaximumSpend int64 `json:"maximum_spend"`
	Known        bool  `json:"known"`
}

type Target struct {
	Kind  string `json:"kind"`
	ID    string `json:"id,omitempty"`
	Level int    `json:"level"`
}

// QuestStage is one reviewed node in a quest dependency chain. Steps from
// every stage are flattened into Plan.Steps so one checkpoint, lease and
// cumulative budget cover the complete chain. StartStep is inclusive and
// EndStep is exclusive.
type QuestStage struct {
	ID            string      `json:"id"`
	Title         string      `json:"title,omitempty"`
	Dependencies  []string    `json:"dependencies,omitempty"`
	StartStep     int         `json:"start_step"`
	EndStep       int         `json:"end_step"`
	Preconditions []Condition `json:"preconditions,omitempty"`
	Completion    []Condition `json:"completion"`
}

type Plan struct {
	ID                string       `json:"id"`
	CharacterID       string       `json:"character_id"`
	Mode              string       `json:"mode"`
	KnowledgeRevision string       `json:"knowledge_revision"`
	Title             string       `json:"title"`
	Preconditions     []Condition  `json:"preconditions"`
	Completion        []Condition  `json:"completion"`
	Steps             []Step       `json:"steps"`
	Targets           []Target     `json:"targets"`
	TargetPolicy      string       `json:"target_policy"`
	Budget            Budget       `json:"budget"`
	MaximumSeconds    int          `json:"maximum_seconds"`
	MaximumDeaths     int          `json:"maximum_deaths"`
	OfflineContinue   bool         `json:"offline_continue"`
	Stages            []QuestStage `json:"stages,omitempty"`
}

type Status string

const (
	Running   Status = "running"
	Paused    Status = "paused"
	Completed Status = "completed"
)

type Checkpoint struct {
	StepProgress map[string]StepProgress `json:"step_progress,omitempty"`
	// OwnerContext is opaque server-owned recovery metadata. Engines preserve it
	// without interpreting it; clients and models do not supply this field.
	OwnerContext json.RawMessage `json:"owner_context,omitempty"`
	Confirmation *Observation    `json:"confirmation,omitempty"`
	Plan         Plan            `json:"plan"`
	Revision     uint64          `json:"revision"`
	Status       Status          `json:"status"`
	Step         int             `json:"step"`
	// Stage is the index of the current chain node. It is intentionally
	// separate from Step, whose index remains global across all flattened
	// stages. For plans without Stages these fields are ignored.
	Stage         int       `json:"stage,omitempty"`
	StageEntered  bool      `json:"stage_entered,omitempty"`
	Phase         string    `json:"phase"`
	Reason        string    `json:"reason"`
	StartedAt     time.Time `json:"started_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	StepStartedAt time.Time `json:"step_started_at"`
	InitialSpent  int64     `json:"initial_spent"`
	ReservedSpend int64     `json:"reserved_spend"`
	Deaths        int       `json:"deaths"`
	WasDead       bool      `json:"was_dead"`
}

type Store interface {
	Create(context.Context, Checkpoint) error
	Load(context.Context, string) (Checkpoint, error)
	Save(context.Context, Checkpoint, uint64) error
}

var ErrConflict = errors.New("automation checkpoint changed")
var ErrNotFound = errors.New("automation plan not found")
