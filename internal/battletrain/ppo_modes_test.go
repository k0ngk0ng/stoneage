package battletrain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlenet"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

func TestMixedPPOHistoricalSingleMode(t *testing.T) {
	for _, guard := range []string{"", "backtrack-v1"} {
		m, adam := testModel(t), &battlenet.Adam[float32]{}
		c := DefaultPPOConfig()
		c.UpdateGuard, c.Epochs, c.SequenceLength = guard, 2, 2
		for i := 0; i < 2; i++ {
			if _, err := Train(context.Background(), m, adam, []Trajectory{testTrajectory(t, m, 5)}, c); err != nil {
				t.Fatal(err)
			}
		}
		data, err := os.ReadFile("testdata/historical-single-mode.json")
		if err != nil {
			t.Fatal(err)
		}
		var reference LearningState
		if err := json.Unmarshal(data, &reference); err != nil {
			t.Fatal(err)
		}
		// This fixture retains the original pre-TrainMixed ARM64 reference.
		// Hash the fixture to protect provenance, then compare every tensor
		// numerically: Go versions/architectures may round float32 differently.
		id, err := Digest(reference)
		if err != nil || id != "47f04e2308d980abdbad5997c96777e65fe21aa1fd9c9ea7a697226355fa7a8b" {
			t.Fatal("historical fixture changed", id, err)
		}
		if !reflect.DeepEqual(m.Config, reference.Model.Config) || adam.Step != reference.Optimizer.Step || len(m.Parameters) != len(reference.Model.Parameters) || len(adam.Moments) != len(reference.Optimizer.Moments) {
			t.Fatal("historical model/optimizer structure changed")
		}
		compare := func(name string, actual, expected []float32) {
			t.Helper()
			if len(actual) != len(expected) {
				t.Fatal("historical tensor shape changed", name)
			}
			for i, value := range actual {
				want := float64(expected[i])
				if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) || math.Abs(float64(value)-want) > 2e-6+2e-5*math.Abs(want) {
					t.Fatalf("historical guard=%q %s[%d]: got %.9g want %.9g", guard, name, i, value, want)
				}
			}
		}
		for name, expected := range reference.Model.Parameters {
			actual, ok := m.Parameters[name]
			if !ok || actual.Rows != expected.Rows || actual.Cols != expected.Cols {
				t.Fatal("historical parameter missing or reshaped", name)
			}
			compare(name, actual.Values, expected.Values)
			moments, ok := adam.Moments[name]
			if !ok {
				t.Fatal("historical optimizer moment missing", name)
			}
			compare(name+"/first", moments.First, reference.Optimizer.Moments[name].First)
			compare(name+"/second", moments.Second, reference.Optimizer.Moments[name].Second)
		}
	}
}

