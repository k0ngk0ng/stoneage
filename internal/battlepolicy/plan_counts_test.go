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

func TestPlanTargetCountsSemantics(t *testing.T) {
	makeCandidate := func(target int, features ...int) Candidate {
		c := Candidate{Target: target, Supported: true}
		for _, k := range features {
			c.Features[k] = 1
		}
		return c
	}
	// Public rows: own player/pet, enemy player, dead ally, reserve ally.
	f := Frame{Entities: make([][EntityFeatures]float32, 5)}
	for i := range f.Entities {
		f.Entities[i][eAlly], f.Entities[i][eAlive] = 1, 1
	}
	f.Entities[2][eAlly], f.Entities[3][eAlive], f.Entities[4][eBench] = 0, 0, 1
	stone := makeCandidate(2, 0, 15, 17)
	attack := makeCandidate(2, 0)
	group := makeCandidate(-1, 22, 25, 7)
	heal := makeCandidate(0, 22, 7)
	f.Slots = []Slot{
		{Member: 0, Candidates: []Candidate{stone}},
		{Member: 0, Candidates: []Candidate{attack}},
		{Member: 1, Candidates: []Candidate{group}},
		{Member: 1, Candidates: []Candidate{heal}},
		{Member: 1, Candidates: []Candidate{stone, makeCandidate(0), makeCandidate(1), group, makeCandidate(-1, 1), makeCandidate(3), makeCandidate(4), makeCandidate(-1, 22, 25, 8)}},
	}
	before, _ := json.Marshal(f)
	got, err := PlanTargetCounts(f, 4, []int{0, 0, 0, 0}, "")
	if err != nil {
		t.Fatal(err)
	}
	want := [][8]float32{
		{.4, .2, .2, 0, .1, 0, 0, 0}, {.4, .1, 0, 0, 0, 0, 0, .2}, {.4, 0, 0, 0, 0, 0, 0, .1},
		{.4, 0, 0, 0, 0, 0, 0, .2}, {.4, 0, 0, 0, 0, 0, 0, 0}, {.4, 0, 0, 0, 0, 0, 0, 0},
		{.4, 0, 0, 0, 0, 0, 0, 0}, {.4, 0, 0, 0, 0, 0, 0, 0},
	}
	for i, row := range want {
		if !reflect.DeepEqual(got[i*8:(i+1)*8], row[:]) {
			t.Fatal("wrong counts", i, got[i*8:(i+1)*8], row)
		}
	}
	local, err := PlanTargetCounts(f, 4, []int{0, 0, 0, 0}, "member")
	if err != nil || !reflect.DeepEqual(local[:8], []float32{.2, 0, 0, 0, 0, 0, 0, 0}) {
		t.Fatal("teammate choice leaked", err, local)
	}
	// Every status has its own column; guard-break is physical as well.
	for feature, column := range map[int]int{16: 3, 17: 4, 18: 5, 19: 6} {
		f.Slots[0].Candidates[0] = makeCandidate(2, 0, 15, feature)
		f.Slots[1].Candidates[0] = makeCandidate(2, 3)
		v, err := PlanTargetCounts(f, 4, []int{0, 0, 0, 0}, "")
		if err != nil || v[2] != .2 || v[column] != .1 {
			t.Fatal(feature, v, err)
		}
	}
	f.Slots[2].Candidates[0] = makeCandidate(-1, 22, 25, 8)
	enemyHeal, err := PlanTargetCounts(f, 4, []int{0, 0, 0, 0}, "")
	if err != nil || enemyHeal[7] != .1 || enemyHeal[8+7] != .1 || enemyHeal[2*8+7] != 0 || enemyHeal[7*8+7] != .1 {
		t.Fatal("healing opponent was counted on wrong side", enemyHeal, err)
	}
	f.Slots[2].Candidates[0] = group
	f.Slots[0].Candidates[0], f.Slots[1].Candidates[0] = stone, attack
	after, _ := json.Marshal(f)
	if string(before) != string(after) {
		t.Fatal("counting changed observations")
	}
	for _, prefix := range [][]int{{0, 0, 0}, {0, 0, 0, 0, 0}, {99, 0, 0, 0}} {
		if _, err := PlanTargetCounts(f, 4, prefix, ""); err == nil {
			t.Fatal("noncausal or invalid prefix accepted")
		}
	}
	if _, err := PlanTargetCounts(f, 4, []int{0, 0, 0, 0}, "unknown"); err == nil {
		t.Fatal("unknown scope")
	}
}

