package battlepolicy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlenet"
)

// Artifact is the inference-only model. Optimizer and sampler progress belong
// in training checkpoints. A collected/trained candidate is not a certified
// arena model; certification must reference a separate evaluation/parity gate.
type Artifact struct {
	Mixed                      *MixedTraining            `json:"mixed_training,omitempty"`
	RecordedSelectionArtifacts []string                  `json:"recorded_selection_artifacts,omitempty"`
	Recorded                   *RecordedTraining         `json:"recorded_training,omitempty"`
	Parent                     string                    `json:"parent_artifact,omitempty"`
	SelectionGroups            []string                  `json:"selection_groups,omitempty"`
	HeldoutGroups              []string                  `json:"heldout_groups,omitempty"`
	Experiment                 string                    `json:"experiment,omitempty"`
	Schema                     int                       `json:"schema_version"`
	Architecture               string                    `json:"architecture"`
	Features                   string                    `json:"features"`
	Actions                    string                    `json:"actions"`
	Status                     string                    `json:"status"`
	Environment                battleenv.Metadata        `json:"environment"`
	Modes                      []int                     `json:"modes"`
	WeightsDigest              string                    `json:"weights_digest"`
	TrainingReport             string                    `json:"training_report"`
	TrainingShards             []string                  `json:"training_shards"`
	TrainingGroups             []string                  `json:"training_groups"`
	Network                    *battlenet.Model[float32] `json:"network"`
}

func NetworkDigest(m *battlenet.Model[float32]) (string, error) {
	if m == nil {
		return "", fmt.Errorf("network required")
	}
	if e := m.Validate(); e != nil {
		return "", e
	}
	b, e := json.Marshal(m)
	if e != nil {
		return "", e
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}
func hashString(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(s) == 64 && len(b) == 32
}
func (a Artifact) Validate() error {
	if err := a.validateMixedTraining(); err != nil {
		return err
	}
	if err := a.validateRecordedSelection(); err != nil {
		return err
	}
	if a.Schema == 3 || a.Schema == 4 || (a.Schema == 5 || a.Schema == 6) && a.Recorded != nil {
		if err := a.validateRecordedTraining(); err != nil {
			return err
		}
	} else {
		if a.Recorded != nil {
			return fmt.Errorf("recorded training requires artifact schema 3, 4, 5 or 6")
		}
		if err := ValidateFeatureEnvironment(a.Features, a.Environment.Scenario); err != nil {
			return err
		}
	}
	if !SupportedFeatures(a.Features) || a.Actions != ActionsForFeatures(a.Features) {
		return fmt.Errorf("model feature/action schema %q/%q differs from %q/%q; train a new model, do not relabel old weights", a.Features, a.Actions, FeatureVersion, ActionVersion)
	}
	if a.Parent != "" && !hashString(a.Parent) {
		return fmt.Errorf("invalid parent artifact identity")
	}
	previousSelection := ""
	for _, id := range a.SelectionGroups {
		if !hashString(id) || id <= previousSelection {
			return fmt.Errorf("invalid model selection provenance")
		}
		previousSelection = id
	}
	previousHeldout := ""
	if len(a.HeldoutGroups) > 0 && a.Experiment == "" {
		return fmt.Errorf("held-out groups require an experiment identity")
	}
	for _, id := range a.HeldoutGroups {
		if !hashString(id) || id <= previousHeldout {
			return fmt.Errorf("invalid declared held-out groups")
		}
		previousHeldout = id
	}
	if a.Experiment != "" && !hashString(a.Experiment) {
		return fmt.Errorf("invalid model experiment identity")
	}
	if a.Schema != 2 && a.Schema != 3 && a.Schema != 4 && a.Schema != 5 && a.Schema != 6 || a.Network == nil || a.Architecture != NetworkArchitecture(a.Network.Config) || a.Status != "candidate" || len(a.Modes) < 1 || len(a.Modes) > 5 || !hashString(a.TrainingReport) || a.Schema != 3 && len(a.TrainingShards) == 0 {
		return fmt.Errorf("incompatible or uncertified model manifest")
	}
	if a.Schema != 3 {
		if e := a.Environment.Validate(); e != nil {
			return e
		}
	}
	last := 0
	for _, mode := range a.Modes {
		if mode <= last || mode > 5 {
			return fmt.Errorf("invalid model modes")
		}
		last = mode
	}
	seen := map[string]bool{}
	for _, id := range a.TrainingShards {
		if !hashString(id) || seen[id] {
			return fmt.Errorf("invalid model training provenance")
		}
		seen[id] = true
	}
	if a.Schema != 3 && len(a.TrainingGroups) == 0 {
		return fmt.Errorf("model requires training groups for independent evaluation")
	}
	previous := ""
	consumed := map[string]bool{}
	for _, id := range a.SelectionGroups {
		consumed[id] = true
	}
	for _, id := range a.TrainingGroups {
		if !hashString(id) || id <= previous {
			return fmt.Errorf("invalid training group manifest")
		}
		previous = id
		consumed[id] = true
	}
	for _, id := range a.HeldoutGroups {
		if consumed[id] {
			return fmt.Errorf("model held-out groups overlap training/selection provenance")
		}
	}
	id, e := NetworkDigest(a.Network)
	if e != nil {
		return e
	}
	if id != a.WeightsDigest {
		return fmt.Errorf("model weights checksum mismatch")
	}
	c := a.Network.Config
	if a.Features != NetworkFeatures(c) {
		return fmt.Errorf("model manifest/input contract mismatch; do not relabel weights")
	}
	if c.EntityFeatures != EntityFeatures || c.CandidateFeatures != CandidateFeatures || c.EventFeatures != EventFeatures {
		return fmt.Errorf("incompatible model feature dimensions")
	}
	return nil
}

// DataGroups covers synthetic configuration groups only. Recorded roster
// groups live in Recorded and cannot establish synthetic held-out independence.
// Native consumers must check the environment/provenance before using this.
func (a Artifact) DataGroups() []string {
	return append(append([]string(nil), a.TrainingGroups...), a.SelectionGroups...)
}
func LoadArtifact(path string) (Artifact, error) {
	var a Artifact
	f, e := os.Open(path)
	if e != nil {
		return a, e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return a, e
	}
	if !info.Mode().IsRegular() || info.Size() > 128<<20 {
		return a, fmt.Errorf("model file exceeds limit")
	}
	b, e := io.ReadAll(io.LimitReader(f, (128<<20)+1))
	if e != nil {
		return a, e
	}
	if len(b) > 128<<20 {
		return a, fmt.Errorf("model file exceeds limit")
	}
	return DecodeArtifact(b)
}

// DecodeArtifact validates the same bytes the caller pinned for this process;
// schema dispatch must not reopen a model file that could have been replaced.
func DecodeArtifact(b []byte) (Artifact, error) {
	var a Artifact
	if len(b) > 128<<20 {
		return a, fmt.Errorf("model file exceeds limit")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e := d.Decode(&a); e != nil {
		return a, e
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return a, fmt.Errorf("trailing model content")
	}
	return a, a.Validate()
}
