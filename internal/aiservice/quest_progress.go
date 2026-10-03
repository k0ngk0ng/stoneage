package aiservice

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/k0ngk0ng/stoneage/internal/automation"
)

type PersistentSkill interface {
	ExecuteStep(context.Context, automation.Action, automation.StepExecution) error
	CanResumeStep(context.Context, automation.Action, automation.StepProgress) error
}

func (g *AutomationGame) ExecuteStep(ctx context.Context, a automation.Action, scope automation.StepExecution) error {
	if g.Backend == nil {
		return errors.New("task backend missing")
	}
	if err := g.Backend.check(g.Backend.Binding); err != nil {
		return err
	}
	if skills, ok := g.Skills.(SkillSet); ok {
		if skill, ok := skills[a.Skill].(PersistentSkill); ok {
			return skill.ExecuteStep(ctx, a, scope)
		}
	}
	if a.Skill == "npc.window" || a.Skill == "npc.dialogue" {
		args, err := decodeNPCWindow(a.Arguments)
		if err != nil {
			return err
		}
		if args.PetCollection != "" {
			if len(args.PetIDs) > 0 {
				return npcInvalid("pet collection and explicit pet IDs cannot be combined")
			}
			result, ok := scope.Results[args.PetCollection]
			if !ok || !result.Confirmed {
				return npcInvalid("pet collection has not been confirmed in this task")
			}
			if err := validatePetDeliveryIDs(result.PetIDs); err != nil {
				return err
			}
			args.PetIDs = append([]string(nil), result.PetIDs...)
			args.PetCollection = ""
			a.Arguments, err = json.Marshal(args)
			if err != nil {
				return err
			}
		}
	}
	return g.Execute(ctx, a)
}

func (g *AutomationGame) CanResumeStep(ctx context.Context, a automation.Action, p automation.StepProgress) error {
	if skills, ok := g.Skills.(SkillSet); ok {
		if skill, ok := skills[a.Skill].(PersistentSkill); ok {
			return skill.CanResumeStep(ctx, a, p)
		}
	}
	return errors.New("this skill cannot resume an unfinished step")
}
