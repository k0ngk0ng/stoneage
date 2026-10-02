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

const DemonstrationPointer = "demonstration-latest.json"

type DemonstrationRunConfig struct {
	Seed    int64            `json:"seed"`
	Network battlenet.Config `json:"network"`
	Update  ImitationConfig  `json:"update"`
}

type DemonstrationEpoch struct {
	Schema         string          `json:"schema"`
	Dataset        string          `json:"dataset"`
	Epoch          int             `json:"epoch"`
	BeforeLearning string          `json:"before_learning"`
	AfterLearning  string          `json:"after_learning"`
	Report         ImitationReport `json:"report"`
}

type DemonstrationCheckpoint struct {
	Schema   string                 `json:"schema"`
	Config   DemonstrationRunConfig `json:"config"`
	Dataset  DemonstrationManifest  `json:"dataset"`
	Rules    string                 `json:"rules"`
	Platform string                 `json:"platform"`
	Features string                 `json:"features"`
	Mode     int                    `json:"mode"`
	Initial  string                 `json:"initial_learning"`
	Learning string                 `json:"learning"`
	Reports  []string               `json:"epoch_reports"`
}

type DemonstrationProgress struct {
	Event    string           `json:"event"`
	Epochs   int              `json:"completed_epochs"`
	Dataset  string           `json:"dataset"`
	Learning string           `json:"learning_state"`
	Report   *ImitationReport `json:"report,omitempty"`
}

type DemonstrationRunOptions struct {
	Directory, Dataset string
	Resume             bool
	Config             *DemonstrationRunConfig
	Epochs             int // Additional complete epochs; settings remain frozen on resume.
	Progress           func(DemonstrationProgress) error
}

func demonstrationCompatibility(ds []Demonstration, config DemonstrationRunConfig) error {
	if err := config.Update.validate(); err != nil {
		return err
	}
	if len(ds) == 0 {
		return fmt.Errorf("demonstration dataset is empty")
	}
	first := ds[0]
	if battlepolicy.NetworkFeatures(config.Network) != first.Features || config.Network.EntityFeatures != battlepolicy.EntityFeatures || config.Network.CandidateFeatures != battlepolicy.CandidateFeatures || config.Network.EventFeatures != battlepolicy.EventFeatures {
		return fmt.Errorf("demonstration network feature contract differs")
	}
	for _, d := range ds {
		if d.Rules != first.Rules || d.Platform != first.Platform || d.Mode != first.Mode || d.Features != first.Features {
			return fmt.Errorf("demonstration training requires one rules/platform/mode/features combination")
		}
	}
	return nil
}

