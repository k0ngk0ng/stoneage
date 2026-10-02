package battletrain

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"

	"github.com/k0ngk0ng/stoneage/internal/battlenet"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

const FeedbackPointer = "feedback-latest.json"

type FeedbackDatasetInput struct {
	Directory string
	ID        string
}

type FeedbackCheckpoint struct {
	Schema     string          `json:"schema"`
	Experiment string          `json:"experiment"`
	Datasets   []string        `json:"datasets"`
	Initial    string          `json:"initial_artifact"`
	Start      string          `json:"initial_learning"`
	Learning   string          `json:"learning"`
	Update     ImitationConfig `json:"update"`
	Reports    []string        `json:"epoch_reports"`
}

type FeedbackEpoch struct {
	Schema         string         `json:"schema"`
	Recipe         string         `json:"recipe_digest"`
	Epoch          int            `json:"epoch"`
	BeforeLearning string         `json:"before_learning"`
	AfterLearning  string         `json:"after_learning"`
	Report         FeedbackReport `json:"report"`
}

func feedbackRecipe(c FeedbackCheckpoint) string {
	// Progress fields are excluded; every epoch binds the same immutable
	// initialization, ordered aggregate and optimizer settings.
	c.Learning, c.Reports = "", nil
	id, _ := Digest(c)
	return id
}

type FeedbackProgress struct {
	StorageTriggerStage string          `json:"storage_trigger_stage,omitempty"`
	Event               string          `json:"event"`
	Epochs              int             `json:"completed_epochs"`
	Learning            string          `json:"learning_state"`
	Report              *FeedbackReport `json:"report,omitempty"`
	DataBytes           int64           `json:"data_bytes,omitempty"`
	StopAtBytes         int64           `json:"stop_at_data_bytes,omitempty"`
}

type FeedbackRunOptions struct {
	Directory       string
	Datasets        []FeedbackDatasetInput
	Initial         *battlepolicy.Artifact
	Update          *ImitationConfig
	Resume          bool
	Epochs          int   // Additional complete epochs over the frozen aggregate.
	StopAtDataBytes int64 // Invocation-only threshold, checked after checkpoints.
	Progress        func(FeedbackProgress) error
}

type loadedFeedback struct {
	dataset   FeedbackDataset
	x         Experiment
	examples  []FeedbackExample
	artifacts map[string]battlepolicy.Artifact
}

func loadFeedbackInputs(ctx context.Context, inputs []FeedbackDatasetInput) ([]loadedFeedback, []FeedbackExample, error) {
	if len(inputs) < 1 || len(inputs) > 256 {
		return nil, nil, fmt.Errorf("feedback training requires 1..256 datasets")
	}
	var loaded []loadedFeedback
	var examples []FeedbackExample
	seen := map[string]bool{}
	for _, input := range inputs {
		if seen[input.ID] {
			return nil, nil, fmt.Errorf("duplicate feedback dataset")
		}
		seen[input.ID] = true
		d, x, es, err := LoadFeedbackDataset(ctx, input.Directory, input.ID)
		if err != nil {
			return nil, nil, err
		}
		if len(loaded) > 0 && d.Experiment != loaded[0].dataset.Experiment || len(examples)+len(es) > 10000 {
			return nil, nil, fmt.Errorf("feedback aggregate must share one experiment and have at most 10000 trajectories")
		}
		artifacts := map[string]battlepolicy.Artifact{}
		for i, entry := range d.Examples {
			if entry.Behavior.Artifact == "" {
				continue
			}
			policy := es[i].Source.Policy
			if _, ok := artifacts[policy]; ok {
				continue
			}
			var a battlepolicy.Artifact
			if err := readObject(filepath.Join(input.Directory, "behaviors", entry.Behavior.Artifact+".json"), &a, 128<<20); err != nil {
				return nil, nil, err
			}
			id, _ := Digest(a)
			if id != entry.Behavior.Artifact {
				return nil, nil, fmt.Errorf("feedback behavior changed while copying input")
			}
			artifacts[policy] = a
		}
		loaded = append(loaded, loadedFeedback{d, x, es, artifacts})
		examples = append(examples, es...)
	}
	return loaded, examples, nil
}

