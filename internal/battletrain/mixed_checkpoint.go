package battletrain

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"reflect"

	"github.com/k0ngk0ng/stoneage/internal/battlenet"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

const MixedPointer = "mixed-latest.json"

// MixedRunConfig freezes the learner and opponent category mixture. Sampling
// within categories is uniform in this version; historical entries are only
// states produced by accepted mixed updates in this same run, never an
// unqualified single-mode parent relabeled as an all-mode opponent.
type MixedRunConfig struct {
	InitialPolicyScale float64          `json:"initial_policy_scale,omitempty"`
	Schema             string           `json:"schema"`
	Experiment         string           `json:"experiment"`
	Seed               int64            `json:"seed"`
	Network            battlenet.Config `json:"network"`
	PPO                PPOConfig        `json:"ppo"`
	RuleOpponents      []string         `json:"rule_opponents"`
	OpponentMix        OpponentMix      `json:"opponent_mix"`
}

func (c MixedRunConfig) Validate() error {
	if err := validateInitialPolicyScale(c.InitialPolicyScale); err != nil {
		return err
	}
	wantSchema := "commander-mixed-run-v1"
	if c.InitialPolicyScale != 0 {
		wantSchema = "commander-mixed-run-v2"
	}
	if c.Schema != wantSchema || !digest(c.Experiment) || c.PPO.UpdateGuard != "backtrack-v1" {
		return fmt.Errorf("mixed run requires its versioned experiment and per-mode update guard")
	}
	if err := c.Network.Validate(); err != nil {
		return err
	}
	if !battlepolicy.SupportedFeatures(battlepolicy.NetworkFeatures(c.Network)) || c.Network.EntityFeatures != battlepolicy.EntityFeatures || c.Network.CandidateFeatures != battlepolicy.CandidateFeatures || c.Network.EventFeatures != battlepolicy.EventFeatures {
		return fmt.Errorf("incompatible mixed network features")
	}
	if err := c.PPO.validate(); err != nil {
		return err
	}
	if err := c.OpponentMix.Validate(); err != nil {
		return err
	}
	if len(c.RuleOpponents) < 1 || len(c.RuleOpponents) > len(battlepolicy.RuleNames()) {
		return fmt.Errorf("mixed run requires a bounded frozen rule pool")
	}
	seen := map[string]bool{}
	for _, rule := range c.RuleOpponents {
		if seen[rule] || !battlepolicy.RuleSupported(rule) {
			return fmt.Errorf("invalid or duplicate mixed opponent rule")
		}
		seen[rule] = true
	}
	return nil
}

type MixedCheckpoint struct {
	Initialization string         `json:"initialization,omitempty"`
	Schema         string         `json:"schema"`
	Config         MixedRunConfig `json:"config"`
	Start          string         `json:"initial_learning"`
	Learning       string         `json:"learning"`
	NextGame       uint64         `json:"next_game"`
	Pending        []string       `json:"pending_shards"`
	Reports        []string       `json:"batch_reports"`
}

type MixedBatchReceipt struct {
	Schema string   `json:"schema"`
	Recipe string   `json:"recipe"`
	Batch  int      `json:"batch"`
	Before string   `json:"before_learning"`
	After  string   `json:"after_learning"`
	Shards []string `json:"shards"`
	Report Report   `json:"report"`
}

type LoadedMixedRun struct {
	Checkpoint     MixedCheckpoint
	Experiment     MixedExperiment
	Learning       LearningState
	Parent         *battlepolicy.Artifact
	History        []string
	TrainingGroups []string
	Pending        []Trajectory
	Receipts       []MixedBatchReceipt
}

