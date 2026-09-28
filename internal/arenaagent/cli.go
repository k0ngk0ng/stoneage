package arenaagent

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
)

type pathsFlag []string

func (p *pathsFlag) String() string     { return fmt.Sprint([]string(*p)) }
func (p *pathsFlag) Set(v string) error { *p = append(*p, v); return nil }
func Main(ctx context.Context, args []string, version string, out io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "help" {
		_, e := fmt.Fprintln(out, "arena-agent: local StoneAge squad commander\nCommands: version, init, check, run, train, evaluate, simulate\nUse <command> --help for options. Online play requires sactl; no Python or Docker.")
		return e
	}
	if args[0] == "version" {
		_, e := fmt.Fprintln(out, "arena-agent", version)
		return e
	}
	command := args[0]
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	f.SetOutput(out)
	if command == "init" {
		dir := f.String("directory", "", "new local team directory")
		mode := f.Int("mode", 1, "team size 1–5")
		if e := f.Parse(args[1:]); e != nil {
			if e == flag.ErrHelp {
				return nil
			}
			return e
		}
		v, e := Init(*dir, *mode)
		if e != nil {
			return e
		}
		_, e = out.Write(append(enc(v), '\n'))
		return e
	}
	if command == "simulate" || command == "native-simulate" {
		var s Simulation
		f.StringVar(&s.Root, "root", ".", "repository root with prepared native binaries")
		f.StringVar(&s.Work, "work", "", "new directory under build/")
		f.IntVar(&s.Mode, "mode", 1, "team size 1–5")
		f.IntVar(&s.Matches, "matches", 2, "matches to collect")
		f.StringVar(&s.Strategy, "strategy", "basic", "basic, learned or explore")
		f.StringVar(&s.Model, "model", "", "trained model")
		f.Int64Var(&s.Seed, "seed", 1, "exploration seed")
		f.StringVar(&s.Image, "image", "gcc:13-bookworm", "already installed compatible image; never pulled")
		f.BoolVar(&s.Reconnect, "reconnect", false, "verify daemon recovery")
		if e := f.Parse(args[1:]); e != nil {
			if e == flag.ErrHelp {
				return nil
			}
			return e
		}
		if s.Work == "" {
			return fmt.Errorf("--work required")
		}
		s.Output = out
		ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
		defer cancel()
		if command == "simulate" {
			return Simulate(ctx, s)
		}
		return NativeSimulate(ctx, s)
	}
	config := f.String("config", "", "team JSON")
	model := f.String("model", "", "trained model")
	output := f.String("output", "", "new model path")
	matches := f.Int("matches", 1, "number of matches")
	forever := f.Bool("forever", false, "continuous matchmaking")
	seed := f.Int64("seed", 1, "training seed")
	epochs := f.Int("epochs", 80, "training epochs")
	var databases pathsFlag
	f.Var(&databases, "database", "SQLite dataset (repeatable)")
	if e := f.Parse(args[1:]); e != nil {
		if e == flag.ErrHelp {
			return nil
		}
		return e
	}
	emit := func(v any, e error) error {
		if e != nil {
			return e
		}
		_, e = out.Write(append(enc(v), '\n'))
		return e
	}
	switch command {
	case "train":
		v, e := Train(databases, *output, *seed, *epochs)
		return emit(v, e)
	case "evaluate":
		v, e := Evaluate(databases, *model)
		return emit(v, e)
	case "check", "run":
		if *config == "" {
			return fmt.Errorf("--config required")
		}
		c, e := LoadConfig(*config)
		if e != nil {
			return e
		}
		s, e := makeStrategy(c)
		if e != nil {
			return e
		}
		if command == "check" {
			ids := []string{}
			for _, m := range c.Members {
				ids = append(ids, m.ID)
			}
			return emit(Object{"ok": true, "mode": c.Mode, "strategy": s.ID(), "members": ids}, nil)
		}
		if *matches < 1 {
			return fmt.Errorf("matches must be positive; use --forever")
		}
		r, e := NewRunner(c)
		if e != nil {
			return e
		}
		r.Output = out
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		signals := make(chan os.Signal, 2)
		signal.Notify(signals, os.Interrupt)
		defer signal.Stop(signals)
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-signals:
					if r.Stop.Swap(true) {
						cancel()
					} else {
						r.report("stopping_after_match", Object{})
					}
				}
			}
		}()
		n := *matches
		if *forever {
			n = 0
		}
		return r.Run(ctx, n)
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}
