package arenaagent

import (
	"context"
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

func experimentCommand(ctx context.Context, args []string, out io.Writer) (resultErr error) {
	f := flag.NewFlagSet("sactl ai experiment", flag.ContinueOnError)
	f.SetOutput(out)
	environment := trainingEnvironmentFlag(f)
	output := f.String("output", "", "new immutable train/validation/test manifest JSON")
	poolPath := f.String("build-pool", "", "frozen build-pool JSON; use its rosters for training and new rosters for held-out sets")
	poolTrain := f.Int("pool-train-groups", 0, "opt-in mixed training: evenly interleave pool roster pairs with fresh families within explicit --train-groups total; existing manifests retain their order")
	fromModel := f.String("from-model", "", "parent weights for training, including a new --mode; preserve ancestry, no inherited mode certification")
	c := battletrain.DefaultEvaluationConfig()
	f.Int64Var(&c.Seed, "seed", c.Seed, "configuration generation and split seed")
	f.IntVar(&c.Mode, "mode", c.Mode, "team size 1..5")
	f.IntVar(&c.Points, "points", c.Points, "equal points per player")
	f.IntVar(&c.PetPoints, "pet-points", c.PetPoints, "equal points per pet; 0 disables pets")
	f.IntVar(&c.HealingMagic, "healing-magic", c.HealingMagic, "fixed healing armour: 0 none, 10 single target, 20 side; supplies 100 MP")
	f.IntVar(&c.HealingItems, "healing-items", c.HealingItems, "single-use small meats (template 1234) per character, 0..15")
	f.IntVar(&c.ReservePets, "reserve-pets", c.ReservePets, "extra pets per member, 0..2; each retains the active pet point budget")
	petSkillFlags(f, &c.PetSkillMask)
	f.IntVar(&c.Level, "level", c.Level, "controlled scenario level")
	f.IntVar(&c.MaxTurns, "max-turns", c.MaxTurns, "fixed collection cutoff")
	train := f.Int("train-groups", 1024, "distinct training configuration families")
	validation := f.Int("validation-groups", 256, "validation families; eight games crossing seeds, roster ownership and arena side")
	test := f.Int("test-groups", 256, "final-test families; eight games per family/opponent")
	if e := f.Parse(args); e != nil {
		if e == flag.ErrHelp {
			return nil
		}
		return e
	}
	if f.NArg() != 0 || *environment == "" || *output == "" {
		return fmt.Errorf("experiment requires --environment and --output, with no positional arguments")
	}
	provided := map[string]bool{}
	f.Visit(func(item *flag.Flag) { provided[item.Name] = true })
	if provided["pool-train-groups"] && (*poolPath == "" || !provided["train-groups"] || *poolTrain < 1 || *poolTrain >= *train) {
		return fmt.Errorf("--pool-train-groups requires --build-pool and explicit --train-groups with 0 < pool groups < total training groups")
	}
	var pool *battletrain.BuildPool
	var parent *battlepolicy.Artifact
	if *poolPath != "" {
		for _, name := range []string{"mode", "points", "pet-points", "reserve-pets", "pet-skills", "healing-items", "healing-magic", "level"} {
			if provided[name] {
				return fmt.Errorf("--%s is fixed by --build-pool", name)
			}
		}
		p, e := battletrain.LoadBuildPool(*poolPath)
		if e != nil {
			return e
		}
		if provided["pool-train-groups"] && *poolTrain > len(p.Rosters)*(len(p.Rosters)+1)/2 {
			return fmt.Errorf("--pool-train-groups exceeds the frozen pool's distinct roster pairs")
		}
		pool = &p
		c.Mode, c.Points, c.PetPoints, c.Level = p.Config.Mode, p.Config.Points, p.Config.PetPoints, p.Config.Level
		c.HealingMagic = p.Config.HealingMagic
		c.HealingItems = p.Config.HealingItems
		c.ReservePets = p.Config.ReservePets
		c.PetSkillMask = p.Config.PetSkillMask
		if !provided["max-turns"] {
			c.MaxTurns = p.Config.MaxTurns
		}
		if !provided["train-groups"] {
			*train = len(p.Rosters) * (len(p.Rosters) + 1) / 2
		}
	}
	if *fromModel != "" {
		a, e := battlepolicy.LoadArtifact(*fromModel)
		if e != nil {
			return e
		}
		parent = &a
	}
	if _, e := os.Stat(*output); e == nil {
		return fmt.Errorf("experiment exists; choose a new --output")
	} else if !os.IsNotExist(e) {
		return e
	}
	command, e := loadTrainingEnvironment(*environment)
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(*output), 0700); e != nil {
		return e
	}
	log, e := os.OpenFile(*output+".engine.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
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
	var x battletrain.Experiment
	if provided["pool-train-groups"] {
		x, e = battletrain.NewExperimentWithPoolMix(ctx, engine.Metadata(), c, [3]int{*train, *validation, *test}, pool, parent, *poolTrain)
	} else {
		x, e = battletrain.NewExperimentFromSources(ctx, engine.Metadata(), c, [3]int{*train, *validation, *test}, pool, parent)
	}
	if e != nil {
		return e
	}
	if e = engine.Close(); e != nil {
		return e
	}
	id, e := battletrain.SaveExperiment(*output, x)
	if e != nil {
		return e
	}
	event := Object{"event": "experiment_created", "experiment": id, "manifest": *output, "train_groups": *train, "validation_groups": *validation, "test_groups": *test, "recorded_selection_artifacts": x.RecordedSelectionArtifacts}
	if x.PoolTrainingGroups != nil {
		event["pool_train_groups"] = *x.PoolTrainingGroups
		event["fresh_train_groups"] = *train - *x.PoolTrainingGroups
	}
	_, e = out.Write(append(enc(event), '\n'))
	return e
}
