package battletrain

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlenet"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

type imitationCancelCounter struct {
	context.Context
	Calls, Limit int
}

func (c *imitationCancelCounter) Err() error {
	c.Calls++
	if c.Limit > 0 && c.Calls >= c.Limit {
		return context.Canceled
	}
	return c.Context.Err()
}

func TestImitationMinibatchesPreserveEpisodesAndRollback(t *testing.T) {
	m := testModel(t)
	var episodes []Trajectory
	for i := 0; i < 5; i++ {
		tr := teacherTrajectory(t, m, i+2)
		b, e := json.Marshal(tr)
		if e != nil {
			t.Fatal(e)
		}
		b = bytes.ReplaceAll(b, []byte(`"test"`), []byte(fmt.Sprintf(`"test-%d"`, i)))
		if e := json.Unmarshal(b, &tr); e != nil {
			t.Fatal(e)
		}
		if e := tr.Validate(); e != nil {
			t.Fatal(e)
		}
		episodes = append(episodes, tr)
	}
	c := DefaultWarmupConfig().Update
	c.BatchEpisodes, c.SequenceLength = 2, 2
	a, b := testModel(t), testModel(t)
	oa, ob := &battlenet.Adam[float32]{}, &battlenet.Adam[float32]{}
	before := 0.
	for _, tr := range episodes {
		before += teacherLoss(t, a, tr)
	}
	for epoch := 0; epoch < 2; epoch++ {
		r, e := Imitate(context.Background(), a, oa, episodes, c)
		if e != nil || r.OptimizerUpdates != 3 || r.Episodes != 5 || r.Actions != 20 || oa.Step != (epoch+1)*3 {
			t.Fatal("minibatch count/tail incorrect", r, e)
		}
		if _, e := Imitate(context.Background(), b, ob, episodes, c); e != nil {
			t.Fatal(e)
		}
		// Simulate restoring serialized weights and optimizer between epochs.
		raw, e := json.Marshal(LearningState{Schema: 1, Model: b, Optimizer: ob})
		if e != nil {
			t.Fatal(e)
		}
		var restored LearningState
		if e := json.Unmarshal(raw, &restored); e != nil {
			t.Fatal(e)
		}
		b, ob = restored.Model, restored.Optimizer
	}
	if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(oa, ob) {
		t.Fatal("minibatch resume is not deterministic")
	}
	after := 0.
	for _, tr := range episodes {
		after += teacherLoss(t, a, tr)
	}
	if after >= before {
		t.Fatal("minibatch updates did not improve fixture teacher likelihood", before, after)
	}
	// Count a full epoch, then cancel late enough to have internally updated
	// several minibatches. No weights or optimizer state may escape on error.
	c.BatchEpisodes = 1
	probe := &imitationCancelCounter{Context: context.Background()}
	if _, e := Imitate(probe, testModel(t), &battlenet.Adam[float32]{}, episodes, c); e != nil {
		t.Fatal(e)
	}
	cancelled := &imitationCancelCounter{Context: context.Background(), Limit: probe.Calls * 3 / 4}
	original := testModel(t)
	opt := &battlenet.Adam[float32]{}
	id, _ := ModelDigest(original)
	r, e := Imitate(cancelled, original, opt, episodes, c)
	if !errors.Is(e, context.Canceled) || r.OptimizerUpdates < 1 || r.OptimizerUpdates >= 5 {
		t.Fatal("fixture did not cancel within a later minibatch", r, e)
	}
	actual, _ := ModelDigest(original)
	if actual != id || opt.Step != 0 || opt.Moments != nil {
		t.Fatal("cancel leaked partial epoch updates")
	}
	// A single trajectory produces the exact historical update with batching
	// disabled. Zero-valued new fields do not alter persisted legacy objects.
	legacy, modern := testModel(t), testModel(t)
	lo, mo := &battlenet.Adam[float32]{}, &battlenet.Adam[float32]{}
	c.BatchEpisodes = 0
	lr, e := Imitate(context.Background(), legacy, lo, episodes[:1], c)
	if e != nil {
		t.Fatal(e)
	}
	c.BatchEpisodes = 8
	mr, e := Imitate(context.Background(), modern, mo, episodes[:1], c)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(legacy, modern) || !reflect.DeepEqual(lo, mo) || lr.OptimizerUpdates != 0 || mr.OptimizerUpdates != 1 || lr.CrossEntropy != mr.CrossEntropy || lr.GradientNorm != mr.GradientNorm {
		t.Fatal("single update compatibility changed")
	}
}

