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

func championCommand(ctx context.Context, args []string, out io.Writer) (resultErr error) {
	if len(args) == 0 || args[0] == "--help" || args[0] == "help" {
		_, e := fmt.Fprintln(out, "sactl ai champion: local controlled-native champion registry\nCommands: init, challenge, status, rollback, abandon\nFreeze gates with init; challenge runs the final test and automatically retains or replaces the native champion. This is not online arena certification. Use <command> --help.")
		return e
	}
	sub := args[0]
	f := flag.NewFlagSet("sactl ai champion "+sub, flag.ContinueOnError)
	f.SetOutput(out)
	root := f.String("directory", "", "local native champion registry directory")
	var experiment, mixedExperiment, model, environment, target, reason string
	gate := battletrain.DefaultPromotionGate()
	switch sub {
	case "init":
		f.StringVar(&experiment, "experiment", "", "freeze environment and scenario conditions from this experiment")
		f.StringVar(&mixedExperiment, "mixed-experiment", "", "freeze every mode's conditions and one shared promotion error budget")
		f.IntVar(&gate.MinGroups, "min-groups", gate.MinGroups, "minimum independent final-test families per opponent, at least 20")
		f.Float64Var(&gate.Alpha, "alpha", gate.Alpha, "total error budget across attempts and opponents, at most .05")
		f.Float64Var(&gate.RuleScore, "rule-score", gate.RuleScore, "required score lower bound against each fixed rule, at least .5")
		f.Float64Var(&gate.ChampionMargin, "champion-margin", gate.ChampionMargin, "noninferiority margin against previous champion, at most .05")
	case "challenge":
		f.StringVar(&experiment, "experiment", "", "candidate's frozen experiment; consumes the complete final-test split")
		f.StringVar(&mixedExperiment, "mixed-experiment", "", "candidate's frozen mixed experiment; consumes every mode's complete final-test split")
		f.StringVar(&model, "model", "", "candidate commander model")
		f.StringVar(&environment, "environment", os.Getenv(trainingEnvironmentVariable), "native environment argv JSON; defaults to STONEAGE_TRAINING_ENVIRONMENT")
	case "status":
	case "rollback":
		f.StringVar(&target, "to", "", "earlier passing event digest from status")
		f.StringVar(&reason, "reason", "", "reason for selecting the earlier native champion")
	case "abandon":
		f.StringVar(&reason, "reason", "", "reason for abandoning the pending attempt; its test families remain exposed")
	default:
		return fmt.Errorf("unknown champion command %q; use champion --help", sub)
	}
	if e := f.Parse(args[1:]); e != nil {
		if e == flag.ErrHelp {
			return nil
		}
		return e
	}
	if *root == "" || f.NArg() != 0 {
		return fmt.Errorf("champion %s requires --directory and no positional arguments", sub)
	}
	if experiment != "" && mixedExperiment != "" {
		return fmt.Errorf("--experiment and --mixed-experiment are mutually exclusive")
	}
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()
	emit := func(v any) error { _, e := out.Write(append(enc(v), '\n')); return e }
	switch sub {
	case "init":
		if mixedExperiment != "" {
			x, e := battletrain.LoadMixedExperiment(mixedExperiment)
			if e != nil {
				return e
			}
			if e = battletrain.InitMixedChampionRegistry(*root, x, gate); e != nil {
				return e
			}
			return emit(Object{"event": "champion_registry_created", "directory": *root, "gate": gate, "modes": x.Mixture.Modes, "arena_certified": false})
		}
		if experiment == "" {
			return fmt.Errorf("champion init requires --experiment")
		}
		x, e := battletrain.LoadExperiment(experiment)
		if e != nil {
			return e
		}
		if e = battletrain.InitChampionRegistry(*root, x, gate); e != nil {
			return e
		}
		return emit(Object{"event": "champion_registry_created", "directory": *root, "gate": gate, "arena_certified": false})
	case "status":
		s, e := battletrain.LoadChampionRegistry(ctx, *root)
		if e != nil {
			return e
		}
		return emit(s)
	case "rollback", "abandon":
		if sub == "rollback" && target == "" {
			return fmt.Errorf("champion rollback requires --to and --reason")
		}
		event, e := battletrain.ChangeChampion(ctx, *root, target, reason, sub == "abandon")
		if e != nil {
			return e
		}
		id, e := battletrain.Digest(event)
		if e != nil {
			return e
		}
		return emit(Object{"event": "champion_" + sub, "event_digest": id, "result": event, "arena_certified": false})
	case "challenge":
		if experiment == "" && mixedExperiment == "" || model == "" || environment == "" {
			return fmt.Errorf("champion challenge requires --experiment or --mixed-experiment, --model and --environment")
		}
		ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
		defer cancel()
		var x battletrain.Experiment
		var mixed *battletrain.MixedExperiment
		if mixedExperiment != "" {
			m, e := battletrain.LoadMixedExperiment(mixedExperiment)
			if e != nil {
				return e
			}
			mixed = &m
		} else {
			var e error
			x, e = battletrain.LoadExperiment(experiment)
			if e != nil {
				return e
			}
		}
		a, e := battlepolicy.LoadArtifact(model)
		if e != nil {
			return e
		}
		command, e := loadTrainingEnvironment(environment)
		if e != nil {
			return e
		}
		log, e := os.OpenFile(filepath.Join(*root, "engine.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		defer log.Close()
		engine, e := battleenv.Start(ctx, command, log)
		if e != nil {
			return e
		}
		defer closeNativeEngine(engine, &resultErr)
		completed := 0
		progress := func(g battletrain.EvaluationGame) error {
			completed++
			return emit(Object{"event": "champion_evaluation_game", "completed": completed, "mode": g.Scenario.Mode, "opponent": g.Opponent, "turns": g.Turns, "truncated": g.Truncated})
		}
		var event battletrain.ChampionEvent
		if mixed != nil {
			event, e = battletrain.ChallengeMixedChampion(ctx, *root, engine, a, *mixed, mixedExperiment+".test-selection.json", progress)
		} else {
			event, e = battletrain.ChallengeChampion(ctx, *root, engine, a, x, experiment+".test-selection.json", progress)
		}
		if e != nil {
			return e
		}
		if e = engine.Close(); e != nil {
			return e
		}
		id, e := battletrain.Digest(event)
		if e != nil {
			return e
		}
		return emit(Object{"event": "champion_assessed", "event_digest": id, "result": event, "arena_certified": false})
	}
	return nil
}
