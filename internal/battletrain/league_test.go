package battletrain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestLeagueWeightingCoverageAndLegacySchedule(t *testing.T) {
	c := Checkpoint{Config: DefaultRunConfig()}
	c.Config.RuleOpponents = []string{"basic", "sustain"}
	c.OpponentScores = []OpponentScore{{Key: "rule:basic", Wins: 40}, {Key: "rule:sustain", Losses: 40}}
	counts := map[string]int{}
	for game := uint64(0); game < 20000; game++ {
		choice := c.opponentAt(game)
		counts[choice.Key]++
		if choice.Side != int(game%2) {
			t.Fatal("side not rotated")
		}
	}
	if counts["rule:basic"] < 500 || counts["rule:sustain"] < 3*counts["rule:basic"] || counts["self"] < 12000 {
		t.Fatal("weakness bias lost exploration or category schedule", counts)
	}
	if difficultyWeight(OpponentScore{Truncated: 100}) != difficultyWeight(OpponentScore{}) {
		t.Fatal("cutoffs counted as losses")
	}
	if difficultyWeight(OpponentScore{Draws: 100}) != 3000 {
		t.Fatal("draws not neutral")
	}
	for _, sampling := range []string{"", "uniform"} {
		c.Config.OpponentSampling = sampling
		c.Opponents = []string{fmt.Sprintf("%064d", 1), fmt.Sprintf("%064d", 2)}
		for game := uint64(0); game < 500; game++ {
			got := c.opponentAt(game)
			bucket := gameSeed(c.Config.Seed, game, 7) % 10
			want := "self"
			if bucket < 3 {
				want = "rule:" + c.Config.ruleOpponents()[gameSeed(c.Config.Seed, game, 8)%2]
			} else if bucket < 8 {
				want = "history:" + c.Opponents[gameSeed(c.Config.Seed, game, 5)%2]
			}
			if got.Key != want {
				t.Fatal("old deterministic schedule changed")
			}
		}
	}
	t.Logf("weakness schedule draws: %v", counts)
}

func TestLeagueWindowAndHistoricalRetention(t *testing.T) {
	var reports []LeagueReport
	for i := 0; i < 10; i++ {
		reports = append(reports, LeagueReport{Games: []LeagueGame{{Key: "rule:sustain", Outcome: -1}, {Key: "self", Outcome: 1}, {Key: "rule:basic", Outcome: -2}}})
	}
	scores := leagueScores(reports)
	if !reflect.DeepEqual(scores, []OpponentScore{{Key: "rule:basic", Truncated: 8}, {Key: "rule:sustain", Losses: 8}}) {
		t.Fatal("window included stale/self evidence", scores)
	}
	var pool []string
	for i := 0; i < 32; i++ {
		pool = append(pool, fmt.Sprintf("%064d", i+1))
	}
	oldest := pool[0]
	latest := fmt.Sprintf("%064d", 33)
	kept := retainOpponents(pool, latest, []OpponentScore{{Key: "history:" + oldest, Losses: 50}}, true)
	if len(kept) != 32 || kept[0] != oldest || kept[31] != latest || !reflect.DeepEqual(kept[16:], append(pool[17:], latest)) {
		t.Fatal("forgot hard opponent or recent diversity", kept)
	}
	legacy := retainOpponents(pool, latest, nil, false)
	if legacy[0] != pool[1] || legacy[31] != latest {
		t.Fatal("legacy pool changed")
	}
}

func TestRejectedUpdateDoesNotDuplicateOpponentState(t *testing.T) {
	c := Checkpoint{Config: DefaultRunConfig(), Opponents: []string{"one", "two"}}
	got := c.retainOpponent("one")
	if !reflect.DeepEqual(got, []string{"two", "one"}) || !reflect.DeepEqual(c.Opponents, []string{"one", "two"}) {
		t.Fatal("unchanged state duplicated or source pool mutated", got)
	}
	c.Opponents = got
	if !reflect.DeepEqual(c.retainOpponent("one"), got) {
		t.Fatal("repeated rejection changed pool")
	}
	c.Config.PPO.UpdateGuard = ""
	if !reflect.DeepEqual(c.retainOpponent("one"), []string{"two", "one", "one"}) {
		t.Fatal("old pool schedule changed")
	}
}