func TestPlanCountsInitialEquivalenceCausalityAndLearning(t *testing.T) {
	for mode := 1; mode <= 5; mode++ {
		f, err := Encode(fixture(mode, 0), History{First: true})
		if err != nil {
			t.Fatal(err)
		}
		original, _ := json.Marshal(f)
		for _, scope := range []string{"", "member"} {
			c := NetworkConfig()
			c.Width, c.Heads, c.Layers = 16, 2, 1
			c.PlanScope = scope
			old, err := battlenet.NewModel[float32](c, 11)
			if err != nil {
				t.Fatal(err)
			}
			c.PlanFeatures = "target-counts-v1"
			model, err := battlenet.NewModel[float32](c, 11)
			if err != nil {
				t.Fatal(err)
			}
			for name, p := range old.Parameters {
				if !reflect.DeepEqual(p, model.Parameters[name]) {
					t.Fatal("original initialization changed", name)
				}
			}
			if model.Count() != old.Count()+8*c.Width {
				t.Fatal("unexpected capacity")
			}
			run := func(m *battlenet.Model[float32], forced []int) Output {
				g := &battlenet.Graph[float32]{}
				o, e := Forward(context.Background(), m.Bind(g), f, nil, forced, nil)
				if e != nil {
					t.Fatal(e)
				}
				return o
			}
			a, b := run(old, nil), run(model, nil)
			if !reflect.DeepEqual(a.Choices, b.Choices) || !reflect.DeepEqual(a.ConditionalLogProbs, b.ConditionalLogProbs) || a.Value.Data[0] != b.Value.Data[0] || !reflect.DeepEqual(a.Memory.Data, b.Memory.Data) {
				t.Fatal("zero extension changed initial inference", mode, scope)
			}
			// Exercise a trained, nonzero projection without changing masks.
			p := model.Parameters["score.plan.w"]
			for j := range p.Values {
				p.Values[j] = float32((j%13)-6) * .1
			}
			base := run(model, a.Choices)
			changedOwn, changedOther := false, false
			for j, candidate := range f.Slots[0].Candidates {
				if !candidate.Supported || j == a.Choices[0] {
					continue
				}
				forced := append([]int(nil), a.Choices...)
				forced[0] = j
				next := run(model, forced)
				if !reflect.DeepEqual(next.Memory.Data, base.Memory.Data) || next.Value.Data[0] != base.Value.Data[0] {
					t.Fatal("plan leaked into history/value")
				}
				for k, s := range f.Slots {
					if k == 0 {
						continue
					}
					different := next.ConditionalLogProbs[k] != base.ConditionalLogProbs[k]
					if s.Member == f.Slots[0].Member {
						changedOwn = changedOwn || different
					} else {
						changedOther = changedOther || different
						if scope == "member" && different {
							t.Fatal("independent member leaked team plan")
						}
					}
				}
			}
			if !changedOwn || mode > 1 && scope == "" && !changedOther {
				t.Fatal("fixture did not exercise coordination", mode, scope)
			}
			// Future forced choices cannot influence earlier distributions.
			last := len(f.Slots) - 1
			for j, candidate := range f.Slots[last].Candidates {
				if !candidate.Supported {
					continue
				}
				forced := append([]int(nil), a.Choices...)
				forced[last] = j
				o := run(model, forced)
				if !reflect.DeepEqual(o.ConditionalLogProbs[:last], base.ConditionalLogProbs[:last]) {
					t.Fatal("future choice leaked")
				}
			}
			g := &battlenet.Graph[float32]{Train: true}
			bound := model.Bind(g)
			o, err := Forward(context.Background(), bound, f, nil, a.Choices, nil)
			if err != nil {
				t.Fatal(err)
			}
			g.Backward(g.Scale(o.LogProb, -1))
			norm := 0.
			for _, v := range bound.Gradients()["score.plan.w"] {
				norm += math.Abs(float64(v))
			}
			if norm == 0 {
				t.Fatal("plan projection cannot learn")
			}
			if _, err := f.Selections(run(model, nil).Choices); err != nil {
				t.Fatal(err)
			}
		}
		after, _ := json.Marshal(f)
		if string(original) != string(after) {
			t.Fatal("inference mutated frame")
		}
	}
}

func TestPlanCountsArtifactContract(t *testing.T) {
	a := recordedArtifactFixture(t)
	old, _ := json.Marshal(a.Network)
	if strings.Contains(string(old), "plan_features") {
		t.Fatal("changed old serialization")
	}
	c := a.Network.Config
	c.PlanFeatures = "target-counts-v1"
	m, err := battlenet.NewModel[float32](c, 3)
	if err != nil {
		t.Fatal(err)
	}
	a.Network = m
	a.WeightsDigest, _ = NetworkDigest(m)
	if a.Validate() == nil {
		t.Fatal("new network relabeled old architecture")
	}
	a.Architecture = NetworkArchitecture(c)
	raw, _ := json.Marshal(a)
	decoded, err := DecodeArtifact(raw)
	if err != nil || decoded.Architecture != "commander-policy-v3" {
		t.Fatal(err)
	}
	a.Network.Config.PlanFeatures = ""
	if a.Network.Validate() == nil {
		t.Fatal("new weights accepted as old model")
	}
	a.Network.Config.PlanFeatures = "typo"
	if a.Network.Config.Validate() == nil {
		t.Fatal("unknown feature contract accepted")
	}
}
