package main

import (
	"context"
	"errors"

	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

// previewQuest uses the same installed skill contracts and evaluator as Start.
// Observation is the already-read authoritative snapshot. In particular, do
// not call GameBackend.Observe here: its own-state refresher may send a game
// packet. Constructing controllers and validating their contracts neither
// starts a task nor takes over the current game lease.
func (executor *AutomationExecutor) previewQuest(ctx context.Context, session *AutomationSession, request AutomationStartRequest, binding aimcp.Binding, plan automation.Plan, observation automation.Observation) (automation.Preflight, error) {
	handle, err := executor.buildWebAutomationHandle(ctx, session, request, binding)
	if err != nil {
		return automation.Preflight{}, err
	}
	defer handle.closeBackend()
	if handle.questTasks == nil || handle.questTasks.Engine == nil {
		return automation.Preflight{}, errors.New("automation quest preflight is unavailable")
	}
	validator, ok := handle.questTasks.Engine.Game.(automation.SkillValidator)
	if !ok {
		return automation.Preflight{}, errors.New("automation quest validation is unavailable")
	}
	return automation.EvaluatePreflight(ctx, plan, observation, validator)
}
