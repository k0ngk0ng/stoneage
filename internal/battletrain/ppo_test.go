package battletrain

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"math/rand"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlenet"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

func testModel(t *testing.T) *battlenet.Model[float32] {
	t.Helper()
	c := battlepolicy.NetworkConfig()
	c.Width = 8
	c.Heads = 2
	c.Layers = 1
	m, e := battlenet.NewModel[float32](c, 71)
	if e != nil {
		t.Fatal(e)
	}
	return m
}

// A controlled numerical fixture for update/rollback checks. Actual game
// trajectory correctness is tested separately against the native executable.
func testTrajectory(t *testing.T, m *battlenet.Model[float32], turns int) Trajectory {
	t.Helper()
	version, e := ModelDigest(m)
	if e != nil {
		t.Fatal(e)
	}
	hash := strings.Repeat("0", 64)
	tr := Trajectory{Schema: TrajectorySchema, Match: "test", Group: "numeric-fixture", Policy: version, Opponent: version, Rules: hash, Platform: "linux-amd64", Environment: "controlled-battle-v8", Scenario: hash, Side: 0, Mode: 1, Terminated: true, Winner: 0}
	tr.Setup = battleenv.Scenario{Seed: 1, Level: 35, MaxTurns: turns, Mode: 1, Builds: []battleenv.Build{{30, 30, 30, 30}, {30, 30, 30, 30}}}
	tr.Scenario, _ = Digest(tr.Setup)
	tr.Group = ScenarioGroup(tr.Setup)
	tr.PolicyKind = "network-sampled"
	var memory []float32
	for i := 0; i < turns; i++ {
		s := aigame.Snapshot{Phase: aigame.PhaseBattle, Battle: aigame.BattleSnapshot{LadderID: tr.Match, Active: true, MyNoKnown: true, BPReceived: true, BCReceived: true, Turn: int32(i), Participants: []aigame.BattleParticipant{{BattleID: 0, HP: 100, MaxHP: 100, Player: true}, {BattleID: 10, HP: 100 - int32(i), MaxHP: 100, Player: true}}}}
		v := aigame.NewBattleView(s)
		v.Mode = 1
		batch := aigame.BattleEventBatch{Stream: "test", Observation: v}
		h := battlepolicy.History{Batch: &batch, First: i == 0}
		if i > 0 {
			h.PreviousStream = "test"
			h.PreviousCursor = uint64(i - 1)
			batch.Cursor = uint64(i)
			heal := 2
			recipient := 10
			batch.Events = []aigame.BattleEvent{{Sequence: uint64(i), MatchID: tr.Match, Effects: []aigame.BattleLogEntry{
				{Kind: "attack", Actor: 0, Target: 10, Damage: 3, Recipient: &recipient},
				{Kind: "BD", Actor: -1, Target: 10, Resource: "hp", Delta: &heal},
			}}}
		}
		f, e := battlepolicy.EncodeVersion([]aigame.BattleView{v}, h, battlepolicy.NetworkFeatures(m.Config))
		if e != nil {
			t.Fatal(e)
		}
		g := &battlenet.Graph[float32]{}
		o, e := battlepolicy.Forward(context.Background(), m.Bind(g), f, g.New(1, m.Config.Width, memory), nil, nil)
		if e != nil {
			t.Fatal(e)
		}
		tr.Steps = append(tr.Steps, Transition{Frame: f, Choices: o.Choices, ConditionalLogProbs: o.ConditionalLogProbs, LogProb: o.LogProb.Data[0], Value: o.Value.Data[0], Observations: []aigame.BattleView{v}, History: batch})
		memory = append([]float32(nil), o.Memory.Data...)
	}
	tr.Steps[turns-1].Reward = 1
	final := tr.Steps[turns-1].Observations[0]
	final.Turn = int32(turns)
	final.ID = "terminal-fixture"
	tr.FinalObservations = []aigame.BattleView{final}
	tr.FinalHistory = aigame.BattleEventBatch{Stream: "test", Cursor: uint64(turns - 1), Observation: final}
	if e = tr.Validate(); e != nil {
		t.Fatal(e)
	}
	return tr
}

