package arenaagent

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestImitationWeightingCLIHelpAndRefusals(t *testing.T) {
	for _, cmd := range []string{"train", "train-feedback"} {
		var out bytes.Buffer
		if e := Main(context.Background(), []string{cmd, "--help"}, "test", &out); e != nil || !strings.Contains(out.String(), "sqrt-action-frequency-v1") {
			t.Fatal(cmd, e, out.String())
		}
	}
	for _, args := range [][]string{
		{"train", "--database", "missing.sqlite", "--warmup-action-weighting", "sqrt-action-frequency-v1"},
		{"train", "--demonstrations", "missing.jsonl", "--data-dir", "unused", "--warmup-action-weighting", "sqrt-action-frequency-v1"},
		{"train", "--mixed-experiment", "missing.json", "--data-dir", "unused", "--warmup-action-weighting", "sqrt-action-frequency-v1"},
		{"train", "--resume", "--data-dir", "unused", "--environment", "missing.json", "--warmup-action-weighting", "none"},
		{"train", "--action-weighting", "unknown"},
		{"train-feedback", "--resume", "--data-dir", "unused", "--action-weighting", "none"},
		{"train-feedback", "--action-weighting", "unknown"},
	} {
		var out bytes.Buffer
		if e := Main(context.Background(), args, "test", &out); e == nil {
			t.Fatal("invalid weighting invocation accepted", args)
		}
	}
}
