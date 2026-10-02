package arenaagent

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
	"github.com/k0ngk0ng/stoneage/internal/battletrain"
)

func buildSearchCommand(ctx context.Context, args []string, out io.Writer) (resultErr error) {
	f := flag.NewFlagSet("sactl ai build-search", flag.ContinueOnError)
	f.SetOutput(out)
	c := battletrain.DefaultBuildSearchConfig()
	environment := trainingEnvironmentFlag(f)
	directory := f.String("data-dir", "", "search states, fixed policies, native trajectories and final report")
	resume := f.Bool("resume", false, "continue saved search with pinned settings/policies")
	model := f.String("model", "", "frozen commander model used for all proposed builds")
	policy := f.String("policy", "", "basic, focus, guard-break, defensive, sustain, control or independent-control (default basic); exclusive with --model")
	var rules, models pathsFlag
	f.Var(&rules, "opponent", "basic, focus, guard-break, defensive, sustain, control or independent-control (repeatable; default original five)")
	f.Var(&models, "opponent-model", "frozen opponent model (repeatable)")
	f.Int64Var(&c.Seed, "seed", c.Seed, "search, split and combat seed")
	f.IntVar(&c.Mode, "mode", c.Mode, "team size 1..5; each member keeps a separate budget")
	f.IntVar(&c.Points, "points", c.Points, "integer points per player, minimum 4")
	f.IntVar(&c.PetPoints, "pet-points", c.PetPoints, "integer points per pet; 0 disables pets")
	f.IntVar(&c.HealingMagic, "healing-magic", c.HealingMagic, "fixed healing armour: 0 none, 10 single target, 20 side; supplies 100 MP")
	f.IntVar(&c.HealingItems, "healing-items", c.HealingItems, "single-use small meats (template 1234) per character, 0..15")
	f.IntVar(&c.ReservePets, "reserve-pets", c.ReservePets, "extra pets per member, 0..2; each retains the active pet point budget")
	petSkillFlags(f, &c.PetSkillMask)
	f.IntVar(&c.Level, "level", c.Level, "controlled native scenario level")
	f.IntVar(&c.MaxTurns, "max-turns", c.MaxTurns, "collection cutoff; any cutoff stops search without advice")
	f.IntVar(&c.InitialCandidates, "initial-candidates", c.InitialCandidates, "native initial builds including balanced/extreme/random, at least 5")
	f.IntVar(&c.Generations, "generations", c.Generations, "integer mutation/scorer/native refinement rounds")
	f.IntVar(&c.Proposals, "proposals", c.Proposals, "legal candidate rosters screened per generation")
	f.IntVar(&c.NativeCandidates, "native-candidates", c.NativeCandidates, "screened proposals actually tested per generation")
	f.IntVar(&c.Finalists, "finalists", c.Finalists, "native validation candidates, always including balanced")
	f.IntVar(&c.FitEpochs, "fit-epochs", c.FitEpochs, "two-layer build scorer fitting epochs")
	f.IntVar(&c.SearchGroups, "search-groups", c.SearchGroups, "opponent rosters used for search scores")
	f.IntVar(&c.ValidationGroups, "validation-groups", c.ValidationGroups, "unseen opponent rosters for finalist selection")
	f.IntVar(&c.TestGroups, "test-groups", c.TestGroups, "unseen final-test rosters after selection; each group is four games per opponent")
	if e := f.Parse(args); e != nil {
		if e == flag.ErrHelp {
			return nil
		}
		return e
	}
	if f.NArg() != 0 || *environment == "" || *directory == "" {
		return fmt.Errorf("build-search requires --environment and --data-dir, with no positional arguments")
	}
	provided := map[string]bool{}
	f.Visit(func(item *flag.Flag) { provided[item.Name] = true })
	var controller battletrain.Opponent
	var opponents []battletrain.Opponent
	if *resume {
		for name := range provided {
			if name != "environment" && name != "data-dir" && name != "resume" {
				return fmt.Errorf("--%s cannot change saved build search settings/policies", name)
			}
		}
		state, e := battletrain.LoadBuildSearch(*directory)
		if e != nil {
			return e
		}
		c = state.Spec.Config
		controller, e = battletrain.LoadBuildSearchPolicy(*directory, state.Spec.Controller)
		if e != nil {
			return e
		}
		for _, id := range state.Spec.Opponents {
			o, e := battletrain.LoadBuildSearchPolicy(*directory, id)
			if e != nil {
				return e
			}
			opponents = append(opponents, o)
		}
	} else {
		if *model != "" && *policy != "" {
			return fmt.Errorf("choose --model or --policy, not both")
		}
		controller = battletrain.Opponent{Name: "controller", Rule: *policy}
		if *model != "" {
			a, e := battlepolicy.LoadArtifact(*model)
			if e != nil {
				return e
			}
			controller.Model = &a
		} else if controller.Rule == "" {
			controller.Rule = "basic"
		}
		if len(rules) == 0 && len(models) == 0 {
			rules = pathsFlag(battlepolicy.DefaultRuleNames())
		}
		for _, rule := range rules {
			opponents = append(opponents, battletrain.Opponent{Name: rule, Rule: rule})
		}
		for i, path := range models {
			a, e := battlepolicy.LoadArtifact(path)
			if e != nil {
				return e
			}
			opponents = append(opponents, battletrain.Opponent{Name: fmt.Sprintf("model-%d:%s", i, a.WeightsDigest), Model: &a})
		}
	}
	if e := c.Validate(); e != nil {
		return e
	}
	command, e := loadTrainingEnvironment(*environment)
	if e != nil {
		return e
	}
	if e = os.MkdirAll(*directory, 0700); e != nil {
		return e
	}
	log, e := os.OpenFile(filepath.Join(*directory, "engine.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if e != nil {
		return e
	}
	defer log.Close()
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()
	engine, e := battleenv.Start(ctx, command, log)
	if e != nil {
		return e
	}
	defer closeNativeEngine(engine, &resultErr)
	state, e := battletrain.RunBuildSearch(ctx, battletrain.BuildSearchOptions{Directory: *directory, Engine: engine, Controller: controller, Opponents: opponents, Config: c, Resume: *resume, Progress: func(stage string, games int) error {
		_, e := out.Write(append(enc(Object{"event": "build_search_progress", "stage": stage, "completed_games": games}), '\n'))
		return e
	}})
	if errors.Is(e, context.Canceled) {
		_, e = out.Write(append(enc(Object{"event": "build_search_interrupted", "data_dir": *directory, "resume": true}), '\n'))
		return e
	}
	if e != nil {
		return e
	}
	if e = engine.Close(); e != nil {
		return e
	}
	path, e := battletrain.SaveBuildSearchReport(*directory, state)
	if e != nil {
		return e
	}
	var selected battletrain.Roster
	for _, test := range state.Test {
		if test.Roster.ID() == state.Selected {
			selected = test.Roster
		}
	}
	_, e = out.Write(append(enc(Object{"event": "build_search_complete", "report": path, "selected": selected, "status": "candidate-advice", "arena_certified": false, "recorded_selection_artifacts": state.Spec.RecordedSelectionArtifacts}), '\n'))
	return e
}
