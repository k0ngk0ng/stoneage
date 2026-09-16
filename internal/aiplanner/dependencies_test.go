package aiplanner

import (
	"context"
	"errors"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

func TestBuildTaskAddsDirectDependencyCompletionAsEntryGuard(t *testing.T) {
	knowledge, root := verifiedFixture(t)
	dependency := dependencyTask(root, "prepare", nil)
	dependency.Success = []aiknowledge.SuccessCondition{dependencySuccess(root, "prepare-done")}
	root.Dependencies = []string{dependency.ID}
	knowledge.TaskDefinitions = []aiknowledge.TaskDefinition{root, dependency}

	plan, err := New(knowledge).BuildTask(context.Background(), root.ID, TaskOptions{CharacterID: "acct:0"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasAutomationCondition(plan.Preconditions, automation.Condition{Kind: "flag_set", ID: "prepare-done"}) {
		t.Fatalf("direct dependency completion was not added to entry guards: %#v", plan.Preconditions)
	}
	if len(plan.Steps) != len(root.Steps) {
		t.Fatalf("dependency steps were merged into root plan: got %d want %d", len(plan.Steps), len(root.Steps))
	}
}

func TestBuildTaskDoesNotFlattenConsumedAncestorCompletion(t *testing.T) {
	knowledge, root := verifiedFixture(t)
	ancestor := dependencyTask(root, "ancestor", nil)
	ancestor.Success = []aiknowledge.SuccessCondition{{
		MachineCondition: aiknowledge.MachineCondition{Kind: "item_count", ID: "item:quest", Value: 1},
		Description:      "ancestor supplied the quest item",
		Evidence:         cloneSourceRefs(root.Success[0].Evidence),
	}}
	middle := dependencyTask(root, "middle", []string{ancestor.ID})
	middle.Success = []aiknowledge.SuccessCondition{dependencySuccess(root, "middle-done")}
	root.Dependencies = []string{middle.ID}
	knowledge.TaskDefinitions = []aiknowledge.TaskDefinition{root, middle, ancestor}

	plan, err := New(knowledge).BuildTask(context.Background(), root.ID, TaskOptions{CharacterID: "acct:0"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasAutomationCondition(plan.Preconditions, automation.Condition{Kind: "flag_set", ID: "middle-done"}) {
		t.Fatalf("direct dependency completion was not retained: %#v", plan.Preconditions)
	}
	if hasAutomationCondition(plan.Preconditions, automation.Condition{Kind: "item_count", ID: "item:quest", Value: 1}) {
		t.Fatalf("consumed ancestor completion was flattened into root guards: %#v", plan.Preconditions)
	}
}

func TestTaskOrderAndBuildTaskHandleSharedDependenciesOnce(t *testing.T) {
	knowledge, root := verifiedFixture(t)
	shared := dependencyTask(root, "shared", nil)
	shared.Success = []aiknowledge.SuccessCondition{dependencySuccess(root, "shared-done")}
	left := dependencyTask(root, "left", []string{shared.ID})
	left.Success = []aiknowledge.SuccessCondition{dependencySuccess(root, "left-done")}
	right := dependencyTask(root, "right", []string{shared.ID})
	right.Success = []aiknowledge.SuccessCondition{dependencySuccess(root, "right-done")}
	root.Dependencies = []string{left.ID, right.ID}
	knowledge.TaskDefinitions = []aiknowledge.TaskDefinition{root, left, right, shared}

	order, err := knowledge.TaskOrder(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	counts := make(map[string]int, len(order))
	for _, task := range order {
		counts[task.ID]++
	}
	if counts[shared.ID] != 1 || len(order) != 4 {
		t.Fatalf("shared dependency was not visited once: order=%v counts=%v", taskIDs(order), counts)
	}
	if indexOfTask(order, shared.ID) > indexOfTask(order, left.ID) || indexOfTask(order, shared.ID) > indexOfTask(order, right.ID) || indexOfTask(order, left.ID) > indexOfTask(order, root.ID) || indexOfTask(order, right.ID) > indexOfTask(order, root.ID) {
		t.Fatalf("dependency order does not precede dependants: %v", taskIDs(order))
	}

	plan, err := New(knowledge).BuildTask(context.Background(), root.ID, TaskOptions{CharacterID: "acct:0"})
	if err != nil {
		t.Fatal(err)
	}
	if countAutomationCondition(plan.Preconditions, automation.Condition{Kind: "flag_set", ID: "shared-done"}) != 0 {
		t.Fatalf("transitive shared completion leaked into root guards: %#v", plan.Preconditions)
	}
	for _, id := range []string{"left-done", "right-done"} {
		if countAutomationCondition(plan.Preconditions, automation.Condition{Kind: "flag_set", ID: id}) != 1 {
			t.Fatalf("direct dependency %q was not represented exactly once: %#v", id, plan.Preconditions)
		}
	}
}

func TestBuildTaskRejectsMissingAndCyclicDependencies(t *testing.T) {
	tests := []struct {
		name string
		edit func(*aiknowledge.TaskDefinition, *aiknowledge.TaskDefinition)
	}{
		{
			name: "missing",
			edit: func(root, _ *aiknowledge.TaskDefinition) { root.Dependencies = []string{"missing-task"} },
		},
		{
			name: "cycle",
			edit: func(root, dependency *aiknowledge.TaskDefinition) {
				root.Dependencies = []string{dependency.ID}
				dependency.Dependencies = []string{root.ID}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			knowledge, root := verifiedFixture(t)
			dependency := dependencyTask(root, "dependency", nil)
			tc.edit(&root, &dependency)
			knowledge.TaskDefinitions = []aiknowledge.TaskDefinition{root, dependency}
			_, err := New(knowledge).BuildTask(context.Background(), root.ID, TaskOptions{CharacterID: "acct:0"})
			if !errors.Is(err, ErrTaskDependency) {
				t.Fatalf("dependency error %v does not contain %v", err, ErrTaskDependency)
			}
		})
	}
}

func TestBuildTaskRejectsUnverifiedDependency(t *testing.T) {
	knowledge, root := verifiedFixture(t)
	dependency := dependencyTask(root, "unverified-dependency", nil)
	dependency.Status = aiknowledge.TaskUnverified
	root.Dependencies = []string{dependency.ID}
	knowledge.TaskDefinitions = []aiknowledge.TaskDefinition{root, dependency}

	_, err := New(knowledge).BuildTask(context.Background(), root.ID, TaskOptions{CharacterID: "acct:0"})
	if !errors.Is(err, ErrTaskDependency) || !errors.Is(err, ErrTaskUnverified) {
		t.Fatalf("unverified dependency error %v does not preserve dependency and status causes", err)
	}
}

func TestBuildTaskKeepsRootBudgetAndStepsIndependentFromDependencies(t *testing.T) {
	knowledge, root := verifiedFixture(t)
	dependency := dependencyTask(root, "expensive-dependency", nil)
	dependency.Steps = append([]aiknowledge.TaskStep(nil), root.Steps...)
	dependency.Steps[0].MaximumCost = 7000
	dependency.Budget.GoldMin = 12000
	dependency.Budget.GoldExpected = 12000
	dependency.Budget.GoldMax = 12000
	dependency.Budget.Evidence = cloneSourceRefs(root.Budget.Evidence)
	dependency.Success = []aiknowledge.SuccessCondition{dependencySuccess(root, "dependency-done")}
	root.Dependencies = []string{dependency.ID}
	knowledge.TaskDefinitions = []aiknowledge.TaskDefinition{root, dependency}

	plan, err := New(knowledge).BuildTask(context.Background(), root.ID, TaskOptions{CharacterID: "acct:0"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Budget.MaximumSpend != root.Budget.GoldMax || plan.Budget.ExpectedHigh != root.Budget.GoldMax {
		t.Fatalf("dependency budget was merged into root plan: %#v", plan.Budget)
	}
	if len(plan.Steps) != len(root.Steps) {
		t.Fatalf("dependency steps were merged into root plan: got %d want %d", len(plan.Steps), len(root.Steps))
	}
}

func dependencyTask(base aiknowledge.TaskDefinition, id string, dependencies []string) aiknowledge.TaskDefinition {
	task := base
	task.ID = id
	task.Name = id
	task.Dependencies = append([]string(nil), dependencies...)
	task.Preconditions = append([]aiknowledge.Precondition(nil), base.Preconditions...)
	task.Steps = append([]aiknowledge.TaskStep(nil), base.Steps...)
	task.Success = append([]aiknowledge.SuccessCondition(nil), base.Success...)
	task.Evidence = append([]aiknowledge.Evidence(nil), base.Evidence...)
	return task
}

func dependencySuccess(base aiknowledge.TaskDefinition, id string) aiknowledge.SuccessCondition {
	return aiknowledge.SuccessCondition{
		MachineCondition: aiknowledge.MachineCondition{Kind: "flag_set", ID: id},
		Description:      "dependency completed",
		Evidence:         cloneSourceRefs(base.Success[0].Evidence),
	}
}

func cloneSourceRefs(refs []aiknowledge.SourceRef) []aiknowledge.SourceRef {
	return append([]aiknowledge.SourceRef(nil), refs...)
}

func hasAutomationCondition(conditions []automation.Condition, want automation.Condition) bool {
	return countAutomationCondition(conditions, want) > 0
}

func countAutomationCondition(conditions []automation.Condition, want automation.Condition) int {
	count := 0
	for _, condition := range conditions {
		if condition == want {
			count++
		}
	}
	return count
}

func taskIDs(tasks []aiknowledge.TaskDefinition) []string {
	ids := make([]string, 0, len(tasks))
	for _, task := range tasks {
		ids = append(ids, task.ID)
	}
	return ids
}

func indexOfTask(tasks []aiknowledge.TaskDefinition, id string) int {
	for index, task := range tasks {
		if task.ID == id {
			return index
		}
	}
	return len(tasks)
}