func teacherTrajectory(t *testing.T, m *battlenet.Model[float32], turns int) Trajectory {
	t.Helper()
	tr := testTrajectory(t, m, turns)
	p := Policy{Rule: "basic"}
	tr.Policy, _ = p.Version()
	tr.PolicyKind = p.Kind()
	for i := range tr.Steps {
		s := &tr.Steps[i]
		var e error
		s.Choices, e = battlepolicy.RuleChoices(s.Frame, p.Rule)
		if e != nil {
			t.Fatal(e)
		}
		s.ConditionalLogProbs = make([]float32, len(s.Choices))
		s.LogProb, s.Value = 0, 0
	}
	return tr
}

func teacherLoss(t *testing.T, m *battlenet.Model[float32], tr Trajectory) float64 {
	t.Helper()
	var memory []float32
	loss, count := 0., 0
	for _, s := range tr.Steps {
		g := &battlenet.Graph[float32]{}
		o, e := battlepolicy.Forward(context.Background(), m.Bind(g), s.Frame, g.New(1, m.Config.Width, memory), s.Choices, nil)
		if e != nil {
			t.Fatal(e)
		}
		loss -= float64(o.LogProb.Data[0])
		count += len(s.Choices)
		memory = append([]float32(nil), o.Memory.Data...)
	}
	return loss / float64(count)
}

func TestImitationLearnsTeacherWithContinuousMemory(t *testing.T) {
	m := testModel(t)
	tr := teacherTrajectory(t, m, 5)
	c := DefaultWarmupConfig().Update
	c.SequenceLength = 2
	before := teacherLoss(t, m, tr)
	valueHead := append([]float32(nil), m.Parameters["value.1.w"].Values...)
	adam := &battlenet.Adam[float32]{}
	r, e := Imitate(context.Background(), m, adam, []Trajectory{tr}, c)
	if e != nil {
		t.Fatal(e)
	}
	after := teacherLoss(t, m, tr)
	// Report sums float32 graph segments, while teacherLoss sums individual
	// float32 terms in float64. Their reduction rounding need not be identical.
	if after >= before || math.Abs(r.CrossEntropy-before) > 1e-6 || adam.Step != 1 || r.Actions != 5 || r.TeamTurns != 5 || r.BeforePolicy == r.AfterPolicy || !reflect.DeepEqual(valueHead, m.Parameters["value.1.w"].Values) {
		t.Fatal("imitation failed to improve teacher likelihood or trained value head", before, after, r)
	}
	// Rule trajectories deliberately do not have current neural sampling
	// probabilities. They must remain invalid inputs to PPO after warmup.
	if _, e = Train(context.Background(), m, adam, []Trajectory{tr}, DefaultPPOConfig()); e == nil {
		t.Fatal("teacher data entered PPO")
	}
}

func TestImitationDoesNotTreatWinningActionsAsTruth(t *testing.T) {
	a, b := testModel(t), testModel(t)
	win, lose := teacherTrajectory(t, a, 3), teacherTrajectory(t, b, 3)
	lose.Winner, lose.Steps[2].Reward = 1, -1
	oa, ob := &battlenet.Adam[float32]{}, &battlenet.Adam[float32]{}
	for _, pair := range []struct {
		m *battlenet.Model[float32]
		o *battlenet.Adam[float32]
		t Trajectory
	}{{a, oa, win}, {b, ob, lose}} {
		if _, e := Imitate(context.Background(), pair.m, pair.o, []Trajectory{pair.t}, DefaultWarmupConfig().Update); e != nil {
			t.Fatal(e)
		}
	}
	if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(oa, ob) {
		t.Fatal("match outcome changed behavior-cloning objective")
	}
}

