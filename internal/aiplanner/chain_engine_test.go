package aiplanner

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

type chainIntegrationGame struct {
	observation automation.Observation
	submissions []string
	hold        bool
}

func (g *chainIntegrationGame) Observe(context.Context) (automation.Observation, error) {
	return g.observation, nil
}
func (g *chainIntegrationGame) Execute(_ context.Context, a automation.Action) error {
	var args struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(a.Arguments, &args); err != nil {
		return err
	}
	g.submissions = append(g.submissions, args.ID)
	if !g.hold {
		g.observation.Flags[args.ID+"-done"] = true
	}
	g.observation.Gold -= a.MaximumCost
	g.observation.Spent += a.MaximumCost
	g.observation.Revision++
	return nil
}

func TestCompiledChainRunsThroughDurableStageRestart(t *testing.T) {
	ctx := context.Background()
	knowledge, base := verifiedFixture(t)
	tasks := make([]aiknowledge.TaskDefinition, 0, 2)
	for _, id := range []string{"prepare", "target"} {
		task := dependencyTask(base, id, nil)
		task.Success = []aiknowledge.SuccessCondition{dependencySuccess(base, id+"-done")}
		task.Steps = []aiknowledge.TaskStep{{ID: "complete", Action: aiknowledge.TaskAction{Skill: "test.complete", Arguments: json.RawMessage(`{"id":"` + id + `"}`)}, TimeoutSeconds: 20, CostKnown: true, MaximumCost: 10, SuccessConditions: []aiknowledge.MachineCondition{{Kind: "flag_set", ID: id + "-done"}}}}
		task.Budget.GoldMin, task.Budget.GoldExpected, task.Budget.GoldMax = 10, 10, 10
		if id == "target" {
			task.Dependencies = []string{"prepare"}
		}
		tasks = append(tasks, task)
	}
	knowledge.TaskDefinitions = tasks
	plan, err := New(knowledge).BuildChain(ctx, "target", TaskOptions{CharacterID: "acct:0", ReserveGold: 17})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "chain.db")
	store, err := automation.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	game := &chainIntegrationGame{observation: automation.Observation{Connected: true, Ready: true, Revision: 1, CharacterID: "acct:0", Character: automation.Entity{HP: 100}, Gold: 100, SpendingKnown: true, Flags: map[string]bool{}}}
	engine := &automation.Engine{Game: game, Store: store}
	checkpoint, err := engine.Start(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	restarted := false
	for ticks := 0; ticks < 20 && checkpoint.Status == automation.Running; ticks++ {
		checkpoint, err = engine.Tick(ctx, plan.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !restarted && checkpoint.Stage == 1 {
			if checkpoint.ReservedSpend != 10 {
				t.Fatalf("spent lost before restart: %+v", checkpoint)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = automation.OpenStore(path)
			if err != nil {
				t.Fatal(err)
			}
			engine = &automation.Engine{Game: game, Store: store}
			restarted = true
		}
	}
	if !restarted || checkpoint.Status != automation.Completed || checkpoint.ReservedSpend != 20 || len(game.submissions) != 2 || game.submissions[0] != "prepare" || game.submissions[1] != "target" {
		t.Fatalf("chain/restart mismatch: checkpoint=%+v actions=%v restarted=%v", checkpoint, game.submissions, restarted)
	}
}

func TestCompiledChainPendingSubmissionKeepsWholeRunLimits(t *testing.T) {
	for _, kind := range []string{"time", "death", "ledger"} {
		t.Run(kind, func(t *testing.T) {
			k, root := verifiedFixture(t)
			dep := dependencyTask(root, "dependency", nil)
			dep.Success = []aiknowledge.SuccessCondition{dependencySuccess(root, "dependency-done")}
			root.Dependencies = []string{dep.ID}
			k.TaskDefinitions = []aiknowledge.TaskDefinition{root, dep}
			plan, err := New(k).BuildChain(context.Background(), root.ID, TaskOptions{CharacterID: "acct:0", MaximumSeconds: 1})
			if err != nil {
				t.Fatal(err)
			}
			store, err := automation.OpenStore(filepath.Join(t.TempDir(), "limits.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			now := time.Now()
			game := &chainIntegrationGame{hold: true, observation: automation.Observation{Connected: true, Ready: true, CharacterID: "acct:0", Character: automation.Entity{HP: 100}, Gold: 20000, SpendingKnown: true, Spent: 10, Flags: map[string]bool{}}}
			engine := &automation.Engine{Store: store, Game: game, Now: func() time.Time { return now }}
			if _, err := engine.Start(context.Background(), plan); err != nil {
				t.Fatal(err)
			}
			before, err := engine.Tick(context.Background(), plan.ID)
			if err != nil || before.Phase != "submitted" {
				t.Fatalf("initial submission: %+v %v", before, err)
			}
			switch kind {
			case "time":
				now = now.Add(2 * time.Second)
			case "death":
				game.observation.Dead = true
				game.observation.Flags["dependency-done"] = true
			case "ledger":
				game.observation.Spent = 0
			}
			after, err := engine.Tick(context.Background(), plan.ID)
			if err != nil || after.Status != automation.Paused || after.Stage != 0 || len(game.submissions) != 1 {
				t.Fatalf("%s fence missed: %+v %v", kind, after, err)
			}
			if kind == "death" && after.Deaths != 1 {
				t.Fatal("death at stage completion was lost")
			}
		})
	}
}

func TestCompiledChainResumeAdvancesConfirmedStageAndRechecksNextEntry(t *testing.T) {
	for _, ready := range []bool{false, true} {
		k, root := verifiedFixture(t)
		dep := dependencyTask(root, "dependency", nil)
		dep.Success = []aiknowledge.SuccessCondition{dependencySuccess(root, "dependency-done")}
		root.Dependencies = []string{dep.ID}
		root.Preconditions = append(root.Preconditions, aiknowledge.Precondition{MachineCondition: aiknowledge.MachineCondition{Kind: "character_level", Value: 35}})
		k.TaskDefinitions = []aiknowledge.TaskDefinition{root, dep}
		plan, err := New(k).BuildChain(context.Background(), root.ID, TaskOptions{CharacterID: "acct:0"})
		if err != nil {
			t.Fatal(err)
		}
		store, err := automation.OpenStore(filepath.Join(t.TempDir(), "resume.db"))
		if err != nil {
			t.Fatal(err)
		}
		game := &chainIntegrationGame{hold: true, observation: automation.Observation{Connected: true, Ready: true, CharacterID: "acct:0", Character: automation.Entity{HP: 100, Level: 1}, Gold: 20000, Flags: map[string]bool{}}}
		engine := &automation.Engine{Store: store, Game: game}
		if _, err := engine.Start(context.Background(), plan); err != nil {
			t.Fatal(err)
		}
		if _, err := engine.Tick(context.Background(), plan.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := engine.Pause(context.Background(), plan.ID, "user"); err != nil {
			t.Fatal(err)
		}
		game.observation.Flags["dependency-done"] = true
		if ready {
			game.observation.Character.Level = 35
		}
		result, err := engine.Resume(context.Background(), plan.ID)
		if ready && (err != nil || result.Status != automation.Running) {
			t.Fatalf("confirmed stage did not resume: %+v %v", result, err)
		}
		if !ready && (err == nil || result.Status != automation.Paused) {
			t.Fatalf("next stage level ignored: %+v %v", result, err)
		}
		saved, loadErr := store.Load(context.Background(), plan.ID)
		if loadErr != nil || saved.Stage != 1 || saved.StageEntered || saved.Phase != "ready" || len(game.submissions) != 1 {
			t.Fatalf("reconciliation not persisted: %+v %v", saved, loadErr)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
