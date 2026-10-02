package battletrain

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlenet"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

type LearningState struct {
	Schema    int                       `json:"schema_version"`
	Model     *battlenet.Model[float32] `json:"model"`
	Optimizer *battlenet.Adam[float32]  `json:"optimizer"`
}

func (s LearningState) Validate() error {
	if s.Schema != 1 || s.Model == nil || s.Optimizer == nil {
		return fmt.Errorf("invalid learning state")
	}
	if e := s.Model.Validate(); e != nil {
		return e
	}
	a := s.Optimizer
	if a.Step < 0 || a.Step >= 1000000000 || a.Step == 0 && len(a.Moments) != 0 || a.Step > 0 && len(a.Moments) != len(s.Model.Parameters) {
		return fmt.Errorf("invalid optimizer checkpoint")
	}
	for name, m := range a.Moments {
		p, ok := s.Model.Parameters[name]
		if !ok || len(m.First) != len(p.Values) || len(m.Second) != len(p.Values) {
			return fmt.Errorf("optimizer checkpoint shape mismatch")
		}
		for i, x := range m.First {
			if !finite(float64(x)) || !finite(float64(m.Second[i])) || m.Second[i] < 0 {
				return fmt.Errorf("nonfinite optimizer checkpoint")
			}
		}
	}
	return nil
}

// Weights/moments are saved only after an optimizer batch, not duplicated for
// every collected match. Lightweight progress snapshots reference these blobs.
func saveLearning(directory string, s LearningState) (string, error) {
	if e := s.Validate(); e != nil {
		return "", e
	}
	id, e := Digest(s)
	if e != nil {
		return "", e
	}
	if e = os.MkdirAll(directory, 0700); e != nil {
		return "", e
	}
	return id, writeObject(filepath.Join(directory, id+".json"), s)
}
func loadLearning(directory, id string) (LearningState, error) {
	var s LearningState
	if !digest(id) {
		return s, fmt.Errorf("invalid learning state identity")
	}
	if e := readObject(filepath.Join(directory, id+".json"), &s, 128<<20); e != nil {
		return s, e
	}
	actual, e := Digest(s)
	if e != nil {
		return s, e
	}
	if actual != id {
		return s, fmt.Errorf("learning state checksum mismatch")
	}
	return s, s.Validate()
}

type Checkpoint struct {
	Initialization   string             `json:"initialization,omitempty"`
	OpeningReports   []string           `json:"opening_reports,omitempty"`
	LeagueReports    []string           `json:"league_reports,omitempty"`
	OpponentScores   []OpponentScore    `json:"opponent_scores,omitempty"`
	Schema           int                `json:"schema_version"`
	Features         string             `json:"features"`
	Actions          string             `json:"actions"`
	Config           RunConfig          `json:"config"`
	Environment      battleenv.Metadata `json:"environment"`
	Learning         string             `json:"learning"`
	CompletedBatches int                `json:"completed_batches"`
	NextGame         uint64             `json:"next_game"`
	Pending          []string           `json:"pending_shards"`
	Trained          []string           `json:"trained_shards"`
	TrainingGroups   []string           `json:"training_groups"`
	SelectionGroups  []string           `json:"selection_groups,omitempty"`
	Opponents        []string           `json:"opponent_learning_states"`
	LastReport       *Report            `json:"last_report,omitempty"`
	Warmup           *WarmupState       `json:"warmup,omitempty"`
}

