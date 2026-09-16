package aiplanner

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

func TestChainCompilesSharedPrerequisitesOnceAndBudgetsWholeRun(t *testing.T) {
	k, root := verifiedFixture(t)
	shared := dependencyTask(root, "shared", nil)
	left := dependencyTask(root, "left", []string{"shared"})
	right := dependencyTask(root, "right", []string{"shared"})
	shared.Success = []aiknowledge.SuccessCondition{dependencySuccess(root, "shared-done")}
	left.Success = []aiknowledge.SuccessCondition{dependencySuccess(root, "left-done")}
	right.Success = []aiknowledge.SuccessCondition{dependencySuccess(root, "right-done")}
	root.Dependencies = []string{"left", "right"}
	k.TaskDefinitions = []aiknowledge.TaskDefinition{root, left, right, shared}
	plan, err := New(k).BuildChain(context.Background(), root.ID, TaskOptions{CharacterID: "acct:0", ReserveGold: 17, MaximumDeaths: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Stages) != 4 || len(plan.Steps) != 8 || plan.Budget.MaximumSpend != 20000 || plan.Budget.Minimum != 20000 || plan.Budget.Reserve != 17 || plan.MaximumSeconds != 200 || plan.MaximumDeaths != 2 {
		t.Fatalf("chain not aggregated: %+v", plan)
	}
	if len(plan.Preconditions) != 0 {
		t.Fatal("later requirements applied to departure")
	}
	for i, s := range plan.Stages {
		if s.StartStep != i*2 || s.EndStep != (i+1)*2 {
			t.Fatalf("invalid stage: %+v", s)
		}
	}
	guards := plan.Stages[3].Preconditions
	if hasAutomationCondition(guards, automation.Condition{Kind: "flag_set", ID: "shared-done"}) {
		t.Fatal("transitive completion leaked into root")
	}
	for _, id := range []string{"left-done", "right-done"} {
		if !hasAutomationCondition(guards, automation.Condition{Kind: "flag_set", ID: id}) {
			t.Fatal("missing direct prerequisite")
		}
	}
	single, err := New(k).BuildTask(context.Background(), root.ID, TaskOptions{CharacterID: "acct:0"})
	if err != nil || len(single.Stages) != 0 || single.Budget.MaximumSpend != 5000 {
		t.Fatalf("single mode changed: %+v %v", single, err)
	}
	explicit, err := New(k).BuildChain(context.Background(), root.ID, TaskOptions{CharacterID: "acct:0", MaximumSeconds: 77})
	if err != nil || explicit.MaximumSeconds != 77 {
		t.Fatalf("whole-chain time override: %v", err)
	}
	if root.Steps[0].ID == plan.Steps[0].ID {
		t.Fatal("stage step IDs were not namespaced")
	}
}

func TestChainRefusesUnreviewedStageLocalCompletionAndOverflow(t *testing.T) {
	for _, kind := range []string{"preparation", "local-completion", "overflow", "stale-root"} {
		t.Run(kind, func(t *testing.T) {
			k, root := verifiedFixture(t)
			dep := dependencyTask(root, "dependency", nil)
			dep.Success = []aiknowledge.SuccessCondition{dependencySuccess(root, "dependency-done")}
			root.Dependencies = []string{dep.ID}
			opt := TaskOptions{CharacterID: "acct:0"}
			switch kind {
			case "preparation":
				dep.PreparationReviewed = false
			case "local-completion":
				root.Success[0].MachineCondition = aiknowledge.MachineCondition{Kind: "window_submitted", ID: "trainer", Value: 1}
			case "overflow":
				dep.Budget.GoldMax = math.MaxInt64
			case "stale-root":
				opt.EvidenceFingerprint = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
			}
			k.TaskDefinitions = []aiknowledge.TaskDefinition{root, dep}
			_, err := New(k).BuildChain(context.Background(), root.ID, opt)
			if err == nil {
				t.Fatal("invalid chain accepted")
			}
			if kind == "overflow" && !errors.Is(err, ErrInvalidCost) {
				t.Fatalf("unexpected overflow error: %v", err)
			}
		})
	}
}

func TestChainBindsPetForEveryStage(t *testing.T) {
	k, root := verifiedFixture(t)
	root.Preconditions = append(root.Preconditions, aiknowledge.Precondition{MachineCondition: aiknowledge.MachineCondition{Kind: "pet_level", ID: "$selected_pet", Value: 35}})
	dep := dependencyTask(root, "dependency", nil)
	dep.Success = []aiknowledge.SuccessCondition{dependencySuccess(root, "dependency-done")}
	root.Dependencies = []string{dep.ID}
	k.TaskDefinitions = []aiknowledge.TaskDefinition{root, dep}
	plan, err := New(k).BuildChain(context.Background(), root.ID, TaskOptions{CharacterID: "acct:0", SelectedPetID: "owned-pet"})
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range plan.Stages {
		if !hasAutomationCondition(stage.Preconditions, automation.Condition{Kind: "pet_level", ID: "owned-pet", Value: 35}) {
			t.Fatalf("pet binding missing: %+v", stage)
		}
	}
}

func TestChainCompletedSiblingCannotWaiveDirectItemPrerequisite(t *testing.T) {
	k, root := verifiedFixture(t)
	ancestor := dependencyTask(root, "ancestor", nil)
	ancestor.Success = []aiknowledge.SuccessCondition{{MachineCondition: aiknowledge.MachineCondition{Kind: "item_count", ID: "item:ticket", Value: 1}}}
	left := dependencyTask(root, "left", []string{ancestor.ID})
	left.Success = []aiknowledge.SuccessCondition{dependencySuccess(root, "left-done")}
	right := dependencyTask(root, "right", []string{ancestor.ID})
	right.Success = []aiknowledge.SuccessCondition{dependencySuccess(root, "right-done")}
	root.Dependencies = []string{left.ID, right.ID}
	k.TaskDefinitions = []aiknowledge.TaskDefinition{root, ancestor, left, right}
	plan, err := New(k).BuildChain(context.Background(), root.ID, TaskOptions{CharacterID: "acct:0"})
	if err != nil {
		t.Fatal(err)
	}
	observed := automation.Observation{Connected: true, Ready: true, CharacterID: "acct:0", Character: automation.Entity{HP: 100}, Flags: map[string]bool{"left-done": true}, Inventory: map[string]int{"item:ticket": 0}}
	guards := plan.EntryPreconditions(observed)
	ticket := automation.Condition{Kind: "item_count", ID: "item:ticket", Value: 1}
	if !hasAutomationCondition(guards, ticket) {
		t.Fatalf("consumed prerequisite of an incomplete sibling was waived: %+v", guards)
	}
	if ticket.Match(observed) {
		t.Fatal("missing ticket was invented")
	}
}