func TestNativeLeagueResumeAndEvidence(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit native engine required")
	}
	var command []string
	if e := json.Unmarshal([]byte(raw), &command); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	c := DefaultRunConfig()
	c.Network = testModel(t).Config
	c.OpponentMix = &OpponentMix{Rules: 80, History: 10, Self: 10}
	c.Warmup = nil
	c.BatchMatches = 8
	c.MaxTurns = 12
	c.ReservePets = 2
	c.HealingMagic = 20
	c.HealingItems = 2
	c.PPO.Epochs = 1
	c.PPO.SequenceLength = 4
	one, two := t.TempDir(), t.TempDir()
	if e := Run(ctx, RunOptions{Directory: one, Command: command, Config: &c, Batches: 3, Stderr: io.Discard}); e != nil {
		t.Fatal(e)
	}
	stop := errors.New("stop in second frozen batch")
	e := Run(ctx, RunOptions{Directory: two, Command: command, Config: &c, Batches: 3, Stderr: io.Discard, Progress: func(p Progress) error {
		if p.CompletedBatches == 1 && p.PendingMatches == 3 {
			return stop
		}
		return nil
	}})
	if !errors.Is(e, stop) {
		t.Fatal("mid-batch stop failed", e)
	}
	if e = Run(ctx, RunOptions{Directory: two, Command: command, Resume: true, Batches: 2, Stderr: io.Discard}); e != nil {
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
	if !reflect.DeepEqual(sa, sb) || !reflect.DeepEqual(a.OpponentScores, b.OpponentScores) || !reflect.DeepEqual(a.Opponents, b.Opponents) || a.CompletedBatches != 3 || b.NextGame != 24 {
		t.Fatal("resume changed model, optimizer, difficulty or pool")
	}
	x, e := InspectLeague(ctx, one)
	if e != nil {
		t.Fatal(e)
	}
	y, e := InspectLeague(ctx, two)
	if e != nil {
		t.Fatal(e)
	}
	if len(x.Reports) != 3 || !reflect.DeepEqual(x.Weights, y.Weights) || x.Mix != *c.OpponentMix || y.Mix != x.Mix {
		t.Fatal("matrix or weights missing")
	}
	for i, r := range x.Reports {
		if !reflect.DeepEqual(r.Rows, y.Reports[i].Rows) || r.Learner != y.Reports[i].Learner {
			t.Fatal("matrix changed after resume")
		}
	}
	// Rehashing a changed mix cannot reinterpret the previously collected
	// rule/history games as self play. Reconstruct the frozen schedule.
	changedMix := b
	changedMix.Config.OpponentMix = &OpponentMix{Self: 100}
	if e = saveCheckpoint(two, changedMix); e != nil {
		t.Fatal(e)
	}
	if _, e = InspectLeague(ctx, two); e == nil {
		t.Fatal("rewritten category mixture accepted existing incompatible games")
	}
	if _, e = ExportCandidate(two, ""); e == nil {
		t.Fatal("export accepted silently rewritten category mixture")
	}
	if e = saveCheckpoint(two, b); e != nil {
		t.Fatal(e)
	}
	// Even a self-consistent rewritten report and pointer cannot override the
	// native outcome: verification reopens the referenced trajectory shards.
	r := y.Reports[2]
	r.Games = append([]LeagueGame(nil), r.Games...)
	r.Games[0].Outcome = 1
	if y.Reports[2].Games[0].Outcome == 1 {
		r.Games[0].Outcome = -1
	}
	r.Rows = scoreGames(r.Games)
	id, e := saveLeagueReport(two, r)
	if e != nil {
		t.Fatal(e)
	}
	bad := b
	bad.LeagueReports = append([]string(nil), b.LeagueReports...)
	bad.LeagueReports[2] = id
	y.Reports[2] = r
	bad.OpponentScores = leagueScores(y.Reports)
	if e = saveCheckpoint(two, bad); e != nil {
		t.Fatal(e)
	}
	if _, e = InspectLeague(ctx, two); e == nil {
		t.Fatal("fabricated win accepted as training evidence")
	}
	if _, e = ExportCandidate(two, ""); e == nil {
		t.Fatal("model exported despite corrupt league evidence")
	}
	if e = saveCheckpoint(two, b); e != nil {
		t.Fatal(e)
	}
	if _, e = ExportCandidate(two, ""); e != nil {
		t.Fatal(e)
	}
	t.Logf("24 native matches across three frozen batches: exact resume and forged outcome rejection; scores=%+v", b.OpponentScores)
}

func TestNativeRejectedPPOBatchesResumeWithoutDuplicateOpponents(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit native engine required")
	}
	var command []string
	if e := json.Unmarshal([]byte(raw), &command); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	c := DefaultRunConfig()
	c.Network, c.Warmup = testModel(t).Config, nil
	c.BatchMatches, c.MaxTurns = 4, 4
	c.PPO.LearningRate, c.PPO.TargetKL = .05, 1e-30
	dir := t.TempDir()
	if e := Run(ctx, RunOptions{Directory: dir, Command: command, Config: &c, Batches: 2, Stderr: io.Discard}); e != nil {
		t.Fatal(e)
	}
	if e := Run(ctx, RunOptions{Directory: dir, Command: command, Resume: true, Batches: 1, Stderr: io.Discard}); e != nil {
		t.Fatal(e)
	}
	cp, state, e := LoadCheckpoint(dir)
	if e != nil {
		t.Fatal(e)
	}
	if cp.CompletedBatches != 3 || cp.NextGame != 12 || len(cp.Opponents) != 1 || cp.Opponents[0] != cp.Learning || state.Optimizer.Step != 0 || !cp.LastReport.StoppedForKL || cp.LastReport.RejectedUpdates != 9 {
		t.Fatal("rejected batches changed weights, duplicated pool or lost progress", cp.LastReport, cp.Opponents, state.Optimizer.Step)
	}
	matrix, e := InspectLeague(ctx, dir)
	if e != nil || len(matrix.Reports) != 3 {
		t.Fatal("no-update league cannot be reconstructed", e)
	}
	if _, e = ExportCandidate(dir, ""); e != nil {
		t.Fatal(e)
	}
	bad := cp
	bad.Opponents = append(append([]string(nil), cp.Opponents...), cp.Opponents[0])
	if e := bad.Validate(); e == nil {
		t.Fatal("guarded checkpoint accepts duplicate states")
	}
}