func (c MixedCheckpoint) validate(x MixedExperiment) error {
	if err := c.Config.Validate(); err != nil {
		return err
	}
	if err := x.Validate(); err != nil {
		return err
	}
	id, _ := Digest(x)
	wantSchema := "commander-mixed-checkpoint-v1"
	if c.Config.InitialPolicyScale != 0 {
		wantSchema = "commander-mixed-checkpoint-v2"
		if !digest(c.Initialization) || x.Parts[0].Experiment.InitialModel == "" {
			return fmt.Errorf("scaled mixed initialization needs parent and receipt")
		}
	} else if c.Initialization != "" {
		return fmt.Errorf("unexpected mixed initialization receipt")
	}
	if c.Schema != wantSchema || id != c.Config.Experiment || !digest(c.Start) || !digest(c.Learning) || len(c.Reports) > 10000 || c.NextGame > math.MaxInt32 {
		return fmt.Errorf("invalid mixed checkpoint schema, identity or progress")
	}
	batch := 0
	for _, p := range x.Parts {
		batch += 8 * p.FamiliesPerBatch
	}
	if len(c.Pending) > batch || c.NextGame != uint64(len(c.Reports)*batch+len(c.Pending)) {
		return fmt.Errorf("mixed checkpoint committed prefix differs from batch progress")
	}
	seen := map[string]bool{}
	for _, refs := range [][]string{c.Pending, c.Reports} {
		for _, ref := range refs {
			if !digest(ref) || seen[ref] {
				return fmt.Errorf("invalid or duplicate mixed progress reference")
			}
			seen[ref] = true
		}
	}
	return battlepolicy.ValidateFeatureEnvironment(battlepolicy.NetworkFeatures(c.Config.Network), x.Parts[0].Experiment.Environment.Scenario)
}

func saveMixedCheckpoint(root string, c MixedCheckpoint, x MixedExperiment) error {
	if err := c.validate(x); err != nil {
		return err
	}
	return saveFeedbackCheckpointObject(root, "mixed-checkpoints", MixedPointer, c)
}

func mixedOpponentAt(c MixedRunConfig, game MixedScheduledGame, history []string) opponentChoice {
	// Reuse category selection and empty-history -> self behavior. Independent
	// mode RNG streams keep a mode's opponent schedule stable across workers.
	base := Checkpoint{Config: RunConfig{Pairing: BalancedPairing, Seed: gameSeed(c.Seed, uint64(game.Mode), 95), RuleOpponents: c.RuleOpponents, OpponentMix: &c.OpponentMix}, Opponents: history}
	return base.opponentAt(game.ModeGame)
}

func retainMixedHistory(history []string, state string, report Report) []string {
	if len(report.Epochs) == 0 {
		return history
	}
	base := Checkpoint{Config: RunConfig{PPO: DefaultPPOConfig()}, Opponents: history}
	return base.retainOpponent(state)
}

func mixedPolicies(root string, c MixedRunConfig, game MixedScheduledGame, learner LearningState, history []string, cache map[string]LearningState) ([2]Policy, opponentChoice, error) {
	choice := mixedOpponentAt(c, game, history)
	policies := [2]Policy{{Model: learner.Model}, {Model: learner.Model}}
	if choice.Rule != "" {
		policies[choice.Side] = Policy{Rule: choice.Rule, Features: battlepolicy.NetworkFeatures(c.Network)}
	} else if choice.Learning != "" {
		old, ok := cache[choice.Learning]
		if !ok {
			var err error
			old, err = loadLearning(filepath.Join(root, "learning"), choice.Learning)
			if err != nil {
				return policies, choice, err
			}
			if old.Model.Config != c.Network {
				return policies, choice, fmt.Errorf("mixed historical opponent changed architecture")
			}
			cache[choice.Learning] = old
		}
		policies[choice.Side] = Policy{Model: old.Model}
	}
	return policies, choice, nil
}

