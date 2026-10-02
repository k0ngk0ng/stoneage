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

func evaluateCommand(ctx context.Context, args []string, out io.Writer) (resultErr error) {
	f := flag.NewFlagSet("sactl ai evaluate", flag.ContinueOnError)
	f.SetOutput(out)
	environment := trainingEnvironmentFlag(f)
	model := f.String("model", "", "candidate model JSON; validation also accepts the experiment's exact declared parent")
	output := f.String("output", "", "new report JSON; frozen models and raw trajectories saved in <output>.data")
	resume := f.Bool("resume", false, "verify and reuse completed games from interrupted <output>.data; requires the identical specification")
	experimentPath := f.String("experiment", "", "frozen experiment manifest; evaluates the complete declared split")
	mixedPath := f.String("mixed-experiment", "", "frozen mixed experiment; requires --mode, evaluates that complete mode split with shared model provenance")
	comparisonPath := f.String("validation-comparison", "", "explicit shared-validation declaration; requires --mode and exactly one --opponent-model; no final test")
	split := f.String("split", "validation", "experiment split: validation for model selection; test freezes the final candidate")
	c := battletrain.DefaultEvaluationConfig()
	f.Int64Var(&c.Seed, "seed", c.Seed, "independent evaluation scenario seed")
	f.IntVar(&c.Mode, "mode", c.Mode, "trained team size to evaluate")
	f.IntVar(&c.Points, "points", c.Points, "equal player allocation budget")
	f.IntVar(&c.PetPoints, "pet-points", c.PetPoints, "equal pet allocation budget; 0 disables pets")
	f.IntVar(&c.HealingMagic, "healing-magic", c.HealingMagic, "fixed healing armour: 0 none, 10 single target, 20 side; supplies 100 MP")
	f.IntVar(&c.HealingItems, "healing-items", c.HealingItems, "single-use small meats (template 1234) per character, 0..15")
	f.IntVar(&c.ReservePets, "reserve-pets", c.ReservePets, "extra pets per member, 0..2; each retains the active pet point budget")
	petSkillFlags(f, &c.PetSkillMask)
	f.IntVar(&c.Level, "level", c.Level, "scenario level")
	f.IntVar(&c.MaxTurns, "max-turns", c.MaxTurns, "collection cutoff; reported separately from draws")
	f.IntVar(&c.MatchesPerOpponent, "matches", c.MatchesPerOpponent, "games PER opponent, multiple of 8 (seeds, roster ownership and sides); old experiments retain four-game families")
	var databases, rules, models pathsFlag
	f.Var(&databases, "database", "legacy SQLite prediction evaluation, not native policy play (repeatable)")
	f.Var(&rules, "opponent", "basic, focus, guard-break, defensive, sustain, control or independent-control (repeatable; default original five)")
	f.Var(&models, "opponent-model", "frozen candidate/champion model file (repeatable)")
	if e := f.Parse(args); e != nil {
		if e == flag.ErrHelp {
			return nil
		}
		return e
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected evaluation positional arguments")
	}
	provided := map[string]bool{}
	f.Visit(func(item *flag.Flag) { provided[item.Name] = true })
	if provided["experiment"] && provided["mixed-experiment"] {
		return fmt.Errorf("--experiment and --mixed-experiment are mutually exclusive")
	}
	if *experimentPath == "" && *mixedPath == "" && *comparisonPath == "" && provided["split"] {
		return fmt.Errorf("--split requires --experiment or --mixed-experiment")
	}
	var comparison *battletrain.MixedValidationComparison
	if provided["validation-comparison"] {
		if *comparisonPath == "" || !provided["mode"] || *split != "validation" || len(models) != 1 || len(rules) != 0 {
			return fmt.Errorf("--validation-comparison requires --mode, exactly one --opponent-model and validation only")
		}
		for _, flag := range []string{"experiment", "mixed-experiment", "seed", "points", "pet-points", "reserve-pets", "pet-skills", "healing-items", "healing-magic", "level", "max-turns", "matches", "database"} {
			if provided[flag] {
				return fmt.Errorf("--%s cannot override --validation-comparison", flag)
			}
		}
		x, err := battletrain.LoadMixedValidationComparison(*comparisonPath)
		if err != nil {
			return err
		}
		comparison = &x
	}
	var experiment *battletrain.Experiment
	var mixed *battletrain.MixedExperiment
	if provided["mixed-experiment"] {
		if *mixedPath == "" || !provided["mode"] {
			return fmt.Errorf("--mixed-experiment requires a manifest and explicit --mode")
		}
		for _, name := range []string{"points", "pet-points", "reserve-pets", "pet-skills", "healing-items", "healing-magic", "level", "max-turns", "matches", "seed", "database"} {
			if provided[name] {
				return fmt.Errorf("--%s cannot override a frozen mixed experiment evaluation", name)
			}
		}
		if *split != "validation" && *split != "test" {
			return fmt.Errorf("--split must be validation or test")
		}
		x, e := battletrain.LoadMixedExperiment(*mixedPath)
		if e != nil {
			return e
		}
		mixed = &x
	}
	if *experimentPath != "" {
		for _, name := range []string{"mode", "points", "pet-points", "reserve-pets", "pet-skills", "healing-items", "healing-magic", "level", "max-turns", "matches", "seed", "database"} {
			if provided[name] {
				return fmt.Errorf("--%s cannot override a frozen experiment evaluation", name)
			}
		}
		if *split != "validation" && *split != "test" {
			return fmt.Errorf("--split must be validation or test")
		}
		x, e := battletrain.LoadExperiment(*experimentPath)
		if e != nil {
			return e
		}
		experiment = &x
	}
	if len(databases) > 0 {
		var invalid string
		f.Visit(func(item *flag.Flag) {
			if item.Name != "database" && item.Name != "model" {
				invalid = item.Name
			}
		})
		if invalid != "" {
			return fmt.Errorf("--%s cannot be combined with legacy prediction evaluation", invalid)
		}
		v, e := Evaluate(databases, *model)
		if e != nil {
			return e
		}
		_, e = out.Write(append(enc(v), '\n'))
		return e
	}
	if *environment == "" || *model == "" || *output == "" {
		return fmt.Errorf("native evaluation requires --environment, --model and --output; legacy prediction uses --database")
	}
	if _, e := os.Stat(*output); e == nil {
		return fmt.Errorf("evaluation report exists; choose a new --output")
	} else if !os.IsNotExist(e) {
		return e
	}
	_, evidenceErr := os.Stat(filepath.Join(*output+".data", "spec.json"))
	if evidenceErr != nil && !os.IsNotExist(evidenceErr) {
		return evidenceErr
	}
	if *resume && os.IsNotExist(evidenceErr) {
		return fmt.Errorf("--resume requires existing frozen evaluation evidence")
	}
	if !*resume && evidenceErr == nil {
		return fmt.Errorf("interrupted evaluation exists; repeat the identical command with --resume")
	}
	command, e := loadTrainingEnvironment(*environment)
	if e != nil {
		return e
	}
	candidate, e := battlepolicy.LoadArtifact(*model)
	if e != nil {
		return e
	}
	var opponents []battletrain.Opponent
	if len(rules) == 0 && len(models) == 0 {
		rules = pathsFlag(battlepolicy.DefaultRuleNames())
	}
	for _, name := range rules {
		if !battlepolicy.RuleSupported(name) {
			return fmt.Errorf("unknown rule opponent %q", name)
		}
		opponents = append(opponents, battletrain.Opponent{Name: name, Rule: name})
	}
	for i, path := range models {
		m, e := battlepolicy.LoadArtifact(path)
		if e != nil {
			return e
		}
		opponents = append(opponents, battletrain.Opponent{Name: fmt.Sprintf("model-%d:%s", i, m.WeightsDigest), Model: &m})
	}
	if comparison != nil {
		if _, err := comparison.ValidatePair(candidate, *opponents[0].Model, c.Mode, "validation"); err != nil {
			return err
		}
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
	if mixed != nil && *split == "test" {
		if engine.Metadata() != mixed.Parts[0].Experiment.Environment {
			return fmt.Errorf("final-test engine differs from frozen mixed experiment")
		}
		if e = battletrain.FreezeMixedFinalSelection(ctx, *mixedPath+".test-selection.json", *mixed, c.Mode, candidate, opponents); e != nil {
			return e
		}
	}
	if experiment != nil && *split == "test" {
		if engine.Metadata() != experiment.Environment {
			return fmt.Errorf("final-test engine differs from frozen experiment")
		}
		if e = battletrain.FreezeFinalSelection(*experimentPath+".test-selection.json", *experiment, candidate, opponents); e != nil {
			return e
		}
	}
	completed, executed := 0, 0
	options := battletrain.EvaluationRecordingOptions{Resume: *resume, Restored: func(reused, total int) error {
		completed = reused
		_, err := out.Write(append(enc(Object{"event": "evaluation_resumed", "reused": reused, "completed": reused, "total": total, "remaining": total - reused, "executed": 0}), '\n'))
		return err
	}}
	progress := func(g battletrain.EvaluationGame) error {
		completed++
		executed++
		_, e := out.Write(append(enc(Object{"event": "evaluation_game", "completed": completed, "executed": executed, "opponent": g.Opponent, "turns": g.Turns, "winner_side": g.Winner, "candidate_side": g.CandidateSide, "truncated": g.Truncated}), '\n'))
		return e
	}
	evaluationSplit := ""
	if experiment != nil || mixed != nil {
		evaluationSplit = *split
	}
	var report battletrain.EvaluationReport
	if comparison != nil {
		report, e = battletrain.EvaluateMixedComparisonRecorded(ctx, engine, candidate, *opponents[0].Model, *comparison, c.Mode, *output, progress, options)
	} else if mixed != nil {
		report, e = battletrain.EvaluateMixedRecorded(ctx, engine, candidate, opponents, *mixed, c.Mode, evaluationSplit, *output, progress, options)
	} else {
		report, e = battletrain.EvaluateRecorded(ctx, engine, candidate, opponents, c, experiment, evaluationSplit, *output, progress, options)
	}
	if e != nil {
		return e
	}
	if e = engine.Close(); e != nil {
		return e
	}
	_, e = out.Write(append(enc(Object{"event": "evaluation_complete", "report": *output, "comparisons": report.Comparisons, "unverified_source_artifacts": report.UnverifiedSourceArtifacts, "arena_certified": false}), '\n'))
	return e
}
