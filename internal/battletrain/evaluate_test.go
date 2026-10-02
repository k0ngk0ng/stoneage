package battletrain

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlenet"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

func TestEvaluationClustersAndTruncation(t *testing.T) {
	var games []EvaluationGame
	for group := 0; group < 20; group++ {
		for repeat := 0; repeat < 4; repeat++ {
			games = append(games, EvaluationGame{Opponent: "basic", Group: fmt.Sprint(group), CandidateSide: 0, Terminated: true, Winner: group % 2, Turns: 10})
		}
	}
	games = append(games, EvaluationGame{Opponent: "basic", Group: "cutoff", CandidateSide: 0, Winner: -1, Truncated: true, Turns: 20})
	c := Summarize(games, 29)[0]
	if c.Wins != 40 || c.Losses != 40 || c.Draws != 0 || c.Truncated != 1 || c.Groups != 21 || c.CompletedScore == nil || *c.CompletedScore != .5 {
		t.Fatal("incorrect outcome statistics", c)
	}
	if c.ClusterCI95 == nil || c.ClusterCI95[0] > .35 || c.ClusterCI95[1] < .65 {
		t.Fatal("repeats were treated as independent games", c.ClusterCI95)
	}
	if !reflect.DeepEqual(Summarize(games, 29), Summarize(games, 29)) {
		t.Fatal("bootstrap not reproducible")
	}
	c = Summarize(games[len(games)-1:], 29)[0]
	if c.CompletedScore != nil || c.ClusterCI95 != nil || c.Draws != 0 {
		t.Fatal("cutoff reported as a draw/observed score")
	}
}

func TestNativeRulesAndIndependentEvaluation(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit native environment wrapper required")
	}
	var command []string
	if e := json.Unmarshal([]byte(raw), &command); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c := DefaultRunConfig()
	c.Warmup = nil
	c.Network = testModel(t).Config
	c.PPO.Epochs = 1
	c.PPO.SequenceLength = 2
	c.BatchMatches = 2
	c.MaxTurns = 8
	dir := t.TempDir()
	if e := Run(ctx, RunOptions{Directory: dir, Command: command, Config: &c, Batches: 1, Stderr: io.Discard}); e != nil {
		t.Fatal(e)
	}
	path, e := ExportCandidate(dir, "")
	if e != nil {
		t.Fatal(e)
	}
	a, e := battlepolicy.LoadArtifact(path)
	if e != nil {
		t.Fatal(e)
	}
	engine, e := battleenv.Start(ctx, command, io.Discard)
	if e != nil {
		t.Fatal(e)
	}
	defer engine.Close()
	s, _ := scenarioFor(c, 20)
	for _, rule := range battlepolicy.RuleNames() {
		tr, e := CollectPolicies(ctx, engine, s, [2]Policy{{Rule: rule}, {Model: a.Network}}, [2]*rand.Rand{nil, rand.New(rand.NewSource(5))}, "", "")
		if e != nil {
			t.Fatal(rule, e)
		}
		if tr[0].PolicyKind != "rule:"+rule || tr[1].PolicyKind != "network-sampled" {
			t.Fatal("behavior kind lost")
		}
		if _, e := Train(ctx, a.Network, &battlenet.Adam[float32]{}, []Trajectory{tr[0]}, c.PPO); e == nil {
			t.Fatal("rule trajectory entered on-policy PPO")
		}
	}
	ec := DefaultEvaluationConfig()
	ec.Seed = c.Seed
	ec.MatchesPerOpponent = 8
	ec.MaxTurns = 10
	r, e := Evaluate(ctx, engine, a, []Opponent{{Name: "basic", Rule: "basic"}, {Name: "focus", Rule: "focus"}}, ec, nil)
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Games) != 16 || len(r.Comparisons) != 2 {
		t.Fatal("missing comparison games")
	}
	training := map[string]bool{}
	for _, g := range a.TrainingGroups {
		training[g] = true
	}
	for _, g := range r.Games {
		if training[g.Group] {
			t.Fatal("evaluation leaked training configuration despite changed seed/side")
		}
	}
	if e = SaveEvaluation(filepath.Join(dir, "evaluation.json"), r); e != nil {
		t.Fatal(e)
	}
	t.Logf("unseen grouped games=%d comparisons=%+v", len(r.Games), r.Comparisons)
}
