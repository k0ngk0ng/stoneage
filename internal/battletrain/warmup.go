package battletrain

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlenet"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

type WarmupConfig struct {
	Matches  int             `json:"matches"`
	Epochs   int             `json:"epochs"`
	Teachers []string        `json:"teachers"`
	Update   ImitationConfig `json:"update"`
}

func DefaultWarmupConfig() *WarmupConfig {
	return &WarmupConfig{Matches: 32, Epochs: 8, Teachers: battlepolicy.DefaultRuleNames(), Update: ImitationConfig{BatchEpisodes: 8, SequenceLength: 16, LearningRate: .001, GradientClip: .5}}
}

func (c *WarmupConfig) validate() error {
	if c == nil {
		return nil // v1 checkpoints and explicit ablation runs have no warmup.
	}
	if c.Matches == 0 && c.Update.ActionWeighting != "" {
		return fmt.Errorf("action weighting requires enabled imitation warmup")
	}
	if c.Matches < 0 || c.Matches > 1000 || c.Matches%4 != 0 || c.Epochs < 1 || c.Epochs > 1000 || len(c.Teachers) < 1 || len(c.Teachers) > len(battlepolicy.RuleNames()) {
		return fmt.Errorf("warmup needs 0 or a multiple of 4 matches, 1..1000 epochs and known teachers")
	}
	seen := map[string]bool{}
	for _, rule := range c.Teachers {
		if seen[rule] || !battlepolicy.RuleSupported(rule) {
			return fmt.Errorf("invalid or duplicate warmup teacher %q", rule)
		}
		seen[rule] = true
	}
	return c.Update.validate()
}

type WarmupState struct {
	Shards          []string         `json:"shards"`
	CompletedEpochs int              `json:"completed_epochs"`
	LastReport      *ImitationReport `json:"last_report,omitempty"`
}

func (c Checkpoint) warmupComplete() bool {
	return c.Config.Warmup == nil || c.Config.Warmup.Matches == 0 || c.Warmup != nil && c.Warmup.CompletedEpochs == c.Config.Warmup.Epochs
}

func warmupScenario(c RunConfig, game uint64, experiments ...*Experiment) (battleenv.Scenario, string, [2]Policy) {
	// Independent deterministic sequence from PPO, still canonicalized by
	// actual initial conditions for exclusion from validation/test suites.
	c.Seed = gameSeed(c.Seed, 0, 20)
	s, group := scenarioFor(c, game)
	if len(experiments) > 0 && experiments[0] != nil {
		s, group = experiments[0].trainingScenario(c.Seed, game)
	}
	names := c.Warmup.Teachers
	size := uint64(familyGames(c.Pairing))
	policies := [2]Policy{{Rule: names[(game/size)%uint64(len(names))]}, {Rule: names[(game/size+1)%uint64(len(names))]}}
	for i := range policies {
		policies[i].Features = battlepolicy.NetworkFeatures(c.Network)
	}
	if game%2 == 1 {
		policies[0], policies[1] = policies[1], policies[0]
	}
	return s, group, policies
}

// A warmup-only export has no PPO league evidence. Verify the collected
// teacher shards, exact schedule and consumed groups before publishing it.
func validateWarmupExport(ctx context.Context, root string, c Checkpoint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	x, err := loadRunExperiment(root, c.Config)
	if err != nil {
		return err
	}
	initial, err := loadInitialModel(root, c.Config)
	if err != nil {
		return err
	}
	groups := map[string]bool{}
	if initial != nil {
		for _, group := range initial.TrainingGroups {
			groups[group] = true
		}
	}
	turns, actions := 0, 0
	var counts map[string]int
	if c.Config.Warmup.Update.ActionWeighting != "" {
		counts = map[string]int{}
	}
	for game, id := range c.Warmup.Shards {
		if err := ctx.Err(); err != nil {
			return err
		}
		trajectories, _, err := LoadShard(filepath.Join(root, "shards"), id)
		if err != nil {
			return err
		}
		scenario, group, policies := warmupScenario(c.Config, uint64(game), x)
		scenarioID, _ := Digest(scenario)
		if len(trajectories) != 2 {
			return fmt.Errorf("warmup export requires both perspectives")
		}
		for side, tr := range trajectories {
			policy, _ := policies[side].Version()
			if tr.Rules != c.Environment.Rules || tr.Platform != c.Environment.Platform || tr.Environment != c.Environment.Scenario || tr.Side != side || tr.Scenario != scenarioID || tr.Group != group || tr.Policy != policy || tr.PolicyKind != policies[side].Kind() {
				return fmt.Errorf("warmup export differs from saved teacher schedule")
			}
			turns += len(tr.Steps)
			for _, step := range tr.Steps {
				if err := ctx.Err(); err != nil {
					return err
				}
				choices, err := battlepolicy.RuleChoices(step.Frame, policies[side].Rule)
				if err != nil || !reflect.DeepEqual(choices, step.Choices) {
					return fmt.Errorf("warmup export teacher choices differ from frozen rule")
				}
				actions += len(step.Choices)
				if counts != nil {
					if err := addImitationCounts(counts, step.Frame, step.Choices); err != nil {
						return err
					}
				}
			}
		}
		groups[group] = true
	}
	var expected []string
	for group := range groups {
		expected = append(expected, group)
	}
	sort.Strings(expected)
	if !reflect.DeepEqual(expected, c.TrainingGroups) || turns != c.Warmup.LastReport.TeamTurns || actions != c.Warmup.LastReport.Actions {
		return fmt.Errorf("warmup export provenance or report differs from original trajectories")
	}
	return validateImitationWeights(c.Config.Warmup.Update, *c.Warmup.LastReport, counts)
}

