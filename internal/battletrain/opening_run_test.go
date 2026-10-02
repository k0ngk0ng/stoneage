package battletrain

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type openingCancelContext struct {
	context.Context
	cancel context.CancelFunc
	checks atomic.Int64
}

func (c *openingCancelContext) Err() error {
	if c.checks.Add(1) == 6 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestOpeningScheduleAndConfig(t *testing.T) {
	c := DefaultRunConfig()
	c.OpeningRollouts = 4
	checkpoint := Checkpoint{Config: c}
	for game := uint64(0); game < 128; game += 4 {
		s, group := trainingScenario(c, game, nil)
		choice := checkpoint.opponentAt(game)
		seeds := map[int64]bool{}
		for i := game; i < game+4; i++ {
			other, otherGroup := trainingScenario(c, i, nil)
			if !reflect.DeepEqual(s, other) || group != otherGroup || checkpoint.opponentAt(i) != choice {
				t.Fatal("opening changed within group")
			}
			seed := gameSeed(c.Seed, i, 3)
			if seeds[seed] {
				t.Fatal("repeated action RNG")
			}
			seeds[seed] = true
		}
	}
	for _, n := range []int{-1, 1, 3, 65} {
		bad := c
		bad.OpeningRollouts = n
		if bad.Validate() == nil {
			t.Fatal("bad replicate count accepted", n)
		}
	}
	c.PolicyAdvantage = "opening-loo"
	if e := c.Validate(); e != nil {
		t.Fatal(e)
	}
	c.PPO.Gamma = .9
	if c.Validate() == nil {
		t.Fatal("discounted LOO accepted")
	}
	c.PolicyAdvantage = ""
	if e := c.Validate(); e != nil {
		t.Fatal("GAE control should allow discount", e)
	}
	x := experimentFixture(t)
	for game := uint64(0); game < 256; game++ {
		s, group := trainingScenario(c, game, &x)
		want, wantGroup := x.trainingScenario(c.Seed, game/4)
		if !reflect.DeepEqual(s, want) || group != wantGroup {
			t.Fatal("repeated frozen experiment schedule changed")
		}
	}
	c.OpeningRollouts = 0
	for game := uint64(0); game < 128; game++ {
		legacy, group := scenarioFor(c, game)
		current, currentGroup := trainingScenario(c, game, nil)
		if !reflect.DeepEqual(legacy, current) || group != currentGroup {
			t.Fatal("default schedule changed")
		}
	}
}

func TestOpeningLogicalGroupsMayRevisitIdenticalSetup(t *testing.T) {
	m := testModel(t)
	samples := openingFixture(t, m, []float64{1, 1, -1, -1})
	for i := range samples {
		samples[i].Opening = []string{"first", "first", "second", "second"}[i]
	}
	_, a, s, e := openingAdvantages(context.Background(), m, samples, 2)
	if e != nil || s.groups != 2 || s.varying != 0 || !reflect.DeepEqual(a, []float64{0, 0, 0, 0}) {
		t.Fatal("identical setups merged distinct collection groups", a, s, e)
	}
	samples[0].Opening = "second"
	if _, _, _, e := openingAdvantages(context.Background(), m, samples, 2); e == nil {
		t.Fatal("malformed logical group accepted")
	}
}

func TestNativeOpeningResumeAndEvidence(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit native environment required")
	}
	var command []string
	if e := json.Unmarshal([]byte(raw), &command); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	for _, estimator := range []string{"", "opening-loo"} {
		t.Run("estimator="+estimator, func(t *testing.T) {
			c := DefaultRunConfig()
			c.Network = testModel(t).Config
			c.Seed = 71
			c.Warmup = nil
			c.PetPoints = 0
			c.MaxTurns = 200
			c.PPO.Epochs = 1
			c.BatchMatches, c.OpeningRollouts, c.PolicyAdvantage = 4, 2, estimator
			if estimator == "" {
				c.OpponentSampling = "uniform"
			}
			one, two := t.TempDir(), t.TempDir()
			if e := Run(ctx, RunOptions{Directory: one, Command: command, Config: &c, Batches: 2, Stderr: io.Discard}); e != nil {
				t.Fatal(e)
			}
			stop := errors.New("interrupt committed progress")
			interruptAt := func(event string) func(Progress) error {
				return func(p Progress) error {
					if p.Event == event {
						return stop
					}
					return nil
				}
			}
			e := Run(ctx, RunOptions{Directory: two, Command: command, Config: &c, Batches: 2, Stderr: io.Discard, Progress: interruptAt("collected")})
			if !errors.Is(e, stop) {
				t.Fatal(e)
			}
			partial, _, e := LoadCheckpoint(two)
			if e != nil || partial.NextGame != 1 || len(partial.Pending) != 1 || partial.Schema != 4 {
				t.Fatal("partial repeat not saved", e)
			}
			e = Run(ctx, RunOptions{Directory: two, Command: command, Resume: true, Batches: 2, Stderr: io.Discard, Progress: interruptAt("trained_candidate")})
			if !errors.Is(e, stop) {
				t.Fatal(e)
			}
			if e := Run(ctx, RunOptions{Directory: two, Command: command, Resume: true, Batches: 1, Stderr: io.Discard}); e != nil {
				t.Fatal(e)
			}
			a, sa, e := LoadCheckpoint(one)
			if e != nil {
				t.Fatal(e)
			}
			b, sb, e := LoadCheckpoint(two)
			if e != nil {
				t.Fatal(e)
			}
			if a.NextGame != 8 || b.NextGame != 8 || b.CompletedBatches != 2 || !reflect.DeepEqual(sa, sb) {
				t.Fatal("resumed model/Adam differs")
			}
			if len(b.OpeningReports) != 2 || sb.Optimizer.Step < 1 {
				t.Fatal("no committed updates")
			}
			canceled, cancelLoad := context.WithCancel(ctx)
			cancelLoad()
			if _, _, e := LoadCheckpointContext(canceled, two); !errors.Is(e, context.Canceled) {
				t.Fatal("historical verification ignored cancellation", e)
			}
			for _, checkpoint := range []string{"", mustCheckpointDigest(t, b)} {
				base, cancelExport := context.WithCancel(ctx)
				during := &openingCancelContext{Context: base, cancel: cancelExport}
				output := filepath.Join(two, "canceled-model.json")
				_, e := ExportCheckpointCandidateContext(during, two, checkpoint, output)
				cancelExport()
				if !errors.Is(e, context.Canceled) || during.checks.Load() < 6 {
					t.Fatal("export did not cancel within source verification", e)
				}
				if _, e := os.Stat(output); !os.IsNotExist(e) {
					t.Fatal("canceled export published model", e)
				}
			}
			if _, e := ExportCandidate(two, ""); e != nil {
				t.Fatal(e)
			}
			original := b
			for _, tamper := range []string{"sample-digest", "estimator", "shards", "before", "after", "groups"} {
				t.Run(tamper, func(t *testing.T) {
					var receipt OpeningReceipt
					if e := readObject(filepath.Join(two, "openings", original.OpeningReports[1]+".json"), &receipt, 4<<20); e != nil {
						t.Fatal(e)
					}
					switch tamper {
					case "sample-digest":
						receipt.Report.SamplesDigest = strings.Repeat("0", 64)
					case "estimator":
						receipt.Report.Method = "unrecorded-estimator"
					case "shards":
						receipt.Shards[0], receipt.Shards[1] = receipt.Shards[1], receipt.Shards[0]
					case "before":
						receipt.Before = receipt.After
					case "after":
						receipt.After = receipt.Before
					case "groups":
						receipt.Report.Groups++
					}
					id, e := saveOpeningReceipt(two, receipt)
					if e != nil {
						t.Fatal(e)
					}
					bad := original
					bad.OpeningReports = append([]string(nil), original.OpeningReports...)
					bad.OpeningReports[1] = id
					if e := saveCheckpoint(two, bad); e != nil {
						t.Fatal(e)
					}
					if _, _, e := LoadCheckpoint(two); e == nil {
						t.Fatal("rehashed evidence tampering accepted")
					}
				})
			}
			if e := saveCheckpoint(two, original); e != nil {
				t.Fatal(e)
			}
		})
	}
}