// This summary is rebuilt from verified examples, never trusted from an epoch
// report. The aggregate may contain data from several collection rounds but
// must not silently count the same perspective twice with different labels.
func summarizeFeedback(examples []FeedbackExample, features string) (FeedbackReport, error) {
	var r FeedbackReport
	seen, teachers, behaviors := map[string]bool{}, map[string]bool{}, map[string]bool{}
	r.Imitation.Episodes = len(examples)
	for _, e := range examples {
		key := fmt.Sprintf("%s:%d", e.Source.Match, e.Source.Side)
		if seen[key] || e.Source.Steps[0].Frame.Schema != features {
			return r, fmt.Errorf("duplicate or incompatible source across feedback datasets")
		}
		seen[key], teachers[e.Targets.Teacher], behaviors[e.Source.Policy] = true, true, true
		r.Sources = append(r.Sources, e.Targets.Trajectory)
		id, _ := Digest(e.Targets)
		r.Targets = append(r.Targets, id)
		r.Imitation.TeamTurns += len(e.Source.Steps)
		for turn, step := range e.Source.Steps {
			r.Imitation.Actions += len(step.Choices)
			for actor, choice := range step.Choices {
				if choice != e.Targets.Choices[turn][actor] {
					r.Disagreements++
				}
			}
		}
	}
	for teacher := range teachers {
		r.Imitation.Teachers = append(r.Imitation.Teachers, teacher)
	}
	for behavior := range behaviors {
		r.BehaviorPolicies = append(r.BehaviorPolicies, behavior)
	}
	sort.Strings(r.Imitation.Teachers)
	sort.Strings(r.BehaviorPolicies)
	return r, nil
}

func saveFeedbackCheckpoint(root string, c FeedbackCheckpoint) error {
	return saveFeedbackCheckpointObject(root, "feedback-checkpoints", FeedbackPointer, c)
}

func saveFeedbackCheckpointObject(root, directory, pointer string, c any) error {
	id, err := Digest(c)
	if err != nil {
		return err
	}
	if err := writeObject(filepath.Join(root, directory, id+".json"), c); err != nil {
		return err
	}
	f, err := os.CreateTemp(root, ".feedback-latest-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := json.NewEncoder(f).Encode(struct {
		Checkpoint string `json:"checkpoint"`
	}{id}); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), filepath.Join(root, pointer)); err != nil {
		return err
	}
	dir, err := os.Open(root)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// LoadFeedbackCheckpoint checks the immutable dataset/behavior references and
// epoch chain. It does not numerically rerun all past gradient updates.
func LoadFeedbackCheckpoint(ctx context.Context, root string) (FeedbackCheckpoint, LearningState, []FeedbackExample, error) {
	return LoadFeedbackCheckpointID(ctx, root, "")
}

