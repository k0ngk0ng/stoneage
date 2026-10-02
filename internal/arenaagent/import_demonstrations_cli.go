package arenaagent

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

func importDemonstrationsCommand(ctx context.Context, args []string, out io.Writer) error {
	f := flag.NewFlagSet("sactl ai import-demonstrations", flag.ContinueOnError)
	f.SetOutput(out)
	var databases pathsFlag
	f.Var(&databases, "database", "stopped, checkpointed arena.sqlite3; repeat for multiple sources; read only")
	output := f.String("output", "", "new demonstration JSONL plus .manifest.json; parent must exist")
	features := f.String("features", battlepolicy.FeatureVersion, "supported commander observation contract; incompatible choices/history excluded")
	if err := f.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if f.NArg() != 0 || len(databases) == 0 || *output == "" {
		return fmt.Errorf("import-demonstrations requires --database and --output, no positional arguments")
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	r, err := ImportDemonstrations(ctx, databases, *output, *features)
	if err != nil {
		r.Event = "demonstrations_import_failed"
	}
	if _, writeErr := out.Write(append(enc(r), '\n')); writeErr != nil {
		return writeErr
	}
	return err
}
