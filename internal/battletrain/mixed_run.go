package battletrain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

type MixedRunOptions struct {
	Directory       string
	Command         []string
	Config          *MixedRunConfig
	Experiment      *MixedExperiment
	InitialModel    *battlepolicy.Artifact
	Resume          bool
	Batches         int
	Workers         int
	StopAtDataBytes int64
	Stderr          io.Writer
	Progress        func(MixedProgress) error
}

type MixedProgress struct {
	Event             string  `json:"event"`
	CompletedBatches  int     `json:"completed_batches"`
	Games             uint64  `json:"games"`
	PendingMatches    int     `json:"pending_matches"`
	CollectionWorkers int     `json:"collection_workers"`
	Learning          string  `json:"learning_state"`
	Report            *Report `json:"report,omitempty"`
	DataBytes         int64   `json:"data_bytes,omitempty"`
	StopAtDataBytes   int64   `json:"stop_at_data_bytes,omitempty"`
	StorageCheckEvent string  `json:"storage_check_event,omitempty"`
}

// RunMixed owns an independent versioned training directory. Every update uses
// fresh on-policy games of every declared mode, with one shared model/Adam and
// no mode relabeling or old-trajectory PPO reuse. It does not export or promote
// a model. A caller must wait for this function's close result before announcing
// final success or attempting export.
func RunMixed(ctx context.Context, o MixedRunOptions) (resultErr error) {
	if o.Directory == "" || len(o.Command) == 0 || o.Batches < 1 || o.Batches > 10000 || o.Workers < 0 || o.Workers > MaxCollectionWorkers || o.StopAtDataBytes < 0 {
		return fmt.Errorf("mixed training requires directory, environment, 1..10000 batches, valid workers and storage limit")
	}
	if o.Workers == 0 {
		o.Workers = 1
	}
	if o.Resume && (o.Config != nil || o.Experiment != nil || o.InitialModel != nil) || !o.Resume && (o.Config == nil || o.Experiment == nil) {
		return fmt.Errorf("new mixed training requires config/experiment; resume uses only its frozen inputs")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var c MixedCheckpoint
	var x MixedExperiment
	var learning LearningState
	var history []string
	if !o.Resume {
		if err := o.Config.Validate(); err != nil {
			return err
		}
		if err := o.Experiment.ValidateParent(o.InitialModel); err != nil {
			return err
		}
		if o.Config.InitialPolicyScale != 0 && o.InitialModel == nil {
			return fmt.Errorf("initial policy scale requires a declared parent")
		}
		id, _ := Digest(*o.Experiment)
		if id != o.Config.Experiment || o.InitialModel != nil && o.InitialModel.Network.Config != o.Config.Network {
			return fmt.Errorf("mixed recipe differs from experiment or parent network")
		}
		// Freeze caller-owned nested slices before any progress callback runs.
		raw, err := json.Marshal(struct {
			Config     MixedRunConfig
			Experiment MixedExperiment
		}{*o.Config, *o.Experiment})
		if err != nil {
			return err
		}
		var owned struct {
			Config     MixedRunConfig
			Experiment MixedExperiment
		}
		if err := json.Unmarshal(raw, &owned); err != nil {
			return err
		}
		c = MixedCheckpoint{Schema: "commander-mixed-checkpoint-v1", Config: owned.Config}
		if c.Config.InitialPolicyScale != 0 {
			c.Schema = "commander-mixed-checkpoint-v2"
		}
		x = owned.Experiment
		if err := battlepolicy.ValidateFeatureEnvironment(battlepolicy.NetworkFeatures(c.Config.Network), x.Parts[0].Experiment.Environment.Scenario); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(o.Directory, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(o.Directory, ".training.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := lockTraining(lock); err != nil {
		return fmt.Errorf("training directory is already in use: %w", err)
	}
	if o.Resume {
		loaded, err := LoadMixedCheckpoint(ctx, o.Directory)
		if err != nil {
			return err
		}
		c, x, learning, history = loaded.Checkpoint, loaded.Experiment, loaded.Learning, loaded.History
	} else {
		entries, err := os.ReadDir(o.Directory)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Name() != ".training.lock" {
				return fmt.Errorf("new mixed training needs an empty data directory; preserve existing files and use --resume for a committed mixed run")
			}
		}
		for _, directory := range []string{"learning", "shards", "mixed-checkpoints", "mixed-reports", "initial-models"} {
			if err := os.MkdirAll(filepath.Join(o.Directory, directory), 0700); err != nil {
				return err
			}
		}
		if _, err := SaveMixedExperiment(filepath.Join(o.Directory, "mixed-experiments", c.Config.Experiment+".json"), x); err != nil {
			return err
		}
		if o.InitialModel != nil {
			id, _ := Digest(*o.InitialModel)
			if err := writeObject(filepath.Join(o.Directory, "initial-models", id+".json"), o.InitialModel); err != nil {
				return err
			}
		}
		learning, err = newInitialLearning(c.Config.Network, c.Config.Seed, o.InitialModel, c.Config.InitialPolicyScale)
		if err != nil {
			return err
		}
	}
	end := len(c.Reports) + o.Batches
	if end > 10000 {
		return fmt.Errorf("mixed training batch counter limit exceeded")
	}
	schedule, err := NewMixedSchedule(x, c.Config.Seed)
	if err != nil {
		return err
	}
	if o.Stderr == nil {
		log, err := os.OpenFile(filepath.Join(o.Directory, "engine.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return err
		}
		defer log.Close()
		o.Stderr = log
	}
	o.Stderr = &collectionLog{writer: o.Stderr}
	engine, err := battleenv.Start(ctx, o.Command, o.Stderr)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, engine.Close()) }()
	if engine.Metadata() != x.Parts[0].Experiment.Environment {
		return fmt.Errorf("mixed engine environment differs from frozen experiment")
	}
	workers, err := startCollectionWorkers(ctx, engine, o.Command, o.Workers, o.Stderr)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, workers.closeAdditional()) }()
	if !o.Resume {
		c.Start, err = saveLearning(filepath.Join(o.Directory, "learning"), learning)
		if err != nil {
			return err
		}
		c.Learning = c.Start
		if c.Config.InitialPolicyScale != 0 {
			c.Initialization, err = savePolicyInitialization(o.Directory, c.Config, o.InitialModel, c.Config.InitialPolicyScale, learning)
			if err != nil {
				return err
			}
		}
		if err := saveMixedCheckpoint(o.Directory, c, x); err != nil {
			return err
		}
	}
	emit := func(event string, report *Report) error {
		p := MixedProgress{Event: event, CompletedBatches: len(c.Reports), Games: c.NextGame, PendingMatches: len(c.Pending), CollectionWorkers: o.Workers, Learning: c.Learning, Report: report}
		var limit error
		if o.StopAtDataBytes > 0 {
			p.DataBytes, err = trainingDataBytes(ctx, o.Directory)
			if err != nil {
				return err
			}
			p.StopAtDataBytes = o.StopAtDataBytes
			if p.DataBytes >= o.StopAtDataBytes {
				p.Event, p.StorageCheckEvent = "storage_limit_reached", event
				limit = &StorageLimitError{Bytes: p.DataBytes, Limit: o.StopAtDataBytes}
			}
		}
		if o.Progress != nil {
			if err := o.Progress(p); err != nil {
				return err
			}
		}
		return limit
	}
	if err := emit("mixed_ready", nil); err != nil {
		return err
	}
	for len(c.Reports) < end {
		cache := map[string]LearningState{}
		for len(c.Pending) < schedule.BatchMatches() {
			jobs := make([]collectionJob, min(o.Workers, schedule.BatchMatches()-len(c.Pending)))
			for i := range jobs {
				game, err := schedule.Game(c.NextGame + uint64(i))
				if err != nil {
					return err
				}
				policies, _, err := mixedPolicies(o.Directory, c.Config, game, learning, history, cache)
				if err != nil {
					return err
				}
				rng := [2]*rand.Rand{rand.New(rand.NewSource(gameSeed(c.Config.Seed, game.Index, 96))), rand.New(rand.NewSource(gameSeed(c.Config.Seed, game.Index, 97)))}
				jobs[i] = collectionJob{scenario: game.Scenario, group: game.Group, policies: policies, random: rng}
			}
			results, err := workers.collect(ctx, jobs)
			if err != nil {
				return err
			}
			for _, pair := range results {
				if err := ctx.Err(); err != nil {
					return err
				}
				shard, err := SaveShard(filepath.Join(o.Directory, "shards"), pair[:])
				if err != nil {
					return err
				}
				c.Pending = append(c.Pending, shard.Digest)
				c.NextGame++
				if err := saveMixedCheckpoint(o.Directory, c, x); err != nil {
					return err
				}
				if err := emit("mixed_collected", nil); err != nil {
					return err
				}
			}
		}
		batch, err := loadMixedBatch(ctx, o.Directory, c.Config, x, schedule, c.NextGame-uint64(len(c.Pending)), c.Pending, learning, history, map[string]bool{})
		if err != nil {
			return err
		}
		before, err := loadLearning(filepath.Join(o.Directory, "learning"), c.Learning)
		if err != nil {
			return err
		}
		report, err := TrainMixed(ctx, learning.Model, learning.Optimizer, batch, c.Config.PPO, x.Mixture)
		if err != nil {
			return err
		}
		if err := validateMixedReport(report, c.Config, x.Mixture, batch, before, learning); err != nil {
			return err
		}
		after, err := saveLearning(filepath.Join(o.Directory, "learning"), learning)
		if err != nil {
			return err
		}
		recipe, _ := Digest(c.Config)
		receipt := MixedBatchReceipt{Schema: "commander-mixed-batch-v1", Recipe: recipe, Batch: len(c.Reports), Before: c.Learning, After: after, Shards: append([]string(nil), c.Pending...), Report: report}
		ref, err := Digest(receipt)
		if err != nil {
			return err
		}
		if err := writeObject(filepath.Join(o.Directory, "mixed-reports", ref+".json"), receipt); err != nil {
			return err
		}
		c.Reports, c.Pending, c.Learning = append(c.Reports, ref), nil, after
		if err := saveMixedCheckpoint(o.Directory, c, x); err != nil {
			return err
		}
		history = retainMixedHistory(history, after, report)
		if err := emit("mixed_batch_committed", &report); err != nil {
			return err
		}
	}
	return nil
}