func LoadFeedbackCheckpointID(ctx context.Context, root, checkpoint string) (FeedbackCheckpoint, LearningState, []FeedbackExample, error) {
	var c FeedbackCheckpoint
	var state LearningState
	fail := func(err error) (FeedbackCheckpoint, LearningState, []FeedbackExample, error) {
		return c, LearningState{}, nil, err
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if checkpoint == "" {
		var p struct {
			Checkpoint string `json:"checkpoint"`
		}
		if err := readObject(filepath.Join(root, FeedbackPointer), &p, 1024); err != nil {
			return fail(err)
		}
		checkpoint = p.Checkpoint
	}
	if !digest(checkpoint) {
		return fail(fmt.Errorf("invalid feedback checkpoint reference"))
	}
	if err := readObject(filepath.Join(root, "feedback-checkpoints", checkpoint+".json"), &c, 4<<20); err != nil {
		return fail(err)
	}
	id, _ := Digest(c)
	if id != checkpoint || c.Schema != "commander-feedback-training-v1" || !digest(c.Initial) || !digest(c.Start) || !digest(c.Learning) || !digest(c.Experiment) || len(c.Reports) > 10000 {
		return fail(fmt.Errorf("invalid feedback checkpoint schema or identity"))
	}
	if err := c.Update.validate(); err != nil {
		return fail(err)
	}
	var inputs []FeedbackDatasetInput
	for _, id := range c.Datasets {
		inputs = append(inputs, FeedbackDatasetInput{filepath.Join(root, "feedback-data"), id})
	}
	loaded, examples, err := loadFeedbackInputs(ctx, inputs)
	if err != nil {
		return fail(err)
	}
	if loaded[0].dataset.Experiment != c.Experiment {
		return fail(fmt.Errorf("feedback checkpoint experiment differs from datasets"))
	}
	var initial battlepolicy.Artifact
	if err := readObject(filepath.Join(root, "initial-models", c.Initial+".json"), &initial, 128<<20); err != nil {
		return fail(err)
	}
	initialID, _ := Digest(initial)
	if initialID != c.Initial {
		return fail(fmt.Errorf("feedback initial artifact checksum mismatch"))
	}
	if err := validateFeedbackArtifact(loaded[0].x, initial); err != nil {
		return fail(err)
	}
	start := LearningState{1, initial.Network, &battlenet.Adam[float32]{}}
	startID, _ := Digest(start)
	if startID != c.Start {
		return fail(fmt.Errorf("feedback initialization differs from declared artifact and fresh Adam"))
	}
	if _, err := loadLearning(filepath.Join(root, "learning"), c.Start); err != nil {
		return fail(err)
	}
	expected, err := summarizeFeedback(examples, initial.Features)
	if err != nil {
		return fail(err)
	}
	var counts map[string]int
	if c.Update.ActionWeighting != "" {
		counts = map[string]int{}
		for _, example := range examples {
			for i, step := range example.Source.Steps {
				if err := addImitationCounts(counts, step.Frame, example.Targets.Choices[i]); err != nil {
					return fail(err)
				}
			}
		}
	}
	updates := 1
	if c.Update.BatchEpisodes > 0 {
		updates = (len(examples) + c.Update.BatchEpisodes - 1) / c.Update.BatchEpisodes
		expected.Imitation.OptimizerUpdates = updates
	}
	recipe := feedbackRecipe(c)
	previous, policy := c.Start, initial.WeightsDigest
	for i, ref := range c.Reports {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if !digest(ref) {
			return fail(fmt.Errorf("invalid feedback epoch reference"))
		}
		var epoch FeedbackEpoch
		if err := readObject(filepath.Join(root, "feedback-reports", ref+".json"), &epoch, 16<<20); err != nil {
			return fail(err)
		}
		actual, _ := Digest(epoch)
		r := epoch.Report.Imitation
		if err := validateImitationWeights(c.Update, r, counts); err != nil {
			return fail(err)
		}
		if actual != ref || epoch.Schema != "commander-feedback-epoch-v1" || epoch.Recipe != recipe || epoch.Epoch != i+1 || epoch.BeforeLearning != previous || !digest(epoch.AfterLearning) || r.BeforePolicy != policy || !digest(r.AfterPolicy) || !finite(r.CrossEntropy) || r.CrossEntropy < 0 || !finite(r.GradientNorm) || r.GradientNorm < 0 {
			return fail(fmt.Errorf("feedback epoch chain differs"))
		}
		want := expected
		want.Imitation.BeforePolicy, want.Imitation.AfterPolicy = policy, r.AfterPolicy
		want.Imitation.CrossEntropy, want.Imitation.GradientNorm = r.CrossEntropy, r.GradientNorm
		want.Imitation.ActionWeighting, want.Imitation.WeightedCrossEntropy = r.ActionWeighting, r.WeightedCrossEntropy
		if !reflect.DeepEqual(want, epoch.Report) {
			return fail(fmt.Errorf("feedback epoch summary differs from frozen sources and targets"))
		}
		// Every epoch retains its own recoverable weights/moments, not just a
		// report claiming that a state once existed.
		state, err = loadLearning(filepath.Join(root, "learning"), epoch.AfterLearning)
		if err != nil {
			return fail(err)
		}
		weights, _ := ModelDigest(state.Model)
		if state.Model.Config != initial.Network.Config || weights != r.AfterPolicy || state.Optimizer.Step != (i+1)*updates {
			return fail(fmt.Errorf("feedback epoch weights/Adam differ from progress"))
		}
		previous, policy = epoch.AfterLearning, r.AfterPolicy
	}
	if c.Learning != previous {
		return fail(fmt.Errorf("feedback final state differs from epoch chain"))
	}
	if len(c.Reports) == 0 {
		state = start
	}
	return c, state, examples, nil
}

// RunFeedback trains a frozen aggregate. A new aggregate requires a new run;
// resume never silently reads newly added external data or changes settings.
// Full collection/label/training rounds are composed above this runner.
func RunFeedback(ctx context.Context, o FeedbackRunOptions) error {
	if o.Directory == "" || o.Epochs < 1 || o.Epochs > 10000 || o.StopAtDataBytes < 0 || o.Resume && (o.Initial != nil || o.Update != nil || len(o.Datasets) != 0) || !o.Resume && (o.Initial == nil || o.Update == nil || len(o.Datasets) == 0) {
		return fmt.Errorf("feedback training needs new data-dir, frozen datasets/model/update and 1..10000 epochs; resume uses saved inputs")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var c FeedbackCheckpoint
	var state LearningState
	var loaded []loadedFeedback
	var examples []FeedbackExample
	var initial battlepolicy.Artifact
	var err error
	if !o.Resume {
		if err := o.Update.validate(); err != nil {
			return err
		}
		loaded, examples, err = loadFeedbackInputs(ctx, o.Datasets)
		if err != nil {
			return err
		}
		if err := validateFeedbackArtifact(loaded[0].x, *o.Initial); err != nil {
			return err
		}
		if _, err := summarizeFeedback(examples, o.Initial.Features); err != nil {
			return err
		}
		b, err := json.Marshal(o.Initial)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(b, &initial); err != nil {
			return err
		}
		state = LearningState{1, initial.Network, &battlenet.Adam[float32]{}}
		c = FeedbackCheckpoint{Schema: "commander-feedback-training-v1", Experiment: loaded[0].dataset.Experiment, Update: *o.Update}
		c.Initial, _ = Digest(initial)
		if err := os.Mkdir(o.Directory, 0700); err != nil {
			return fmt.Errorf("feedback data-dir must be new (parent must exist): %w", err)
		}
	}
	lock, err := os.OpenFile(filepath.Join(o.Directory, ".training.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := lockTraining(lock); err != nil {
		return fmt.Errorf("training directory in use: %w", err)
	}
	if o.Resume {
		c, state, examples, err = LoadFeedbackCheckpoint(ctx, o.Directory)
		if err != nil {
			return err
		}
	} else {
		for _, name := range []string{"feedback-checkpoints", "feedback-reports", "initial-models", "learning"} {
			if err := os.Mkdir(filepath.Join(o.Directory, name), 0700); err != nil {
				return err
			}
		}
		for i, data := range loaded {
			id, err := SaveFeedbackDataset(ctx, filepath.Join(o.Directory, "feedback-data"), data.x, data.examples, data.artifacts)
			if err != nil {
				return err
			}
			if id != o.Datasets[i].ID {
				return fmt.Errorf("feedback copy changed dataset identity")
			}
			c.Datasets = append(c.Datasets, id)
		}
		if err := writeFeedbackObject(filepath.Join(o.Directory, "initial-models", c.Initial+".json"), initial, 128<<20); err != nil {
			return err
		}
		c.Start, err = saveLearning(filepath.Join(o.Directory, "learning"), state)
		if err != nil {
			return err
		}
		c.Learning = c.Start
		if err := saveFeedbackCheckpoint(o.Directory, c); err != nil {
			return err
		}
	}
	if len(c.Reports)+o.Epochs > 10000 {
		return fmt.Errorf("feedback run exceeds 10000 epochs")
	}
	emit := func(event string, report *FeedbackReport) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		p := FeedbackProgress{Event: event, Epochs: len(c.Reports), Learning: c.Learning, Report: report, StopAtBytes: o.StopAtDataBytes}
		if o.StopAtDataBytes > 0 {
			p.DataBytes, err = trainingDataBytes(ctx, o.Directory)
			if err != nil {
				return err
			}
			if p.DataBytes >= o.StopAtDataBytes {
				p.StorageTriggerStage = event
				p.Event = "storage_limit_reached"
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
	if err := emit("feedback_training_ready", nil); err != nil {
		return err
	}
	recipe := feedbackRecipe(c)
	for i := 0; i < o.Epochs; i++ {
		report, err := ImitateFeedback(ctx, state.Model, state.Optimizer, examples, c.Update)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		learning, err := saveLearning(filepath.Join(o.Directory, "learning"), state)
		if err != nil {
			return err
		}
		epoch := FeedbackEpoch{"commander-feedback-epoch-v1", recipe, len(c.Reports) + 1, c.Learning, learning, report}
		ref, _ := Digest(epoch)
		if err := writeFeedbackObject(filepath.Join(o.Directory, "feedback-reports", ref+".json"), epoch, 16<<20); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		c.Learning, c.Reports = learning, append(c.Reports, ref)
		if err := saveFeedbackCheckpoint(o.Directory, c); err != nil {
			return err
		}
		if err := emit("feedback_epoch_committed", &report); err != nil {
			return err
		}
	}
	return emit("feedback_training_saved", nil)
}