func (c Checkpoint) Validate() error {
	if e := c.Config.Validate(); e != nil {
		return e
	}
	if (c.Schema == 5) != (c.Config.InitialPolicyScale != 0) || c.Config.InitialPolicyScale != 0 && !digest(c.Initialization) || c.Config.InitialPolicyScale == 0 && c.Initialization != "" {
		return fmt.Errorf("policy initialization checkpoint schema mismatch")
	}
	if c.Schema != 5 && (c.Schema == 4) != (c.Config.OpeningRollouts > 0) || c.Config.OpeningRollouts > 0 && len(c.OpeningReports) != c.CompletedBatches || c.Config.OpeningRollouts == 0 && len(c.OpeningReports) != 0 {
		return fmt.Errorf("opening checkpoint schema/progress mismatch")
	}
	for _, id := range c.OpeningReports {
		if !digest(id) {
			return fmt.Errorf("invalid opening receipt reference")
		}
	}
	if c.Config.OpponentSampling == "weakness-v1" {
		if len(c.LeagueReports) != c.CompletedBatches {
			return fmt.Errorf("league progress mismatch")
		}
		for _, id := range c.LeagueReports {
			if !digest(id) {
				return fmt.Errorf("invalid league reference")
			}
		}
		previous := ""
		for _, s := range c.OpponentScores {
			validKey := false
			for _, rule := range c.Config.ruleOpponents() {
				validKey = validKey || s.Key == "rule:"+rule
			}
			if strings.HasPrefix(s.Key, "history:") {
				validKey = digest(strings.TrimPrefix(s.Key, "history:"))
			}
			if !validKey || s.Key <= previous || s.Wins < 0 || s.Losses < 0 || s.Draws < 0 || s.Truncated < 0 || s.Wins > 8000 || s.Losses > 8000 || s.Draws > 8000 || s.Truncated > 8000 || s.Wins+s.Losses+s.Draws+s.Truncated == 0 || s.Wins+s.Losses+s.Draws+s.Truncated > 8*c.Config.BatchMatches {
				return fmt.Errorf("invalid opponent score")
			}
			previous = s.Key
		}
	} else if len(c.LeagueReports) > 0 || len(c.OpponentScores) > 0 {
		return fmt.Errorf("uniform schedule cannot consume weakness scores")
	}

	if !validGroupList(c.SelectionGroups) {
		return fmt.Errorf("invalid checkpoint selection provenance")
	}
	if c.Config.Experiment == "" && len(c.SelectionGroups) > 0 {
		return fmt.Errorf("checkpoint selection data requires its frozen experiment")
	}
	if !battlepolicy.SupportedFeatures(c.Features) || c.Features != battlepolicy.NetworkFeatures(c.Config.Network) || c.Actions != battlepolicy.ActionsForFeatures(c.Features) {
		return fmt.Errorf("checkpoint feature/action schema incompatible")
	}
	if c.Schema < 1 || c.Schema > 5 || c.Schema < 4 && ((c.Schema == 3) != (c.Config.Pairing == BalancedPairing)) || c.Schema == 1 && (c.Config.Warmup != nil || c.Warmup != nil || c.Config.InitialModel != "" || len(c.SelectionGroups) > 0) || !digest(c.Learning) || c.CompletedBatches < 0 || c.CompletedBatches > 1000000 || c.NextGame > math.MaxInt32 || len(c.Pending) > c.Config.BatchMatches || len(c.Opponents) > 32 {
		return fmt.Errorf("invalid training checkpoint")
	}
	if e := c.Config.Validate(); e != nil {
		return e
	}
	if e := c.Environment.Validate(); e != nil {
		return e
	}
	if e := battlepolicy.ValidateFeatureEnvironment(c.Features, c.Environment.Scenario); e != nil {
		return e
	}
	if c.Config.PetSkillMask != 0 && c.Environment.Scenario != "controlled-battle-v8" {
		return fmt.Errorf("active pet skill configuration requires controlled-battle-v8")
	}
	seen := map[string]bool{}
	lists := [][]string{c.Pending, c.Trained}
	if c.Warmup != nil {
		w, config := c.Warmup, c.Config.Warmup
		if config == nil || config.Matches == 0 || len(w.Shards) > config.Matches || w.CompletedEpochs < 0 || w.CompletedEpochs > config.Epochs || w.CompletedEpochs > 0 && (len(w.Shards) != config.Matches || w.LastReport == nil || len(c.TrainingGroups) == 0) || w.CompletedEpochs == 0 && w.LastReport != nil {
			return fmt.Errorf("invalid warmup checkpoint progress")
		}
		if w.LastReport != nil && (!digest(w.LastReport.BeforePolicy) || !digest(w.LastReport.AfterPolicy) || w.LastReport.Episodes != config.Matches*2 || w.LastReport.TeamTurns < w.LastReport.Episodes || w.LastReport.Actions < w.LastReport.TeamTurns || !finite(w.LastReport.CrossEntropy) || w.LastReport.CrossEntropy < 0 || !finite(w.LastReport.GradientNorm) || w.LastReport.GradientNorm < 0) {
			return fmt.Errorf("invalid warmup checkpoint report")
		}
		if w.LastReport != nil {
			if err := validateImitationWeights(config.Update, *w.LastReport, nil); err != nil {
				return err
			}
			updates := 0
			if config.Update.BatchEpisodes > 0 {
				updates = (config.Matches*2 + config.Update.BatchEpisodes - 1) / config.Update.BatchEpisodes
			}
			if w.LastReport.OptimizerUpdates != updates {
				return fmt.Errorf("warmup minibatch update count mismatch")
			}
		}
		lists = append(lists, w.Shards)
	}
	if !c.warmupComplete() && (c.NextGame != 0 || c.CompletedBatches != 0 || len(c.Opponents) != 0 || c.LastReport != nil) {
		return fmt.Errorf("PPO progress before warmup completion")
	}
	for _, list := range lists {
		for _, s := range list {
			if !digest(s) || seen[s] {
				return fmt.Errorf("duplicate or invalid checkpoint shard")
			}
			seen[s] = true
		}
	}
	if c.NextGame != uint64(len(c.Pending)+len(c.Trained)) || len(c.Trained) != c.CompletedBatches*c.Config.BatchMatches {
		return fmt.Errorf("checkpoint progress/count mismatch")
	}
	if c.CompletedBatches > 0 && len(c.TrainingGroups) == 0 {
		return fmt.Errorf("checkpoint training groups missing")
	}
	previous := ""
	for _, id := range c.TrainingGroups {
		if !digest(id) || id <= previous {
			return fmt.Errorf("invalid checkpoint training groups")
		}
		previous = id
	}
	states := map[string]bool{}
	for _, s := range c.Opponents {
		if !digest(s) || c.Config.PPO.UpdateGuard == "backtrack-v1" && states[s] {
			return fmt.Errorf("invalid opponent state")
		}
		states[s] = true
	}
	return nil
}

