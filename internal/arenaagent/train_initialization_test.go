package arenaagent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrainingInitialPolicyScaleCLI(t *testing.T) {
	root := t.TempDir()
	environment := filepath.Join(root, "environment.json")
	if err := os.WriteFile(environment, []byte(`{"schema_version":1,"command":["must-not-run"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"0", "-1", "1.1", "NaN", "+Inf", "1e-100", ".5", "1"} {
		dir := filepath.Join(root, "must-not-create")
		err := Main(context.Background(), []string{"train", "--environment", environment, "--data-dir", dir, "--initial-policy-scale", value}, "test", &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "--initial-policy-scale") || strings.Contains(err.Error(), "must-not-run") {
			t.Fatal("invalid/missing parent reached engine", value, err)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatal("invalid invocation mutated directory", err)
		}
	}
	for _, args := range [][]string{
		{"train", "--environment", environment, "--data-dir", root, "--resume"},
		{"train", "--database", "unread.sqlite", "--from-model", "unread.json"},
		{"train", "--demonstrations", "unread.jsonl", "--data-dir", root, "--from-model", "unread.json"},
	} {
		err := Main(context.Background(), append(args, "--initial-policy-scale", ".5"), "test", &bytes.Buffer{})
		// These paths may reject the other unsupported parent flag first;
		// none may open a provider, model, dataset or worker.
		if err == nil || strings.Contains(err.Error(), "no such file") || strings.Contains(err.Error(), "must-not-run") {
			t.Fatal("unsupported training accepted initialization", args, err)
		}
	}
	var help bytes.Buffer
	if err := Main(context.Background(), []string{"train", "--help"}, "test", &help); err != nil || !strings.Contains(help.String(), "initial-policy-scale") {
		t.Fatal("missing initialization help", err)
	}
}