// Synthetic observation-consistent numerical fixture, not a native strength
// result. Rebuild candidates and frames for every real member of each mode;
// changing only the mode label on a 1v1 frame must never pass validation.
func mixedTrajectory(t *testing.T, model *battlenet.Model[float32], mode, turns int) Trajectory {
	t.Helper()
	tr := testTrajectory(t, model, turns)
	tr.Match, tr.Mode = fmt.Sprintf("mixed-%d", mode), mode
	tr.Setup.Mode, tr.Setup.Builds = mode, nil
	for i := 0; i < 2*mode; i++ {
		tr.Setup.Builds = append(tr.Setup.Builds, battleenv.Build{30, 30, 30, 30})
	}
	tr.Scenario, _ = Digest(tr.Setup)
	tr.Group = ScenarioGroup(tr.Setup)
	var memory []float32
	for turn := range tr.Steps {
		s := &tr.Steps[turn]
		var roster []aigame.BattleParticipant
		for side := 0; side < 2; side++ {
			for member := 0; member < mode; member++ {
				roster = append(roster, aigame.BattleParticipant{BattleID: int32(side*10 + member), HP: int32(100 - turn - member), MaxHP: 100, Player: true})
			}
		}
		s.Observations = nil
		for member := 0; member < mode; member++ {
			v := aigame.NewBattleView(aigame.Snapshot{Phase: aigame.PhaseBattle, Battle: aigame.BattleSnapshot{LadderID: tr.Match, Active: true, MyNo: int32(member), MyNoKnown: true, BPReceived: true, BCReceived: true, Turn: int32(turn), Participants: roster}})
			v.Mode = mode
			s.Observations = append(s.Observations, v)
		}
		s.History.Observation = s.Observations[0]
		for j := range s.History.Events {
			s.History.Events[j].MatchID = tr.Match
		}
		h := battlepolicy.History{Batch: &s.History, First: turn == 0}
		if turn > 0 {
			h.PreviousStream, h.PreviousCursor = s.History.Stream, uint64(turn-1)
		}
		var err error
		s.Frame, err = battlepolicy.EncodeVersion(s.Observations, h, battlepolicy.NetworkFeatures(model.Config))
		if err != nil {
			t.Fatal(err)
		}
		g := &battlenet.Graph[float32]{}
		o, err := battlepolicy.Forward(context.Background(), model.Bind(g), s.Frame, g.New(1, model.Config.Width, memory), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		s.Choices, s.ConditionalLogProbs, s.LogProb, s.Value = o.Choices, o.ConditionalLogProbs, o.LogProb.Data[0], o.Value.Data[0]
		memory = append([]float32(nil), o.Memory.Data...)
	}
	tr.FinalObservations = append([]aigame.BattleView(nil), tr.Steps[turns-1].Observations...)
	for i := range tr.FinalObservations {
		tr.FinalObservations[i].Turn = int32(turns)
		tr.FinalObservations[i].ID = fmt.Sprintf("terminal-%d-%d", mode, i)
	}
	tr.FinalHistory.Observation = tr.FinalObservations[0]
	if err := tr.Validate(); err != nil {
		t.Fatal(err)
	}
	return tr
}

func testModeMixture() ModeMixture {
	return ModeMixture{Schema: "mode-weighted-ppo-v1", Modes: []ModeWeight{{Mode: 1, Weight: 1}, {Mode: 5, Weight: 3}}}
}

func TestMixedPPOWeightsModeMeansNotTurnsOrActors(t *testing.T) {
	ctx := context.Background()
	model, adam := testModel(t), &battlenet.Adam[float32]{}
	c := DefaultPPOConfig()
	c.Epochs, c.SequenceLength, c.GradientClip, c.LearningRate = 1, 2, 1000, 1e-5
	episodes := []Trajectory{mixedTrajectory(t, model, 1, 2), mixedTrajectory(t, model, 5, 5)}
	// Independent reference: recover each mode's unclipped mean gradient
	// from its first Adam moment using the historical single-mode path.
	var isolated [2]*battlenet.Adam[float32]
	var isolatedReports [2]Report
	for i, tr := range episodes {
		isolated[i] = &battlenet.Adam[float32]{}
		r, err := Train(ctx, testModel(t), isolated[i], []Trajectory{tr}, c)
		if err != nil || len(r.Epochs) != 1 || r.Epochs[0].Backtracks != 0 || r.Epochs[0].GradientNorm >= c.GradientClip {
			t.Fatal("invalid independent reference", r, err)
		}
		isolatedReports[i] = r
	}
	mix := testModeMixture()
	report, err := TrainMixed(ctx, model, adam, episodes, c, mix)
	if err != nil || len(report.Epochs) != 1 || report.TeamTurns != 7 || len(report.Modes) != 2 || adam.Step != 1 {
		t.Fatal(report, err)
	}
	if report.Modes[0] != (ModeBatchReport{Mode: 1, Episodes: 1, TeamTurns: 2, Actions: 2}) || report.Modes[1] != (ModeBatchReport{Mode: 5, Episodes: 1, TeamTurns: 5, Actions: 25}) {
		t.Fatal("wrong per-mode denominators", report.Modes)
	}
	maxError, pooledDifference := 0., 0.
	for name, actual := range adam.Moments {
		a, b := isolated[0].Moments[name], isolated[1].Moments[name]
		for j, value := range actual.First {
			want := .25*float64(a.First[j]) + .75*float64(b.First[j])
			pooled := (2*float64(a.First[j]) + 5*float64(b.First[j])) / 7
			maxError = max(maxError, math.Abs(float64(value)-want))
			pooledDifference = max(pooledDifference, math.Abs(float64(value)-pooled))
		}
	}
	// A weighted sum of independently rounded float32 moments can differ by
	// several float32 ULPs across architectures; pooled-turn weighting is still
	// required to remain distinguishable by at least 1e-6.
	if maxError > 4e-7 || pooledDifference < 1e-6 {
		t.Fatal("mode weighting wrong or fixture cannot distinguish pooled turns", maxError, pooledDifference)
	}
	// Near-zero gradients suffer cancellation when recovered from separately
	// rounded float32 moments. Compare the weighted gradient above, then check
	// the actual Adam step analytically using its stored first/second moments.
	reference := testModel(t)
	for name, parameter := range model.Parameters {
		for i, value := range parameter.Values {
			moments := adam.Moments[name]
			want := float64(reference.Parameters[name].Values[i]) - c.LearningRate*(float64(moments.First[i])/.1)/(math.Sqrt(float64(moments.Second[i])/.001)+1e-8)
			if math.Abs(float64(value)-want) > 2e-7 {
				t.Fatalf("weighted Adam parameter differs: %s[%d]", name, i)
			}
		}
	}
	wantLoss := .25*isolatedReports[0].Epochs[0].Loss + .75*isolatedReports[1].Epochs[0].Loss
	if math.Abs(report.Epochs[0].Loss-wantLoss) > 1e-7 {
		t.Fatal("reported objective does not match declared weighted means", report.Epochs[0].Loss, wantLoss)
	}
	for _, r := range report.Epochs[0].Modes {
		if r.PostUpdateKL == nil || *r.PostUpdateKL > c.TargetKL {
			t.Fatal("missing per-mode update verification", r)
		}
	}
	mix.Modes[0].Weight = 100
	if report.ModeMixture.Modes[0].Weight != 1 {
		t.Fatal("report aliases caller mode declaration")
	}
}

func TestMixedPPOPerModeKLRejectsAcceptableCombinedMean(t *testing.T) {
	ctx, mix := context.Background(), testModeMixture()
	c := DefaultPPOConfig()
	c.Epochs, c.SequenceLength, c.LearningRate, c.TargetKL = 1, 2, .1, 1000
	probe, probeAdam := testModel(t), &battlenet.Adam[float32]{}
	episodes := []Trajectory{mixedTrajectory(t, probe, 1, 2), mixedTrajectory(t, probe, 5, 5)}
	r, err := TrainMixed(ctx, probe, probeAdam, episodes, c, mix)
	if err != nil || len(r.Epochs) != 1 || r.RejectedUpdates != 0 {
		t.Fatal("unbounded test trial did not update", r, err)
	}
	first := r.Epochs[0]
	maximum, mode := 0., 0
	for _, m := range first.Modes {
		if *m.PostUpdateKL > maximum {
			maximum, mode = *m.PostUpdateKL, m.Mode
		}
	}
	weighted := *first.PostUpdateKL
	if !finite(maximum) || maximum-weighted < 1e-5 {
		t.Fatal("fixture does not distinguish per-mode from weighted KL", first)
	}
	// Choose a test boundary strictly between the measured mean and maximum;
	// the first trial must fail solely because one mode exceeds that boundary.
	c.TargetKL = (maximum + weighted) / 2
	m, adam := testModel(t), &battlenet.Adam[float32]{}
	r, err = TrainMixed(ctx, m, adam, episodes, c, mix)
	if err != nil || len(r.Epochs) != 1 || r.RejectedUpdates < 1 || len(r.ModeGuardFailures) != r.RejectedUpdates || r.ModeGuardFailures[0].Epoch != 0 || r.ModeGuardFailures[0].LearningRate != .1 {
		t.Fatal("per-mode guard did not reject the globally acceptable update", r, err)
	}
	if !reflect.DeepEqual(r.ModeGuardFailures[0].Modes, []int{mode}) {
		t.Fatal("wrong rejection mode", mode, r.ModeGuardFailures)
	}
	for _, m := range r.Epochs[0].Modes {
		if m.PostUpdateKL == nil || *m.PostUpdateKL > c.TargetKL {
			t.Fatal("accepted update violates an individual mode's bound", m)
		}
	}
	// A direct update at the accepted rate must reproduce every weight and
	// optimizer moment: rejected attempts cannot increment or alter Adam.
	c.LearningRate = r.Epochs[0].LearningRate
	reference, referenceAdam := testModel(t), &battlenet.Adam[float32]{}
	direct, err := TrainMixed(ctx, reference, referenceAdam, episodes, c, mix)
	if err != nil || direct.RejectedUpdates != 0 || !reflect.DeepEqual(reference, m) || !reflect.DeepEqual(referenceAdam, adam) {
		t.Fatal("backtracking leaked rejected model or optimizer state", err)
	}
	t.Logf("first weighted KL=%g < boundary=%g < mode%d KL=%g; accepted after %d backtracks", weighted, c.TargetKL, mode, maximum, r.RejectedUpdates)
}

func TestMixedPPORejectsIncompatibleBatchesAtomically(t *testing.T) {
	for _, kind := range []string{"empty", "unknown-schema", "unsorted", "duplicate-mode", "invalid-weight", "missing", "undeclared", "rules", "off-policy", "duplicate-trajectory", "probability", "late-value", "legacy-guard", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			model, adam := testModel(t), &battlenet.Adam[float32]{}
			episodes := []Trajectory{mixedTrajectory(t, model, 1, 2), mixedTrajectory(t, model, 5, 3)}
			mix, c := testModeMixture(), DefaultPPOConfig()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "empty":
				episodes = nil
			case "unknown-schema":
				mix.Schema = ""
			case "unsorted":
				mix.Modes[0], mix.Modes[1] = mix.Modes[1], mix.Modes[0]
			case "duplicate-mode":
				mix.Modes[1].Mode = 1
			case "invalid-weight":
				mix.Modes[0].Weight = 0
			case "missing":
				episodes = episodes[:1]
			case "undeclared":
				mix.Modes[1].Mode = 3
			case "rules":
				episodes[1].Rules = "1" + episodes[1].Rules[1:]
			case "off-policy":
				episodes[1].Policy = episodes[1].Rules
			case "duplicate-trajectory":
				episodes = append(episodes, episodes[1])
			case "probability":
				episodes[1].Steps[0].LogProb -= .1
				episodes[1].Steps[0].ConditionalLogProbs[0] -= .1
			case "late-value":
				episodes[1].Steps[2].Value += .02
			case "legacy-guard":
				c.UpdateGuard = ""
			case "cancel":
				cancel()
			}
			before, _ := Digest(LearningState{Schema: 1, Model: model, Optimizer: adam})
			if _, err := TrainMixed(ctx, model, adam, episodes, c, mix); err == nil {
				t.Fatal("invalid mixed batch accepted")
			}
			after, _ := Digest(LearningState{Schema: 1, Model: model, Optimizer: adam})
			if before != after {
				t.Fatal("failed mixed update mutated model or Adam")
			}
		})
	}
}

