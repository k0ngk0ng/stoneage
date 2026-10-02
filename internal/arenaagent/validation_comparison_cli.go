package arenaagent

import (
	"context"
	"flag"
	"fmt"
	"github.com/k0ngk0ng/stoneage/internal/battletrain"
	"io"
)

func validationComparisonCommand(ctx context.Context, args []string, out io.Writer) error {
	f := flag.NewFlagSet("sactl ai experiment-compare", flag.ContinueOnError)
	f.SetOutput(out)
	left := f.String("left-experiment", "", "candidate's frozen mixed experiment")
	right := f.String("right-experiment", "", "opponent's frozen mixed experiment with exactly the same families/partitions")
	output := f.String("output", "", "new immutable validation-only comparison declaration")
	if err := f.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if f.NArg() != 0 || *left == "" || *right == "" || *output == "" {
		return fmt.Errorf("experiment-compare requires --left-experiment, --right-experiment and --output")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	a, err := battletrain.LoadMixedExperiment(*left)
	if err != nil {
		return err
	}
	b, err := battletrain.LoadMixedExperiment(*right)
	if err != nil {
		return err
	}
	x, err := battletrain.NewMixedValidationComparison(a, b)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	id, err := battletrain.SaveMixedValidationComparison(*output, x)
	if err != nil {
		return err
	}
	_, err = out.Write(append(enc(Object{"event": "validation_comparison_created", "comparison": id, "manifest": *output, "split": "validation", "arena_certified": false}), '\n'))
	return err
}
