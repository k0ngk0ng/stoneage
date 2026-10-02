package arenaagent

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battletrain"
)

func TestValidationComparisonCLI(t *testing.T) {
	ctx := context.Background()
	for _, command := range []string{"experiment-compare", "evaluate"} {
		var out bytes.Buffer
		if err := Main(ctx, []string{command, "--help"}, "test", &out); err != nil {
			t.Fatal(err)
		}
		want := "validation-comparison"
		if command == "experiment-compare" {
			want = "right-experiment"
		}
		if !strings.Contains(out.String(), want) {
			t.Fatal("missing comparison help")
		}
	}
	for _, flag := range []string{"experiment", "mixed-experiment", "seed", "points", "pet-points", "reserve-pets", "pet-skills", "healing-items", "healing-magic", "level", "max-turns", "matches", "database"} {
		var out bytes.Buffer
		args := []string{"evaluate", "--validation-comparison", "unread", "--mode", "1", "--opponent-model", "unread", "--" + flag, "1"}
		err := Main(ctx, args, "test", &out)
		if err == nil || !strings.Contains(err.Error(), "--"+flag) {
			t.Fatal("conflict reached file or engine IO", flag, err)
		}
	}
	for _, extra := range [][]string{{"--split", "test"}, {"--opponent", "basic"}, {"--opponent-model", "second"}} {
		var out bytes.Buffer
		args := append([]string{"evaluate", "--validation-comparison", "unread", "--mode", "1", "--opponent-model", "unread"}, extra...)
		if err := Main(ctx, args, "test", &out); err == nil || !strings.Contains(err.Error(), "validation-comparison") {
			t.Fatal("invalid comparison arguments reached IO", err)
		}
	}
	meta := battleenv.Metadata{Rules: strings.Repeat("a", 64), Platform: "linux-arm64", Scenario: "controlled-battle-v8"}
	var parts []battletrain.MixedExperimentPart
	mix := battletrain.ModeMixture{Schema: "mode-weighted-ppo-v1"}
	for _, mode := range []int{1, 5} {
		c := battletrain.DefaultEvaluationConfig()
		c.Mode = mode
		c.Seed = int64(mode + 8161)
		x, err := battletrain.NewExperiment(ctx, meta, c, [3]int{1, 1, 1})
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, battletrain.MixedExperimentPart{Experiment: x, FamiliesPerBatch: 1})
		mix.Modes = append(mix.Modes, battletrain.ModeWeight{Mode: mode, Weight: 1})
	}
	left, err := battletrain.NewMixedExperiment(mix, parts, nil)
	if err != nil {
		t.Fatal(err)
	}
	mix.Modes[0].Weight = 2
	right, err := battletrain.NewMixedExperiment(mix, parts, nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	a, b, path := filepath.Join(dir, "left.json"), filepath.Join(dir, "right.json"), filepath.Join(dir, "comparison.json")
	if _, err := battletrain.SaveMixedExperiment(a, left); err != nil {
		t.Fatal(err)
	}
	if _, err := battletrain.SaveMixedExperiment(b, right); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Main(ctx, []string{"experiment-compare", "--left-experiment", a, "--right-experiment", b, "--output", path}, "test", &out); err != nil {
		t.Fatal(err)
	}
	x, err := battletrain.LoadMixedValidationComparison(path)
	if err != nil || x.LeftDigest == x.RightDigest || !strings.Contains(out.String(), "validation_comparison_created") {
		t.Fatal("declaration lost distinct identity", err)
	}
}