func saveCheckpoint(root string, c Checkpoint) error {
	if e := c.Validate(); e != nil {
		return e
	}
	id, e := Digest(c)
	if e != nil {
		return e
	}
	dir := filepath.Join(root, "checkpoints")
	if e = os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	if e = writeObject(filepath.Join(dir, id+".json"), c); e != nil {
		return e
	}
	// A single atomic pointer chooses the committed state. The process lock
	// is held across this write; interruption before rename leaves old state.
	f, e := os.CreateTemp(root, ".latest-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if e = json.NewEncoder(f).Encode(struct {
		Checkpoint string `json:"checkpoint"`
	}{id}); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = os.Rename(f.Name(), filepath.Join(root, "latest.json")); e != nil {
		return e
	}
	d, e := os.Open(root)
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func LoadCheckpoint(root string) (Checkpoint, LearningState, error) {
	return loadCheckpoint(context.Background(), root)
}

// LoadCheckpointContext allows callers to cancel historical sampling checks.
func LoadCheckpointContext(ctx context.Context, root string) (Checkpoint, LearningState, error) {
	return loadCheckpoint(ctx, root)
}

func loadCheckpoint(ctx context.Context, root string) (Checkpoint, LearningState, error) {
	var pointer struct {
		Checkpoint string `json:"checkpoint"`
	}
	var c Checkpoint
	var s LearningState
	if e := readObject(filepath.Join(root, "latest.json"), &pointer, 1024); e != nil {
		return c, s, e
	}
	if !digest(pointer.Checkpoint) {
		return c, s, fmt.Errorf("invalid checkpoint pointer")
	}
	return loadCheckpointID(ctx, root, pointer.Checkpoint)
}

// LoadCheckpointID verifies an immutable saved checkpoint without changing the
// training pointer. Historical exports use exactly the same provenance checks
// as a resumed current checkpoint; ids are digests, never arbitrary paths.
func LoadCheckpointID(root, checkpoint string) (Checkpoint, LearningState, error) {
	return loadCheckpointID(context.Background(), root, checkpoint)
}

