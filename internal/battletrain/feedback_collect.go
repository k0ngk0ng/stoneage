package battletrain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

const FeedbackCollectionPointer = "feedback-collection-latest.json"

type FeedbackCollectionConfig struct {
	Seed      int64    `json:"seed"`
	Matches   int      `json:"matches"`
	Teacher   string   `json:"teacher"`
	Opponents []string `json:"opponents"`
	Greedy    bool     `json:"greedy"`
}

func (c FeedbackCollectionConfig) validate(x Experiment) error {
	if c.Matches < familyGames(x.Pairing)*len(c.Opponents) || c.Matches < 1 || c.Matches > 256 || c.Matches%familyGames(x.Pairing) != 0 || !battlepolicy.RuleSupported(c.Teacher) || len(c.Opponents) < 1 || len(c.Opponents) > len(battlepolicy.RuleNames()) {
		return fmt.Errorf("feedback collection needs 1..256 complete paired games, a known teacher and rule opponents")
	}
	seen := map[string]bool{}
	for _, rule := range c.Opponents {
		if seen[rule] || !battlepolicy.RuleSupported(rule) {
			return fmt.Errorf("unknown or duplicate feedback opponent")
		}
		seen[rule] = true
	}
	return nil
}

type FeedbackCollectedGame struct {
	Recipe  string `json:"recipe_digest"`
	Raw     string `json:"raw_shard"`
	Dataset string `json:"feedback_dataset"`
}

func feedbackCollectionRecipe(c FeedbackCollectionCheckpoint) string {
	c.Games = nil
	id, _ := Digest(c)
	return id
}

type FeedbackCollectionCheckpoint struct {
	Schema     string                   `json:"schema"`
	Experiment string                   `json:"experiment"`
	Model      string                   `json:"behavior_artifact"`
	Config     FeedbackCollectionConfig `json:"config"`
	Games      []FeedbackCollectedGame  `json:"games"`
}

type FeedbackCollectionProgress struct {
	Event               string `json:"event"`
	Games               int    `json:"completed_games"`
	Matches             int    `json:"matches"`
	Checkpoint          string `json:"checkpoint"`
	DataBytes           int64  `json:"data_bytes,omitempty"`
	StopAtBytes         int64  `json:"stop_at_data_bytes,omitempty"`
	StorageTriggerStage string `json:"storage_trigger_stage,omitempty"`
}

type FeedbackCollectionOptions struct {
	Directory       string
	Command         []string
	Experiment      *Experiment
	Model           *battlepolicy.Artifact
	Config          *FeedbackCollectionConfig
	Resume          bool
	Workers         int
	StopAtDataBytes int64
	Stderr          io.Writer
	Progress        func(FeedbackCollectionProgress) error
}

// The experiment owns family/side pairing; this schedule adds behavior-side
// swaps, rule opponents and independent action RNGs. Workers and resume points
// cannot change any numerical initial conditions or sampled policy choices.
func feedbackCollectionJob(x Experiment, a battlepolicy.Artifact, c FeedbackCollectionConfig, game int) collectionJob {
	scenario, group := x.trainingScenario(c.Seed, uint64(game))
	side := game % 2
	var policies [2]Policy
	policies[side] = Policy{Model: a.Network, Greedy: c.Greedy}
	policies[1-side] = Policy{Rule: c.Opponents[(game/familyGames(x.Pairing))%len(c.Opponents)], Features: a.Features}
	var random [2]*rand.Rand
	if !c.Greedy {
		random[side] = rand.New(rand.NewSource(gameSeed(c.Seed, uint64(game), 101)))
	}
	return collectionJob{scenario: scenario, group: group, policies: policies, random: random}
}

func saveFeedbackCollection(root string, c FeedbackCollectionCheckpoint) error {
	return saveFeedbackCheckpointObject(root, "feedback-collection-checkpoints", FeedbackCollectionPointer, c)
}

