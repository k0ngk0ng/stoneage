package battletrain

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/k0ngk0ng/stoneage/internal/battlenet"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

func validateInitialPolicyScale(scale float64) error {
	// Zero is the absent legacy field. One must remain absent as well, so an
	// unchanged initialization preserves existing checkpoint/report identities.
	if scale != 0 && (!finite(scale) || scale <= 0 || scale >= 1 || float32(scale) <= 0) {
		return fmt.Errorf("initial policy scale must be finite and between 0 and 1, or absent")
	}
	return nil
}

// newInitialLearning never mutates an inference artifact. The score transform
// changes the sampled-policy identity and starts fresh Adam; it is not a
// decoding-time temperature or permission to relabel old PPO trajectories.
func newInitialLearning(network battlenet.Config, seed int64, parent *battlepolicy.Artifact, scale float64) (LearningState, error) {
	var state LearningState
	if err := validateInitialPolicyScale(scale); err != nil {
		return state, err
	}
	if scale != 0 && parent == nil {
		return state, fmt.Errorf("initial policy scale requires a declared parent")
	}
	model, err := battlenet.NewModel[float32](network, seed)
	if err != nil {
		return state, err
	}
	if parent != nil {
		if err := parent.Validate(); err != nil {
			return state, err
		}
		if parent.Network.Config != network {
			return state, fmt.Errorf("initial parent network mismatch")
		}
		raw, err := json.Marshal(parent.Network)
		if err != nil {
			return state, err
		}
		if err = json.Unmarshal(raw, model); err != nil {
			return state, err
		}
	}
	if scale != 0 {
		for _, name := range []string{"score.1.w", "score.1.b"} {
			p := model.Parameters[name]
			for i := range p.Values {
				p.Values[i] *= float32(scale)
			}
			model.Parameters[name] = p
		}
	}
	state = LearningState{Schema: 1, Model: model, Optimizer: &battlenet.Adam[float32]{}}
	return state, state.Validate()
}

type PolicyInitialization struct {
	Schema        string  `json:"schema"`
	Method        string  `json:"method"`
	Recipe        string  `json:"recipe"`
	Parent        string  `json:"parent_artifact"`
	ParentWeights string  `json:"parent_weights"`
	Scale         float64 `json:"scale"`
	Weights       string  `json:"initialized_weights"`
	Learning      string  `json:"initial_learning"`
}

func policyInitialization(recipe any, parent *battlepolicy.Artifact, scale float64, initial LearningState) (PolicyInitialization, error) {
	var r PolicyInitialization
	if parent == nil || scale == 0 {
		return r, fmt.Errorf("scaled initialization requires a parent and scale")
	}
	if err := validateInitialPolicyScale(scale); err != nil {
		return r, err
	}
	if err := initial.Validate(); err != nil {
		return r, err
	}
	if initial.Optimizer.Step != 0 {
		return r, fmt.Errorf("initialization requires fresh Adam")
	}
	r = PolicyInitialization{Schema: "commander-policy-initialization-v1", Method: "score-head-scale-v1", ParentWeights: parent.WeightsDigest, Scale: scale}
	var err error
	if r.Recipe, err = Digest(recipe); err != nil {
		return r, err
	}
	if r.Parent, err = Digest(*parent); err != nil {
		return r, err
	}
	if r.Weights, err = ModelDigest(initial.Model); err != nil {
		return r, err
	}
	r.Learning, err = Digest(initial)
	return r, err
}

func savePolicyInitialization(root string, recipe any, parent *battlepolicy.Artifact, scale float64, initial LearningState) (string, error) {
	r, err := policyInitialization(recipe, parent, scale, initial)
	if err != nil {
		return "", err
	}
	id, err := Digest(r)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(root, "initializations"), 0700); err != nil {
		return "", err
	}
	return id, writeObject(filepath.Join(root, "initializations", id+".json"), r)
}

func verifyPolicyInitialization(ctx context.Context, root, id string, recipe any, parent *battlepolicy.Artifact, network battlenet.Config, seed int64, scale float64) (*PolicyInitialization, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if scale == 0 {
		if id != "" {
			return nil, fmt.Errorf("unexpected policy initialization receipt")
		}
		return nil, nil
	}
	if !digest(id) {
		return nil, fmt.Errorf("missing policy initialization receipt")
	}
	var r PolicyInitialization
	if err := readObject(filepath.Join(root, "initializations", id+".json"), &r, 4<<20); err != nil {
		return nil, err
	}
	actual, err := Digest(r)
	if err != nil || actual != id {
		return nil, fmt.Errorf("initialization receipt checksum mismatch")
	}
	initial, err := newInitialLearning(network, seed, parent, scale)
	if err != nil {
		return nil, err
	}
	expected, err := policyInitialization(recipe, parent, scale, initial)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(r, expected) {
		return nil, fmt.Errorf("initialization differs from parent, transform or recipe")
	}
	if _, err := loadLearning(filepath.Join(root, "learning"), r.Learning); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &r, nil
}

// Bind the first actual PPO batch to the transformed initial policy, including
// checkpoints after later updates. A rehashed scale/receipt alone cannot turn
// games collected by the unscaled parent into on-policy initialization data.
func validateInitialPolicyData(ctx context.Context, root string, c Checkpoint, x *Experiment, r *PolicyInitialization) error {
	if r == nil {
		return nil
	}
	if c.CompletedBatches == 0 && c.Learning != r.Learning {
		return fmt.Errorf("initial learning state differs from transform")
	}
	shards := c.Pending
	if c.CompletedBatches > 0 {
		shards = c.Trained[:c.Config.BatchMatches]
	}
	replay := Checkpoint{Config: c.Config, Environment: c.Environment}
	versions := map[string]string{}
	for i, id := range shards {
		if err := ctx.Err(); err != nil {
			return err
		}
		pair, _, err := LoadShard(filepath.Join(root, "shards"), id)
		if err != nil {
			return err
		}
		g, err := makeLeagueGame(replay, uint64(i), id, pair, r.Weights)
		if err != nil {
			return fmt.Errorf("initial policy data: %w", err)
		}
		want, err := opponentVersion(root, replay.opponentAt(uint64(i)), r.Weights, versions)
		if err != nil {
			return err
		}
		s, group := trainingScenario(c.Config, uint64(i), x)
		scenario, err := Digest(s)
		if err != nil {
			return err
		}
		if g.Version != want || pair[0].Group != group || pair[0].Scenario != scenario {
			return fmt.Errorf("initial policy data differs from schedule")
		}
	}
	return nil
}
