package battletrain

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/battlenet"
)

func TestPolicyInitializationPreservesDefaultAndParent(t *testing.T) {
	parent := experimentCandidate(t, experimentFixture(t))
	before := mustJSON(t, parent)
	for _, scale := range []float64{0, .5} {
		state, err := newInitialLearning(parent.Network.Config, 27, &parent, scale)
		if err != nil {
			t.Fatal(err)
		}
		if state.Optimizer.Step != 0 || len(state.Optimizer.Moments) != 0 {
			t.Fatal("optimizer was inherited")
		}
		changed := 0
		for name, p := range state.Model.Parameters {
			for i, value := range p.Values {
				want := parent.Network.Parameters[name].Values[i]
				if scale != 0 && (name == "score.1.w" || name == "score.1.b") {
					want *= float32(scale)
				}
				if value != want {
					t.Fatalf("unexpected transformed parameter %s[%d]", name, i)
				}
				if value != parent.Network.Parameters[name].Values[i] {
					changed++
				}
			}
		}
		id, _ := ModelDigest(state.Model)
		if scale == 0 && (id != parent.WeightsDigest || changed != 0) || scale != 0 && (id == parent.WeightsDigest || changed == 0) {
			t.Fatal("wrong policy identity")
		}
		p := state.Model.Parameters["score.1.w"]
		p.Values[0]++
		if string(before) != string(mustJSON(t, parent)) {
			t.Fatal("initialization aliases parent")
		}
	}
	state, err := newInitialLearning(parent.Network.Config, 27, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	want, err := battlenet.NewModel[float32](parent.Network.Config, 27)
	if err != nil || !reflect.DeepEqual(state.Model, want) {
		t.Fatal("default seeded initialization changed", err)
	}
	for _, scale := range []float64{-1, 1, 2, math.NaN(), math.Inf(1), math.SmallestNonzeroFloat64} {
		if _, err := newInitialLearning(parent.Network.Config, 27, &parent, scale); err == nil {
			t.Fatal("invalid scale accepted", scale)
		}
	}
	if _, err := newInitialLearning(parent.Network.Config, 27, nil, .5); err == nil {
		t.Fatal("missing parent accepted")
	}
	bad := parent.Network.Config
	bad.Width *= 2
	if _, err := newInitialLearning(bad, 27, &parent, .5); err == nil {
		t.Fatal("different network accepted")
	}
}

func TestPolicyInitializationReceiptRecomputesTransform(t *testing.T) {
	parent := experimentCandidate(t, experimentFixture(t))
	root := t.TempDir()
	c := DefaultRunConfig()
	c.InitialModel, _ = Digest(parent)
	c.Experiment, c.Warmup, c.InitialPolicyScale, c.Network = parent.Experiment, nil, .5, parent.Network.Config
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	state, err := newInitialLearning(c.Network, c.Seed, &parent, c.InitialPolicyScale)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := saveLearning(filepath.Join(root, "learning"), state); err != nil {
		t.Fatal(err)
	}
	id, err := savePolicyInitialization(root, c, &parent, c.InitialPolicyScale, state)
	if err != nil {
		t.Fatal(err)
	}
	check := func(id string, recipe RunConfig) error {
		_, err := verifyPolicyInitialization(context.Background(), root, id, recipe, &parent, recipe.Network, recipe.Seed, recipe.InitialPolicyScale)
		return err
	}
	if err := check(id, c); err != nil {
		t.Fatal(err)
	}
	var original PolicyInitialization
	if err := readObject(filepath.Join(root, "initializations", id+".json"), &original, 4<<20); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"schema", "method", "recipe", "parent", "parent-weights", "scale", "weights", "learning"} {
		t.Run(field, func(t *testing.T) {
			r := original
			switch field {
			case "schema":
				r.Schema = "unknown"
			case "method":
				r.Method = "runtime-temperature"
			case "recipe":
				r.Recipe = strings.Repeat("f", 64)
			case "parent":
				r.Parent = strings.Repeat("f", 64)
			case "parent-weights":
				r.ParentWeights = strings.Repeat("f", 64)
			case "scale":
				r.Scale = .25
			case "weights":
				r.Weights = parent.WeightsDigest
			case "learning":
				r.Learning = strings.Repeat("f", 64)
			}
			fake, _ := Digest(r)
			if err := writeObject(filepath.Join(root, "initializations", fake+".json"), r); err != nil {
				t.Fatal(err)
			}
			if check(fake, c) == nil {
				t.Fatal("rehashed false receipt accepted")
			}
		})
	}
	if check("", c) == nil || check(strings.Repeat("f", 64), c) == nil {
		t.Fatal("missing receipt accepted")
	}
	changed := c
	changed.Seed++
	if check(id, changed) == nil {
		t.Fatal("changed recipe accepted")
	}
	changed = c
	changed.InitialPolicyScale = 0
	if check(id, changed) == nil {
		t.Fatal("receipt accepted without transform")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := verifyPolicyInitialization(ctx, root, id, c, &parent, c.Network, c.Seed, c.InitialPolicyScale); err != context.Canceled {
		t.Fatal("lost cancellation", err)
	}
	if err := os.Remove(filepath.Join(root, "learning", original.Learning+".json")); err != nil {
		t.Fatal(err)
	}
	if check(id, c) == nil {
		t.Fatal("missing initial optimizer state accepted")
	}
}

func TestPolicyInitializationConfigRestrictions(t *testing.T) {
	c := DefaultRunConfig()
	raw, _ := json.Marshal(c)
	if strings.Contains(string(raw), "initial_policy_scale") {
		t.Fatal("default JSON changed")
	}
	c.InitialPolicyScale = .5
	if c.Validate() == nil {
		t.Fatal("undeclared parent accepted")
	}
	c.InitialModel, c.Experiment = strings.Repeat("1", 64), strings.Repeat("2", 64)
	if c.Validate() == nil {
		t.Fatal("warmup before scaled PPO accepted")
	}
	c.Warmup = nil
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}