// LoadFeedbackCollection verifies committed complete games, their fixed
// schedule and separate learner-only feedback. Opponent observations/actions
// remain in the raw pair; they are not silently added to supervised updates.
func LoadFeedbackCollection(ctx context.Context, root string) (FeedbackCollectionCheckpoint, Experiment, battlepolicy.Artifact, error) {
	var c FeedbackCollectionCheckpoint
	var x Experiment
	var a battlepolicy.Artifact
	fail := func(err error) (FeedbackCollectionCheckpoint, Experiment, battlepolicy.Artifact, error) {
		return c, x, a, err
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	var p struct {
		Checkpoint string `json:"checkpoint"`
	}
	if err := readObject(filepath.Join(root, FeedbackCollectionPointer), &p, 1024); err != nil {
		return fail(err)
	}
	if !digest(p.Checkpoint) {
		return fail(fmt.Errorf("invalid feedback collection checkpoint reference"))
	}
	if err := readObject(filepath.Join(root, "feedback-collection-checkpoints", p.Checkpoint+".json"), &c, 4<<20); err != nil {
		return fail(err)
	}
	id, _ := Digest(c)
	if id != p.Checkpoint || c.Schema != "commander-feedback-collection-v1" || !digest(c.Experiment) || !digest(c.Model) || len(c.Games) > 256 {
		return fail(fmt.Errorf("invalid feedback collection checkpoint"))
	}
	var err error
	x, err = LoadExperiment(filepath.Join(root, "experiments", c.Experiment+".json"))
	if err != nil {
		return fail(err)
	}
	id, _ = Digest(x)
	if id != c.Experiment {
		return fail(fmt.Errorf("feedback collection experiment checksum differs"))
	}
	if err := c.Config.validate(x); err != nil {
		return fail(err)
	}
	if len(c.Games) > c.Config.Matches {
		return fail(fmt.Errorf("feedback collection exceeds frozen budget"))
	}
	a, err = battlepolicy.LoadArtifact(filepath.Join(root, "behaviors", c.Model+".json"))
	if err != nil {
		return fail(err)
	}
	id, _ = Digest(a)
	if id != c.Model {
		return fail(fmt.Errorf("feedback collection behavior checksum differs"))
	}
	if err := validateFeedbackArtifact(x, a); err != nil {
		return fail(err)
	}
	seen := map[string]bool{}
	recipe := feedbackCollectionRecipe(c)
	for game, ref := range c.Games {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if ref.Recipe != recipe || !digest(ref.Raw) || !digest(ref.Dataset) || seen[ref.Raw] || seen[ref.Dataset] {
			return fail(fmt.Errorf("invalid or repeated collection game reference"))
		}
		seen[ref.Raw], seen[ref.Dataset] = true, true
		pair, _, err := LoadShard(filepath.Join(root, "games"), ref.Raw)
		if err != nil {
			return fail(err)
		}
		if len(pair) != 2 {
			return fail(fmt.Errorf("feedback collection needs both raw perspectives"))
		}
		job := feedbackCollectionJob(x, a, c.Config, game)
		scenario, _ := Digest(job.scenario)
		var versions [2]string
		for side, policy := range job.policies {
			versions[side], err = policy.Version()
			if err != nil {
				return fail(err)
			}
		}
		for side, tr := range pair {
			other := pair[1-side]
			if tr.Side != side || tr.Match != other.Match || tr.Scenario != scenario || tr.Group != job.group || tr.Rules != x.Environment.Rules || tr.Platform != x.Environment.Platform || tr.Environment != x.Environment.Scenario || tr.Mode != x.Mode || tr.Policy != versions[side] || tr.PolicyKind != job.policies[side].Kind() || tr.Opponent != versions[1-side] || tr.Terminated != other.Terminated || tr.Truncated != other.Truncated || tr.Winner != other.Winner || len(tr.Steps) != len(other.Steps) {
				return fail(fmt.Errorf("feedback raw game differs from frozen schedule at game %d side %d", game, side))
			}
			if side != game%2 {
				if err := verifyFeedbackBehavior(ctx, tr, job.policies[side], versions[side]); err != nil {
					return fail(err)
				}
			}
		}
		d, _, examples, err := LoadFeedbackDataset(ctx, filepath.Join(root, "feedback-data"), ref.Dataset)
		if err != nil {
			return fail(err)
		}
		if d.Experiment != c.Experiment || len(examples) != 1 || d.Examples[0].Behavior.Artifact != c.Model || examples[0].Targets.Teacher != c.Config.Teacher {
			return fail(fmt.Errorf("collection feedback is not its frozen learner/teacher"))
		}
		actual, _ := Digest(pair[game%2])
		if examples[0].Targets.Trajectory != actual {
			return fail(fmt.Errorf("feedback source differs from its paired raw game"))
		}
		// Dataset loading checks probabilities of recorded actions. Collection
		// additionally binds the actual RNG stream, so another legal sample
		// cannot masquerade as this seed's scheduled trajectory.
		var memory []float32
		for turn, step := range pair[game%2].Steps {
			decision, err := job.policies[game%2].decide(ctx, step.Frame, memory, job.random[game%2])
			if err != nil {
				return fail(err)
			}
			if !reflect.DeepEqual(decision.choices, step.Choices) {
				return fail(fmt.Errorf("feedback sampled action differs from frozen RNG at game %d turn %d", game, turn))
			}
			memory = decision.memory
		}
	}
	return c, x, a, nil
}

// FeedbackCollectionInputs describes the saved aggregate, in game order.
// Call LoadFeedbackCollection first to verify it, and require a full budget
// before starting a training round; partial collections are resumable only.
func FeedbackCollectionInputs(root string, c FeedbackCollectionCheckpoint) ([]FeedbackDatasetInput, error) {
	if c.Schema != "commander-feedback-collection-v1" || c.Config.Matches < 1 || len(c.Games) != c.Config.Matches {
		return nil, fmt.Errorf("feedback training requires a complete collection round")
	}
	var inputs []FeedbackDatasetInput
	for _, game := range c.Games {
		if !digest(game.Dataset) {
			return nil, fmt.Errorf("invalid feedback collection dataset")
		}
		inputs = append(inputs, FeedbackDatasetInput{filepath.Join(root, "feedback-data"), game.Dataset})
	}
	return inputs, nil
}

// CollectFeedback runs a fixed behavior artifact against rule opponents. It
// commits both actual perspectives after each complete match and labels only
// the learner's public frames. Teacher suggestions never drive the raw game.
func CollectFeedback(ctx context.Context, o FeedbackCollectionOptions) (resultErr error) {
	if o.Directory == "" || o.Workers < 0 || o.Workers > MaxCollectionWorkers || o.StopAtDataBytes < 0 || o.Resume && (o.Experiment != nil || o.Model != nil || o.Config != nil) || !o.Resume && (o.Experiment == nil || o.Model == nil || o.Config == nil) {
		return fmt.Errorf("feedback collection requires new data-dir and experiment/model/config; resume uses frozen settings")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var c FeedbackCollectionCheckpoint
	var x Experiment
	var a battlepolicy.Artifact
	if !o.Resume {
		if err := o.Experiment.Validate(); err != nil {
			return err
		}
		if err := o.Config.validate(*o.Experiment); err != nil {
			return err
		}
		if err := validateFeedbackArtifact(*o.Experiment, *o.Model); err != nil {
			return err
		}
		// Freeze owned copies before callbacks or collection can run.
		type frozenInputs struct {
			X Experiment
			A battlepolicy.Artifact
			C FeedbackCollectionConfig
		}
		b, err := json.Marshal(frozenInputs{*o.Experiment, *o.Model, *o.Config})
		if err != nil {
			return err
		}
		var frozen frozenInputs
		if err := json.Unmarshal(b, &frozen); err != nil {
			return err
		}
		x, a = frozen.X, frozen.A
		c = FeedbackCollectionCheckpoint{Schema: "commander-feedback-collection-v1", Config: frozen.C}
		c.Experiment, _ = Digest(x)
		c.Model, _ = Digest(a)
		if err := os.Mkdir(o.Directory, 0700); err != nil {
			return fmt.Errorf("feedback collection data-dir must be new (parent must exist): %w", err)
		}
	}
	lock, err := os.OpenFile(filepath.Join(o.Directory, ".training.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := lockTraining(lock); err != nil {
		return fmt.Errorf("feedback collection directory in use: %w", err)
	}
	if o.Resume {
		c, x, a, err = LoadFeedbackCollection(ctx, o.Directory)
		if err != nil {
			return err
		}
	} else {
		if err := os.Mkdir(filepath.Join(o.Directory, "feedback-collection-checkpoints"), 0700); err != nil {
			return err
		}
		if _, err := SaveExperiment(filepath.Join(o.Directory, "experiments", c.Experiment+".json"), x); err != nil {
			return err
		}
		if err := writeFeedbackObject(filepath.Join(o.Directory, "behaviors", c.Model+".json"), a, 128<<20); err != nil {
			return err
		}
		if err := saveFeedbackCollection(o.Directory, c); err != nil {
			return err
		}
	}
	emit := func(event string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		id, _ := Digest(c)
		p := FeedbackCollectionProgress{Event: event, Games: len(c.Games), Matches: c.Config.Matches, Checkpoint: id, StopAtBytes: o.StopAtDataBytes}
		if o.StopAtDataBytes > 0 {
			p.DataBytes, err = trainingDataBytes(ctx, o.Directory)
			if err != nil {
				return err
			}
			if p.DataBytes >= o.StopAtDataBytes {
				p.Event, p.StorageTriggerStage = "storage_limit_reached", event
				if o.Progress != nil {
					if err := o.Progress(p); err != nil {
						return err
					}
				}
				return &StorageLimitError{p.DataBytes, o.StopAtDataBytes}
			}
		}
		if o.Progress != nil {
			return o.Progress(p)
		}
		return nil
	}
	if err := emit("feedback_collection_ready"); err != nil {
		return err
	}
	if len(c.Games) == c.Config.Matches {
		return emit("feedback_collection_saved")
	}
	if o.Stderr == nil {
		log, err := os.OpenFile(filepath.Join(o.Directory, "worker.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return err
		}
		defer log.Close()
		o.Stderr = log
	}
	stderr := &collectionLog{writer: o.Stderr}
	engine, err := battleenv.Start(ctx, o.Command, stderr)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, engine.Close()) }()
	if engine.Metadata() != x.Environment {
		return fmt.Errorf("feedback collection worker differs from frozen environment")
	}
	workers, err := startCollectionWorkers(ctx, engine, o.Command, max(1, o.Workers), stderr)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, workers.closeAdditional()) }()
	for len(c.Games) < c.Config.Matches {
		jobs := make([]collectionJob, min(len(workers.engines), c.Config.Matches-len(c.Games)))
		for i := range jobs {
			jobs[i] = feedbackCollectionJob(x, a, c.Config, len(c.Games)+i)
		}
		results, err := workers.collect(ctx, jobs)
		if err != nil {
			return err
		}
		for i, pair := range results {
			if err := ctx.Err(); err != nil {
				return err
			}
			game := len(c.Games)
			raw, err := SaveShard(filepath.Join(o.Directory, "games"), pair[:])
			if err != nil {
				return err
			}
			source := pair[game%2]
			labels, err := LabelRuleFeedback(ctx, source, c.Config.Teacher)
			if err != nil {
				return err
			}
			dataset, err := SaveFeedbackDataset(ctx, filepath.Join(o.Directory, "feedback-data"), x, []FeedbackExample{{Source: source, Targets: labels, Behavior: jobs[i].policies[game%2]}}, map[string]battlepolicy.Artifact{source.Policy: a})
			if err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			c.Games = append(c.Games, FeedbackCollectedGame{feedbackCollectionRecipe(c), raw.Digest, dataset})
			if err := saveFeedbackCollection(o.Directory, c); err != nil {
				return err
			}
			if err := emit("feedback_game_committed"); err != nil {
				return err
			}
		}
	}
	if err := errors.Join(workers.closeAdditional(), engine.Close()); err != nil {
		return err
	}
	return emit("feedback_collection_saved")
}
