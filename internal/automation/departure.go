package automation

import "context"

// DepartureValidator checks the first action's feasibility against the supplied
// observation without obtaining a lease, observing again, or sending actions.
// Later actions may depend on NPC effects; they are checked during execution.
type DepartureValidator interface {
	ValidateDeparture(context.Context, Action, Observation) error
}

func (p Plan) departureAction(o Observation) (Action, bool) {
	index := 0
	if len(p.Stages) > 0 {
		stage := p.firstIncompleteStage(o)
		if stage >= len(p.Stages) {
			return Action{}, false
		}
		index = p.Stages[stage].StartStep
	}
	if index < 0 || index >= len(p.Steps) {
		return Action{}, false
	}
	action := p.Steps[index].Action
	action.MaximumCost = p.Steps[index].MaximumCost
	return action, true
}
