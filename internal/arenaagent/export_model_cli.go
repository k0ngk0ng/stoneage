package arenaagent

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
	"github.com/k0ngk0ng/stoneage/internal/battletrain"
)

// Re-export committed weights without starting an engine or taking a training
// step. This also recovers provenance from the saved experiment of an older
// artifact. Existing content-addressed models remain immutable.
func exportModelCommand(ctx context.Context, args []string, out io.Writer) error {
	f := flag.NewFlagSet("sactl ai export-model", flag.ContinueOnError)
	f.SetOutput(out)
	model := f.String("model", "", "convert an existing neural model without training; use --output file.safetensors")
	root := f.String("data-dir", "", "existing training directory with a completed checkpoint")
	checkpoint := f.String("checkpoint", "", "saved checkpoint digest to export; default latest committed state; does not rewind training")
	output := f.String("output", "", "new candidate path; default content-addressed .safetensors under data-dir/models; explicit .json for old clients")
	if e := f.Parse(args); e != nil {
		if e == flag.ErrHelp {
			return nil
		}
		return e
	}
	if f.NArg() != 0 || (*root == "") == (*model == "") || *model != "" && (*checkpoint != "" || !strings.EqualFold(filepath.Ext(*output), ".safetensors")) {
		return fmt.Errorf("export-model requires either --data-dir, or --model with --output file.safetensors; --checkpoint is only for --data-dir; no positional arguments")
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	if *model != "" {
		a, err := battlepolicy.LoadArtifact(*model)
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = battlepolicy.SaveArtifact(*output, a); err != nil {
			return err
		}
		_, err = out.Write(append(enc(Object{"event": "model_converted", "model": *output, "format": "safetensors", "weights_digest": a.WeightsDigest, "training_performed": false}), '\n'))
		return err
	}
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()
	mixed, e := hasMixedCheckpoint(*root)
	if e != nil {
		return e
	}
	feedback, e := hasFeedbackCheckpoint(*root)
	if e != nil {
		return e
	}
	recorded, e := hasDemonstrationCheckpoint(*root)
	if e != nil {
		return e
	}
	var path string
	if mixed {
		path, e = battletrain.ExportMixedCandidate(ctx, *root, *checkpoint, *output)
	} else if feedback {
		path, e = battletrain.ExportFeedbackCandidate(ctx, *root, *checkpoint, *output)
	} else if recorded {
		path, e = battletrain.ExportDemonstrationCandidate(ctx, *root, *checkpoint, *output)
	} else {
		path, e = battletrain.ExportCheckpointCandidateContext(ctx, *root, *checkpoint, *output)
	}
	if e != nil {
		return e
	}
	_, e = out.Write(append(enc(Object{"event": "candidate_saved", "model": path, "status": "candidate", "arena_certified": false}), '\n'))
	return e
}
