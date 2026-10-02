package battletrain

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

const FeedbackDatasetSchema = "commander-feedback-dataset-v1"

// FeedbackDataset binds separately stored facts, suggestions and frozen
// behavior artifacts to one experiment's training partition. It is not an
// on-policy batch, and suggestions never replace actions in the source shard.
type FeedbackDataset struct {
	Schema     string          `json:"schema"`
	Experiment string          `json:"experiment"`
	Source     string          `json:"source_shard"`
	Examples   []FeedbackEntry `json:"examples"`
}

type FeedbackEntry struct {
	Trajectory string           `json:"trajectory_digest"`
	Targets    string           `json:"feedback_digest"`
	Behavior   FeedbackBehavior `json:"behavior"`
}

type FeedbackBehavior struct {
	Artifact string `json:"artifact,omitempty"`
	Rule     string `json:"rule,omitempty"`
	Greedy   bool   `json:"greedy,omitempty"`
	Features string `json:"features,omitempty"`
}

// validateFeedbackArtifact permits the exact declared parent or a candidate
// from this experiment. It checks declared training/selection ancestry; this
// local integrity check is not proof of an artifact's external origin.
func validateFeedbackArtifact(x Experiment, a battlepolicy.Artifact) error {
	if err := a.Validate(); err != nil {
		return err
	}
	if !a.CompatibleNativeEnvironment(x.Environment) || !supports(a, x.Mode) {
		return fmt.Errorf("feedback behavior artifact has incompatible environment or mode")
	}
	return x.validateEvaluationCandidate(a, "validation")
}

func describeFeedback(ctx context.Context, x Experiment, examples []FeedbackExample, artifacts map[string]battlepolicy.Artifact) (FeedbackDataset, error) {
	var d FeedbackDataset
	if err := ctx.Err(); err != nil {
		return d, err
	}
	if err := x.Validate(); err != nil {
		return d, err
	}
	if len(examples) == 0 || len(examples) > 10000 {
		return d, fmt.Errorf("feedback dataset requires 1..10000 complete trajectories")
	}
	d.Schema = FeedbackDatasetSchema
	d.Experiment, _ = Digest(x)
	training := map[string]bool{}
	for _, f := range x.groups("train") {
		training[f.Group] = true
	}
	seen, usedArtifacts := map[string]bool{}, map[string]bool{}
	refs := map[Policy]FeedbackBehavior{}
	versions := map[Policy]string{}
	var features string
	for _, example := range examples {
		source := example.Source
		if err := example.Targets.Validate(ctx, source); err != nil {
			return d, err
		}
		if !training[source.Group] || source.Rules != x.Environment.Rules || source.Platform != x.Environment.Platform || source.Environment != x.Environment.Scenario || source.Mode != x.Mode || source.Setup.MaxTurns != x.MaxTurns {
			return d, fmt.Errorf("feedback source is outside the experiment training partition")
		}
		key := fmt.Sprintf("%s:%d", source.Match, source.Side)
		if features == "" {
			features = source.Steps[0].Frame.Schema
		}
		if seen[key] || source.Steps[0].Frame.Schema != features {
			return d, fmt.Errorf("duplicate or mixed-feature feedback source")
		}
		seen[key] = true
		p := example.Behavior
		ref, cached := refs[p]
		if !cached {
			id, err := p.Version()
			if err != nil {
				return d, err
			}
			ref = FeedbackBehavior{Rule: p.Rule, Greedy: p.Greedy, Features: p.Features}
			if p.Model != nil {
				a, ok := artifacts[id]
				if !ok {
					return d, fmt.Errorf("feedback requires the frozen behavior artifact for %s", id)
				}
				if err := validateFeedbackArtifact(x, a); err != nil {
					return d, err
				}
				weights, err := ModelDigest(p.Model)
				if err != nil || weights != a.WeightsDigest {
					return d, fmt.Errorf("feedback behavior weights differ from artifact")
				}
				ref.Artifact, _ = Digest(a)
				usedArtifacts[id] = true
			}
			refs[p] = ref
			versions[p] = id
		}
		if p.features() != source.Steps[0].Frame.Schema {
			return d, fmt.Errorf("feedback behavior feature contract differs from source")
		}
		if err := verifyFeedbackBehavior(ctx, source, p, versions[p]); err != nil {
			return d, err
		}
		target, _ := Digest(example.Targets)
		d.Examples = append(d.Examples, FeedbackEntry{example.Targets.Trajectory, target, ref})
	}
	if len(usedArtifacts) != len(artifacts) {
		return d, fmt.Errorf("feedback dataset contains unused behavior artifacts")
	}
	return d, nil
}

func writeFeedbackObject(path string, value any, limit int) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(b)+1 > limit {
		return fmt.Errorf("feedback object exceeds load limit")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return writeObject(path, value)
}

