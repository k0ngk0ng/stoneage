package battlepolicy

import "fmt"

// MixedTraining declares the shared commander's actual mixed updates, not a
// strength certificate. Export must reconstruct these counts from validated
// receipts; the standalone inference loader checks their structural contract.
type MixedTraining struct {
	Schema     string              `json:"schema"`
	Checkpoint string              `json:"checkpoint"`
	Objective  string              `json:"objective"`
	Batches    int                 `json:"accepted_batches"`
	Updates    int                 `json:"accepted_updates"`
	Modes      []MixedModeTraining `json:"modes"`
}

type MixedModeTraining struct {
	Mode             int    `json:"mode"`
	Experiment       string `json:"experiment"`
	Weight           int    `json:"weight"`
	FamiliesPerBatch int    `json:"families_per_batch"`
	Episodes         int    `json:"episodes"`
	TeamTurns        int    `json:"team_turns"`
	Actions          int    `json:"actions"`
}

func (a Artifact) validateMixedTraining() error {
	if a.Schema != 6 {
		if a.Mixed != nil {
			return fmt.Errorf("mixed training requires artifact schema 6")
		}
		return nil
	}
	m := a.Mixed
	if m == nil || m.Schema != "commander-mixed-training-v1" || m.Objective != "mode-weighted-ppo-v1" || !hashString(m.Checkpoint) || !hashString(a.Experiment) || m.Batches < 1 || m.Batches > 10000 || m.Updates < m.Batches || len(m.Modes) < 2 || len(m.Modes) > 5 || len(m.Modes) != len(a.Modes) {
		return fmt.Errorf("invalid mixed model training declaration")
	}
	quota, previous := 0, 0
	seen := map[string]bool{}
	for i, mode := range m.Modes {
		if mode.Mode <= previous || mode.Mode > 5 || mode.Mode != a.Modes[i] || !hashString(mode.Experiment) || seen[mode.Experiment] || mode.Weight < 1 || mode.Weight > 1000000 || mode.FamiliesPerBatch < 1 || mode.FamiliesPerBatch > 125 || mode.Episodes < 1 || mode.TeamTurns < mode.Episodes || mode.Actions < 0 {
			return fmt.Errorf("invalid mixed model per-mode training declaration")
		}
		quota += mode.FamiliesPerBatch
		previous, seen[mode.Experiment] = mode.Mode, true
	}
	if quota > 125 {
		return fmt.Errorf("mixed model batch exceeds 1000 games")
	}
	return nil
}
