package arenaagent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battletrain"
)

func TestPoolMixCLIContract(t *testing.T) {
	var out bytes.Buffer
	if e := Main(context.Background(), []string{"experiment", "--help"}, "test", &out); e != nil || !strings.Contains(out.String(), "pool-train-groups") {
		t.Fatal("missing help", e)
	}
	for _, extra := range [][]string{
		{"--pool-train-groups", "1", "--train-groups", "4"},
		{"--pool-train-groups", "1", "--build-pool", "unread"},
		{"--pool-train-groups", "0", "--train-groups", "4", "--build-pool", "unread"},
		{"--pool-train-groups", "-1", "--train-groups", "4", "--build-pool", "unread"},
		{"--pool-train-groups", "4", "--train-groups", "4", "--build-pool", "unread"},
	} {
		output := filepath.Join(t.TempDir(), "experiment.json")
		args := append([]string{"experiment", "--environment", "unread", "--output", output}, extra...)
		out.Reset()
		e := Main(context.Background(), args, "test", &out)
		if e == nil || !strings.Contains(e.Error(), "--pool-train-groups requires") {
			t.Fatal("invalid mix reached IO", args, e)
		}
		if _, e = os.Stat(output); !os.IsNotExist(e) {
			t.Fatal("created invalid manifest", e)
		}
	}
	dir := t.TempDir()
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	env := filepath.Join(dir, "environment.json")
	raw, _ := json.Marshal(Object{"schema_version": 1, "command": []string{exe, "-test.run=^TestEnvironmentCloseHelper$", "--", "--environment-close=success"}})
	if e = os.WriteFile(env, raw, 0600); e != nil {
		t.Fatal(e)
	}
	c := battletrain.DefaultBuildSearchConfig()
	c.Mode, c.Points, c.PetPoints, c.Level = 1, 20, 0, 10
	version, e := (battletrain.Policy{Rule: "basic"}).Version()
	if e != nil {
		t.Fatal(e)
	}
	pool := battletrain.BuildPool{Schema: "commander-build-pool-v1", Config: c, Environment: battleenv.Metadata{Scenario: "controlled-battle-v8", Platform: "linux-arm64", Rules: strings.Repeat("a", 64)}, SourceState: strings.Repeat("b", 64), SourcePolicy: battletrain.BuildPolicyIdentity{Name: "basic", Rule: "basic", Version: version}, SelectionGroups: []string{strings.Repeat("c", 64)}, Rosters: []battletrain.Roster{{Players: []battleenv.Build{{5, 5, 5, 5}}}, {Players: []battleenv.Build{{8, 4, 4, 4}}}}}
	sort.Slice(pool.Rosters, func(i, j int) bool { return pool.Rosters[i].ID() < pool.Rosters[j].ID() })
	if e = pool.Validate(); e != nil {
		t.Fatal(e)
	}
	poolPath := filepath.Join(dir, "pool.json")
	raw, _ = json.Marshal(pool)
	if e = os.WriteFile(poolPath, raw, 0600); e != nil {
		t.Fatal(e)
	}
	for _, mix := range []bool{false, true} {
		name := "legacy"
		if mix {
			name = "mixed"
		}
		output := filepath.Join(dir, name+".json")
		args := []string{"experiment", "--environment", env, "--output", output, "--build-pool", poolPath, "--validation-groups", "1", "--test-groups", "1"}
		if mix {
			args = append(args, "--pool-train-groups", "2", "--train-groups", "4")
		}
		out.Reset()
		if e = Main(context.Background(), args, "test", &out); e != nil {
			t.Fatal(e)
		}
		manifest, e := battletrain.LoadExperiment(output)
		if e != nil {
			t.Fatal(e)
		}
		var event Object
		if e = json.Unmarshal(out.Bytes(), &event); e != nil {
			t.Fatal(e)
		}
		if mix {
			if manifest.PoolTrainingGroups == nil || *manifest.PoolTrainingGroups != 2 || manifest.Schema != "commander-pool-mix-experiment-v1" || event["pool_train_groups"] != float64(2) || event["fresh_train_groups"] != float64(2) {
				t.Fatal("mixed contract missing", event)
			}
		} else {
			if manifest.PoolTrainingGroups != nil || event["pool_train_groups"] != nil || event["train_groups"] != float64(3) {
				t.Fatal("legacy default changed", event)
			}
		}
	}
	out.Reset()
	e = Main(context.Background(), []string{"experiment", "--environment", "unread", "--output", filepath.Join(dir, "bad.json"), "--build-pool", poolPath, "--pool-train-groups", "4", "--train-groups", "5"}, "test", &out)
	if e == nil || !strings.Contains(e.Error(), "exceeds the frozen pool") {
		t.Fatal("invented pool pair reached engine", e)
	}
}