// loadMixedBatch validates both raw perspectives against the frozen schedule
// and replays their actual rule/neural behavior. It returns only the scheduled
// learner perspective, or both for explicit self-play. An identical historical
// model does not silently double a non-self game's training contribution.
func loadMixedBatch(ctx context.Context, root string, c MixedRunConfig, x MixedExperiment, schedule *MixedSchedule, first uint64, shards []string, learner LearningState, history []string, seen map[string]bool) ([]Trajectory, error) {
	var batch []Trajectory
	meta := x.Parts[0].Experiment.Environment
	cache := map[string]LearningState{}
	for i, ref := range shards {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if seen[ref] {
			return nil, fmt.Errorf("mixed shard reused in another committed slot")
		}
		seen[ref] = true
		game, err := schedule.Game(first + uint64(i))
		if err != nil {
			return nil, err
		}
		policies, choice, err := mixedPolicies(root, c, game, learner, history, cache)
		if err != nil {
			return nil, err
		}
		var versions [2]string
		for side, p := range policies {
			versions[side], err = p.Version()
			if err != nil {
				return nil, err
			}
		}
		pair, _, err := LoadShard(filepath.Join(root, "shards"), ref)
		if err != nil {
			return nil, err
		}
		if len(pair) != 2 {
			return nil, fmt.Errorf("mixed collection requires both game perspectives")
		}
		for side, tr := range pair {
			if tr.Side != side || tr.Mode != game.Mode || tr.Group != game.Group || !reflect.DeepEqual(tr.Setup, game.Scenario) || tr.Rules != meta.Rules || tr.Platform != meta.Platform || tr.Environment != meta.Scenario || tr.Policy != versions[side] || tr.Opponent != versions[1-side] || tr.PolicyKind != policies[side].Kind() || tr.Steps[0].Frame.Schema != battlepolicy.NetworkFeatures(c.Network) {
				return nil, fmt.Errorf("mixed shard differs from scheduled scenario, environment or policies")
			}
			other := pair[1-side]
			if tr.Match != other.Match || tr.Winner != other.Winner || tr.Terminated != other.Terminated || tr.Truncated != other.Truncated || len(tr.Steps) != len(other.Steps) {
				return nil, fmt.Errorf("mixed game perspectives disagree on outcome or boundary")
			}
			if err := verifyFeedbackBehavior(ctx, tr, policies[side], versions[side]); err != nil {
				return nil, fmt.Errorf("mixed behavior replay: %w", err)
			}
			if choice.Key == "self" || side != choice.Side {
				batch = append(batch, tr)
			}
		}
	}
	return batch, nil
}

// LoadMixedCheckpoint reconstructs the committed schedule, provenance and
// historical pool; replays all raw behavior; and verifies model/Adam links and
// update accounting. It does not rerun past gradient optimization.
func LoadMixedCheckpoint(ctx context.Context, root string) (LoadedMixedRun, error) {
	return LoadMixedCheckpointID(ctx, root, "")
}

