package arenaagent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
	"github.com/k0ngk0ng/stoneage/internal/battletrain"
)

func hasDemonstrationCheckpoint(directory string) (bool, error) {
	recorded := false
	for _, name := range []string{battletrain.DemonstrationPointer, "demonstration-checkpoints"} {
		_, err := os.Lstat(filepath.Join(directory, name))
		if err == nil {
			recorded = true
		} else if !os.IsNotExist(err) {
			return false, err
		}
	}
	if recorded {
		if _, err := os.Lstat(filepath.Join(directory, "latest.json")); err == nil {
			return false, fmt.Errorf("ambiguous training directory contains both native and demonstration state")
		} else if !os.IsNotExist(err) {
			return false, err
		}
	}
	return recorded, nil
}

func trainDemonstrationsCommand(ctx context.Context, directory, dataset string, resume bool, epochs int, seed int64, update battletrain.ImitationConfig, provided map[string]bool, out io.Writer) error {
	for flag := range provided {
		allowed := flag == "data-dir" || flag == "resume" || flag == "epochs"
		if !resume {
			allowed = allowed || flag == "demonstrations" || flag == "seed" || flag == "sequence-length" || flag == "learning-rate" || flag == "gradient-clip" || flag == "batch-episodes" || flag == "action-weighting"
		}
		if !allowed {
			return fmt.Errorf("--%s cannot be used in this demonstration training invocation; resume uses only stored data and settings", flag)
		}
	}
	if directory == "" || !resume && dataset == "" {
		return fmt.Errorf("demonstration training requires --data-dir and --demonstrations; resume uses --data-dir --resume")
	}
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()
	var config *battletrain.DemonstrationRunConfig
	if !resume {
		ds, _, err := battletrain.LoadDemonstrations(ctx, dataset)
		if err != nil {
			return err
		}
		network := battlepolicy.NetworkConfig()
		network.InputSchema = ds[0].Features
		config = &battletrain.DemonstrationRunConfig{Seed: seed, Network: network, Update: update}
	}
	err := battletrain.RunDemonstrations(ctx, battletrain.DemonstrationRunOptions{Directory: directory, Dataset: dataset, Resume: resume, Config: config, Epochs: epochs,
		Progress: func(p battletrain.DemonstrationProgress) error { _, err := out.Write(append(enc(p), '\n')); return err },
	})
	if errors.Is(err, context.Canceled) {
		c, _, _, loadErr := battletrain.LoadDemonstrationCheckpoint(context.Background(), directory)
		if _, writeErr := out.Write(append(enc(Object{"event": "demonstration_training_interrupted", "checkpoint_available": loadErr == nil, "completed_epochs": len(c.Reports)}), '\n')); writeErr != nil {
			return writeErr
		}
	}
	return err
}
