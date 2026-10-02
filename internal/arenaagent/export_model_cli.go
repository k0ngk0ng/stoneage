package arenaagent

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/k0ngk0ng/stoneage/internal/battletrain"
)

// Re-export committed weights without starting an engine or taking a training
// step. This also recovers provenance from the saved experiment of an older
// artifact. Existing content-addressed models remain immutable.
func exportModelCommand(ctx context.Context, args []string, out io.Writer) error {
	f := flag.NewFlagSet("sactl ai export-model", flag.ContinueOnError)
	f.SetOutput(out)
	root := f.String("data-dir", "", "existing training directory with a completed checkpoint")
	checkpoint := f.String("checkpoint", "", "saved checkpoint digest to export; default latest committed state; does not rewind training")
	output := f.String("output", "", "new candidate path; default content-addressed file under data-dir/models")
	if e := f.Parse(args); e != nil {
		if e == flag.ErrHelp {
			return nil
		}
		return e
	}
	if *root == "" || f.NArg() != 0 {
		return fmt.Errorf("export-model requires --data-dir and no positional arguments")
	}
	if e := ctx.Err(); e != nil {
		return e
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
