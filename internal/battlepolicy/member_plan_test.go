package battlepolicy

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/battlenet"
)

func TestIndependentMemberPlanInformationAndCausality(t *testing.T) {
	for mode := 1; mode <= 5; mode++ {
		f, err := Encode(fixture(mode, 0), History{First: true})
		if err != nil {
			t.Fatal(err)
		}
		before, _ := json.Marshal(f)
		config := NetworkConfig()
		config.Width, config.Heads, config.Layers = 16, 2, 1
		central, err := battlenet.NewModel[float32](config, 11)
		if err != nil {
			t.Fatal(err)
		}
		config.PlanScope = "member"
		independent, err := battlenet.NewModel[float32](config, 11)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(central.Parameters, independent.Parameters) {
			t.Fatal("different parameter capacity or initial values")
		}
		centralID, _ := NetworkDigest(central)
		independentID, _ := NetworkDigest(independent)
		if centralID == independentID || NetworkArchitecture(config) == NetworkArchitecture(central.Config) {
			t.Fatal("independent semantics share model identity")
		}
		run := func(m *battlenet.Model[float32], frame Frame, forced []int) Output {
			g := &battlenet.Graph[float32]{}
			o, err := Forward(context.Background(), m.Bind(g), frame, nil, forced, nil)
			if err != nil {
				t.Fatal(err)
			}
			return o
		}
		a := run(independent, f, nil)
		c := run(central, f, a.Choices)
		if !reflect.DeepEqual(a.Memory.Data, c.Memory.Data) || !reflect.DeepEqual(a.Value.Data, c.Value.Data) {
			t.Fatal("ablation changed public information or critic")
		}
		if mode == 1 && (!reflect.DeepEqual(a.ConditionalLogProbs, c.ConditionalLogProbs) || a.LogProb.Data[0] != c.LogProb.Data[0]) {
			t.Fatal("1v1 behavior changed")
		}
		if _, err := f.Selections(a.Choices); err != nil {
			t.Fatal("illegal independent team plan", err)
		}
		ownChanged, centralChanged := false, false
		for j, candidate := range f.Slots[0].Candidates {
			if !candidate.Supported || j == a.Choices[0] {
				continue
			}
			forced := append([]int(nil), a.Choices...)
			forced[0] = j
			b := run(independent, f, forced)
			d := run(central, f, forced)
			if !reflect.DeepEqual(a.Memory.Data, b.Memory.Data) {
				t.Fatal("private current choices leaked into shared history memory")
			}
			for k, slot := range f.Slots {
				if slot.Member != f.Slots[0].Member && a.ConditionalLogProbs[k] != b.ConditionalLogProbs[k] {
					t.Fatal("other member's planned action changed independent likelihood", mode, k)
				}
				if slot.Member == f.Slots[0].Member && slot.Actor == "pet" && a.ConditionalLogProbs[k] != b.ConditionalLogProbs[k] {
					ownChanged = true
				}
				if slot.Member != f.Slots[0].Member && c.ConditionalLogProbs[k] != d.ConditionalLogProbs[k] {
					centralChanged = true
				}
			}
		}
		if !ownChanged || mode > 1 && !centralChanged {
			t.Fatal("fixture failed to distinguish local and team coordination", mode, ownChanged, centralChanged)
		}
		// Even interleaved members retain their own player-to-pet prefix.
		interleaved := f
		interleaved.Slots = nil
		var choices []int
		var original []int
		for _, actor := range []string{"player", "pet"} {
			for i, slot := range f.Slots {
				if slot.Actor == actor {
					interleaved.Slots = append(interleaved.Slots, slot)
					choices = append(choices, a.Choices[i])
					original = append(original, i)
				}
			}
		}
		b := run(independent, interleaved, choices)
		for i, old := range original {
			if b.ConditionalLogProbs[i] != a.ConditionalLogProbs[old] {
				t.Fatal("interleaving changed member's conditional decision", mode)
			}
		}
		// Changing visible teammate health is allowed to affect independent
		// decisions: independence removes planned-action sharing, not information.
		changed := f
		changed.Entities = append([][EntityFeatures]float32(nil), f.Entities...)
		changed.Entities[0][eHPRatio] = .01
		visible := run(independent, changed, a.Choices)
		if reflect.DeepEqual(visible.Memory.Data, a.Memory.Data) {
			t.Fatal("visible team information ignored")
		}
		after, _ := json.Marshal(f)
		if string(before) != string(after) {
			t.Fatal("inference mutated observation")
		}
		g := &battlenet.Graph[float32]{Train: true}
		bound := independent.Bind(g)
		out, err := Forward(context.Background(), bound, f, nil, a.Choices, nil)
		if err != nil {
			t.Fatal(err)
		}
		g.Backward(g.Scale(out.LogProb, -1))
		norm := 0.
		for _, value := range bound.Gradients()["plan.gates.w"] {
			norm += math.Abs(float64(value))
		}
		if norm == 0 {
			t.Fatal("own player/pet coordination cannot learn")
		}
	}
}

func TestIndependentMemberArtifactContract(t *testing.T) {
	a := recordedArtifactFixture(t)
	legacy, err := json.Marshal(a.Network)
	if err != nil || strings.Contains(string(legacy), "plan_scope") {
		t.Fatal("default model serialization changed", err)
	}
	a.Network.Config.PlanScope = "member"
	a.WeightsDigest, _ = NetworkDigest(a.Network)
	if a.Validate() == nil {
		t.Fatal("commander manifest accepted independent model")
	}
	a.Architecture = NetworkArchitecture(a.Network.Config)
	raw, _ := json.Marshal(a)
	decoded, err := DecodeArtifact(raw)
	if err != nil || decoded.Network.Config.PlanScope != "member" {
		t.Fatal("independent artifact lost its semantics", err)
	}
	a.Network.Config.PlanScope = "unknown"
	if a.Network.Validate() == nil {
		t.Fatal("unknown plan scope accepted")
	}
}
