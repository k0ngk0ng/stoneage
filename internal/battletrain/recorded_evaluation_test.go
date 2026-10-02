package battletrain

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

func recordedEvaluationFixture(t *testing.T) battlepolicy.Artifact {
	t.Helper()
	dataset, config := demonstrationRunFixture(t)
	root := filepath.Join(t.TempDir(), "run")
	ctx := context.Background()
	if err := RunDemonstrations(ctx, DemonstrationRunOptions{Directory: root, Dataset: dataset, Config: &config, Epochs: 1}); err != nil {
		t.Fatal(err)
	}
	path, err := ExportDemonstrationCandidate(ctx, root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	a, err := battlepolicy.LoadArtifact(path)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestRecordedEvaluationMarksBothCandidateAndOpponentSources(t *testing.T) {
	recorded := recordedEvaluationFixture(t)
	meta := recorded.Environment
	meta.Scenario = "controlled-battle-v8"
	native := experimentCandidate(t, experimentFixture(t))
	native.Environment, native.Experiment, native.HeldoutGroups = meta, "", nil
	c := DefaultEvaluationConfig()
	c.MatchesPerOpponent = 8
	id, _ := Digest(recorded)
	for _, candidate := range []battlepolicy.Artifact{recorded, native} {
		for _, opponent := range []battlepolicy.Artifact{recorded, native} {
			s, err := prepareEvaluation(context.Background(), meta, candidate, []Opponent{{Name: "opponent", Model: &opponent}}, c, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			var want []string
			if candidate.Recorded != nil || opponent.Recorded != nil {
				want = []string{id}
			}
			if !reflect.DeepEqual(s.Report.UnverifiedSourceArtifacts, want) {
				t.Fatal("source uncertainty dropped or duplicated", s.Report.UnverifiedSourceArtifacts, want)
			}
			if len(want) > 0 {
				if _, err := assessPromotion(s.Report, DefaultPromotionGate(), 1, false); err == nil || !strings.Contains(err.Error(), "source separation") {
					t.Fatal("unverified source allowed promotion", err)
				}
			}
		}
	}
	for _, change := range []string{"rules", "platform", "features"} {
		wrong := meta
		switch change {
		case "rules":
			wrong.Rules = strings.Repeat("f", 64)
		case "platform":
			wrong.Platform = "linux-arm64"
		case "features":
			wrong.Scenario = "controlled-battle-v7"
		}
		if _, err := prepareEvaluation(context.Background(), wrong, recorded, []Opponent{{Name: "basic", Rule: "basic"}}, c, nil, ""); err == nil {
			t.Fatal("execution incompatibility ignored", change)
		}
	}
}

func TestNativeRecordedEvaluationEvidenceRetainsUncertainty(t *testing.T) {
	raw, modelPath := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND"), os.Getenv("STONEAGE_RECORDED_MODEL")
	if raw == "" || modelPath == "" {
		t.Skip("explicit native engine and recorded model required")
	}
	var command []string
	if err := json.Unmarshal([]byte(raw), &command); err != nil {
		t.Fatal(err)
	}
	model, err := battlepolicy.LoadArtifact(modelPath)
	if err != nil || model.Recorded == nil {
		t.Fatal("recorded model required", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	engine, err := battleenv.Start(ctx, command, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	c := DefaultEvaluationConfig()
	c.Mode, c.MatchesPerOpponent, c.MaxTurns = model.Modes[0], 8, 3
	path := filepath.Join(t.TempDir(), "report.json")
	report, err := EvaluateRecorded(ctx, engine, model, []Opponent{{Name: "basic", Rule: "basic"}}, c, nil, "", path, nil)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := Digest(model)
	if !reflect.DeepEqual(report.UnverifiedSourceArtifacts, []string{id}) {
		t.Fatal("actual evaluation lost source limitation")
	}
	verified, err := VerifyEvaluation(ctx, path)
	if err != nil || !reflect.DeepEqual(report, verified) {
		t.Fatal("actual evidence did not verify", err)
	}
	// A consistent report with otherwise unchanged statistics cannot remove or
	// redirect the declaration derived from its frozen candidate/opponents.
	for _, refs := range [][]string{nil, {strings.Repeat("f", 64)}} {
		forged := report
		forged.UnverifiedSourceArtifacts = refs
		body, _ := json.Marshal(forged)
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyEvaluation(ctx, path); err == nil {
			t.Fatal("tampered overlap declaration accepted")
		}
	}
	t.Logf("native recorded model=%s games=%d source uncertainty retained and tampering rejected; no strength claim", id, len(report.Games))
}