func TestPositiveReturnImprovesChosenProbability(t *testing.T) {
	m := testModel(t)
	trajectory := testTrajectory(t, m, 1)
	config := DefaultPPOConfig()
	config.Epochs = 1
	config.ValueWeight = 0
	config.EntropyWeight = 0
	adam := &battlenet.Adam[float32]{}
	r, e := Train(context.Background(), m, adam, []Trajectory{trajectory}, config)
	if e != nil {
		t.Fatal(e)
	}
	if r.TeamTurns != 1 || len(r.Epochs) != 1 || adam.Step != 1 || r.CandidatePolicy == r.BehaviorPolicy {
		t.Fatal("missing actual optimizer update", r)
	}
	o, e := battlepolicy.Forward(context.Background(), m.Bind(&battlenet.Graph[float32]{}), trajectory.Steps[0].Frame, nil, trajectory.Steps[0].Choices, nil)
	if e != nil {
		t.Fatal(e)
	}
	if o.LogProb.Data[0] <= trajectory.Steps[0].LogProb {
		t.Fatal("positive return decreased selected action probability")
	}
	if _, e = Train(context.Background(), m, adam, []Trajectory{trajectory}, config); e == nil {
		t.Fatal("old batch reused after model changed")
	}
}

func TestRecurrentPPOValidationAndAtomicFailure(t *testing.T) {
	for _, kind := range []string{"recurrent-value", "probability", "missing-history", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			m := testModel(t)
			trajectory := testTrajectory(t, m, 3)
			before, _ := ModelDigest(m)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "recurrent-value":
				trajectory.Steps[1].Value += .02
			case "probability":
				trajectory.Steps[1].LogProb -= .1
				trajectory.Steps[1].ConditionalLogProbs[0] -= .1
			case "missing-history":
				trajectory.Steps[1].Frame.Events[12] = 1
			case "cancel":
				cancel()
			}
			adam := &battlenet.Adam[float32]{}
			if _, e := Train(ctx, m, adam, []Trajectory{trajectory}, DefaultPPOConfig()); e == nil {
				t.Fatal("bad batch accepted")
			}
			after, _ := ModelDigest(m)
			if before != after || adam.Step != 0 || adam.Moments != nil {
				t.Fatal("failed update mutated model/optimizer")
			}
		})
	}
}

func TestResumedRecurrentUpdateIsIdentical(t *testing.T) {
	m := testModel(t)
	adam := &battlenet.Adam[float32]{}
	c := DefaultPPOConfig()
	c.SequenceLength = 2
	c.Epochs = 2
	if _, e := Train(context.Background(), m, adam, []Trajectory{testTrajectory(t, m, 5)}, c); e != nil {
		t.Fatal(e)
	}
	data, e := json.Marshal(struct {
		Model     *battlenet.Model[float32]
		Optimizer *battlenet.Adam[float32]
	}{m, adam})
	if e != nil {
		t.Fatal(e)
	}
	var restored struct {
		Model     *battlenet.Model[float32]
		Optimizer *battlenet.Adam[float32]
	}
	if e = json.Unmarshal(data, &restored); e != nil {
		t.Fatal(e)
	}
	batch := []Trajectory{testTrajectory(t, m, 5)}
	a, e := Train(context.Background(), m, adam, batch, c)
	if e != nil {
		t.Fatal(e)
	}
	b, e := Train(context.Background(), restored.Model, restored.Optimizer, batch, c)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(m, restored.Model) || !reflect.DeepEqual(adam, restored.Optimizer) {
		t.Fatal("resumed recurrent update diverged")
	}
}