// LoadMixedCheckpointID reads a retained immutable checkpoint without changing
// the latest pointer. Empty checkpoint selects the current committed pointer.
func LoadMixedCheckpointID(ctx context.Context, root, checkpoint string) (LoadedMixedRun, error) {
	var out LoadedMixedRun
	fail := func(err error) (LoadedMixedRun, error) { return LoadedMixedRun{}, err }
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	var pointer struct {
		Checkpoint string `json:"checkpoint"`
	}
	if checkpoint == "" {
		if err := readObject(filepath.Join(root, MixedPointer), &pointer, 1024); err != nil {
			return fail(err)
		}
	} else {
		pointer.Checkpoint = checkpoint
	}
	if !digest(pointer.Checkpoint) {
		return fail(fmt.Errorf("invalid mixed checkpoint pointer"))
	}
	var c MixedCheckpoint
	if err := readObject(filepath.Join(root, "mixed-checkpoints", pointer.Checkpoint+".json"), &c, 4<<20); err != nil {
		return fail(err)
	}
	id, _ := Digest(c)
	if id != pointer.Checkpoint || !digest(c.Config.Experiment) {
		return fail(fmt.Errorf("mixed checkpoint checksum mismatch"))
	}
	x, err := LoadMixedExperiment(filepath.Join(root, "mixed-experiments", c.Config.Experiment+".json"))
	if err != nil {
		return fail(err)
	}
	if err := c.validate(x); err != nil {
		return fail(err)
	}
	if parent := x.Parts[0].Experiment.InitialModel; parent != "" {
		a, err := battlepolicy.LoadArtifact(filepath.Join(root, "initial-models", parent+".json"))
		if err != nil {
			return fail(err)
		}
		out.Parent = &a
	}
	if err := x.ValidateParent(out.Parent); err != nil {
		return fail(err)
	}
	if out.Parent != nil {
		if out.Parent.Network.Config != c.Config.Network {
			return fail(fmt.Errorf("mixed parent network differs from frozen learner"))
		}
		out.TrainingGroups = append([]string(nil), out.Parent.TrainingGroups...)
	}
	initial, err := newInitialLearning(c.Config.Network, c.Config.Seed, out.Parent, c.Config.InitialPolicyScale)
	if err != nil {
		return fail(err)
	}
	receipt, err := verifyPolicyInitialization(ctx, root, c.Initialization, c.Config, out.Parent, c.Config.Network, c.Config.Seed, c.Config.InitialPolicyScale)
	if err != nil {
		return fail(err)
	}
	if receipt != nil && receipt.Learning != c.Start {
		return fail(fmt.Errorf("mixed initialization receipt differs from initial learning"))
	}
	initialID, _ := Digest(initial)
	if initialID != c.Start {
		return fail(fmt.Errorf("mixed initial model or fresh Adam differs from recipe"))
	}
	state, err := loadLearning(filepath.Join(root, "learning"), c.Start)
	if err != nil {
		return fail(err)
	}
	schedule, err := NewMixedSchedule(x, c.Config.Seed)
	if err != nil {
		return fail(err)
	}
	recipe, _ := Digest(c.Config)
	previous, first := c.Start, uint64(0)
	seen := map[string]bool{}
	for batchIndex, ref := range c.Reports {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		var receipt MixedBatchReceipt
		if err := readObject(filepath.Join(root, "mixed-reports", ref+".json"), &receipt, 4<<20); err != nil {
			return fail(err)
		}
		id, _ := Digest(receipt)
		if id != ref || receipt.Schema != "commander-mixed-batch-v1" || receipt.Recipe != recipe || receipt.Batch != batchIndex || receipt.Before != previous || !digest(receipt.After) || len(receipt.Shards) != schedule.BatchMatches() {
			return fail(fmt.Errorf("mixed batch receipt chain mismatch"))
		}
		episodes, err := loadMixedBatch(ctx, root, c.Config, x, schedule, first, receipt.Shards, state, out.History, seen)
		if err != nil {
			return fail(err)
		}
		next, err := loadLearning(filepath.Join(root, "learning"), receipt.After)
		if err != nil {
			return fail(err)
		}
		if err := validateMixedReport(receipt.Report, c.Config, x.Mixture, episodes, state, next); err != nil {
			return fail(err)
		}
		groups := make([]string, 0, len(episodes))
		for _, tr := range episodes {
			groups = append(groups, tr.Group)
		}
		out.TrainingGroups = sortedUnion(out.TrainingGroups, groups)
		out.History = retainMixedHistory(out.History, receipt.After, receipt.Report)
		out.Receipts = append(out.Receipts, receipt)
		previous, state = receipt.After, next
		first += uint64(len(receipt.Shards))
	}
	if previous != c.Learning {
		return fail(fmt.Errorf("mixed current learning differs from committed receipt chain"))
	}
	out.Pending, err = loadMixedBatch(ctx, root, c.Config, x, schedule, first, c.Pending, state, out.History, seen)
	if err != nil {
		return fail(err)
	}
	out.Checkpoint, out.Experiment, out.Learning = c, x, state
	return out, nil
}

