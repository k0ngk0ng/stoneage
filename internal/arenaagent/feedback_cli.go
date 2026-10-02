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

	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
	"github.com/k0ngk0ng/stoneage/internal/battletrain"
)

func collectFeedbackCommand(ctx context.Context, args []string, out io.Writer) error {
	f := flag.NewFlagSet("sactl ai collect-feedback", flag.ContinueOnError)
	f.SetOutput(out)
	environment := trainingEnvironmentFlag(f)
	root := f.String("data-dir", "", "new collection directory; resume uses its frozen model and schedule")
	experiment := f.String("experiment", "", "frozen native experiment; only its training families are collected")
	model := f.String("model", "", "compatible trained candidate or exact declared parent")
	resume := f.Bool("resume", false, "finish the saved match budget without overriding its settings")
	workers := f.Int("workers", 1, "native collection processes, 1..8; may change on resume")
	limit := f.Int64("stop-at-data-bytes", 0, "stop after a committed checkpoint at this logical retained-byte threshold; 0 disables")
	c := battletrain.FeedbackCollectionConfig{Seed: 1, Matches: 32, Teacher: "sustain"}
	f.Int64Var(&c.Seed, "seed", c.Seed, "frozen scenario and action sampling seed")
	f.IntVar(&c.Matches, "matches", c.Matches, "total games, at most 256; complete paired families and at least one family per opponent")
	f.StringVar(&c.Teacher, "teacher", c.Teacher, "rule to suggest actions on learner observations; default sustain")
	f.BoolVar(&c.Greedy, "greedy", false, "collect deterministic learned choices instead of sampled actions")
	var opponents pathsFlag
	f.Var(&opponents, "opponent", "rule opponent, repeatable; default basic and sustain")
	if err := f.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if *root == "" || f.NArg() != 0 || *workers < 1 || *workers > battletrain.MaxCollectionWorkers {
		return fmt.Errorf("collect-feedback requires --data-dir, 1..8 workers and no positional arguments")
	}
	var invalid string
	f.Visit(func(item *flag.Flag) {
		if *resume && item.Name != "data-dir" && item.Name != "resume" && item.Name != "environment" && item.Name != "workers" && item.Name != "stop-at-data-bytes" {
			invalid = item.Name
		}
	})
	if invalid != "" {
		return fmt.Errorf("--%s cannot override a saved feedback collection", invalid)
	}
	if !*resume && (*experiment == "" || *model == "" || *environment == "") {
		return fmt.Errorf("new collect-feedback requires --environment, --experiment and --model")
	}
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()
	o := battletrain.FeedbackCollectionOptions{Directory: *root, Resume: *resume, Workers: *workers, StopAtDataBytes: *limit, Progress: func(p battletrain.FeedbackCollectionProgress) error {
		_, err := out.Write(append(enc(p), '\n'))
		return err
	}}
	if *environment != "" {
		command, err := loadTrainingEnvironment(*environment)
		if err != nil {
			return err
		}
		o.Command = command
	}
	if !*resume {
		x, err := battletrain.LoadExperiment(*experiment)
		if err != nil {
			return err
		}
		a, err := battlepolicy.LoadArtifact(*model)
		if err != nil {
			return err
		}
		if len(opponents) == 0 {
			opponents = pathsFlag{"basic", "sustain"}
		}
		c.Opponents = []string(opponents)
		o.Experiment, o.Model, o.Config = &x, &a, &c
	}
	err := battletrain.CollectFeedback(ctx, o)
	if errors.Is(err, context.Canceled) {
		// Do not perform a potentially long model replay while reporting an
		// interrupt. The pointer identifies the last commit; resume verifies it.
		_, statErr := os.Stat(filepath.Join(*root, battletrain.FeedbackCollectionPointer))
		if _, writeErr := out.Write(append(enc(Object{"event": "feedback_collection_interrupted", "checkpoint_pointer_present": statErr == nil}), '\n')); writeErr != nil {
			return writeErr
		}
	}
	return err
}