func TestPPOBacktrackBoundsEveryUpdateAndRestoresAdam(t *testing.T) {
	m := testModel(t)
	adam := &battlenet.Adam[float32]{}
	config := DefaultPPOConfig()
	config.Epochs = 1
	if _, e := Train(context.Background(), m, adam, []Trajectory{testTrajectory(t, m, 5)}, config); e != nil {
		t.Fatal(e)
	}
	// Start from nonempty moments, so rejected retries cannot silently retain
	// an Adam step/momentum from an earlier, oversized attempt.
	state, _ := json.Marshal(LearningState{Schema: 1, Model: m, Optimizer: adam})
	var expected LearningState
	if e := json.Unmarshal(state, &expected); e != nil {
		t.Fatal(e)
	}
	batch := []Trajectory{testTrajectory(t, m, 5)}
	config.LearningRate = .003
	// Measure the fixture's unguarded large and small steps independently,
	// then put the limit between them rather than relying on a magic scale.
	var endpoints []float64
	for _, rate := range []float64{config.LearningRate, config.LearningRate / 256} {
		var trial LearningState
		if e := json.Unmarshal(state, &trial); e != nil {
			t.Fatal(e)
		}
		legacy := config
		legacy.UpdateGuard, legacy.LearningRate = "", rate
		if _, e := Train(context.Background(), trial.Model, trial.Optimizer, batch, legacy); e != nil {
			t.Fatal(e)
		}
		kl, e := replayKL(context.Background(), trial.Model, batch)
		if e != nil {
			t.Fatal(e)
		}
		endpoints = append(endpoints, kl)
	}
	if endpoints[0] <= endpoints[1] || endpoints[1] < 0 {
		t.Fatal("fixture does not improve KL with a smaller step", endpoints)
	}
	config.TargetKL = (endpoints[0] + endpoints[1]) / 2
	r, e := Train(context.Background(), m, adam, batch, config)
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Epochs) != 1 || r.RejectedUpdates < 1 || r.StoppedForKL {
		t.Fatal("fixture must backtrack and accept one update", r)
	}
	s := r.Epochs[0]
	if s.PostUpdateKL == nil || *s.PostUpdateKL > config.TargetKL || s.Backtracks != r.RejectedUpdates || s.LearningRate != math.Ldexp(config.LearningRate, -s.Backtracks) {
		t.Fatal("missing or excessive post-update KL", s)
	}
	kl, e := replayKL(context.Background(), m, batch)
	if e != nil || kl != *s.PostUpdateKL {
		t.Fatal("report differs from complete recurrent replay", kl, e)
	}
	// Exactly one legacy Adam step at the accepted rate must yield exactly
	// the same parameters and moments, including the restored step counter.
	config.UpdateGuard, config.LearningRate = "", s.LearningRate
	if _, e = Train(context.Background(), expected.Model, expected.Optimizer, batch, config); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(m, expected.Model) || !reflect.DeepEqual(adam, expected.Optimizer) {
		t.Fatal("rejected attempts leaked optimizer state")
	}
	t.Logf("accepted rate %g after %d rejections, joint KL %g", s.LearningRate, s.Backtracks, kl)

	// Every epoch, including the final one, gets the post-update check.
	config = DefaultPPOConfig()
	config.Epochs, config.LearningRate, config.TargetKL = 3, .003, .003
	batch = []Trajectory{testTrajectory(t, m, 5)}
	r, e = Train(context.Background(), m, adam, batch, config)
	if e != nil || len(r.Epochs) == 0 {
		t.Fatal(r, e)
	}
	for _, s := range r.Epochs {
		if s.PostUpdateKL == nil || !finite(*s.PostUpdateKL) || *s.PostUpdateKL > config.TargetKL {
			t.Fatal("accepted unchecked epoch", s)
		}
	}
	if kl, e = replayKL(context.Background(), m, batch); e != nil || kl > config.TargetKL {
		t.Fatal("final model exceeds bound", kl, e)
	}
}

