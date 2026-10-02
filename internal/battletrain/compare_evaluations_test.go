package battletrain

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
)

// These are statistical fixtures, not fabricated native evidence. The public
// entrypoint must reject them unless real raw archives can be verified.
func pairedFixture(t *testing.T, pairing string, n int, symmetric bool) EvaluationReport {
	t.Helper()
	c := DefaultEvaluationConfig()
	c.Pairing = pairing
	x, e := NewExperiment(context.Background(), battleenv.Metadata{Rules: strings.Repeat("0", 64), Platform: "linux-arm64", Scenario: "controlled-battle-v8"}, c, [3]int{1, n, 1})
	if e != nil {
		t.Fatal(e)
	}
	if symmetric {
		for i := range x.Families {
			if x.Families[i].Split == "validation" {
				f := &x.Families[i]
				f.Scenario.Builds[1] = f.Scenario.Builds[0]
				f.Scenario.PetBuilds[1] = f.Scenario.PetBuilds[0]
				f.Group = ScenarioGroup(f.Scenario)
				break
			}
		}
	}
	if e := x.Validate(); e != nil {
		t.Fatal(e)
	}
	id, _ := Digest(x)
	r := EvaluationReport{Schema: evaluationSchema(pairing), Evidence: strings.Repeat("1", 64), Candidate: strings.Repeat("2", 64), CandidateArtifact: strings.Repeat("3", 64), Environment: x.Environment, Experiment: &x, ExperimentDigest: id, Split: "validation", Config: experimentEvaluationConfig(x, "validation")}
	suite, e := x.evaluationSuite("validation")
	if e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"basic", "focus"} {
		version, e := (Policy{Rule: name}).Version()
		if e != nil {
			t.Fatal(e)
		}
		for i, s := range suite {
			r.Games = append(r.Games, EvaluationGame{Shard: strings.Repeat("4", 64), Opponent: name, OpponentPolicy: version, Group: ScenarioGroup(s), Scenario: s, CandidateSide: i % 2, Turns: 1, Winner: 1 - i%2, Terminated: true})
		}
	}
	r.Comparisons = Summarize(r.Games, r.Config.Seed)
	if e := ValidateEvaluation(r); e != nil {
		t.Fatal(e)
	}
	return r
}

func clonePaired(t *testing.T, r EvaluationReport) EvaluationReport {
	t.Helper()
	b, e := json.Marshal(r)
	if e != nil {
		t.Fatal(e)
	}
	var out EvaluationReport
	if e := json.Unmarshal(b, &out); e != nil {
		t.Fatal(e)
	}
	return out
}

func TestPairedComparisonGroupsDrawsAndSymmetricScenarios(t *testing.T) {
	for _, pairing := range []string{"", BalancedPairing} {
		a := pairedFixture(t, pairing, 20, true)
		b := clonePaired(t, a)
		for i := range b.Games {
			b.Games[i].Winner = -1
		}
		b.Comparisons = Summarize(b.Games, b.Config.Seed)
		before, _ := json.Marshal([]EvaluationReport{a, b})
		r, e := compareVerifiedEvaluations(context.Background(), a, b)
		if e != nil {
			t.Fatal(e)
		}
		if len(r.Comparisons) != 2 || r.AdjustmentComparisons != 2 || r.ArenaCertified {
			t.Fatal(r)
		}
		for _, row := range r.Comparisons {
			if row.Groups != 20 || row.Games != 20*familyGames(pairing) || row.PairedDelta == nil || *row.PairedDelta != .5 || row.CI95 == nil || *row.CI95 != ([2]float64{.5, .5}) || row.AdjustedInterval == nil || *row.AdjustedInterval != *row.CI95 {
				t.Fatal(row)
			}
		}
		after, _ := json.Marshal([]EvaluationReport{a, b})
		if string(before) != string(after) {
			t.Fatal("comparison changed source reports")
		}
		self, e := compareVerifiedEvaluations(context.Background(), b, b)
		if e != nil {
			t.Fatal(e)
		}
		for _, row := range self.Comparisons {
			if row.PairedDelta == nil || *row.PairedDelta != 0 || *row.CI95 != ([2]float64{}) {
				t.Fatal(row)
			}
		}
	}
}

