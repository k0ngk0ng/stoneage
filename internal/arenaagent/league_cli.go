package arenaagent

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/k0ngk0ng/stoneage/internal/battletrain"
)

func leagueCommand(ctx context.Context, args []string, out io.Writer) error {
	f := flag.NewFlagSet("sactl ai league", flag.ContinueOnError)
	f.SetOutput(out)
	root := f.String("data-dir", "", "training directory; verify immutable training matrix and opponent sampling weights")
	if e := f.Parse(args); e != nil {
		if e == flag.ErrHelp {
			return nil
		}
		return e
	}
	if *root == "" || f.NArg() != 0 {
		return fmt.Errorf("league requires --data-dir and no positional arguments")
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	v, e := battletrain.InspectLeague(ctx, *root)
	if e != nil {
		return e
	}
	_, e = out.Write(append(enc(v), '\n'))
	return e
}