func runWarmup(ctx context.Context, root string, workers *collectionWorkers, c *Checkpoint, learning *LearningState, experiment *Experiment, emit func(string) error) error {
	if c.warmupComplete() {
		return nil
	}
	if c.Warmup == nil {
		c.Warmup = &WarmupState{}
	}
	w := c.Warmup
	for len(w.Shards) < c.Config.Warmup.Matches {
		jobs := make([]collectionJob, min(len(workers.engines), c.Config.Warmup.Matches-len(w.Shards)))
		for i := range jobs {
			scenario, group, policies := warmupScenario(c.Config, uint64(len(w.Shards)+i), experiment)
			jobs[i] = collectionJob{scenario: scenario, policies: policies, group: group}
		}
		results, e := workers.collect(ctx, jobs)
		if e != nil {
			return e
		}
		for _, tr := range results {
			if e := ctx.Err(); e != nil {
				return e
			}
			shard, e := SaveShard(filepath.Join(root, "shards"), tr[:])
			if e != nil {
				return e
			}
			w.Shards = append(w.Shards, shard.Digest)
			if e = saveCheckpoint(root, *c); e != nil {
				return e
			}
			if e = emit("warmup_collected"); e != nil {
				return e
			}
		}
	}
	var data teacherDataset
	for game, id := range w.Shards {
		if e := ctx.Err(); e != nil {
			return e
		}
		tr, _, e := LoadShard(filepath.Join(root, "shards"), id)
		if e != nil {
			return e
		}
		scenario, group, policies := warmupScenario(c.Config, uint64(game), experiment)
		scenarioID, _ := Digest(scenario)
		if len(tr) != 2 {
			return fmt.Errorf("warmup shard must contain both perspectives")
		}
		for side, t := range tr {
			policyID, _ := policies[side].Version()
			if t.Rules != c.Environment.Rules || t.Platform != c.Environment.Platform || t.Environment != c.Environment.Scenario || t.Scenario != scenarioID || t.Group != group || t.Side != side || t.Policy != policyID || t.PolicyKind != policies[side].Kind() {
				return fmt.Errorf("warmup shard differs from saved collection schedule")
			}
			if e := data.add(ctx, t); e != nil {
				return e
			}
		}
	}
	for w.CompletedEpochs < c.Config.Warmup.Epochs {
		r, e := data.update(ctx, learning.Model, learning.Optimizer, c.Config.Warmup.Update)
		if e != nil {
			return e
		}
		w.CompletedEpochs++
		w.LastReport = &r
		addTrainingGroupIDs(c, data.groups)
		if c.warmupComplete() {
			// PPO starts with fresh moments; behavior-cloning momentum must not
			// silently dominate the first reinforcement-learning updates.
			learning.Optimizer = &battlenet.Adam[float32]{}
		}
		c.Learning, e = saveLearning(filepath.Join(root, "learning"), *learning)
		if e != nil {
			return e
		}
		if e = saveCheckpoint(root, *c); e != nil {
			return e
		}
		if e = emit("warmup_trained"); e != nil {
			return e
		}
	}
	return nil
}
