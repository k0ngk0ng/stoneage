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

func compareEvaluationsCommand(ctx context.Context, args []string, out io.Writer) error {
	f := flag.NewFlagSet("sactl ai compare-evaluations", flag.ContinueOnError)
	f.SetOutput(out)
	baseline := f.String("baseline", "", "baseline frozen validation report; requires adjacent .data evidence")
	candidate := f.String("candidate", "", "candidate validation report from the same experiment and complete opponent set")
	if e := f.Parse(args); e != nil {
		if e == flag.ErrHelp {
			return nil
		}
		return e
	}
	if *baseline == "" || *candidate == "" || f.NArg() != 0 {
		return fmt.Errorf("compare-evaluations requires --baseline and --candidate, with no positional arguments")
	}
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()
	r, e := battletrain.CompareEvaluations(ctx, *baseline, *candidate)
	if e != nil {
		return e
	}
	_, e = out.Write(append(enc(r), '\n'))
	return e
}
