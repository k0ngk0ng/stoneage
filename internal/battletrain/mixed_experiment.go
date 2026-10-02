package battletrain

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

const mixedExperimentSchema = "commander-mixed-experiment-v1"
const mixedExperimentBytes = 80 << 20

// MixedExperiment binds separate single-mode splits to one shared learning
// objective. It does not rewrite any child experiment or confer mode support
// on a parent model. Parent artifacts are verified separately before training.
type MixedExperiment struct {
	Schema  string                `json:"schema"`
	Mixture ModeMixture           `json:"mixture"`
	Parts   []MixedExperimentPart `json:"parts"`
}

type MixedExperimentPart struct {
	Digest           string     `json:"digest"`
	FamiliesPerBatch int        `json:"families_per_batch"`
	Experiment       Experiment `json:"experiment"`
}

// NewMixedExperiment takes already-created frozen splits. Sampling quotas are
// complete eight-game families per mode per optimizer batch; they are distinct
// from the objective weights and cannot be inferred from observed game lengths.
// Empty part digests are computed; supplied ones must match exactly.
func NewMixedExperiment(mixture ModeMixture, parts []MixedExperimentPart, parent *battlepolicy.Artifact) (MixedExperiment, error) {
	x := MixedExperiment{Schema: mixedExperimentSchema, Mixture: mixture, Parts: parts}
	data, err := json.Marshal(x)
	if err != nil {
		return MixedExperiment{}, err
	}
	if len(data) > mixedExperimentBytes {
		return MixedExperiment{}, fmt.Errorf("mixed experiment exceeds size limit")
	}
	// Own the entire envelope. Caller changes to scenarios, pools, mixtures or
	// provenance after construction must not silently change the frozen object.
	var owned MixedExperiment
	if err := json.Unmarshal(data, &owned); err != nil {
		return MixedExperiment{}, err
	}
	x = owned
	for i := range x.Parts {
		if x.Parts[i].Digest == "" {
			x.Parts[i].Digest, err = Digest(x.Parts[i].Experiment)
			if err != nil {
				return MixedExperiment{}, err
			}
		}
	}
	return x, x.ValidateParent(parent)
}

func (x MixedExperiment) Validate() error {
	if x.Schema != mixedExperimentSchema {
		return fmt.Errorf("unsupported mixed experiment schema")
	}
	if err := x.Mixture.Validate(); err != nil {
		return err
	}
	if len(x.Parts) != len(x.Mixture.Modes) {
		return fmt.Errorf("mixed experiment must bind each declared mode exactly once")
	}
	first := x.Parts[0].Experiment
	seen, inherited, selected := map[string]bool{}, map[string]bool{}, map[string]bool{}
	families, quota := 0, 0
	for i, p := range x.Parts {
		e := p.Experiment
		if err := e.Validate(); err != nil {
			return fmt.Errorf("mixed experiment mode %d: %w", e.Mode, err)
		}
		id, err := Digest(e)
		if err != nil {
			return err
		}
		if p.Digest != id || e.Mode != x.Mixture.Modes[i].Mode || e.Pairing != BalancedPairing {
			return fmt.Errorf("mixed experiment child digest, mode or balanced pairing mismatch")
		}
		if p.FamiliesPerBatch < 1 || p.FamiliesPerBatch > 125 {
			return fmt.Errorf("each mixed mode needs 1..125 complete families per batch")
		}
		quota += p.FamiliesPerBatch
		families += len(e.Families)
		if quota > 125 || families > 4096 {
			return fmt.Errorf("mixed experiment exceeds 1000 games per batch or 4096 split families")
		}
		if e.Environment != first.Environment || e.InitialModel != first.InitialModel ||
			!reflect.DeepEqual(e.InitialTrainingGroups, first.InitialTrainingGroups) || !reflect.DeepEqual(e.InitialRecorded, first.InitialRecorded) {
			return fmt.Errorf("mixed experiment requires one environment and shared parent ancestry")
		}
		for _, group := range e.InitialTrainingGroups {
			inherited[group] = true
		}
		for _, group := range e.SelectionGroups {
			selected[group] = true
		}
		for _, f := range e.Families {
			if seen[f.Group] {
				return fmt.Errorf("configuration family occurs in multiple mixed experiment parts")
			}
			seen[f.Group] = true
		}
	}
	// A per-mode source (such as a build-selection pool) may have consumed
	// families belonging to another mode. Validate the union, not just each
	// otherwise valid child manifest independently. Previously selected/tested
	// identities cannot be repurposed as new training families in this schema.
	for _, p := range x.Parts {
		for _, f := range p.Experiment.Families {
			if selected[f.Group] || f.Split != "train" && inherited[f.Group] {
				return fmt.Errorf("mixed experiment family overlaps shared training/selection ancestry")
			}
		}
	}
	return nil
}

