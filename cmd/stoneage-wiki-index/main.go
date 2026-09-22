// stoneage-wiki-index generates the static encyclopedia locally before release.
package main

import (
	"context"
	"flag"
	"github.com/k0ngk0ng/stoneage/internal/gamewiki"
	"log"
)

func main() {
	root := flag.String("data", "server/legacy/source/2.5/gmsv/data", "native data directory")
	out := flag.String("output", "internal/gamewiki/snapshot", "static snapshot directory")
	media := flag.String("media", "", "prebuilt media references JSON")
	flag.Parse()
	if err := gamewiki.BuildSnapshotWithMedia(context.Background(), *root, *out, *media); err != nil {
		log.Fatal(err)
	}
}
