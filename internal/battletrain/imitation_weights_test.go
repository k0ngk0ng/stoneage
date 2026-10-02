package battletrain

import (
	"context"
	"encoding/json"
	"math"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/battlenet"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

func TestImitationFrequencyWeightsAndReportBinding(t *testing.T) {
	counts := map[string]int{"player:attack": 9, "player:switch": 1}
	w, err := weightsFromCounts(counts, 10)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(w.Weights["player:attack"]-5./6) > 1e-14 || math.Abs(w.Weights["player:switch"]-2.5) > 1e-14 {
		t.Fatal(w)
	}
	counts["player:attack"] = 10
	if w.Counts["player:attack"] != 9 {
		t.Fatal("retained mutable counts")
	}
	single, err := weightsFromCounts(map[string]int{"pet:wait": 12}, 12)
	if err != nil || single.Weights["pet:wait"] != 1 {
		t.Fatal(single, err)
	}
	for _, invalid := range []map[string]int{{"player:attack": 0}, {"player:attack": 11}, {"other:attack": 10}, {"player:unknown": 10}, {"player:attack": 9}} {
		if _, err := weightsFromCounts(invalid, 10); err == nil {
			t.Fatal("accepted invalid counts", invalid)
		}
	}
	c := DefaultWarmupConfig().Update
	c.ActionWeighting = SqrtActionFrequency
	loss := .3
	r := ImitationReport{Actions: 10, ActionWeighting: w, WeightedCrossEntropy: &loss}
	if err := validateImitationWeights(c, r, map[string]int{"player:attack": 9, "player:switch": 1}); err != nil {
		t.Fatal(err)
	}
	if err := validateImitationWeights(c, r, map[string]int{"player:attack": 8, "player:switch": 2}); err == nil {
		t.Fatal("accepted different dataset")
	}
	if err := validateImitationWeights(DefaultWarmupConfig().Update, r, nil); err == nil {
		t.Fatal("weighted report accepted as legacy")
	}
	w.Weights["player:switch"]++
	if err := validateImitationWeights(c, r, nil); err == nil {
		t.Fatal("accepted changed normalization")
	}
}

func TestImitationClassesUseSemanticFlagsNotRouting(t *testing.T) {
	for _, schema := range []string{battlepolicy.LegacyFeatureVersion, battlepolicy.RecipientFeatureVersion, battlepolicy.FeatureVersion} {
		for _, tc := range []struct {
			kind  string
			flags []int
		}{{"attack", []int{0}}, {"guard", []int{1}}, {"wait", []int{2}}, {"guard_break", []int{0, 3}}, {"poison", []int{0, 15, 16}}, {"stone", []int{0, 15, 17}}, {"confuse", []int{0, 15, 18}}, {"sleep", []int{0, 15, 19}}, {"single_heal", []int{22}}, {"group_heal", []int{22, 25}}, {"healing_item", []int{22, 29}}, {"switch", []int{30}}, {"recall", []int{31}}} {
			candidate := battlepolicy.Candidate{ID: "untrusted-name", Target: 14, Supported: true}
			for _, i := range tc.flags {
				candidate.Features[i] = 1
			}
			f := battlepolicy.Frame{Schema: schema, Slots: []battlepolicy.Slot{{Actor: "player", Member: 4, Candidates: []battlepolicy.Candidate{candidate}}}}
			got, err := imitationActionClass(f, 0, 0)
			if err != nil || got != "player:"+tc.kind {
				t.Fatal(schema, tc, got, err)
			}
			f.Slots[0].Candidates[0].ID = "other"
			f.Slots[0].Candidates[0].Target = 1
			if changed, err := imitationActionClass(f, 0, 0); err != nil || changed != got {
				t.Fatal("routing affected class")
			}
			f.Slots[0].Candidates[0].Supported = false
			if _, err := imitationActionClass(f, 0, 0); err == nil {
				t.Fatal("unsupported label accepted")
			}
		}
	}
	f := battlepolicy.Frame{Schema: battlepolicy.FeatureVersion, Slots: []battlepolicy.Slot{{Actor: "pet", Candidates: []battlepolicy.Candidate{{Supported: true}}}}}
	f.Slots[0].Candidates[0].Features[0] = 1
	f.Slots[0].Candidates[0].Features[32] = 1
	if k, e := imitationActionClass(f, 0, 0); e != nil || k != "pet:guardian" {
		t.Fatal(k, e)
	}
	f.Slots[0].Candidates[0].Features[17] = 1
	if _, e := imitationActionClass(f, 0, 0); e == nil {
		t.Fatal("ambiguous class accepted")
	}
}

func TestWeightedConditionalLossGradient(t *testing.T) {
	m := testModel(t)
	tr := teacherTrajectory(t, m, 2)
	for i := range tr.Steps {
		s := &tr.Steps[i]
		pet := s.Frame.Entities[0]
		pet[1], pet[2] = 0, 1
		slot := s.Frame.Slots[0]
		slot.Actor, slot.Entity = "pet", len(s.Frame.Entities)
		slot.Candidates = append([]battlepolicy.Candidate(nil), slot.Candidates...)
		for j := range slot.Candidates {
			slot.Candidates[j].Features[5], slot.Candidates[j].Features[6] = 0, 1
		}
		s.Frame.Entities = append(s.Frame.Entities, pet)
		s.Frame.Slots = append(s.Frame.Slots, slot)
		s.Choices = append(s.Choices, s.Choices[0])
	}
	// Weighted teacher-forced terms, with full recurrent memory across these
	// two turns. Numeric oracle uses scalar outputs, independent of term nodes.
	weight := []float32{.4, 2.3}
	g := &battlenet.Graph[float32]{Train: true}
	bound := m.Bind(g)
	memory := g.New(1, m.Config.Width, nil)
	loss := g.New(1, 1, nil)
	for _, s := range tr.Steps {
		o, e := battlepolicy.Forward(context.Background(), bound, s.Frame, memory, s.Choices, nil)
		if e != nil {
			t.Fatal(e)
		}
		memory = o.Memory
		if len(o.ConditionalLogProbTerms) != len(s.Choices) {
			t.Fatal("missing conditional nodes")
		}
		for k, term := range o.ConditionalLogProbTerms {
			loss = g.Add(loss, g.Scale(term, -weight[k]))
		}
	}
	g.Backward(loss)
	gradient := bound.Gradients()["score.1.w"]
	index := 0
	for i, x := range gradient {
		if math.Abs(float64(x)) > math.Abs(float64(gradient[index])) {
			index = i
		}
	}
	if math.Abs(float64(gradient[index])) < 1e-5 {
		t.Fatal("degenerate derivative fixture")
	}
	oracle := func() float64 {
		var mem []float32
		total := 0.
		for _, s := range tr.Steps {
			graph := &battlenet.Graph[float32]{}
			o, e := battlepolicy.Forward(context.Background(), m.Bind(graph), s.Frame, graph.New(1, m.Config.Width, mem), s.Choices, nil)
			if e != nil {
				t.Fatal(e)
			}
			mem = append([]float32(nil), o.Memory.Data...)
			for k, lp := range o.ConditionalLogProbs {
				total -= float64(weight[k]) * float64(lp)
			}
		}
		return total
	}
	if math.Abs(oracle()-float64(loss.Data[0])) > 1e-6 {
		t.Fatal("wrong weighted objective")
	}
	p := m.Parameters["score.1.w"]
	original := p.Values[index]
	for _, epsilon := range []float32{.001, .0005} {
		p.Values[index] = original + epsilon
		hi := oracle()
		p.Values[index] = original - epsilon
		lo := oracle()
		p.Values[index] = original
		numeric := (hi - lo) / float64(2*epsilon)
		if math.Abs(numeric-float64(gradient[index])) > 1e-3 {
			t.Fatal("conditional gradient differs", epsilon, numeric, gradient[index])
		}
	}
}

func TestWeightedImitationResumeCancellationAndDefault(t *testing.T) {
	a, b := testModel(t), testModel(t)
	tr := teacherTrajectory(t, a, 4)
	config := DefaultWarmupConfig().Update
	config.ActionWeighting = SqrtActionFrequency
	config.SequenceLength = 2
	oa, ob := &battlenet.Adam[float32]{}, &battlenet.Adam[float32]{}
	for epoch := 0; epoch < 2; epoch++ {
		for _, p := range []struct {
			m *battlenet.Model[float32]
			o *battlenet.Adam[float32]
		}{{a, oa}, {b, ob}} {
			r, e := Imitate(context.Background(), p.m, p.o, []Trajectory{tr}, config)
			if e != nil {
				t.Fatal(e)
			}
			if e = validateImitationWeights(config, r, map[string]int{"player:attack": 4}); e != nil {
				t.Fatal(e)
			}
		}
		raw, _ := json.Marshal(LearningState{1, b, ob})
		var restored LearningState
		if e := json.Unmarshal(raw, &restored); e != nil {
			t.Fatal(e)
		}
		b, ob = restored.Model, restored.Optimizer
	}
	if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(oa, ob) {
		t.Fatal("weighted resume drift")
	}
	before, _ := json.Marshal(LearningState{1, a, oa})
	var probeState LearningState
	if e := json.Unmarshal(before, &probeState); e != nil {
		t.Fatal(e)
	}
	probe := &imitationCancelCounter{Context: context.Background()}
	if _, e := Imitate(probe, probeState.Model, probeState.Optimizer, []Trajectory{tr}, config); e != nil {
		t.Fatal(e)
	}
	late := &imitationCancelCounter{Context: context.Background(), Limit: probe.Calls}
	if _, e := Imitate(late, a, oa, []Trajectory{tr}, config); e == nil {
		t.Fatal("late cancellation ignored")
	}
	unchanged, _ := json.Marshal(LearningState{1, a, oa})
	if string(before) != string(unchanged) {
		t.Fatal("late cancellation leaked tentative update")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := Imitate(ctx, a, oa, []Trajectory{tr}, config); e == nil {
		t.Fatal("cancellation ignored")
	}
	after, _ := json.Marshal(LearningState{1, a, oa})
	if string(before) != string(after) {
		t.Fatal("cancellation mutated state")
	}
	legacy := DefaultWarmupConfig().Update
	r, e := Imitate(context.Background(), testModel(t), &battlenet.Adam[float32]{}, []Trajectory{tr}, legacy)
	if e != nil || r.ActionWeighting != nil || r.WeightedCrossEntropy != nil {
		t.Fatal("default changed", r, e)
	}
}

func TestWeightedStoredDemonstrationAndFeedback(t *testing.T) {
	ctx := context.Background()
	t.Run("demonstrations", func(t *testing.T) {
		data, config := demonstrationRunFixture(t)
		config.Update.ActionWeighting = SqrtActionFrequency
		full, resume := filepath.Join(t.TempDir(), "full"), filepath.Join(t.TempDir(), "resume")
		if e := RunDemonstrations(ctx, DemonstrationRunOptions{Directory: full, Dataset: data, Config: &config, Epochs: 2}); e != nil {
			t.Fatal(e)
		}
		if e := RunDemonstrations(ctx, DemonstrationRunOptions{Directory: resume, Dataset: data, Config: &config, Epochs: 1}); e != nil {
			t.Fatal(e)
		}
		if e := RunDemonstrations(ctx, DemonstrationRunOptions{Directory: resume, Resume: true, Epochs: 1}); e != nil {
			t.Fatal(e)
		}
		a, sa, _, e := LoadDemonstrationCheckpoint(ctx, full)
		if e != nil {
			t.Fatal(e)
		}
		b, sb, _, e := LoadDemonstrationCheckpoint(ctx, resume)
		if e != nil {
			t.Fatal(e)
		}
		if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(sa, sb) {
			t.Fatal("weighted demonstration resume differs")
		}
		var report DemonstrationEpoch
		if e := readObject(filepath.Join(resume, "demonstration-reports", b.Reports[0]+".json"), &report, 1<<20); e != nil {
			t.Fatal(e)
		}
		report.Report.ActionWeighting, e = weightsFromCounts(map[string]int{"player:guard": report.Report.Actions}, report.Report.Actions)
		if e != nil {
			t.Fatal(e)
		}
		b.Reports[0], _ = Digest(report)
		if e := writeObject(filepath.Join(resume, "demonstration-reports", b.Reports[0]+".json"), report); e != nil {
			t.Fatal(e)
		}
		if e := saveDemonstrationCheckpoint(resume, b); e != nil {
			t.Fatal(e)
		}
		if _, _, _, e := LoadDemonstrationCheckpoint(ctx, resume); e == nil {
			t.Fatal("rehashed class-count tampering accepted")
		}
	})
	t.Run("feedback", func(t *testing.T) {
		_, data, initial, config := feedbackRunFixture(t)
		config.ActionWeighting = SqrtActionFrequency
		full, resume := filepath.Join(t.TempDir(), "full"), filepath.Join(t.TempDir(), "resume")
		if e := RunFeedback(ctx, FeedbackRunOptions{Directory: full, Datasets: data, Initial: &initial, Update: &config, Epochs: 2}); e != nil {
			t.Fatal(e)
		}
		if e := RunFeedback(ctx, FeedbackRunOptions{Directory: resume, Datasets: data, Initial: &initial, Update: &config, Epochs: 1}); e != nil {
			t.Fatal(e)
		}
		if e := RunFeedback(ctx, FeedbackRunOptions{Directory: resume, Resume: true, Epochs: 1}); e != nil {
			t.Fatal(e)
		}
		a, sa, _, e := LoadFeedbackCheckpoint(ctx, full)
		if e != nil {
			t.Fatal(e)
		}
		b, sb, _, e := LoadFeedbackCheckpoint(ctx, resume)
		if e != nil {
			t.Fatal(e)
		}
		if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(sa, sb) {
			t.Fatal("weighted feedback resume differs")
		}
		var report FeedbackEpoch
		if e := readObject(filepath.Join(resume, "feedback-reports", b.Reports[0]+".json"), &report, 16<<20); e != nil {
			t.Fatal(e)
		}
		report.Report.Imitation.ActionWeighting, e = weightsFromCounts(map[string]int{"player:guard": report.Report.Imitation.Actions}, report.Report.Imitation.Actions)
		if e != nil {
			t.Fatal(e)
		}
		b.Reports[0], _ = Digest(report)
		if e := writeObject(filepath.Join(resume, "feedback-reports", b.Reports[0]+".json"), report); e != nil {
			t.Fatal(e)
		}
		if e := saveFeedbackCheckpoint(resume, b); e != nil {
			t.Fatal(e)
		}
		if _, _, _, e := LoadFeedbackCheckpoint(ctx, resume); e == nil {
			t.Fatal("rehashed feedback counts accepted")
		}
	})
}
