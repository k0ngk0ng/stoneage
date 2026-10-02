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

func verifyEvaluationCommand(ctx context.Context, args []string, out io.Writer) error {
	f := flag.NewFlagSet("sactl ai verify-evaluation", flag.ContinueOnError)
	f.SetOutput(out)
	report := f.String("report", "", "recorded evaluation JSON; requires its adjacent <report>.data directory")
	if e := f.Parse(args); e != nil {
		if e == flag.ErrHelp {
			return nil
		}
		return e
	}
	if *report == "" || f.NArg() != 0 {
		return fmt.Errorf("verify-evaluation requires --report and no positional arguments")
	}
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()
	r, e := battletrain.VerifyEvaluation(ctx, *report)
	if e != nil {
		return e
	}
	id, e := battletrain.Digest(r)
	if e != nil {
		return e
	}
	_, e = out.Write(append(enc(Object{"event": "evaluation_verified", "report_digest": id, "evidence": r.Evidence, "candidate_artifact": r.CandidateArtifact, "split": r.Split, "games": len(r.Games), "comparisons": r.Comparisons, "unverified_source_artifacts": r.UnverifiedSourceArtifacts, "arena_certified": false}), '\n'))
	return e
}
