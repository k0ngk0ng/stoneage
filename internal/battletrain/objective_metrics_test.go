package battletrain

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/battlenet"
)

func TestPPOObjectiveMetricsOnUniformPolicy(t *testing.T) {
	for _, entropyWeight := range []float64{0, .01} {
		m := testModel(t)
		// Zero logits yield exactly the uniform supported-action distribution;
		// the zero value baseline gives a closed-form sparse-terminal return.
		for _, p := range m.Parameters {
			clear(p.Values)
		}
		episode := testTrajectory(t, m, 5)
		config := DefaultPPOConfig()
		config.Epochs, config.SequenceLength = 1, 2
		config.EntropyWeight = entropyWeight
		wantEntropy, wantValue := 0., 0.
		for i, step := range episode.Steps {
			for _, slot := range step.Frame.Slots {
				count := 0
				for _, candidate := range slot.Candidates {
					if candidate.Supported {
						count++
					}
				}
				wantEntropy += math.Log(float64(count))
			}
			valueTarget := math.Pow(config.Lambda, float64(len(episode.Steps)-1-i))
			wantValue += .5 * config.ValueWeight * valueTarget * valueTarget
		}
		wantEntropy /= float64(len(episode.Steps))
		wantValue /= float64(len(episode.Steps))
		r, err := Train(context.Background(), m, &battlenet.Adam[float32]{}, []Trajectory{episode}, config)
		if err != nil || len(r.Epochs) != 1 || r.Epochs[0].Objectives == nil {
			t.Fatal("missing accepted epoch objectives", err)
		}
		e := r.Epochs[0]
		o := e.Objectives
		if math.Abs(o.ConditionalEntropy-wantEntropy) > 1e-6 || math.Abs(o.ValueLoss-wantValue) > 1e-6 || math.Abs(o.PolicyLoss) > 1e-6 || math.Abs(o.EntropyLoss+entropyWeight*wantEntropy) > 1e-6 {
			t.Fatal("objective means/weights differ from analytic uniform policy", o, wantEntropy, wantValue)
		}
		if math.Abs(e.Loss-(o.PolicyLoss+o.ValueLoss+o.EntropyLoss)) > 1e-6 {
			t.Fatal("diagnostics do not describe the optimized loss")
		}
	}
}

func TestLegacyEpochReportDoesNotAcquireObjectiveFields(t *testing.T) {
	const old = `{"loss":0.25,"approx_kl":0,"clip_fraction":0,"gradient_norm":1}`
	var epoch EpochReport
	if err := json.Unmarshal([]byte(old), &epoch); err != nil {
		t.Fatal(err)
	}
	if epoch.Objectives != nil {
		t.Fatal("unknown old objectives became known zeros")
	}
	b, err := json.Marshal(epoch)
	if err != nil || string(b) != old {
		t.Fatal("old report serialization/digest changed", string(b), err)
	}
}
