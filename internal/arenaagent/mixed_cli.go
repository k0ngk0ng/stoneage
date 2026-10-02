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
	"strconv"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
	"github.com/k0ngk0ng/stoneage/internal/battletrain"
)

func hasMixedCheckpoint(directory string) (bool, error) {
	found := false
	for _, name := range []string{battletrain.MixedPointer, "mixed-checkpoints"} {
		if _, err := os.Lstat(filepath.Join(directory, name)); err == nil {
			found = true
		} else if !os.IsNotExist(err) {
			return false, err
		}
	}
	if found {
		for _, name := range []string{"latest.json", "checkpoints", battletrain.DemonstrationPointer, "demonstration-checkpoints", battletrain.FeedbackPointer, "feedback-checkpoints", battletrain.FeedbackCollectionPointer, "feedback-collection-checkpoints"} {
			if _, err := os.Lstat(filepath.Join(directory, name)); err == nil {
				return false, fmt.Errorf("ambiguous training directory contains mixed and another training/collection state")
			} else if !os.IsNotExist(err) {
				return false, err
			}
		}
	}
	return found, nil
}

func positiveList(text string, n, maximum int, name string) ([]int, error) {
	values := make([]int, n)
	if text == "" {
		for i := range values {
			values[i] = 1
		}
		return values, nil
	}
	parts := strings.Split(text, ",")
	if len(parts) != n {
		return nil, fmt.Errorf("--%s requires one value per --experiment, in the same ascending mode order", name)
	}
	for i, part := range parts {
		v, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || v < 1 || v > maximum {
			return nil, fmt.Errorf("--%s values must be 1..%d", name, maximum)
		}
		values[i] = v
	}
	return values, nil
}

