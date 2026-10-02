package arenaagent

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/k0ngk0ng/stoneage/internal/battlerecords"
)

func exportDataCommand(ctx context.Context, args []string, out io.Writer) error {
	f := flag.NewFlagSet("sactl ai export-data", flag.ContinueOnError)
	f.SetOutput(out)
	var c battlerecords.Config
	f.StringVar(&c.Root, "records", "", "existing server battle-record root or single match directory")
	f.StringVar(&c.Output, "output", "", "new JSONL file; parent directory must exist; never overwritten")
	f.StringVar(&c.Format, "format", "transitions", "transitions or builds; legacy observations/outcomes, not PPO shards")
	f.StringVar(&c.Mode, "mode", "pvp-1v1", "pvp-1v1, pvp, pve or all")
	f.BoolVar(&c.EqualPoints, "equal-points", false, "require equal observed player point budgets in 1v1 PvP")
	f.BoolVar(&c.IncludeAbnormal, "include-abnormal", false, "include other recorded end reasons; retain outcome labels")
	if err := f.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if c.Root == "" || c.Output == "" || f.NArg() != 0 {
		return fmt.Errorf("export-data requires --records and --output and no positional arguments")
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	report, err := battlerecords.Export(ctx, c)
	if err != nil {
		// Structured counts are useful even when every source was excluded.
		report.Event = "records_export_failed"
	}
	if _, writeErr := out.Write(append(enc(report), '\n')); writeErr != nil {
		return writeErr
	}
	return err
}