func loadCheckpointID(ctx context.Context, root, checkpoint string) (Checkpoint, LearningState, error) {
	var c Checkpoint
	var s LearningState
	if !digest(checkpoint) {
		return c, s, fmt.Errorf("invalid checkpoint digest")
	}
	if e := readObject(filepath.Join(root, "checkpoints", checkpoint+".json"), &c, 16<<20); e != nil {
		return c, s, e
	}
	id, e := Digest(c)
	if e != nil {
		return c, s, e
	}
	if id != checkpoint {
		return c, s, fmt.Errorf("checkpoint checksum mismatch")
	}
	if e = c.Validate(); e != nil {
		return c, s, e
	}
	x, e := loadRunExperiment(root, c.Config)
	if e != nil {
		return c, s, e
	}
	initial, e := loadInitialModel(root, c.Config)
	if e != nil {
		return c, s, e
	}
	if initial != nil && !initial.CompatibleNativeEnvironment(c.Environment) {
		return c, s, fmt.Errorf("parent environment differs from checkpoint")
	}
	if x != nil {
		if x.Environment != c.Environment {
			return c, s, fmt.Errorf("checkpoint environment differs from experiment")
		}
		if x.InitialModel != c.Config.InitialModel {
			return c, s, fmt.Errorf("checkpoint parent differs from experiment")
		}
		if e = x.validateInitialModel(initial); e != nil {
			return c, s, e
		}
		provenance := battlepolicy.Artifact{Parent: c.Config.InitialModel, TrainingGroups: c.TrainingGroups, SelectionGroups: c.SelectionGroups}
		provenance.RecordedSelectionArtifacts = x.RecordedSelectionArtifacts
		if initial != nil {
			provenance.Recorded = initial.Recorded
		}
		if e = x.validateCandidateProvenance(provenance); e != nil {
			return c, s, e
		}
	}
	s, e = loadLearning(filepath.Join(root, "learning"), c.Learning)
	if e == nil && s.Model.Config != c.Config.Network {
		e = fmt.Errorf("checkpoint network configuration mismatch")
	}
	if e == nil {
		var receipt *PolicyInitialization
		receipt, e = verifyPolicyInitialization(ctx, root, c.Initialization, c.Config, initial, c.Config.Network, c.Config.Seed, c.Config.InitialPolicyScale)
		if e == nil {
			e = validateInitialPolicyData(ctx, root, c, x, receipt)
		}
	}
	if e == nil && initial != nil && c.Config.InitialPolicyScale == 0 && c.CompletedBatches == 0 && (c.Warmup == nil || c.Warmup.CompletedEpochs == 0) {
		version, err := ModelDigest(s.Model)
		if err != nil || version != initial.WeightsDigest || s.Optimizer.Step != 0 {
			e = fmt.Errorf("untrained fine-tuning state differs from initial weights/fresh optimizer")
		}
	}
	if e == nil && c.Warmup != nil && c.CompletedBatches == 0 {
		wantStep := c.Warmup.CompletedEpochs
		if c.Config.Warmup.Update.BatchEpisodes > 0 {
			n := c.Config.Warmup.Matches * 2
			wantStep *= (n + c.Config.Warmup.Update.BatchEpisodes - 1) / c.Config.Warmup.Update.BatchEpisodes
		}
		if c.warmupComplete() {
			wantStep = 0
		}
		if s.Optimizer.Step != wantStep {
			e = fmt.Errorf("warmup optimizer progress mismatch")
		}
		if e == nil && c.Warmup.LastReport != nil {
			version, err := ModelDigest(s.Model)
			if err != nil || version != c.Warmup.LastReport.AfterPolicy {
				e = fmt.Errorf("warmup weights differ from last report")
			}
		}
	}
	if e == nil {
		e = loadOpeningReceipts(ctx, root, c, x)
	}
	if e == nil && c.Warmup != nil && c.Warmup.LastReport != nil && c.Config.Warmup.Update.ActionWeighting != "" {
		e = validateStoredWarmupWeights(ctx, root, c, x)
	}
	return c, s, e
}

// ExportCandidate publishes an inference artifact with an explicit candidate
// status. This cannot grant itself arena certification or update a live model.
func ExportCandidate(root, output string) (string, error) {
	return ExportCheckpointCandidate(root, "", output)
}

// ExportCheckpointCandidate selects an immutable checkpoint, or the current
// committed pointer when checkpoint is empty. A completed imitation epoch is
// a legitimate candidate for independent evaluation, without pretending PPO
// has run. Exporting never rewinds or promotes the ongoing training state.
func ExportCheckpointCandidate(root, checkpoint, output string) (string, error) {
	return ExportCheckpointCandidateContext(context.Background(), root, checkpoint, output)
}

