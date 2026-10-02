package arenaagent

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/k0ngk0ng/stoneage/internal/battletrain"
)

func buildPoolCommand(ctx context.Context, args []string, out io.Writer) error {
	f := flag.NewFlagSet("sactl ai build-pool", flag.ContinueOnError)
	f.SetOutput(out)
	root := f.String("search-dir", "", "completed build-search directory with native evidence")
	output := f.String("output", "", "immutable pool JSON; default search-dir/build-pool.json")
	if e := f.Parse(args); e != nil {
		if e == flag.ErrHelp {
			return nil
		}
		return e
	}
	if *root == "" || f.NArg() != 0 {
		return fmt.Errorf("build-pool requires --search-dir and no positional arguments")
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	pool, e := battletrain.ExportBuildPool(*root, *output)
	if e != nil {
		return e
	}
	id, _ := battletrain.Digest(pool)
	path := *output
	if path == "" {
		path = filepath.Join(*root, "build-pool.json")
	}
	_, e = out.Write(append(enc(Object{"event": "build_pool_frozen", "pool": id, "manifest": path, "rosters": len(pool.Rosters), "reserved_groups": len(pool.SelectionGroups), "recorded_selection_artifacts": pool.RecordedSelectionArtifacts}), '\n'))
	return e
}
