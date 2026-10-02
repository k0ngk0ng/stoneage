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
		_, e := fmt.Fprintln(out, "sactl ai: local StoneAge squad commander\nCommands: version, init, check, run, environment, experiment, experiment-mix, experiment-compare, train, collect-feedback, train-feedback, evaluate, verify-evaluation, compare-evaluations, build-search, build-pool, build-validate, export-data, import-demonstrations, export-model, league, champion, simulate\nUse <command> --help for options. Online play uses this sactl executable; no Python or Docker.")
		return e
	}
	if args[0] == "version" {
		_, e := fmt.Fprintln(out, "sactl ai", version)
		return e
	}
	command := args[0]
	if command == "build-validate" {
		return allocationCommand(ctx, args[1:], out)
	}
	if command == "experiment-compare" {
		return validationComparisonCommand(ctx, args[1:], out)
	}
	if command == "experiment-mix" {
		return mixedExperimentCommand(ctx, args[1:], out)
	}
	if command == "collect-feedback" {
		return collectFeedbackCommand(ctx, args[1:], out)
	}
	if command == "train-feedback" {
		return trainFeedbackCommand(ctx, args[1:], out)
	}
	if command == "compare-evaluations" {
		return compareEvaluationsCommand(ctx, args[1:], out)
	}
	if command == "import-demonstrations" {
		return importDemonstrationsCommand(ctx, args[1:], out)
	}
	if command == "export-data" {
		return exportDataCommand(ctx, args[1:], out)
	}
	if command == "environment" {
		return environmentCommand(ctx, args[1:], out)
	}
	if command == "champion" {
		return championCommand(ctx, args[1:], out)
	}
	if command == "verify-evaluation" {
		return verifyEvaluationCommand(ctx, args[1:], out)
	}
	if command == "league" {
		return leagueCommand(ctx, args[1:], out)
	}
	if command == "build-pool" {
		return buildPoolCommand(ctx, args[1:], out)
	}
	if command == "export-model" {
		return exportModelCommand(ctx, args[1:], out)
	}
	if command == "build-search" {
		return buildSearchCommand(ctx, args[1:], out)
	}
	if command == "experiment" {
		return experimentCommand(ctx, args[1:], out)
	}
	if command == "train" {
		return trainCommand(ctx, args[1:], out)
	}
	if command == "evaluate" {
		return evaluateCommand(ctx, args[1:], out)
	}
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
		f.StringVar(&s.OpponentStrategy, "opponent-strategy", "basic", "other commander: basic, learned or explore")
		f.StringVar(&s.OpponentModel, "opponent-model", "", "other commander's trained model; requires --opponent-strategy learned")
		f.StringVar(&s.NativeDirectory, "native-dir", "", "prepared gmsv/saac binaries under build/; default build/local-arena/native")
		f.StringVar(&s.Allocation, "allocation", "", "new fixture characters: vital,strength,toughness,dexterity, integer sum 20; both sides share it")
		f.Var((*pathsFlag)(&s.MemberAllocations), "member-allocation", "per-seat creation allocation, repeat exactly --mode times; both teams use the same roster")
		f.IntVar(&s.FixtureLevel, "fixture-level", 1, "isolated saved profiles: level 1..140; above 1 scale allocations to 20+3*(level-1) before relogin")
		f.BoolVar(&s.RequireWithdrawal, "require-withdrawal", false, "require a fresh policy decision after its original history observer is knocked out")
		f.IntVar(&s.MinBattleTurns, "min-battle-turns", 0, "require at least one settled match to reach this decision turn count")
		f.Int64Var(&s.Seed, "seed", 1, "exploration seed")
		f.StringVar(&s.Image, "image", "gcc:13-bookworm", "already installed compatible image; never pulled")
		f.BoolVar(&s.Reconnect, "reconnect", false, "verify daemon recovery")
		f.DurationVar(&s.Timeout, "timeout", 0, "overall isolated test timeout, including login/queueing/recovery; 0 uses max(10m, matches*2m)")
		if e := f.Parse(args[1:]); e != nil {
			if e == flag.ErrHelp {
				return nil
			}
			return e
		}
		if s.Work == "" {
			return fmt.Errorf("--work required")
		}
		if s.FixtureLevel < 1 || s.FixtureLevel > 140 {
			return fmt.Errorf("--fixture-level must be 1..140")
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
	matches := f.Int("matches", 1, "number of matches")
	forever := f.Bool("forever", false, "continuous matchmaking")
	online := onlineFlags{}
	online.bind(f)
	if e := f.Parse(args[1:]); e != nil {
		if e == flag.ErrHelp {
			return nil
		}
		return e
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", f.Args())
	}
	emit := func(v any, e error) error {
		if e != nil {
			return e
		}
		_, e = out.Write(append(enc(v), '\n'))
		return e
	}
	switch command {
	case "check", "run":
		c, e := online.config(*config, f)
		if e != nil {
			return e
		}
		if command == "check" {
			s, selection, e := configuredStrategy(ctx, c)
			if e != nil {
				return e
			}
			ids := []string{}
			for _, m := range c.Members {
				ids = append(ids, m.ID)
			}
			fields := Object{"ok": true, "mode": c.Mode, "strategy": s.ID(), "members": ids}
			if selection != nil {
				fields["selection"] = selection
			}
			return emit(fields, nil)
		}
		if *matches < 1 {
			return fmt.Errorf("matches must be positive; use --forever")
		}
		r, e := newRunner(ctx, c)
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