// ExportCheckpointCandidateContext makes source verification cancellable,
// including the potentially long sampled-history replay of repeated openings.
func ExportCheckpointCandidateContext(ctx context.Context, root, checkpoint, output string) (string, error) {
	if e := ctx.Err(); e != nil {
		return "", e
	}
	var c Checkpoint
	var s LearningState
	var e error
	if checkpoint == "" {
		c, s, e = loadCheckpoint(ctx, root)
	} else {
		c, s, e = loadCheckpointID(ctx, root, checkpoint)
	}
	if e != nil {
		return "", e
	}
	if _, e = loadLeague(ctx, root, c); e != nil {
		return "", e
	}
	if (c.LastReport == nil || c.CompletedBatches == 0) && (c.Warmup == nil || c.Warmup.CompletedEpochs == 0 || c.Warmup.LastReport == nil) {
		return "", fmt.Errorf("no completed imitation epoch or PPO batch")
	}
	if c.CompletedBatches == 0 {
		if e = validateWarmupExport(ctx, root, c); e != nil {
			return "", e
		}
	}
	weights, e := ModelDigest(s.Model)
	if e != nil {
		return "", e
	}
	var reportSource any = c.LastReport
	shards := append([]string(nil), c.Trained...)
	if c.Warmup != nil && c.Warmup.CompletedEpochs > 0 {
		shards = append(shards, c.Warmup.Shards...)
		reportSource = struct {
			PPO    *Report       `json:"ppo"`
			Warmup *WarmupState  `json:"warmup"`
			Config *WarmupConfig `json:"warmup_config"`
		}{c.LastReport, c.Warmup, c.Config.Warmup}
	}
	if c.Config.OpponentSampling == "weakness-v1" {
		reportSource = struct {
			Training      any      `json:"training"`
			Sampling      string   `json:"opponent_sampling"`
			RuleOpponents []string `json:"rule_opponents"`
			LeagueReports []string `json:"league_reports"`
		}{reportSource, c.Config.OpponentSampling, c.Config.ruleOpponents(), c.LeagueReports}
	}
	if c.Config.OpponentMix != nil {
		reportSource = struct {
			Training any         `json:"training"`
			Mix      OpponentMix `json:"opponent_mix"`
		}{reportSource, *c.Config.OpponentMix}
	}
	if c.Config.OpeningRollouts > 0 {
		reportSource = struct {
			Training any       `json:"training"`
			Config   RunConfig `json:"config"`
			Receipts []string  `json:"opening_reports"`
		}{reportSource, c.Config, c.OpeningReports}
	}
	initial, e := loadInitialModel(root, c.Config)
	if e != nil {
		return "", e
	}
	if initial != nil {
		shards = sortedUnion(shards, initial.TrainingShards)
	}
	if initial != nil || len(c.SelectionGroups) > 0 {
		reportSource = struct {
			Progress     any       `json:"progress"`
			Config       RunConfig `json:"config"`
			InitialModel string    `json:"initial_model,omitempty"`
		}{reportSource, c.Config, c.Config.InitialModel}
	}
	if c.Initialization != "" {
		receipt, err := verifyPolicyInitialization(ctx, root, c.Initialization, c.Config, initial, c.Config.Network, c.Config.Seed, c.Config.InitialPolicyScale)
		if err != nil {
			return "", err
		}
		reportSource = struct {
			Training       any                   `json:"training"`
			Initialization *PolicyInitialization `json:"initialization"`
		}{reportSource, receipt}
	}
	report, e := Digest(reportSource)
	if e != nil {
		return "", e
	}
	if e = ctx.Err(); e != nil {
		return "", e
	}
	if e = os.MkdirAll(filepath.Join(root, "reports"), 0700); e != nil {
		return "", e
	}
	if e = writeObject(filepath.Join(root, "reports", report+".json"), reportSource); e != nil {
		return "", e
	}
	a := battlepolicy.Artifact{Schema: 2, Architecture: battlepolicy.NetworkArchitecture(s.Model.Config), Features: c.Features, Actions: c.Actions, Status: "candidate", Environment: c.Environment, Modes: []int{c.Config.Mode}, WeightsDigest: weights, TrainingReport: report, TrainingShards: shards, TrainingGroups: c.TrainingGroups, Network: s.Model}
	if initial != nil && initial.Recorded != nil {
		a.Schema, a.Recorded = 4, initial.Recorded
	}
	a.Experiment = c.Config.Experiment
	a.Parent, a.SelectionGroups = c.Config.InitialModel, c.SelectionGroups
	x, e := loadRunExperiment(root, c.Config)
	if e != nil {
		return "", e
	}
	if x != nil {
		a.HeldoutGroups = x.heldoutGroups()
		if len(x.RecordedSelectionArtifacts) > 0 {
			a.Schema = 5
			a.RecordedSelectionArtifacts = append([]string(nil), x.RecordedSelectionArtifacts...)
		}
	}
	if e = a.Validate(); e != nil {
		return "", e
	}
	id, e := Digest(a)
	if e != nil {
		return "", e
	}
	if output == "" {
		output = filepath.Join(root, "models", id+".json")
	}
	if e = ctx.Err(); e != nil {
		return "", e
	}
	if e = os.MkdirAll(filepath.Dir(output), 0700); e != nil {
		return "", e
	}
	return output, writeObject(output, a)
}
