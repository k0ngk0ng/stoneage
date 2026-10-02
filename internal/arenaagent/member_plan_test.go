package arenaagent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

func TestIndependentMemberCLIValidationAndOnlineRejection(t *testing.T) {
	var out bytes.Buffer
	if err := Main(context.Background(), []string{"train", "--help"}, "test", &out); err != nil || !strings.Contains(out.String(), "plan-scope") {
		t.Fatal("missing baseline help", err)
	}
	root := t.TempDir()
	environment := filepath.Join(root, "environment.json")
	if err := os.WriteFile(environment, []byte(`{"schema_version":1,"command":["must-not-run"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"train", "--environment", environment, "--data-dir", filepath.Join(root, "invalid"), "--plan-scope", "typo"},
		{"train", "--environment", environment, "--data-dir", root, "--resume", "--plan-scope", "member"},
		{"train", "--database", "must-not-open", "--plan-scope", "member"},
		{"train", "--demonstrations", "must-not-open", "--plan-scope", "member"},
	} {
		if err := Main(context.Background(), args, "test", &out); err == nil || !strings.Contains(err.Error(), "--plan-scope") {
			t.Fatal("invalid scope reached worker or data", args, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "invalid")); !os.IsNotExist(err) {
		t.Fatal("invalid scope created a training directory")
	}
	local, parentPath := neuralFixtureModel(t, 2)
	args := []string{"train", "--environment", environment, "--data-dir", filepath.Join(root, "child"), "--from-model", parentPath, "--experiment", "must-not-open", "--plan-scope", "member"}
	if err := Main(context.Background(), args, "test", &out); err == nil || !strings.Contains(err.Error(), "cannot change the parent") {
		t.Fatal("parent commander relabeled as independent", err)
	}
	a := *local.neural
	a.Network.Config.PlanScope = "member"
	a.WeightsDigest, _ = battlepolicy.NetworkDigest(a.Network)
	a.Architecture = battlepolicy.NetworkArchitecture(a.Network.Config)
	path := filepath.Join(root, "independent.json")
	if err := os.WriteFile(path, enc(a), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := battlepolicy.LoadArtifact(path); err != nil {
		t.Fatal("offline model rejected", err)
	}
	if _, err := NewLearned(path, 2); err == nil || !strings.Contains(err.Error(), "offline evaluation baselines") {
		t.Fatal("independent model became online commander", err)
	}
	args[len(args)-1], args[6] = "team", path
	if err := Main(context.Background(), args, "test", &out); err == nil || !strings.Contains(err.Error(), "cannot change the parent") {
		t.Fatal("independent parent relabeled as commander", err)
	}
}
