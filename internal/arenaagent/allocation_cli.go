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

func allocationCommand(ctx context.Context, args []string, out io.Writer) (resultErr error) {
	if len(args) == 0 || args[0] == "--help" || args[0] == "help" {
		_, err := fmt.Fprintln(out, "sactl ai build-validate: supplementary parent/child × balanced/selected allocation validation\nCommands: init, run, verify, compare\nUse <command> --help. Local native evaluation; does not modify online characters or promote models.")
		return err
	}
	sub := args[0]
	if sub != "init" && sub != "run" && sub != "verify" && sub != "compare" {
		return fmt.Errorf("unknown build-validate command %q; use init, run, verify or compare", sub)
	}
	f := flag.NewFlagSet("sactl ai build-validate "+sub, flag.ContinueOnError)
	f.SetOutput(out)
	source := f.String("search-dir", "", "completed source build-search directory; retain pinned policies and raw shards")
	var output, experiment, parent, manifest, model, report, baseline, candidate, environment *string
	var seed *int64
	var groups *int
	var resume *bool
	switch sub {
	case "init":
		experiment = f.String("experiment", "", "pooled training experiment declaring the exact parent")
		parent = f.String("from-model", "", "exact parent model JSON")
		output = f.String("output", "", "new immutable supplementary validation manifest")
		seed = f.Int64("seed", 20261001, "fresh opponent generation and paired scenario seed")
		groups = f.Int("groups", 32, "independent opponent rosters, 1..512; intervals need at least 20")
	case "run":
		environment = trainingEnvironmentFlag(f)
		manifest = f.String("manifest", "", "frozen build-validate manifest JSON")
		model = f.String("model", "", "exact declared parent or its trained child")
		output = f.String("output", "", "new supplementary report; frozen models/raw data in <output>.data")
		resume = f.Bool("resume", false, "preflight both branches and reuse the exact interrupted specification")
	case "verify":
		report = f.String("report", "", "supplementary report with matching .data directory; read-only")
	case "compare":
		baseline = f.String("baseline", "", "verified supplementary report for the exact declared parent")
		candidate = f.String("candidate", "", "verified supplementary report for its trained child")
	}
	if err := f.Parse(args[1:]); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if f.NArg() != 0 || *source == "" {
		return fmt.Errorf("build-validate %s requires --search-dir and no positional arguments", sub)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	emit := func(v any) error { _, err := out.Write(append(enc(v), '\n')); return err }
	switch sub {
	case "init":
		if *experiment == "" || *parent == "" || *output == "" || *groups < 1 || *groups > 512 {
			return fmt.Errorf("build-validate init requires --experiment, --from-model, --output and --groups in 1..512")
		}
		if _, err := os.Stat(*output); err == nil {
			return fmt.Errorf("validation manifest exists; choose a new output")
		} else if !os.IsNotExist(err) {
			return err
		}
		x, err := battletrain.LoadExperiment(*experiment)
		if err != nil {
			return err
		}
		a, err := battlepolicy.LoadArtifact(*parent)
		if err != nil {
			return err
		}
		ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
		defer cancel()
		v, err := battletrain.NewAllocationValidationFromSearch(ctx, x, a, *source, *seed, *groups)
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		id, err := battletrain.SaveAllocationValidation(*output, v)
		if err != nil {
			return err
		}
		return emit(Object{"event": "allocation_validation_created", "validation": id, "manifest": *output, "opponent_rosters": v.Groups, "games_per_model": v.Groups * len(v.Source.Spec.Opponents) * 8, "arena_certified": false})
	case "verify":
		if *report == "" {
			return fmt.Errorf("build-validate verify requires --report")
		}
		ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
		defer cancel()
		r, err := battletrain.VerifyAllocationEvaluation(ctx, *report, *source)
		if err != nil {
			return err
		}
		return emit(Object{"event": "allocation_evidence_verified", "report": *report, "validation": r.Validation, "candidate": r.Candidate, "status": r.Status, "arena_certified": false})
	case "compare":
		if *baseline == "" || *candidate == "" {
			return fmt.Errorf("build-validate compare requires --baseline and --candidate")
		}
		ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
		defer cancel()
		r, err := battletrain.CompareAllocationEvaluations(ctx, *baseline, *candidate, *source)
		if err != nil {
			return err
		}
		return emit(r)
	case "run":
		if *manifest == "" || *model == "" || *output == "" || *environment == "" {
			return fmt.Errorf("build-validate run requires --manifest, --model, --environment and --output")
		}
		if _, err := os.Stat(*output); err == nil {
			return fmt.Errorf("allocation report exists; choose a new output")
		} else if !os.IsNotExist(err) {
			return err
		}
		v, err := battletrain.LoadAllocationValidation(*manifest)
		if err != nil {
			return err
		}
		a, err := battlepolicy.LoadArtifact(*model)
		if err != nil {
			return err
		}
		if err = v.ValidateCandidate(a); err != nil {
			return err
		}
		if _, err = os.Stat(*output + ".data/spec.json"); *resume && os.IsNotExist(err) {
			return fmt.Errorf("--resume requires existing frozen allocation evidence")
		} else if err != nil && !os.IsNotExist(err) {
			return err
		} else if !*resume && err == nil {
			return fmt.Errorf("interrupted allocation evaluation exists; use --resume with unchanged arguments")
		}
		command, err := loadTrainingEnvironment(*environment)
		if err != nil {
			return err
		}
		if err = os.MkdirAll(filepath.Dir(*output), 0700); err != nil {
			return err
		}
		log, err := os.OpenFile(*output+".engine.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return err
		}
		defer log.Close()
		ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
		defer cancel()
		engine, err := battleenv.Start(ctx, command, log)
		if err != nil {
			return err
		}
		defer closeNativeEngine(engine, &resultErr)
		r, err := battletrain.RunAllocationValidation(ctx, engine, v, a, *source, *output, func(allocation string, g battletrain.EvaluationGame) error {
			return emit(Object{"event": "allocation_game", "allocation": allocation, "opponent": g.Opponent, "side": g.CandidateSide, "turns": g.Turns, "truncated": g.Truncated})
		}, battletrain.EvaluationRecordingOptions{Resume: *resume, Restored: func(done, total int) error {
			return emit(Object{"event": "allocation_ready", "restored_games": done, "total_games": total})
		}})
		if err != nil {
			return err
		}
		if err = engine.Close(); err != nil {
			return err
		}
		return emit(Object{"event": "allocation_evaluation_complete", "report": *output, "validation": r.Validation, "candidate": r.Candidate, "status": r.Status, "arena_certified": false})
	}
	return nil
}
