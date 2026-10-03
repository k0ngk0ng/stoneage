package automation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

// StepProgress is written only by an installed deterministic skill. It is not
// part of a submitted plan. Confirmed outputs are immutable execution receipts.
type StepProgress struct {
	State     json.RawMessage `json:"state,omitempty"`
	PetIDs    []string        `json:"pet_ids,omitempty"`
	Confirmed bool            `json:"confirmed,omitempty"`
}

type StepExecution struct {
	PlanID, StepID string
	Progress       StepProgress
	Results        map[string]StepProgress
	// Save commits before returning, using the plan's same CAS revision.
	// A skill must stop before its next game action if Save fails.
	Save func(context.Context, StepProgress) error
}

type ProgressGame interface {
	ExecuteStep(context.Context, Action, StepExecution) error
}

// ProgressResumer must prove a checkpoint is at a known boundary. It is only
// consulted for zero-cost skills; ordinary uncertain writes stay unreplayable.
type ProgressResumer interface {
	CanResumeStep(context.Context, Action, StepProgress) error
}

func (p StepProgress) Clone() StepProgress {
	p.State = slices.Clone(p.State)
	p.PetIDs = slices.Clone(p.PetIDs)
	return p
}

func validateStepProgress(p StepProgress) error {
	if len(p.State) > 65536 || len(p.State) > 0 && !json.Valid(p.State) || len(p.PetIDs) > 5 {
		return errors.New("invalid step progress")
	}
	seen := map[string]bool{}
	for _, id := range p.PetIDs {
		if id == "" || len(id) > 512 || seen[id] {
			return errors.New("invalid stable pet output")
		}
		seen[id] = true
	}
	return nil
}

func validateOutputReferences(p Plan) error {
	indices := map[string]int{}
	for i, s := range p.Steps {
		indices[s.ID] = i
	}
	check := func(cs []Condition, before int) error {
		for _, c := range cs {
			if c.Kind != "pet_collection" && c.Kind != "step_confirmed" {
				continue
			}
			i, ok := indices[c.ID]
			if !ok || i >= before {
				return errors.New("step output condition refers to an unknown or later step")
			}
			if c.Kind == "pet_collection" && p.Steps[i].Action.Skill != "pet.collect" {
				return errors.New("pet collection condition requires a pet.collect output")
			}
		}
		return nil
	}
	if err := check(p.Preconditions, 0); err != nil {
		return err
	}
	if err := check(p.Completion, len(p.Steps)); err != nil {
		return err
	}
	for i, s := range p.Steps {
		if err := check(s.Preconditions, i); err != nil {
			return err
		}
		if err := check(s.Success, i+1); err != nil {
			return err
		}
		var args map[string]json.RawMessage
		if json.Unmarshal(s.Action.Arguments, &args) == nil {
			if raw, present := args["pet_collection"]; present {
				var ref string
				j, ok := 0, false
				if json.Unmarshal(raw, &ref) == nil {
					j, ok = indices[ref]
				}
				if !ok || j >= i || p.Steps[j].Action.Skill != "pet.collect" {
					return errors.New("pet delivery must reference an earlier pet.collect step")
				}
			}
		}
	}
	for _, s := range p.Stages {
		if err := check(s.Preconditions, s.StartStep); err != nil {
			return err
		}
		if err := check(s.Completion, s.EndStep); err != nil {
			return err
		}
	}
	return nil
}

func projectStepProgress(o Observation, c Checkpoint) Observation {
	// The game's own response cannot inject checkpoint receipts.
	o.StepPets = map[string][]string{}
	o.ConfirmedSteps = map[string]bool{}
	if !o.Connected || !o.Ready || o.CharacterID != c.Plan.CharacterID {
		return o
	}
	owned := map[string]int{}
	for _, pet := range o.Pets {
		if pet.ID != "" {
			owned[pet.ID]++
		}
	}
	for id, p := range c.StepProgress {
		if !p.Confirmed || validateStepProgress(p) != nil {
			continue
		}
		o.ConfirmedSteps[id] = true
		for _, pet := range p.PetIDs {
			if owned[pet] == 1 {
				o.StepPets[id] = append(o.StepPets[id], pet)
			}
		}
	}
	return o
}

func (e *Engine) executeStep(ctx context.Context, c *Checkpoint, a Action) error {
	remaining := time.Duration(c.Plan.MaximumSeconds)*time.Second - e.now().Sub(c.StartedAt)
	limit := time.Duration(c.Plan.Steps[c.Step].TimeoutSeconds) * time.Second
	if remaining < limit {
		limit = remaining
	}
	if limit <= 0 {
		return context.DeadlineExceeded
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	g, ok := e.Game.(ProgressGame)
	if !ok {
		return e.Game.Execute(ctx, a)
	}
	id := c.Plan.Steps[c.Step].ID
	results := map[string]StepProgress{}
	for i := 0; i < c.Step; i++ {
		key := c.Plan.Steps[i].ID
		if p, exists := c.StepProgress[key]; exists && p.Confirmed {
			results[key] = p.Clone()
		}
	}
	scope := StepExecution{PlanID: c.Plan.ID, StepID: id, Progress: c.StepProgress[id].Clone(), Results: results}
	scope.Save = func(saveCtx context.Context, p StepProgress) error {
		if err := saveCtx.Err(); err != nil {
			return err
		}
		if err := validateStepProgress(p); err != nil {
			return err
		}
		old := c.StepProgress[id]
		if old.Confirmed && (!p.Confirmed || !slices.Equal(old.PetIDs, p.PetIDs) || !bytes.Equal(old.State, p.State)) {
			return errors.New("confirmed step output cannot change")
		}
		if c.StepProgress == nil {
			c.StepProgress = map[string]StepProgress{}
		}
		c.StepProgress[id] = p.Clone()
		if err := e.save(saveCtx, c); err != nil {
			c.StepProgress[id] = old
			return err
		}
		return nil
	}
	return g.ExecuteStep(ctx, a, scope)
}

func (e *Engine) canResumeStep(ctx context.Context, c *Checkpoint) bool {
	if c.Step < 0 || c.Step >= len(c.Plan.Steps) {
		return false
	}
	s := c.Plan.Steps[c.Step]
	if s.MaximumCost != 0 {
		return false
	}
	g, ok := e.Game.(ProgressResumer)
	p, exists := c.StepProgress[s.ID]
	return ok && exists && validateStepProgress(p) == nil && g.CanResumeStep(ctx, s.Action, p.Clone()) == nil
}