func TestImitationRejectsBadProvenanceAndCancelsAtomically(t *testing.T) {
	for _, kind := range []string{"network", "identity", "action", "duplicate", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			m := testModel(t)
			tr := teacherTrajectory(t, m, 3)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "network":
				tr = testTrajectory(t, m, 3)
			case "identity":
				tr.Policy, _ = (Policy{Rule: "focus"}).Version()
			case "action":
				s := &tr.Steps[0]
				for j, candidate := range s.Frame.Slots[0].Candidates {
					if candidate.Supported && j != s.Choices[0] {
						s.Choices[0] = j
						break
					}
				}
			case "cancel":
				cancel()
			}
			episodes := []Trajectory{tr}
			if kind == "duplicate" {
				episodes = append(episodes, tr)
			}
			before, _ := ModelDigest(m)
			adam := &battlenet.Adam[float32]{}
			if _, e := Imitate(ctx, m, adam, episodes, DefaultWarmupConfig().Update); e == nil {
				t.Fatal("invalid teacher update accepted")
			}
			after, _ := ModelDigest(m)
			if before != after || adam.Step != 0 || adam.Moments != nil {
				t.Fatal("rejected update mutated learning state")
			}
		})
	}
}

func TestNativeWarmupResumeThroughCollectionEpochAndPPO(t *testing.T) {
	for _, weighting := range []string{"", SqrtActionFrequency} {
		name := weighting
		if name == "" {
			name = "uniform"
		}
		t.Run(name, func(t *testing.T) { nativeWarmupResumeThroughCollectionEpochAndPPO(t, weighting) })
	}
}

