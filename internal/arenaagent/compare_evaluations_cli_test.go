package arenaagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/battletrain"
)

func TestCompareEvaluationsCLIValidation(t *testing.T) {
	for _, args := range [][]string{{"compare-evaluations"}, {"compare-evaluations", "--baseline", "missing"}, {"compare-evaluations", "--baseline", "missing", "--candidate", "missing", "extra"}} {
		var out bytes.Buffer
		if e := Main(context.Background(), args, "test", &out); e == nil || !strings.Contains(e.Error(), "requires --baseline and --candidate") || out.Len() != 0 {
			t.Fatal(args, e, out.String())
		}
	}
	var help bytes.Buffer
	if e := Main(context.Background(), []string{"compare-evaluations", "--help"}, "test", &help); e != nil || !strings.Contains(help.String(), "baseline") || !strings.Contains(help.String(), "candidate") {
		t.Fatal(e, help.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	if e := Main(ctx, []string{"compare-evaluations", "--baseline", "missing", "--candidate", "missing"}, "test", &out); !errors.Is(e, context.Canceled) || out.Len() != 0 {
		t.Fatal("canceled comparison read files or wrote output", e)
	}
}

func TestCompareEvaluationsCLIRecordedEvidence(t *testing.T) {
	baseline, candidate := os.Getenv("STONEAGE_COMPARE_BASELINE"), os.Getenv("STONEAGE_COMPARE_CANDIDATE")
	if baseline == "" || candidate == "" {
		t.Skip("explicit existing native evidence required; read-only, no worker")
	}
	var out bytes.Buffer
	if e := Main(context.Background(), []string{"compare-evaluations", "--baseline", baseline, "--candidate", candidate}, "test", &out); e != nil {
		t.Fatal(e)
	}
	var r battletrain.PairedEvaluationReport
	if e := json.Unmarshal(out.Bytes(), &r); e != nil {
		t.Fatal(e)
	}
	if r.Schema != "commander-paired-evaluation-v1" || r.Method != "paired-family-percentile-bootstrap-v1" || r.BaselineReport == "" || r.CandidateReport == "" || len(r.Comparisons) == 0 || r.ArenaCertified {
		t.Fatal("incomplete comparison", r)
	}
}