func validateMixedReport(r Report, c MixedRunConfig, mixture ModeMixture, episodes []Trajectory, before, after LearningState) error {
	oldPolicy, _ := ModelDigest(before.Model)
	newPolicy, _ := ModelDigest(after.Model)
	if r.BehaviorPolicy != oldPolicy || r.CandidatePolicy != newPolicy || after.Model.Config != c.Network || !reflect.DeepEqual(r.ModeMixture, &mixture) || r.Episodes != len(episodes) || len(r.Epochs) > c.PPO.Epochs || r.StoppedForKL != (len(r.Epochs) < c.PPO.Epochs) || after.Optimizer.Step != before.Optimizer.Step+len(r.Epochs) || r.RejectedUpdates != len(r.ModeGuardFailures) {
		return fmt.Errorf("mixed report model, optimizer or objective identity mismatch")
	}
	var counts [6]ModeBatchReport
	turns := 0
	for _, tr := range episodes {
		count := &counts[tr.Mode]
		count.Mode, count.Episodes = tr.Mode, count.Episodes+1
		count.TeamTurns += len(tr.Steps)
		turns += len(tr.Steps)
		for _, step := range tr.Steps {
			count.Actions += len(step.Choices)
		}
	}
	var want []ModeBatchReport
	weight := 0
	for _, m := range mixture.Modes {
		if counts[m.Mode].Episodes == 0 {
			return fmt.Errorf("mixed receipt has no learner data for mode %d", m.Mode)
		}
		want = append(want, counts[m.Mode])
		weight += m.Weight
	}
	if turns != r.TeamTurns || !reflect.DeepEqual(want, r.Modes) {
		return fmt.Errorf("mixed report per-mode denominators differ from raw games")
	}
	if len(r.Epochs) == 0 && !reflect.DeepEqual(before, after) {
		return fmt.Errorf("rejected mixed batch changed learning state")
	}
	failures := 0
	for epochIndex, e := range r.Epochs {
		if e.Backtracks < 0 || e.Backtracks > 8 || e.LearningRate != math.Ldexp(c.PPO.LearningRate, -e.Backtracks) || !finite(e.GradientNorm) || e.GradientNorm < 0 || len(e.Modes) != len(want) || e.Objectives == nil || e.PostUpdateKL == nil {
			return fmt.Errorf("invalid mixed epoch update accounting")
		}
		for attempt := 0; attempt < e.Backtracks; attempt++ {
			if failures >= len(r.ModeGuardFailures) || !validMixedFailure(r.ModeGuardFailures[failures], epochIndex, attempt, c, mixture) {
				return fmt.Errorf("mixed accepted update lost its rejected attempts")
			}
			failures++
		}
		loss, kl, clip, post := 0., 0., 0., 0.
		terms := ObjectiveTerms{}
		for i, m := range e.Modes {
			if m.Mode != mixture.Modes[i].Mode || m.PostUpdateKL == nil {
				return fmt.Errorf("mixed epoch mode boundary mismatch")
			}
			for _, value := range []float64{m.Loss, m.ApproxKL, m.ClipFraction, m.Objectives.PolicyLoss, m.Objectives.ValueLoss, m.Objectives.EntropyLoss, m.Objectives.ConditionalEntropy, *m.PostUpdateKL} {
				if !finite(value) {
					return fmt.Errorf("nonfinite mixed epoch value")
				}
			}
			if m.ApproxKL < -1e-10 || m.ApproxKL > c.PPO.TargetKL+1e-10 || m.ClipFraction < 0 || m.ClipFraction > 1 || *m.PostUpdateKL < 0 || *m.PostUpdateKL > c.PPO.TargetKL {
				return fmt.Errorf("mixed mode violated policy update bounds")
			}
			w := float64(mixture.Modes[i].Weight) / float64(weight)
			loss, kl, clip, post = loss+w*m.Loss, kl+w*m.ApproxKL, clip+w*m.ClipFraction, post+w*(*m.PostUpdateKL)
			terms.PolicyLoss += w * m.Objectives.PolicyLoss
			terms.ValueLoss += w * m.Objectives.ValueLoss
			terms.EntropyLoss += w * m.Objectives.EntropyLoss
			terms.ConditionalEntropy += w * m.Objectives.ConditionalEntropy
		}
		if e.Loss != loss || e.ApproxKL != kl || e.ClipFraction != clip || *e.PostUpdateKL != post || *e.Objectives != terms {
			return fmt.Errorf("mixed epoch combined metrics do not match declared weights")
		}
	}
	remaining := len(r.ModeGuardFailures) - failures
	if remaining != 0 && (remaining != 9 || !r.StoppedForKL) {
		return fmt.Errorf("incomplete mixed exhausted-update accounting")
	}
	for attempt := 0; attempt < remaining; attempt++ {
		if !validMixedFailure(r.ModeGuardFailures[failures+attempt], len(r.Epochs), attempt, c, mixture) {
			return fmt.Errorf("invalid mixed exhausted-update attempt")
		}
	}
	return nil
}

func validMixedFailure(f ModeGuardFailure, epoch, attempt int, c MixedRunConfig, mixture ModeMixture) bool {
	if f.Epoch != epoch || f.LearningRate != math.Ldexp(c.PPO.LearningRate, -attempt) {
		return false
	}
	previous := 0
	for _, mode := range f.Modes {
		found := false
		for _, m := range mixture.Modes {
			found = found || m.Mode == mode
		}
		if !found || mode <= previous {
			return false
		}
		previous = mode
	}
	return true
}