func TestMixedPPOResumeAndLateCancellation(t *testing.T) {
	m, adam := testModel(t), &battlenet.Adam[float32]{}
	c := DefaultPPOConfig()
	c.Epochs, c.SequenceLength = 2, 2
	mix := testModeMixture()
	batch := func(model *battlenet.Model[float32]) []Trajectory {
		return []Trajectory{mixedTrajectory(t, model, 1, 3), mixedTrajectory(t, model, 5, 4)}
	}
	if _, err := TrainMixed(context.Background(), m, adam, batch(m), c, mix); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(LearningState{Schema: 1, Model: m, Optimizer: adam})
	if err != nil {
		t.Fatal(err)
	}
	var restored LearningState
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	episodes := batch(m)
	probe := &imitationCancelCounter{Context: context.Background()}
	a, err := TrainMixed(probe, m, adam, episodes, c, mix)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := Digest(restored)
	canceled := &imitationCancelCounter{Context: context.Background(), Limit: probe.Calls * 3 / 4}
	r, err := TrainMixed(canceled, restored.Model, restored.Optimizer, episodes, c, mix)
	if !errors.Is(err, context.Canceled) || len(r.Epochs) < 1 {
		t.Fatal("fixture must cancel after at least one tentative epoch", r, err)
	}
	after, _ := Digest(restored)
	if after != before {
		t.Fatal("late cancellation leaked an epoch or Adam state")
	}
	b, err := TrainMixed(context.Background(), restored.Model, restored.Optimizer, episodes, c, mix)
	if err != nil || !reflect.DeepEqual(a, b) || !reflect.DeepEqual(m, restored.Model) || !reflect.DeepEqual(adam, restored.Optimizer) {
		t.Fatal("mixed resumed update differs", err)
	}
	if _, err := TrainMixed(context.Background(), m, adam, episodes, c, mix); err == nil {
		t.Fatal("mixed batch reused after behavior weights changed")
	}
}

