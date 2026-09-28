package main

import (
	"context"
	"fmt"
	"github.com/k0ngk0ng/stoneage/internal/arenaagent"
	"os"
)

var version = "dev"

func main() {
	if e := arenaagent.Main(context.Background(), os.Args[1:], version, os.Stdout); e != nil {
		fmt.Fprintln(os.Stderr, "arena-agent:", e)
		os.Exit(1)
	}
}