func trainFeedbackCommand(ctx context.Context, args []string, out io.Writer) error {
	f := flag.NewFlagSet("sactl ai train-feedback", flag.ContinueOnError)
	f.SetOutput(out)
	root := f.String("data-dir", "", "new feedback training directory; parent must exist")
	resume := f.Bool("resume", false, "use only saved datasets, initialization and optimizer settings")
	epochs := f.Int("epochs", 1, "additional complete epochs over the frozen aggregate")
	model := f.String("model", "", "optional compatible initialization; default model of the last --collection")
	output := f.String("output", "", "export candidate after successful training; default data-dir/models/<digest>.json")
	limit := f.Int64("stop-at-data-bytes", 0, "stop at a committed checkpoint without exporting or deleting data; 0 disables")
	var collections pathsFlag
	f.Var(&collections, "collection", "completed collect-feedback directory, repeatable in training order")
	c := battletrain.DefaultWarmupConfig().Update
	f.IntVar(&c.BatchEpisodes, "batch-episodes", c.BatchEpisodes, "complete trajectories per optimizer batch; 0 full aggregate")
	f.IntVar(&c.SequenceLength, "sequence-length", c.SequenceLength, "recurrent backpropagation segment length")
	f.Float64Var(&c.LearningRate, "learning-rate", c.LearningRate, "supervised learning rate")
	f.Float64Var(&c.GradientClip, "gradient-clip", c.GradientClip, "global gradient norm bound")
	f.StringVar(&c.ActionWeighting, "action-weighting", "none", "none or sqrt-action-frequency-v1: frozen training-only action class weights")
	if err := f.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if c.ActionWeighting == "none" {
		c.ActionWeighting = ""
	}
	if c.ActionWeighting != "" && c.ActionWeighting != battletrain.SqrtActionFrequency {
		return fmt.Errorf("action weighting must be none or sqrt-action-frequency-v1")
	}
	if *root == "" || f.NArg() != 0 || !*resume && len(collections) == 0 {
		return fmt.Errorf("train-feedback requires --data-dir and --collection; resume uses only --data-dir --resume")
	}
	var invalid string
	f.Visit(func(item *flag.Flag) {
		if *resume && item.Name != "data-dir" && item.Name != "resume" && item.Name != "epochs" && item.Name != "output" && item.Name != "stop-at-data-bytes" {
			invalid = item.Name
		}
	})
	if invalid != "" {
		return fmt.Errorf("--%s cannot override saved feedback training", invalid)
	}
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()
	o := battletrain.FeedbackRunOptions{Directory: *root, Resume: *resume, Epochs: *epochs, StopAtDataBytes: *limit, Progress: func(p battletrain.FeedbackProgress) error { _, err := out.Write(append(enc(p), '\n')); return err }}
	if !*resume {
		var initial battlepolicy.Artifact
		for _, collection := range collections {
			checkpoint, _, a, err := battletrain.LoadFeedbackCollection(ctx, collection)
			if err != nil {
				return err
			}
			inputs, err := battletrain.FeedbackCollectionInputs(collection, checkpoint)
			if err != nil {
				return err
			}
			o.Datasets = append(o.Datasets, inputs...)
			initial = a
		}
		if *model != "" {
			var err error
			initial, err = battlepolicy.LoadArtifact(*model)
			if err != nil {
				return err
			}
		}
		o.Initial, o.Update = &initial, &c
	}
	err := battletrain.RunFeedback(ctx, o)
	if errors.Is(err, context.Canceled) {
		_, statErr := os.Stat(filepath.Join(*root, battletrain.FeedbackPointer))
		if _, writeErr := out.Write(append(enc(Object{"event": "feedback_training_interrupted", "checkpoint_pointer_present": statErr == nil}), '\n')); writeErr != nil {
			return writeErr
		}
	}
	if err != nil {
		return err
	}
	path, err := battletrain.ExportFeedbackCandidate(ctx, *root, "", *output)
	if err != nil {
		return err
	}
	_, err = out.Write(append(enc(Object{"event": "candidate_saved", "model": path, "status": "candidate", "arena_certified": false}), '\n'))
	return err
}

func hasFeedbackCheckpoint(directory string) (bool, error) {
	found := false
	for _, name := range []string{battletrain.FeedbackPointer, "feedback-checkpoints"} {
		if _, err := os.Lstat(filepath.Join(directory, name)); err == nil {
			found = true
		} else if !os.IsNotExist(err) {
			return false, err
		}
	}
	if found {
		for _, name := range []string{"latest.json", "checkpoints", battletrain.DemonstrationPointer, "demonstration-checkpoints", battletrain.FeedbackCollectionPointer, "feedback-collection-checkpoints"} {
			if _, err := os.Lstat(filepath.Join(directory, name)); err == nil {
				return false, fmt.Errorf("ambiguous training directory contains feedback and another training/collection state")
			} else if !os.IsNotExist(err) {
				return false, err
			}
		}
	}
	return found, nil
}