func mustCheckpointDigest(t *testing.T, c Checkpoint) string {
	t.Helper()
	id, e := Digest(c)
	if e != nil {
		t.Fatal(e)
	}
	return id
}

func TestNativeOpeningTeamCollection(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit native environment required")
	}
	var command []string
	if e := json.Unmarshal([]byte(raw), &command); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for _, mode := range []int{2, 3, 4, 5} {
		c := DefaultRunConfig()
		c.Network, c.Warmup = testModel(t).Config, nil
		c.Mode, c.PetPoints = mode, 0
		c.BatchMatches, c.OpeningRollouts, c.PolicyAdvantage = 2, 2, "opening-loo"
		c.PPO.Epochs = 1
		dir := t.TempDir()
		if e := Run(ctx, RunOptions{Directory: dir, Command: command, Config: &c, Batches: 1, Stderr: io.Discard}); e != nil {
			t.Fatal(mode, e)
		}
		checkpoint, _, e := LoadCheckpoint(dir)
		if e != nil || checkpoint.CompletedBatches != 1 || checkpoint.NextGame != 2 {
			t.Fatal(mode, e)
		}
	}
}

func TestNativeOpeningCutoffPreservesBatch(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit native environment required")
	}
	var command []string
	if e := json.Unmarshal([]byte(raw), &command); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	c := DefaultRunConfig()
	c.Network, c.Warmup = testModel(t).Config, nil
	c.MaxTurns, c.BatchMatches, c.OpeningRollouts = 1, 2, 2
	dir := t.TempDir()
	e := Run(ctx, RunOptions{Directory: dir, Command: command, Config: &c, Batches: 1, Stderr: io.Discard})
	if e == nil || !strings.Contains(e.Error(), "cutoff") {
		t.Fatal("cutoff wasn't explicitly rejected", e)
	}
	saved, state, e := LoadCheckpoint(dir)
	if e != nil || saved.NextGame != 2 || len(saved.Pending) != 2 || saved.CompletedBatches != 0 || state.Optimizer.Step != 0 {
		t.Fatal("cutoff changed progress/weights", e)
	}
	before, _ := Digest(saved)
	if e := Run(ctx, RunOptions{Directory: dir, Command: command, Resume: true, Batches: 1, Stderr: io.Discard}); e == nil || !strings.Contains(e.Error(), "cutoff") {
		t.Fatal(e)
	}
	after, _, e := LoadCheckpoint(dir)
	id, _ := Digest(after)
	if e != nil || before != id {
		t.Fatal("retry changed committed evidence", e)
	}
}