func TestMixedPPOExhaustionAndExplicitOptIn(t *testing.T) {
	model, adam := testModel(t), &battlenet.Adam[float32]{}
	episodes := []Trajectory{mixedTrajectory(t, model, 1, 2), mixedTrajectory(t, model, 5, 5)}
	before, _ := Digest(LearningState{Schema: 1, Model: model, Optimizer: adam})
	c := DefaultPPOConfig()
	c.Epochs, c.LearningRate, c.TargetKL = 1, 1, 1e-20
	if _, err := Train(context.Background(), model, adam, episodes, c); err == nil {
		t.Fatal("historical single-mode API silently accepted mixed trajectories")
	}
	r, err := TrainMixed(context.Background(), model, adam, episodes, c, testModeMixture())
	if err != nil || !r.StoppedForKL || r.RejectedUpdates != 9 || len(r.ModeGuardFailures) != 9 || len(r.Epochs) != 0 || r.CandidatePolicy != r.BehaviorPolicy {
		t.Fatal("exhausted per-mode backtracking did not preserve behavior policy", r, err)
	}
	for i, failure := range r.ModeGuardFailures {
		if failure.LearningRate != math.Ldexp(c.LearningRate, -i) || len(failure.Modes) == 0 {
			t.Fatal("incomplete failed attempt accounting", failure)
		}
	}
	after, _ := Digest(LearningState{Schema: 1, Model: model, Optimizer: adam})
	if before != after {
		t.Fatal("exhausted backtracking mutated model or Adam")
	}
	if _, err := json.Marshal(r); err != nil {
		t.Fatal("failure report must remain persistable", err)
	}
}