func mixedExperimentCommand(ctx context.Context, args []string, out io.Writer) error {
	f := flag.NewFlagSet("sactl ai experiment-mix", flag.ContinueOnError)
	f.SetOutput(out)
	var paths pathsFlag
	f.Var(&paths, "experiment", "frozen child experiment, repeat 2..5 times in ascending team-size order")
	weights := f.String("weights", "", "positive integer PPO loss weights, comma separated in experiment order; default all 1")
	families := f.String("families-per-batch", "", "complete eight-game families per mode per update, comma separated in experiment order; default all 1")
	parentPath := f.String("from-model", "", "exact shared parent declared by every child experiment, if present")
	output := f.String("output", "", "new immutable mixed experiment manifest")
	if err := f.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if f.NArg() != 0 || len(paths) < 2 || len(paths) > 5 || *output == "" {
		return fmt.Errorf("experiment-mix requires 2..5 --experiment paths and --output")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	w, err := positiveList(*weights, len(paths), 1000000, "weights")
	if err != nil {
		return err
	}
	q, err := positiveList(*families, len(paths), 125, "families-per-batch")
	if err != nil {
		return err
	}
	var parent *battlepolicy.Artifact
	if *parentPath != "" {
		a, err := battlepolicy.LoadArtifact(*parentPath)
		if err != nil {
			return err
		}
		parent = &a
	}
	mixture := battletrain.ModeMixture{Schema: "mode-weighted-ppo-v1"}
	var parts []battletrain.MixedExperimentPart
	for i, path := range paths {
		x, err := battletrain.LoadExperiment(path)
		if err != nil {
			return err
		}
		parts = append(parts, battletrain.MixedExperimentPart{Experiment: x, FamiliesPerBatch: q[i]})
		mixture.Modes = append(mixture.Modes, battletrain.ModeWeight{Mode: x.Mode, Weight: w[i]})
	}
	x, err := battletrain.NewMixedExperiment(mixture, parts, parent)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	id, err := battletrain.SaveMixedExperiment(*output, x)
	if err != nil {
		return err
	}
	schedule, err := battletrain.NewMixedSchedule(x, 0)
	if err != nil {
		return err
	}
	_, err = out.Write(append(enc(Object{"event": "mixed_experiment_created", "experiment": id, "manifest": *output, "mixture": x.Mixture, "batch_matches": schedule.BatchMatches()}), '\n'))
	return err
}

type mixedCLIOptions struct {
	Directory, Environment, Experiment, FromModel, Output string
	Resume                                                bool
	Batches, Workers                                      int
	StopAtDataBytes                                       int64
	Config                                                battletrain.RunConfig
	Rules                                                 []string
	Mix, PlanScope, PlanFeatures                          string
	Provided                                              map[string]bool
}

func trainMixedCommand(ctx context.Context, o mixedCLIOptions, out io.Writer) error {
	for name := range o.Provided {
		allowed := false
		switch name {
		case "environment", "data-dir", "resume", "batches", "workers", "stop-at-data-bytes", "output":
			allowed = true
		case "mixed-experiment", "from-model", "initial-policy-scale", "seed", "epochs", "sequence-length", "learning-rate", "target-kl", "gae-lambda", "entropy-weight", "rule-opponent", "opponent-mix", "opponent-sampling", "plan-scope", "plan-features":
			allowed = !o.Resume
		}
		if !allowed {
			return fmt.Errorf("--%s is incompatible with mixed training or cannot change stored settings during --resume", name)
		}
	}
	if o.Environment == "" || o.Directory == "" || !o.Resume && o.Experiment == "" || o.Workers < 1 || o.Workers > battletrain.MaxCollectionWorkers || o.StopAtDataBytes < 0 || o.Batches < 1 || o.Batches > 10000 {
		return fmt.Errorf("mixed training requires --environment, --data-dir, a new --mixed-experiment or --resume, 1..8 workers and 1..10000 batches")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var config *battletrain.MixedRunConfig
	var experiment *battletrain.MixedExperiment
	var parent *battlepolicy.Artifact
	if !o.Resume {
		if o.Provided["opponent-sampling"] && o.Config.OpponentSampling != "uniform" {
			return fmt.Errorf("mixed training currently requires --opponent-sampling uniform")
		}
		if o.PlanScope != "team" && o.PlanScope != "member" {
			return fmt.Errorf("--plan-scope must be team or member")
		}
		if o.PlanFeatures != "none" && o.PlanFeatures != "target-counts-v1" {
			return fmt.Errorf("--plan-features must be none or target-counts-v1")
		}
		if o.PlanScope == "member" {
			o.Config.Network.PlanScope = "member"
		}
		if o.PlanFeatures != "none" {
			o.Config.Network.PlanFeatures = o.PlanFeatures
		}
		if o.FromModel != "" {
			a, err := battlepolicy.LoadArtifact(o.FromModel)
			if err != nil {
				return err
			}
			if o.Provided["plan-scope"] && o.Config.Network.PlanScope != a.Network.Config.PlanScope || o.Provided["plan-features"] && o.Config.Network.PlanFeatures != a.Network.Config.PlanFeatures {
				return fmt.Errorf("mixed training cannot change the parent model architecture")
			}
			parent, o.Config.Network = &a, a.Network.Config
		}
		x, err := battletrain.LoadMixedExperiment(o.Experiment)
		if err != nil {
			return err
		}
		if err := x.ValidateParent(parent); err != nil {
			return err
		}
		experiment = &x
		id, _ := battletrain.Digest(x)
		parts := strings.Split(o.Mix, ",")
		if len(parts) != 3 {
			return fmt.Errorf("--opponent-mix requires rule,history,self integer percentages")
		}
		mix := battletrain.OpponentMix{}
		for i, ptr := range []*int{&mix.Rules, &mix.History, &mix.Self} {
			value, err := strconv.Atoi(strings.TrimSpace(parts[i]))
			if err != nil {
				return fmt.Errorf("--opponent-mix requires rule,history,self integer percentages")
			}
			*ptr = value
		}
		if len(o.Rules) == 0 {
			o.Rules = battlepolicy.DefaultRuleNames()
		}
		config = &battletrain.MixedRunConfig{Schema: "commander-mixed-run-v1", Experiment: id, Seed: o.Config.Seed, Network: o.Config.Network, PPO: o.Config.PPO, RuleOpponents: o.Rules, OpponentMix: mix}
		config.InitialPolicyScale = o.Config.InitialPolicyScale
		if config.InitialPolicyScale != 0 {
			config.Schema = "commander-mixed-run-v2"
		}
		if err := config.Validate(); err != nil {
			return err
		}
	}
	command, err := loadTrainingEnvironment(o.Environment)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()
	err = battletrain.RunMixed(ctx, battletrain.MixedRunOptions{Directory: o.Directory, Command: command, Config: config, Experiment: experiment, InitialModel: parent, Resume: o.Resume, Batches: o.Batches, Workers: o.Workers, StopAtDataBytes: o.StopAtDataBytes, Progress: func(p battletrain.MixedProgress) error {
		_, err := out.Write(append(enc(p), '\n'))
		return err
	}})
	if err != nil {
		if errors.Is(err, context.Canceled) || ctx.Err() == context.Canceled {
			_, writeErr := out.Write(append(enc(Object{"event": "training_interrupted", "data_dir": o.Directory, "progress": "committed checkpoints preserved; resume with the same data-dir"}), '\n'))
			return errors.Join(err, writeErr)
		}
		return err
	}
	path, err := battletrain.ExportMixedCandidate(ctx, o.Directory, "", o.Output)
	if err != nil {
		return err
	}
	_, err = out.Write(append(enc(Object{"event": "candidate_saved", "model": path, "status": "candidate", "arena_certified": false}), '\n'))
	return err
}