func TestPairedComparisonCensoringSmallSamplesAndAdjustment(t *testing.T) {
	a := pairedFixture(t, BalancedPairing, 20, false)
	b := clonePaired(t, a)
	// A whole family gets the same improvement. This checks that resampling
	// uses families, not 160 supposedly independent games.
	positive := b.Games[0].Group
	for i := range b.Games {
		if b.Games[i].Group == positive {
			b.Games[i].Winner = b.Games[i].CandidateSide
		}
	}
	b.Comparisons = Summarize(b.Games, b.Config.Seed)
	r, e := compareVerifiedEvaluations(context.Background(), a, b)
	if e != nil {
		t.Fatal(e)
	}
	for _, row := range r.Comparisons {
		if row.PairedDelta == nil || *row.PairedDelta != .05 || row.CI95 == nil || row.CI95[0] != 0 || row.CI95[1] < .1 || row.AdjustedInterval[0] > row.CI95[0] || row.AdjustedInterval[1] < row.CI95[1] {
			t.Fatal(row)
		}
	}
	// Interleaving opponents may change; their own frozen suite order may not.
	b.Games = append(append([]EvaluationGame(nil), b.Games[160:]...), b.Games[:160]...)
	r2, e := compareVerifiedEvaluations(context.Background(), a, b)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(r.Comparisons, r2.Comparisons) {
		t.Fatal("opponent ordering changed statistics")
	}
	b.Games[0].Winner = -1
	b.Games[0].Terminated = false
	b.Games[0].Truncated = true
	b.Comparisons = Summarize(b.Games, b.Config.Seed)
	r, e = compareVerifiedEvaluations(context.Background(), a, b)
	if e != nil {
		t.Fatal(e)
	}
	for _, row := range r.Comparisons {
		if row.Opponent == "focus" && (row.CensoredPairs != 1 || row.PairedDelta != nil || row.CI95 != nil || row.AdjustedInterval != nil || row.IntervalUnavailable != "censored_outcomes") {
			t.Fatal("censored pair discarded or labeled", row)
		}
	}
	small := pairedFixture(t, BalancedPairing, 2, false)
	r, e = compareVerifiedEvaluations(context.Background(), small, small)
	if e != nil {
		t.Fatal(e)
	}
	for _, row := range r.Comparisons {
		if row.PairedDelta == nil || row.CI95 != nil || row.IntervalUnavailable != "fewer_than_20_families" {
			t.Fatal(row)
		}
	}
}

func TestPairedComparisonRejectsIncompatibleReportsAndMissingEvidence(t *testing.T) {
	a := pairedFixture(t, BalancedPairing, 20, false)
	for _, kind := range []string{"opponent", "policy", "missing", "side", "scenario", "experiment", "unrecorded", "test"} {
		t.Run(kind, func(t *testing.T) {
			b := clonePaired(t, a)
			switch kind {
			case "opponent":
				for i := range b.Games {
					if b.Games[i].Opponent == "focus" {
						b.Games[i].Opponent = "other"
					}
				}
			case "policy":
				for i := range b.Games {
					if b.Games[i].Opponent == "focus" {
						b.Games[i].OpponentPolicy = strings.Repeat("5", 64)
					}
				}
			case "missing":
				b.Games = b.Games[:len(b.Games)-1]
			case "side":
				b.Games[0].CandidateSide = 1 - b.Games[0].CandidateSide
			case "scenario":
				b.Games[0].Scenario.Seed++
			case "experiment":
				b.ExperimentDigest = strings.Repeat("6", 64)
			case "unrecorded":
				b.Evidence = ""
			case "test":
				b.Split = "test"
			}
			b.Comparisons = Summarize(b.Games, b.Config.Seed)
			if _, e := compareVerifiedEvaluations(context.Background(), a, b); e == nil {
				t.Fatal("incompatible comparison accepted")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := compareVerifiedEvaluations(ctx, a, a); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	root := t.TempDir()
	path := filepath.Join(root, "report.json")
	if e := SaveEvaluation(path, a); e != nil {
		t.Fatal(e)
	}
	if _, e := CompareEvaluations(context.Background(), path, path); e == nil {
		t.Fatal("summary without raw evidence accepted")
	}
	entries, e := os.ReadDir(root)
	if e != nil || len(entries) != 1 {
		t.Fatal("read-only comparison wrote files", e)
	}
}