func nativeWarmupResumeThroughCollectionEpochAndPPO(t *testing.T, weighting string) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit native environment wrapper required")
	}
	var command []string
	if e := json.Unmarshal([]byte(raw), &command); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	c := DefaultRunConfig()
	c.Network = testModel(t).Config
	c.MaxTurns, c.BatchMatches, c.PPO.Epochs, c.PPO.SequenceLength = 5, 2, 1, 2
	c.Warmup.Matches, c.Warmup.Epochs, c.Warmup.Update.SequenceLength = 4, 2, 2
	c.Warmup.Update.BatchEpisodes = 3 // Eight trajectories: two full minibatches and a tail.
	c.Warmup.Update.ActionWeighting = weighting
	one, two := t.TempDir(), t.TempDir()
	if e := Run(ctx, RunOptions{Directory: one, Command: command, Config: &c, Batches: 2, Stderr: io.Discard}); e != nil {
		t.Fatal(e)
	}
	stop := errors.New("committed boundary interruption")
	var warmupCheckpoint string
	var warmupWeights string
	for stage, event := range []string{"warmup_collected", "warmup_trained", "warmup_trained", "collected"} {
		o := RunOptions{Directory: two, Command: command, Resume: stage > 0, Batches: 2, Stderr: io.Discard, Progress: func(p Progress) error {
			if p.Event == event {
				return stop
			}
			return nil
		}}
		if stage == 0 {
			o.Config = &c
		}
		if e := Run(ctx, o); !errors.Is(e, stop) {
			t.Fatal("interruption boundary missing", stage, e)
		}
		checkpoint, learning, e := LoadCheckpoint(two)
		if e != nil {
			t.Fatal(e)
		}
		switch stage {
		case 0:
			if len(checkpoint.Warmup.Shards) != 1 || checkpoint.Warmup.CompletedEpochs != 0 || checkpoint.NextGame != 0 {
				t.Fatal("partial teacher collection lost")
			}
			if _, err := ExportCandidate(two, ""); err == nil {
				t.Fatal("untrained collected data exported as learned model")
			}
		case 1:
			if checkpoint.Warmup.CompletedEpochs != 1 || learning.Optimizer.Step != 3 || checkpoint.Warmup.LastReport.OptimizerUpdates != 3 {
				t.Fatal("warmup optimizer epoch not saved")
			}
			warmupCheckpoint, e = Digest(checkpoint)
			if e != nil {
				t.Fatal(e)
			}
			warmupWeights, e = ModelDigest(learning.Model)
			if e != nil {
				t.Fatal(e)
			}
		case 2:
			if !checkpoint.warmupComplete() || learning.Optimizer.Step != 0 || len(learning.Optimizer.Moments) != 0 {
				t.Fatal("PPO optimizer not reset at completed warmup")
			}
		case 3:
			if checkpoint.NextGame != 1 || len(checkpoint.Pending) != 1 {
				t.Fatal("PPO did not follow warmup")
			}
		}
	}
	if e := Run(ctx, RunOptions{Directory: two, Command: command, Resume: true, Batches: 2, Stderr: io.Discard}); e != nil {
		t.Fatal(e)
	}
	a, sa, e := LoadCheckpoint(one)
	if e != nil {
		t.Fatal(e)
	}
	b, sb, e := LoadCheckpoint(two)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(sa, sb) || !reflect.DeepEqual(a.TrainingGroups, b.TrainingGroups) || b.CompletedBatches != 2 || b.NextGame != 4 {
		t.Fatal("warmup resume diverged from uninterrupted training")
	}
	latestBefore, e := os.ReadFile(filepath.Join(two, "latest.json"))
	if e != nil {
		t.Fatal(e)
	}
	historical, e := ExportCheckpointCandidate(two, warmupCheckpoint, "")
	if e != nil {
		t.Fatal(e)
	}
	warmArtifact, e := battlepolicy.LoadArtifact(historical)
	if e != nil || warmArtifact.WeightsDigest != warmupWeights || len(warmArtifact.TrainingShards) != 4 || warmArtifact.Status != "candidate" {
		t.Fatal("historical warmup export used current PPO weights/provenance", e)
	}
	var warmReport struct {
		Training struct {
			PPO    *Report      `json:"ppo"`
			Warmup *WarmupState `json:"warmup"`
		} `json:"training"`
	}
	warmReportBytes, e := os.ReadFile(filepath.Join(two, "reports", warmArtifact.TrainingReport+".json"))
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(warmReportBytes, &warmReport); e != nil {
		t.Fatal(e)
	}
	if warmReport.Training.PPO != nil || warmReport.Training.Warmup == nil || warmReport.Training.Warmup.CompletedEpochs != 1 {
		t.Fatal("warmup-only export fabricated PPO progress")
	}
	// Calibrate only the checks before teacher verification. Cancel during the
	// first shard's per-turn rule replay, not at entry or after all data is read.
	prefix := &imitationCancelCounter{Context: ctx}
	warm, _, e := loadCheckpointID(prefix, two, warmupCheckpoint)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = loadLeague(prefix, two, warm); e != nil {
		t.Fatal(e)
	}
	direct := &imitationCancelCounter{Context: ctx, Limit: 3}
	if e = validateWarmupExport(direct, two, warm); !errors.Is(e, context.Canceled) || direct.Calls != direct.Limit {
		t.Fatal("teacher verification did not cancel inside the first shard", e)
	}
	during := &imitationCancelCounter{Context: ctx, Limit: prefix.Calls + 4}
	output := filepath.Join(two, "canceled-warmup-model.json")
	if _, e = ExportCheckpointCandidateContext(during, two, warmupCheckpoint, output); !errors.Is(e, context.Canceled) || during.Calls != during.Limit {
		t.Fatal("historical export did not cancel during teacher replay", e)
	}
	if _, e = os.Stat(output); !os.IsNotExist(e) {
		t.Fatal("canceled teacher verification published a model", e)
	}
	for _, invalid := range []string{"../latest", "", "bad"} {
		if _, _, err := LoadCheckpointID(two, invalid); err == nil {
			t.Fatal("invalid explicit checkpoint accepted")
		}
	}
	latestAfter, e := os.ReadFile(filepath.Join(two, "latest.json"))
	if e != nil || !bytes.Equal(latestBefore, latestAfter) {
		t.Fatal("historical export rewound training", e)
	}
	path, e := ExportCandidate(two, "")
	if e != nil {
		t.Fatal(e)
	}
	artifact, e := battlepolicy.LoadArtifact(path)
	if e != nil {
		t.Fatal(e)
	}
	if len(artifact.TrainingShards) != 8 || !reflect.DeepEqual(artifact.TrainingGroups, b.TrainingGroups) {
		t.Fatal("export omitted teacher provenance")
	}
	reportBytes, e := os.ReadFile(filepath.Join(two, "reports", artifact.TrainingReport+".json"))
	if e != nil {
		t.Fatal(e)
	}
	if fmt.Sprintf("%x", sha256.Sum256(reportBytes[:len(reportBytes)-1])) != artifact.TrainingReport {
		t.Fatal("exported report cannot be resolved by digest")
	}
	currentID, e := Digest(b)
	if e != nil {
		t.Fatal(e)
	}
	currentPath, e := ExportCheckpointCandidate(two, currentID, "")
	if e != nil || currentPath != path {
		t.Fatal("explicit current export changed existing artifact identity", e)
	}
	for _, id := range b.Warmup.Shards {
		tr, _, e := LoadShard(filepath.Join(two, "shards"), id)
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, group := range artifact.TrainingGroups {
			found = found || group == tr[0].Group
		}
		if !found {
			t.Fatal("warmup configuration could leak into evaluation")
		}
	}
	engine, e := battleenv.Start(ctx, command, io.Discard)
	if e != nil {
		t.Fatal(e)
	}
	defer engine.Close()
	ec := DefaultEvaluationConfig()
	ec.Seed, ec.MatchesPerOpponent, ec.MaxTurns = gameSeed(c.Seed, 0, 20), 8, 5
	er, e := Evaluate(ctx, engine, artifact, []Opponent{{Name: "basic", Rule: "basic"}}, ec, nil)
	if e != nil {
		t.Fatal(e)
	}
	for _, game := range er.Games {
		for _, group := range artifact.TrainingGroups {
			if game.Group == group {
				t.Fatal("evaluation accepted a warmup configuration from the same seed schedule")
			}
		}
		corruptOutput := filepath.Join(two, "must-not-export.json")
		if e = os.WriteFile(filepath.Join(two, "shards", b.Warmup.Shards[0]+".jsonl.gz"), []byte("corrupt"), 0600); e != nil {
			t.Fatal(e)
		}
		if _, e = ExportCheckpointCandidate(two, warmupCheckpoint, corruptOutput); e == nil {
			t.Fatal("historical export ignored corrupted teacher evidence")
		}
		if _, e = os.Stat(corruptOutput); !os.IsNotExist(e) {
			t.Fatal("failed export published an artifact", e)
		}
		if e = os.WriteFile(filepath.Join(two, "checkpoints", warmupCheckpoint+".json"), []byte("{}"), 0600); e != nil {
			t.Fatal(e)
		}
		if _, _, e = LoadCheckpointID(two, warmupCheckpoint); e == nil {
			t.Fatal("historical checkpoint checksum bypassed")
		}
		latestAfter, e = os.ReadFile(filepath.Join(two, "latest.json"))
		if e != nil || !bytes.Equal(latestBefore, latestAfter) {
			t.Fatal("failed historical export changed training pointer", e)
		}
	}
	t.Logf("warmup games=%d epochs=%d; PPO games=%d batches=%d; exact resumed model/optimizer match", len(b.Warmup.Shards), b.Warmup.CompletedEpochs, b.NextGame, b.CompletedBatches)
}
