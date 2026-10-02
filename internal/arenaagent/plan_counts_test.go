package arenaagent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/battlenet"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

func TestPlanCountsCLIAndLocalModelContract(t *testing.T) {
	var out bytes.Buffer
	root := t.TempDir()
	environment := filepath.Join(root, "environment.json")
	if err := os.WriteFile(environment, []byte(`{"schema_version":1,"command":["must-not-run"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"train", "--environment", environment, "--data-dir", filepath.Join(root, "invalid"), "--plan-features", "typo"},
		{"train", "--environment", environment, "--data-dir", root, "--resume", "--plan-features", "target-counts-v1"},
		{"train", "--database", "must-not-open", "--plan-features", "target-counts-v1"},
		{"train", "--demonstrations", "must-not-open", "--plan-features", "target-counts-v1"},
	} {
		if err := Main(context.Background(), args, "test", &out); err == nil || !strings.Contains(err.Error(), "--plan-features") {
			t.Fatal(args, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "invalid")); !os.IsNotExist(err) {
		t.Fatal("bad flag created directory")
	}
	local, parent := neuralFixtureModel(t, 2)
	args := []string{"train", "--environment", environment, "--data-dir", filepath.Join(root, "child"), "--from-model", parent, "--experiment", "must-not-open", "--plan-features", "target-counts-v1"}
	if err := Main(context.Background(), args, "test", &out); err == nil || !strings.Contains(err.Error(), "cannot change the parent") {
		t.Fatal("parent silently converted", err)
	}
	a := *local.neural
	c := a.Network.Config
	c.PlanFeatures = "target-counts-v1"
	var err error
	a.Network, err = battlenet.NewModel[float32](c, 19)
	if err != nil {
		t.Fatal(err)
	}
	a.Architecture = battlepolicy.NetworkArchitecture(c)
	a.WeightsDigest, _ = battlepolicy.NetworkDigest(a.Network)
	path := filepath.Join(root, "new.json")
	if err := os.WriteFile(path, enc(a), 0600); err != nil {
		t.Fatal(err)
	}
	l, err := NewLearned(path, 2)
	if err != nil || !strings.HasPrefix(l.Version(), "commander-policy-v3:") {
		t.Fatal("new local commander contract", err)
	}
	a.Network.Config.PlanScope = "member"
	a.Architecture = battlepolicy.NetworkArchitecture(a.Network.Config)
	a.WeightsDigest, _ = battlepolicy.NetworkDigest(a.Network)
	if err := os.WriteFile(path, enc(a), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLearned(path, 2); err == nil || !strings.Contains(err.Error(), "offline evaluation baselines") {
		t.Fatal("new member model became commander", err)
	}
	if err := Main(context.Background(), []string{"train", "--help"}, "test", &out); err != nil || !strings.Contains(out.String(), "plan-features") {
		t.Fatal("missing help", err)
	}
}
