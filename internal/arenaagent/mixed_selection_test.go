package arenaagent

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/battletrain"
)

func TestMixedChampionSelectionAndCachedRecovery(t *testing.T) {
	ctx := context.Background()
	a, path := mixedNeuralFixture(t)
	root := t.TempDir()
	var parts []battletrain.MixedExperimentPart
	for _, mode := range []int{1, 5} {
		c := battletrain.DefaultEvaluationConfig()
		c.Mode = mode
		c.Seed = int64(9700 + mode)
		x, err := battletrain.NewExperiment(ctx, a.Environment, c, [3]int{2, 1, 20})
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, battletrain.MixedExperimentPart{Experiment: x, FamiliesPerBatch: 1})
	}
	x, err := battletrain.NewMixedExperiment(battletrain.ModeMixture{Schema: "mode-weighted-ppo-v1", Modes: []battletrain.ModeWeight{{Mode: 1, Weight: 1}, {Mode: 5, Weight: 1}}}, parts, nil)
	if err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(root, "experiment.json")
	if _, err := battletrain.SaveMixedExperiment(manifest, x); err != nil {
		t.Fatal(err)
	}
	registry := filepath.Join(root, "champions")
	var out bytes.Buffer
	if err := Main(ctx, []string{"champion", "init", "--directory", registry, "--mixed-experiment", manifest}, "test", &out); err != nil {
		t.Fatal(err)
	}
	status, err := battletrain.LoadChampionRegistry(ctx, registry)
	if err != nil {
		t.Fatal(err)
	}
	if status.Registry.Schema != "native-mixed-champion-registry-v1" || !status.Registry.SupportsMode(5) || status.Registry.SupportsMode(3) {
		t.Fatal("mixed registry mode conditions incorrect")
	}
	// Only the verified registry result is substituted. This is a persistence
	// fixture, not a native passing champion or combat-strength claim.
	status.Champion, status.Model, status.Head = hash(a), path, strings.Repeat("d", 64)
	var restores []struct {
		config  Config
		store   *Store
		version string
	}
	for _, mode := range []int{1, 5} {
		c := Config{Schema: 2, Mode: mode, Strategy: "learned", ChampionDirectory: registry, StateDir: filepath.Join(root, fmt.Sprintf("state-%d", mode))}
		s, selected, err := selectChampion(ctx, c, fixedChampion(status))
		if err != nil {
			t.Fatal(err)
		}
		if selected.Artifact != hash(a) || selected.Registry.MixedConditions[1].Mode != 5 {
			t.Fatal("mode selected different artifact")
		}
		store, err := OpenStore(c.StateDir)
		if err != nil {
			t.Fatal(err)
		}
		defer store.DB.Close()
		if err := store.saveSelection(selected, s); err != nil {
			t.Fatal(err)
		}
		restores = append(restores, struct {
			config  Config
			store   *Store
			version string
		}{c, store, s.Version()})
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(registry, registry+".offline"); err != nil {
		t.Fatal(err)
	}
	for _, item := range restores {
		s, selection, err := item.store.restoreSelection(item.config)
		if err != nil || s.Version() != item.version || selection.Artifact != hash(a) {
			t.Fatal("mixed cached recovery required source or changed model", err)
		}
		bad := item.config
		bad.Mode = 3
		if _, _, err := item.store.restoreSelection(bad); err == nil {
			t.Fatal("mixed cached selection accepted undeclared mode")
		}
	}
	for _, sub := range []string{"init", "challenge"} {
		out.Reset()
		if err := Main(ctx, []string{"champion", sub, "--help"}, "test", &out); err != nil || !strings.Contains(out.String(), "mixed-experiment") {
			t.Fatal("missing mixed champion help", err)
		}
		if err := Main(ctx, []string{"champion", sub, "--directory", root, "--experiment", "unread", "--mixed-experiment", "unread"}, "test", &out); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
			t.Fatal("mixed/single champion conflict accepted", err)
		}
	}
}
