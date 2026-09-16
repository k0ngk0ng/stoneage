package aiservice

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strconv"

	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/aiplanner"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

// QuestPlans is the request-to-plan boundary. Parameter names are closed;
// models cannot set verification bits, source paths, costs or protocol steps.
type QuestPlans struct {
	Planner     *aiplanner.Planner
	CharacterID string
	Backend     *GameBackend
}

func (p *QuestPlans) Task(ctx context.Context, q aimcp.TaskRequest) (automation.Plan, error) {
	if p.Planner == nil {
		return automation.Plan{}, aimcp.ErrBackend
	}
	options := aiplanner.TaskOptions{CharacterID: p.CharacterID}
	includeDependencies := false
	for key, raw := range q.Parameters {
		var err error
		switch key {
		case "maximum_seconds":
			err = json.Unmarshal(raw, &options.MaximumSeconds)
		case "maximum_deaths":
			err = json.Unmarshal(raw, &options.MaximumDeaths)
		case "include_dependencies":
			err = json.Unmarshal(raw, &includeDependencies)
		case "selected_pet_id":
			err = json.Unmarshal(raw, &options.SelectedPetID)
		case "reserve_gold":
			err = json.Unmarshal(raw, &options.ReserveGold)
		default:
			return automation.Plan{}, aimcp.ErrInvalidParams
		}
		if err != nil || string(raw) == "null" {
			return automation.Plan{}, aimcp.ErrInvalidParams
		}
	}
	if options.SelectedPetID != "" {
		if p.Backend == nil {
			return automation.Plan{}, aimcp.ErrBackend
		}
		observed, err := p.Backend.Observe(ctx, p.Backend.Binding)
		if err != nil {
			return automation.Plan{}, err
		}
		if err := ValidateQuestPetBinding(projectAutomationObservation(observed), p.CharacterID, options.SelectedPetID); err != nil {
			return automation.Plan{}, err
		}
	}
	if includeDependencies {
		return p.Planner.BuildChain(ctx, q.TaskID, options)
	}
	return p.Planner.BuildTask(ctx, q.TaskID, options)
}
func (*QuestPlans) Leveling(context.Context, aimcp.LevelingRequest) (automation.Plan, error) {
	return automation.Plan{}, errors.New("leveling requires the battle-aware leveling coordinator")
}

// DeterministicSkill validates current state, cost and arguments, then
// performs a bounded game skill. It is application code, not model output.
// The game control gate must fence every individual protocol submission.
type DeterministicSkill interface {
	ValidateSkill(context.Context, automation.Action) error
	Execute(context.Context, automation.Action) error
}
type AutomationGame struct {
	Backend *GameBackend
	Skills  DeterministicSkill
	NPCs    NPCRegistry
	// Funding is a server-owned policy lookup. It is reevaluated for every
	// observation so an active plan sees revocation immediately.
	Funding func(context.Context) (bool, error)
}

func (g *AutomationGame) Observe(ctx context.Context) (automation.Observation, error) {
	if g.Backend == nil {
		return automation.Observation{}, aimcp.ErrBackend
	}
	o, err := g.Backend.Observe(ctx, g.Backend.Binding)
	if err != nil {
		return automation.Observation{}, err
	}
	r := ProjectAutomationObservation(o, g.NPCs)
	if g.Funding != nil {
		r.UnlimitedFunds, err = g.Funding(ctx)
		if err != nil {
			return automation.Observation{}, err
		}
	}
	return r, nil
}

// ProjectAutomationObservation shares authoritative quest evidence between
// human previews and execution. Funding remains an authenticated runtime policy.
func ProjectAutomationObservation(o aimcp.Observation, npcs NPCRegistry) automation.Observation {
	r := projectAutomationObservation(o)
	for alias, sequence := range npcs.ObservedWindows(o) {
		r.Windows[alias] = sequence
	}
	r.SubmittedWindows = npcs.SubmittedWindows(o)
	return r
}

func projectAutomationObservation(observation aimcp.Observation) automation.Observation {
	entity := func(source aimcp.Entity) automation.Entity {
		return automation.Entity{
			ID: source.ID, Name: source.Name, Level: source.Level,
			HP: source.HP, MaxHP: source.MaxHP, Alive: source.Alive,
			Skills: slices.Clone(source.Skills), UseFlag: source.UseFlag,
		}
	}
	result := automation.Observation{
		Revision: observation.Revision, CharacterID: observation.CharacterID,
		Connected: observation.Connected, Ready: observation.Ready,
		Character: entity(observation.Character), Floor: observation.Floor,
		X: observation.X, Y: observation.Y, Gold: observation.Gold,
		UnlimitedFunds: observation.UnlimitedFunds, Spent: observation.Spent,
		SpendingKnown: observation.SpendingKnown, Battle: observation.Battle.Active,
		Dead: !observation.Character.Alive, Inventory: maps.Clone(observation.Inventory),
		Flags: maps.Clone(observation.Flags), Skills: maps.Clone(observation.Skills),
		OwnProgress: maps.Clone(observation.OwnProgress), Windows: make(map[string]int),
	}
	for _, pet := range observation.Pets {
		if pet.ID != "" {
			result.Pets = append(result.Pets, entity(pet))
		}
	}
	if window := observation.ActiveWindow; observation.Ready && window != nil && window.Open && !window.Submitted {
		result.Windows[strconv.Itoa(window.ObjectID)] = window.Sequence
	}
	return result
}
func (g *AutomationGame) Execute(ctx context.Context, a automation.Action) error {
	if g.Backend == nil || g.Skills == nil {
		return aimcp.ErrBackend
	}
	if err := g.Backend.check(g.Backend.Binding); err != nil {
		return err
	}
	return g.Skills.Execute(ctx, a)
}

func (g *AutomationGame) ValidateSkill(ctx context.Context, a automation.Action) error {
	if g.Skills == nil {
		return aimcp.ErrBackend
	}
	return g.Skills.ValidateSkill(ctx, a)
}

var _ PlanBuilder = (*QuestPlans)(nil)
var _ automation.Game = (*AutomationGame)(nil)

// ValidateQuestPetBinding accepts only a uniquely identified pet owned by the
// authenticated character. Both human previews and AI requests use this gate.
// The compiled plan retains this exact identity across pause/resume.
func ValidateQuestPetBinding(observed automation.Observation, characterID, petID string) error {
	if petID == "" {
		return nil
	}
	if !observed.Connected || !observed.Ready || characterID == "" || observed.CharacterID != characterID {
		return errors.New("quest pet binding requires current character state")
	}
	matches := 0
	for _, pet := range observed.Pets {
		if pet.ID == petID {
			matches++
		}
	}
	if matches != 1 {
		return errors.New("selected quest pet must have a unique owned stable identity")
	}
	return nil
}
