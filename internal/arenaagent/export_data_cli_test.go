package arenaagent

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestExportDataCommandValidation(t *testing.T) {
	var out bytes.Buffer
	if err := Main(context.Background(), []string{"export-data", "--help"}, "test", &out); err != nil || !bytes.Contains(out.Bytes(), []byte("not PPO shards")) {
		t.Fatalf("%v %s", err, out.String())
	}
	root := t.TempDir()
	for _, args := range [][]string{
		{"export-data"},
		{"export-data", "--records", root, "--output", filepath.Join(root, "unused.jsonl"), "unexpected"},
		{"export-data", "--records", root, "--output", filepath.Join(root, "unused.jsonl"), "--format", "ppo"},
	} {
		out.Reset()
		if err := Main(context.Background(), args, "test", &out); err == nil {
			t.Fatal(args)
		}
	}
	out.Reset()
	if err := Main(context.Background(), []string{"export-data", "--records", root, "--output", filepath.Join(root, "empty.jsonl")}, "test", &out); err == nil {
		t.Fatal("empty dataset accepted")
	}
	var report map[string]any
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || report["event"] != "records_export_failed" || report["on_policy_ppo"] != false {
		t.Fatalf("%v %s", err, out.String())
	}
}
