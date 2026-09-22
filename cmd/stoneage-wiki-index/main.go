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
	flag.Parse()
	if err := gamewiki.BuildSnapshot(context.Background(), *root, *out); err != nil {
		log.Fatal(err)
	}
}