func saveDemonstrationCheckpoint(root string, c DemonstrationCheckpoint) error {
	id, err := Digest(c)
	if err != nil {
		return err
	}
	if err := writeObject(filepath.Join(root, "demonstration-checkpoints", id+".json"), c); err != nil {
		return err
	}
	f, err := os.CreateTemp(root, ".demonstration-latest-*")
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
	if err := os.Rename(f.Name(), filepath.Join(root, DemonstrationPointer)); err != nil {
		return err
	}
	dir, err := os.Open(root)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// The distinct schema/pointer prevents these records being mistaken for native
// PPO checkpoints. Hashes bind local evidence, not a trusted source signature.
func LoadDemonstrationCheckpoint(ctx context.Context, root string) (DemonstrationCheckpoint, LearningState, []Demonstration, error) {
	return LoadDemonstrationCheckpointID(ctx, root, "")
}

func LoadDemonstrationCheckpointID(ctx context.Context, root, checkpoint string) (DemonstrationCheckpoint, LearningState, []Demonstration, error) {
	var c DemonstrationCheckpoint
	var state LearningState
	var pointer struct {
		Checkpoint string `json:"checkpoint"`
	}
	pointer.Checkpoint = checkpoint
	fail := func(err error) (DemonstrationCheckpoint, LearningState, []Demonstration, error) {
		return c, state, nil, err
	}
	if checkpoint == "" {
		if err := readObject(filepath.Join(root, DemonstrationPointer), &pointer, 1024); err != nil {
			return fail(err)
		}
	}
	if !digest(pointer.Checkpoint) {
		return fail(fmt.Errorf("invalid demonstration checkpoint pointer"))
	}
	if err := readObject(filepath.Join(root, "demonstration-checkpoints", pointer.Checkpoint+".json"), &c, 4<<20); err != nil {
		return fail(err)
	}
	id, err := Digest(c)
	if err != nil || id != pointer.Checkpoint || c.Schema != "commander-demonstration-training-v1" || !digest(c.Dataset.Digest) || !digest(c.Initial) || !digest(c.Learning) || len(c.Reports) > 10000 {
		return fail(fmt.Errorf("invalid demonstration checkpoint identity/schema"))
	}
	ds, manifest, err := LoadDemonstrations(ctx, filepath.Join(root, "demonstrations", c.Dataset.Digest+".jsonl"))
	if err != nil {
		return fail(err)
	}
	if !reflect.DeepEqual(manifest, c.Dataset) || c.Rules != ds[0].Rules || c.Platform != ds[0].Platform || c.Mode != ds[0].Mode || c.Features != ds[0].Features {
		return fail(fmt.Errorf("demonstration checkpoint differs from frozen dataset"))
	}
	if err := demonstrationCompatibility(ds, c.Config); err != nil {
		return fail(err)
	}
	initial, err := battlenet.NewModel[float32](c.Config.Network, c.Config.Seed)
	if err != nil {
		return fail(err)
	}
	initialID, _ := Digest(LearningState{Schema: 1, Model: initial, Optimizer: &battlenet.Adam[float32]{}})
	if initialID != c.Initial {
		return fail(fmt.Errorf("demonstration initialization differs from saved seed/config"))
	}
	previous, previousPolicy := c.Initial, ""
	previousPolicy, _ = ModelDigest(initial)
	batch := c.Config.Update.BatchEpisodes
	if batch == 0 {
		batch = len(ds)
	}
	updates := (len(ds) + batch - 1) / batch
	teacherSet := map[string]bool{}
	var counts map[string]int
	if c.Config.Update.ActionWeighting != "" {
		counts = map[string]int{}
	}
	for _, d := range ds {
		teacherSet[d.Strategy+":"+d.Policy] = true
		if counts != nil {
			for _, step := range d.Steps {
				if err := addImitationCounts(counts, step.Frame, step.Choices); err != nil {
					return fail(err)
				}
			}
		}
	}
	var teachers []string
	for teacher := range teacherSet {
		teachers = append(teachers, teacher)
	}
	sort.Strings(teachers)
	for i, ref := range c.Reports {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if !digest(ref) {
			return fail(fmt.Errorf("invalid demonstration epoch reference"))
		}
		var epoch DemonstrationEpoch
		if err := readObject(filepath.Join(root, "demonstration-reports", ref+".json"), &epoch, 1<<20); err != nil {
			return fail(err)
		}
		actual, _ := Digest(epoch)
		r := epoch.Report
		if err := validateImitationWeights(c.Config.Update, r, counts); err != nil {
			return fail(err)
		}
		if actual != ref || epoch.Schema != "commander-demonstration-epoch-v1" || epoch.Dataset != manifest.Digest || epoch.Epoch != i+1 || epoch.BeforeLearning != previous || !digest(epoch.AfterLearning) || r.BeforePolicy != previousPolicy || !digest(r.AfterPolicy) || r.OptimizerUpdates != updates || r.Episodes != manifest.Episodes || r.TeamTurns != manifest.Turns || r.Actions != manifest.Actions || !reflect.DeepEqual(r.Teachers, teachers) || !finite(r.CrossEntropy) || r.CrossEntropy < 0 || !finite(r.GradientNorm) || r.GradientNorm < 0 {
			return fail(fmt.Errorf("demonstration epoch evidence differs"))
		}
		previous, previousPolicy = epoch.AfterLearning, r.AfterPolicy
	}
	if previous != c.Learning {
		return fail(fmt.Errorf("demonstration learning chain differs"))
	}
	state, err = loadLearning(filepath.Join(root, "learning"), c.Learning)
	if err != nil {
		return fail(err)
	}
	policy, _ := ModelDigest(state.Model)
	if state.Model.Config != c.Config.Network || policy != previousPolicy || state.Optimizer.Step != len(c.Reports)*updates {
		return fail(fmt.Errorf("demonstration model/optimizer progress differs"))
	}
	return c, state, ds, nil
}

func RunDemonstrations(ctx context.Context, o DemonstrationRunOptions) error {
	if o.Directory == "" || o.Epochs < 1 || o.Epochs > 10000 || o.Resume && (o.Config != nil || o.Dataset != "") || !o.Resume && (o.Config == nil || o.Dataset == "") {
		return fmt.Errorf("demonstration training requires new data-dir, dataset/config and 1..10000 epochs; resume uses only saved settings")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var c DemonstrationCheckpoint
	var state LearningState
	var ds []Demonstration
	var err error
	if !o.Resume {
		ds, _, err = LoadDemonstrations(ctx, o.Dataset)
		if err != nil {
			return err
		}
		if err := demonstrationCompatibility(ds, *o.Config); err != nil {
			return err
		}
		state.Model, err = battlenet.NewModel[float32](o.Config.Network, o.Config.Seed)
		if err != nil {
			return err
		}
		state.Schema, state.Optimizer = 1, &battlenet.Adam[float32]{}
		if err := os.Mkdir(o.Directory, 0700); err != nil {
			return fmt.Errorf("demonstration data-dir must be new (parent must exist): %w", err)
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
		c, state, ds, err = LoadDemonstrationCheckpoint(ctx, o.Directory)
		if err != nil {
			return err
		}
	} else {
		for _, name := range []string{"demonstrations", "demonstration-checkpoints", "demonstration-reports", "learning"} {
			if err := os.Mkdir(filepath.Join(o.Directory, name), 0700); err != nil {
				return err
			}
		}
		// SaveDemonstrations canonicalizes the frozen evidence; never read the
		// external dataset again while training or resuming this run.
		manifest, err := SaveDemonstrations(ctx, filepath.Join(o.Directory, "demonstrations", "source.jsonl"), ds)
		if err != nil {
			return err
		}
		for _, suffix := range []string{"", ".manifest.json"} {
			if err := os.Rename(filepath.Join(o.Directory, "demonstrations", "source.jsonl"+suffix), filepath.Join(o.Directory, "demonstrations", manifest.Digest+".jsonl"+suffix)); err != nil {
				return err
			}
		}
		datasetDir, err := os.Open(filepath.Join(o.Directory, "demonstrations"))
		if err != nil {
			return err
		}
		err = datasetDir.Sync()
		datasetDir.Close()
		if err != nil {
			return err
		}
		stateID, err := saveLearning(filepath.Join(o.Directory, "learning"), state)
		if err != nil {
			return err
		}
		c = DemonstrationCheckpoint{Schema: "commander-demonstration-training-v1", Config: *o.Config, Dataset: manifest, Rules: ds[0].Rules, Platform: ds[0].Platform, Features: ds[0].Features, Mode: ds[0].Mode, Initial: stateID, Learning: stateID}
		if err := saveDemonstrationCheckpoint(o.Directory, c); err != nil {
			return err
		}
	}
	if len(c.Reports)+o.Epochs > 10000 {
		return fmt.Errorf("demonstration run exceeds 10000 epochs")
	}
	emit := func(event string, report *ImitationReport) error {
		if o.Progress == nil {
			return nil
		}
		return o.Progress(DemonstrationProgress{event, len(c.Reports), c.Dataset.Digest, c.Learning, report})
	}
	if err := emit("demonstration_training_ready", nil); err != nil {
		return err
	}
	for i := 0; i < o.Epochs; i++ {
		report, err := ImitateDemonstrations(ctx, state.Model, state.Optimizer, ds, c.Config.Update)
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
		epoch := DemonstrationEpoch{"commander-demonstration-epoch-v1", c.Dataset.Digest, len(c.Reports) + 1, c.Learning, learning, report}
		ref, err := Digest(epoch)
		if err != nil {
			return err
		}
		if err := writeObject(filepath.Join(o.Directory, "demonstration-reports", ref+".json"), epoch); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		c.Learning, c.Reports = learning, append(c.Reports, ref)
		if err := saveDemonstrationCheckpoint(o.Directory, c); err != nil {
			return err
		}
		if err := emit("demonstration_epoch_committed", &report); err != nil {
			return err
		}
	}
	return emit("demonstration_training_saved", nil)
}
