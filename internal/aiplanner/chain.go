package aiplanner

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	"github.com/k0ngk0ng/stoneage/internal/automation"
)

// BuildChain explicitly authorizes the reviewed prerequisite graph as one
// durable plan. BuildTask retains its single-task spending semantics.
func (p *Planner) BuildChain(ctx context.Context, taskID string, options TaskOptions) (automation.Plan, error) {
	if p == nil || p.Knowledge == nil || options.MaximumSeconds < 0 {
		return automation.Plan{}, ErrInvalidTaskOptions
	}
	order, err := p.Knowledge.TaskOrder(taskID)
	if err != nil {
		return automation.Plan{}, fmt.Errorf("%w: %v", ErrTaskDependency, err)
	}
	var chain automation.Plan
	var stages []automation.QuestStage
	var steps []automation.Step
	budget := automation.Budget{Known: true, Reserve: options.ReserveGold}
	seconds := 0
	compiled := make(map[string]automation.Plan, len(order))
	for index, task := range order {
		nodeOptions := options
		nodeOptions.MaximumSeconds = 0
		if task.ID != taskID {
			nodeOptions.EvidenceFingerprint = ""
		}
		node, err := p.buildSingleTask(ctx, task.ID, nodeOptions)
		if err != nil {
			return automation.Plan{}, fmt.Errorf("%w: %s: %w", ErrTaskDependency, task.ID, err)
		}
		if err := namespaceStepOutputs(&node, fmt.Sprintf("stage-%d/", index+1)); err != nil {
			return automation.Plan{}, err
		}
		for _, id := range task.Dependencies {
			dependency, ok := compiled[id]
			if !ok {
				return automation.Plan{}, ErrTaskDependency
			}
			node.Preconditions = append(node.Preconditions, dependency.Completion...)
		}
		compiled[task.ID] = node
		stages = append(stages, automation.QuestStage{
			ID: task.ID, Title: task.Name, Dependencies: append([]string(nil), task.Dependencies...),
			StartStep: len(steps), EndStep: len(steps) + len(node.Steps),
			Preconditions: node.Preconditions, Completion: node.Completion,
		})
		for _, step := range node.Steps {
			steps = append(steps, step)
		}
		for _, pair := range []struct {
			total *int64
			value int64
		}{
			{&budget.Minimum, node.Budget.Minimum}, {&budget.ExpectedLow, node.Budget.ExpectedLow},
			{&budget.ExpectedHigh, node.Budget.ExpectedHigh}, {&budget.MaximumSpend, node.Budget.MaximumSpend},
		} {
			if *pair.total > math.MaxInt64-pair.value {
				return automation.Plan{}, fmt.Errorf("%w: chain budget overflow", ErrInvalidCost)
			}
			*pair.total += pair.value
		}
		if seconds > math.MaxInt-node.MaximumSeconds {
			return automation.Plan{}, ErrInvalidTaskOptions
		}
		seconds += node.MaximumSeconds
		chain = node
	}
	chain.Steps, chain.Stages, chain.Budget = steps, stages, budget
	// Conditions belong to stage entry, not departure for the entire chain.
	chain.Preconditions = nil
	chain.MaximumSeconds = seconds
	if options.MaximumSeconds > 0 {
		chain.MaximumSeconds = options.MaximumSeconds
	}
	if err := chain.Validate(); err != nil {
		return automation.Plan{}, fmt.Errorf("%w: chain: %v", ErrInvalidTaskOptions, err)
	}
	return chain, nil
}

func namespaceStepOutputs(p *automation.Plan, prefix string) error {
	conditions := func(cs []automation.Condition) {
		for i := range cs {
			if cs[i].Kind == "pet_collection" || cs[i].Kind == "step_confirmed" {
				cs[i].ID = prefix + cs[i].ID
			}
		}
	}
	conditions(p.Preconditions)
	conditions(p.Completion)
	for i := range p.Steps {
		s := &p.Steps[i]
		s.ID = prefix + s.ID
		conditions(s.Preconditions)
		conditions(s.Success)
		var args map[string]json.RawMessage
		if err := json.Unmarshal(s.Action.Arguments, &args); err != nil {
			return err
		}
		if raw, ok := args["pet_collection"]; ok {
			var ref string
			if err := json.Unmarshal(raw, &ref); err != nil {
				return err
			}
			args["pet_collection"], _ = json.Marshal(prefix + ref)
			s.Action.Arguments, _ = json.Marshal(args)
		}
	}
	return nil
}