func TestPPOBacktrackExhaustionCancellationAndLegacy(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		m := testModel(t)
		adam := &battlenet.Adam[float32]{}
		before, _ := json.Marshal(LearningState{Schema: 1, Model: m, Optimizer: adam})
		batch := []Trajectory{testTrajectory(t, m, 5)}
		config := DefaultPPOConfig()
		config.Epochs, config.LearningRate, config.TargetKL = 1, .05, 1e-15
		if legacy {
			config.UpdateGuard = ""
		}
		r, e := Train(context.Background(), m, adam, batch, config)
		if e != nil {
			t.Fatal(e)
		}
		after, _ := json.Marshal(LearningState{Schema: 1, Model: m, Optimizer: adam})
		if legacy {
			if len(r.Epochs) != 1 || adam.Step != 1 || r.Epochs[0].PostUpdateKL != nil || r.RejectedUpdates != 0 {
				t.Fatal("legacy update semantics changed", r)
			}
			kl, e := replayKL(context.Background(), m, batch)
			if e != nil || kl <= config.TargetKL {
				t.Fatal("fixture doesn't reproduce oversized legacy last update", kl, e)
			}
			encoded, _ := json.Marshal(r)
			if strings.Contains(string(encoded), "post_update_kl") || strings.Contains(string(encoded), "learning_rate") || strings.Contains(string(encoded), "rejected_updates") {
				t.Fatal("legacy report encoding changed")
			}
		} else if !r.StoppedForKL || r.RejectedUpdates != 9 || len(r.Epochs) != 0 || string(before) != string(after) {
			t.Fatal("exhausted retries retained oversized weights or moments", r)
		}
	}

	m := testModel(t)
	adam := &battlenet.Adam[float32]{}
	batch := []Trajectory{testTrajectory(t, m, 5)}
	config := DefaultPPOConfig()
	config.Epochs = 1
	// Locate cancellation inside the extra post-update recurrent replay.
	legacy := config
	legacy.UpdateGuard = ""
	probe := &imitationCancelCounter{Context: context.Background()}
	if _, e := Train(probe, m, adam, batch, legacy); e != nil {
		t.Fatal(e)
	}
	m, adam = testModel(t), &battlenet.Adam[float32]{}
	before, _ := ModelDigest(m)
	cancelled := &imitationCancelCounter{Context: context.Background(), Limit: probe.Calls + 2}
	if _, e := Train(cancelled, m, adam, batch, config); !errors.Is(e, context.Canceled) {
		t.Fatal("did not cancel during post-update replay", e)
	}
	after, _ := ModelDigest(m)
	if before != after || adam.Step != 0 || adam.Moments != nil {
		t.Fatal("cancelled post-update replay changed public state")
	}
	config.UpdateGuard = "future-unknown"
	if _, e := Train(context.Background(), m, adam, batch, config); e == nil {
		t.Fatal("unknown guard accepted")
	}
}

func TestNativeCollectionAndPPO(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	rules := os.Getenv("STONEAGE_BATTLE_TEST_RULES_DIGEST")
	if raw == "" {
		t.Skip("explicit native environment wrapper required")
	}
	var command []string
	if e := json.Unmarshal([]byte(raw), &command); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	engine, e := battleenv.Start(ctx, command, io.Discard)
	if e != nil {
		t.Fatal(e)
	}
	defer engine.Close()
	m := testModel(t)
	var batch []Trajectory
	statusChoices := 0
	healingChoices := map[int]int{}
	itemChoices := 0
	summons, recalls := 0, 0
	for i := 0; i < 3; i++ {
		s := battleenv.Scenario{Seed: 47 + i, Mode: 1, Level: 35, MaxTurns: 100, Builds: []battleenv.Build{{30, 30, 30, 30}, {30, 30, 30, 30}}, PetBuilds: []battleenv.Build{{30, 30, 30, 30}, {30, 30, 30, 30}}}
		if i == 0 {
			s.MaxTurns = 1
		} else {
			s.HealingMagic = i * 10
			s.HealingItems = 2
		}
		tr, e := Collect(ctx, engine, s, [2]*battlenet.Model[float32]{m, m}, [2]*rand.Rand{rand.New(rand.NewSource(int64(i + 1))), rand.New(rand.NewSource(int64(i + 7)))}, ScenarioGroup(s), rules)
		if e != nil {
			t.Fatal(e)
		}
		if i == 0 && (!tr[0].Truncated || tr[0].Next == nil) {
			t.Fatal("collection limit wasn't bootstrapped")
		}
		if i > 0 && !tr[0].Terminated {
			t.Fatal("expected native terminal game")
		}
		for side := 0; side < 2; side++ {
			for _, step := range tr[side].Steps {
				for slot, choice := range step.Choices {
					if step.Frame.Slots[slot].Candidates[choice].Features[30] == 1 {
						summons++
					}
					if step.Frame.Slots[slot].Candidates[choice].Features[31] == 1 {
						recalls++
					}
					if step.Frame.Slots[slot].Candidates[choice].Features[15] == 1 {
						statusChoices++
					}
					if step.Frame.Slots[slot].Candidates[choice].Features[29] == 1 {
						itemChoices++
					} else if step.Frame.Slots[slot].Candidates[choice].Features[22] == 1 {
						healingChoices[s.HealingMagic]++
					}
				}
			}
			batch = append(batch, tr[side])
		}
		t.Logf("match=%d turns=%d winner=%d truncated=%v", i, len(tr[0].Steps), tr[0].Winner, tr[0].Truncated)
	}
	c := DefaultPPOConfig()
	if summons == 0 || recalls == 0 {
		t.Fatal("native PPO did not sample recall and resummon", summons, recalls)
	}
	t.Logf("sampled summons=%d recalls=%d", summons, recalls)
	if statusChoices == 0 {
		t.Fatal("native PPO rollout did not exercise the expanded action vocabulary")
	}
	t.Logf("sampled status attack choices=%d", statusChoices)
	if healingChoices[10] == 0 || healingChoices[20] == 0 {
		t.Fatal("native PPO rollout did not sample both healing spells", healingChoices)
	}
	t.Logf("sampled healing choices=%v item choices=%d", healingChoices, itemChoices)
	if itemChoices == 0 {
		t.Fatal("PPO never sampled consumable actions")
	}
	c.SequenceLength = 4
	c.Epochs = 2
	adam := &battlenet.Adam[float32]{}
	r, e := Train(ctx, m, adam, batch, c)
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Epochs) == 0 || r.CandidatePolicy == r.BehaviorPolicy {
		t.Fatal("native rollouts didn't update policy")
	}
	t.Logf("episodes=%d team_turns=%d epochs=%+v", r.Episodes, r.TeamTurns, r.Epochs)
}

