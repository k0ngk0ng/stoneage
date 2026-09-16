package aiservice

import (
	"context"

	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aiplanner"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

// ValidateDeparture reuses execution's static route search without calling
// Backend.Observe (which may refresh state with packets) or mutating the skill.
func (s *MovementSkill) ValidateDeparture(ctx context.Context, action automation.Action, o automation.Observation) error {
	if err := s.ValidateSkill(ctx, action); err != nil {
		return err
	}
	if !o.Connected || !o.Ready || o.Battle || o.Dead || o.Character.HP <= 0 {
		return ErrUnsafeTravelRoute
	}
	var args movementArguments
	if err := decodeArguments(action.Arguments, &args); err != nil {
		return err
	}
	guarded := *s
	if s.SafeTravel {
		navigator, err := s.travelNavigator(o.Character.Level)
		if err != nil {
			return err
		}
		guarded.Navigator = navigator
	}
	from := aiknowledge.Point{Floor: o.Floor, X: o.X, Y: o.Y}
	to := aiknowledge.Point{Floor: args.Floor, X: args.X, Y: args.Y}
	if from.Floor == to.Floor {
		_, err := guarded.routeBetween(ctx, from, to)
		return err
	}
	graph := guarded.WarpGraph
	if graph == nil {
		graph = aiplanner.NewWarpGraph(s.Backend.Knowledge)
	}
	_, err := guarded.findCrossMapRoute(ctx, graph, from, to)
	return err
}

func (g *AutomationGame) ValidateDeparture(ctx context.Context, action automation.Action, o automation.Observation) error {
	if validator, ok := g.Skills.(automation.DepartureValidator); ok {
		return validator.ValidateDeparture(ctx, action, o)
	}
	return nil
}

func (s SkillSet) ValidateDeparture(ctx context.Context, action automation.Action, o automation.Observation) error {
	if validator, ok := s[action.Skill].(automation.DepartureValidator); ok {
		return validator.ValidateDeparture(ctx, action, o)
	}
	return nil
}