// SaveFeedbackDataset validates all inputs before writing. Its immutable
// manifest is published last, so an interruption cannot expose a complete
// dataset with missing dependencies. Retrying identical input reuses objects.
// artifacts is keyed by behavior policy identity, including sampling mode.
func SaveFeedbackDataset(ctx context.Context, root string, x Experiment, examples []FeedbackExample, artifacts map[string]battlepolicy.Artifact) (string, error) {
	d, err := describeFeedback(ctx, x, examples, artifacts)
	if err != nil {
		return "", err
	}
	if _, err := SaveExperiment(filepath.Join(root, "experiments", d.Experiment+".json"), x); err != nil {
		return "", err
	}
	sources := make([]Trajectory, len(examples))
	for i, e := range examples {
		sources[i] = e.Source
	}
	shard, err := SaveShard(filepath.Join(root, "sources"), sources)
	if err != nil {
		return "", err
	}
	d.Source = shard.Digest
	for _, a := range artifacts {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		id, _ := Digest(a)
		if err := writeFeedbackObject(filepath.Join(root, "behaviors", id+".json"), a, 128<<20); err != nil {
			return "", err
		}
	}
	for i, e := range examples {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if err := writeFeedbackObject(filepath.Join(root, "labels", d.Examples[i].Targets+".json"), e.Targets, 64<<20); err != nil {
			return "", err
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	id, _ := Digest(d)
	return id, writeFeedbackObject(filepath.Join(root, "datasets", id+".json"), d, 16<<20)
}

// LoadFeedbackDataset checks every reference and replays frozen behavior on
// actual observations before returning any examples for supervised training.
func LoadFeedbackDataset(ctx context.Context, root, id string) (FeedbackDataset, Experiment, []FeedbackExample, error) {
	var d FeedbackDataset
	var x Experiment
	fail := func(err error) (FeedbackDataset, Experiment, []FeedbackExample, error) {
		return d, x, nil, err
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if !digest(id) {
		return fail(fmt.Errorf("invalid feedback dataset identity"))
	}
	if err := readObject(filepath.Join(root, "datasets", id+".json"), &d, 16<<20); err != nil {
		return fail(err)
	}
	actual, err := Digest(d)
	if err != nil || actual != id || d.Schema != FeedbackDatasetSchema || !digest(d.Experiment) || !digest(d.Source) || len(d.Examples) < 1 || len(d.Examples) > 10000 {
		return fail(fmt.Errorf("invalid feedback dataset manifest"))
	}
	x, err = LoadExperiment(filepath.Join(root, "experiments", d.Experiment+".json"))
	if err != nil {
		return fail(err)
	}
	xid, _ := Digest(x)
	if xid != d.Experiment {
		return fail(fmt.Errorf("feedback experiment checksum mismatch"))
	}
	sources, _, err := LoadShard(filepath.Join(root, "sources"), d.Source)
	if err != nil {
		return fail(err)
	}
	if len(sources) != len(d.Examples) {
		return fail(fmt.Errorf("feedback source count mismatch"))
	}
	artifacts := map[string]battlepolicy.Artifact{}
	loaded := map[string]battlepolicy.Artifact{}
	examples := make([]FeedbackExample, len(sources))
	for i, entry := range d.Examples {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if !digest(entry.Trajectory) || !digest(entry.Targets) || entry.Behavior.Artifact != "" && !digest(entry.Behavior.Artifact) {
			return fail(fmt.Errorf("invalid feedback object reference"))
		}
		example := FeedbackExample{Source: sources[i]}
		if err := readObject(filepath.Join(root, "labels", entry.Targets+".json"), &example.Targets, 64<<20); err != nil {
			return fail(err)
		}
		target, _ := Digest(example.Targets)
		if target != entry.Targets || example.Targets.Trajectory != entry.Trajectory {
			return fail(fmt.Errorf("feedback label checksum or source mismatch"))
		}
		ref := entry.Behavior
		example.Behavior = Policy{Rule: ref.Rule, Greedy: ref.Greedy, Features: ref.Features}
		if ref.Artifact != "" {
			a, ok := loaded[ref.Artifact]
			if !ok {
				if err := readObject(filepath.Join(root, "behaviors", ref.Artifact+".json"), &a, 128<<20); err != nil {
					return fail(err)
				}
				actual, _ := Digest(a)
				if actual != ref.Artifact {
					return fail(fmt.Errorf("feedback behavior artifact checksum mismatch"))
				}
				loaded[ref.Artifact] = a
			}
			example.Behavior.Model = a.Network
			policy, err := example.Behavior.Version()
			if err != nil {
				return fail(err)
			}
			artifacts[policy] = a
		}
		examples[i] = example
	}
	expected, err := describeFeedback(ctx, x, examples, artifacts)
	if err != nil {
		return fail(err)
	}
	expected.Source = d.Source
	if !reflect.DeepEqual(expected, d) {
		return fail(fmt.Errorf("feedback manifest differs from verified records"))
	}
	return d, x, examples, nil
}