func TestNativePPOContinuesAfterArenaKnockout(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit native environment wrapper required")
	}
	var command []string
	if err := json.Unmarshal([]byte(raw), &command); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	engine, err := battleenv.Start(ctx, command, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	withdrawnTurns := 0
	for mode := 2; mode <= 5; mode++ {
		m := testModel(t)
		scenario := battleenv.Scenario{Seed: 7, Mode: mode, Level: 35, MaxTurns: 50}
		for i := 0; i < mode*2; i++ {
			scenario.Builds = append(scenario.Builds, battleenv.Build{1, 117, 1, 1})
			scenario.PetBuilds = append(scenario.PetBuilds, battleenv.Build{1, 1, 1, 117})
		}
		tr, err := Collect(ctx, engine, scenario, [2]*battlenet.Model[float32]{m, m}, [2]*rand.Rand{rand.New(rand.NewSource(41)), rand.New(rand.NewSource(43))}, "", "")
		if err != nil {
			t.Fatalf("mode %d: %v", mode, err)
		}
		if !tr[0].Terminated || !tr[1].Terminated {
			t.Fatalf("mode %d did not finish", mode)
		}
		for _, trajectory := range tr {
			for _, step := range trajectory.Steps {
				for member, view := range step.Observations {
					if !view.Withdrawn {
						continue
					}
					withdrawnTurns++
					if len(view.Candidates) != 0 || step.Frame.Members[member] != -1 {
						t.Fatal("withdrawn actor remained actionable")
					}
					for _, slot := range step.Frame.Slots {
						if slot.Member == member {
							t.Fatal("policy planned a withdrawn actor")
						}
					}
				}
			}
		}
		cfg := DefaultPPOConfig()
		cfg.Epochs, cfg.SequenceLength = 1, 4
		report, err := Train(ctx, m, &battlenet.Adam[float32]{}, tr[:], cfg)
		if err != nil || report.CandidatePolicy == report.BehaviorPolicy {
			t.Fatalf("mode %d did not train a variable-length joint policy: %v", mode, err)
		}
		t.Logf("mode=%d turns=%d winner=%d team_turns=%d", mode, len(tr[0].Steps), tr[0].Winner, report.TeamTurns)
	}
	if withdrawnTurns == 0 {
		t.Fatal("test never exercised a decision after a member was removed")
	}
	t.Logf("observed and trained %d withdrawn-member decision boundaries", withdrawnTurns)
}