func (x MixedExperiment) ValidateParent(parent *battlepolicy.Artifact) error {
	if err := x.Validate(); err != nil {
		return err
	}
	for _, p := range x.Parts {
		if err := p.Experiment.validateInitialModel(parent); err != nil {
			return fmt.Errorf("mixed experiment mode %d parent: %w", p.Experiment.Mode, err)
		}
	}
	return nil
}

func SaveMixedExperiment(path string, x MixedExperiment) (string, error) {
	if err := x.Validate(); err != nil {
		return "", err
	}
	data, err := json.Marshal(x)
	if err != nil {
		return "", err
	}
	if len(data) > mixedExperimentBytes {
		return "", fmt.Errorf("mixed experiment exceeds size limit")
	}
	id, err := Digest(x)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	return id, writeObject(path, x)
}

func LoadMixedExperiment(path string) (MixedExperiment, error) {
	var x MixedExperiment
	if err := readObject(path, &x, mixedExperimentBytes); err != nil {
		return x, err
	}
	return x, x.Validate()
}

// MixedSchedule is immutable after construction. Each global game index maps
// to a fixed mode and mode-local game index, independently of worker order.
// A committed global prefix therefore determines all per-mode resume cursors.
type MixedSchedule struct {
	experiment MixedExperiment
	seed       int64
	families   []mixedFamilySlot
}

type mixedFamilySlot struct {
	part, ordinal int
}

type MixedScheduledGame struct {
	Index      uint64             `json:"index"`
	Batch      uint64             `json:"batch"`
	ModeGame   uint64             `json:"mode_game"`
	Mode       int                `json:"mode"`
	Experiment string             `json:"experiment"`
	Group      string             `json:"group"`
	Scenario   battleenv.Scenario `json:"scenario"`
}

func NewMixedSchedule(x MixedExperiment, seed int64) (*MixedSchedule, error) {
	if err := x.Validate(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(x)
	if err != nil {
		return nil, err
	}
	s := &MixedSchedule{seed: seed}
	if err := json.Unmarshal(data, &s.experiment); err != nil {
		return nil, err
	}
	for round := 0; ; round++ {
		added := false
		for part, p := range s.experiment.Parts {
			if round < p.FamiliesPerBatch {
				s.families = append(s.families, mixedFamilySlot{part, round})
				added = true
			}
		}
		if !added {
			break
		}
	}
	return s, nil
}

func (s *MixedSchedule) BatchMatches() int {
	if s == nil {
		return 0
	}
	return 8 * len(s.families)
}

func (s *MixedSchedule) Game(index uint64) (MixedScheduledGame, error) {
	if s.BatchMatches() == 0 || index > math.MaxInt32 {
		return MixedScheduledGame{}, fmt.Errorf("mixed schedule exceeds supported game counter")
	}
	batch, offset := index/uint64(s.BatchMatches()), index%uint64(s.BatchMatches())
	slot := s.families[offset/8]
	p := s.experiment.Parts[slot.part]
	local := (batch*uint64(p.FamiliesPerBatch)+uint64(slot.ordinal))*8 + offset%8
	// Separate mode streams preserve local seeds even when collection workers
	// finish out of order. The child owns pairing and canonical family rules.
	scenario, group := p.Experiment.trainingScenario(gameSeed(s.seed, uint64(p.Experiment.Mode), 94), local)
	scenario.Builds = slices.Clone(scenario.Builds)
	scenario.PetBuilds = slices.Clone(scenario.PetBuilds)
	scenario.PetSkillMasks = slices.Clone(scenario.PetSkillMasks)
	scenario.Reserves = battleenv.CloneReserves(scenario.Reserves)
	return MixedScheduledGame{Index: index, Batch: batch, ModeGame: local, Mode: p.Experiment.Mode, Experiment: p.Digest, Group: group, Scenario: scenario}, nil
}
