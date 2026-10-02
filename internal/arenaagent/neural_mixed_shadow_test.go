package arenaagent

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Shadow inference on settled public histories. These counterfactual plans
// were NOT submitted and must never inherit the recorded battle's win/loss or
// become PPO behavior labels. The actual replay test separately checks exact
// decisions made by the original recorded policy.
func TestMixedLearnedShadowOnSettledHistories(t *testing.T) {
	raw := os.Getenv("STONEAGE_MIXED_SHADOW")
	if raw == "" {
		t.Skip("explicit frozen models and settled history manifest required")
	}
	var config struct {
		Models []struct {
			Path            string `json:"path"`
			Version         string `json:"version"`
			OfflineBaseline bool   `json:"offline_baseline"`
		} `json:"models"`
		Corpora []struct {
			Database    string `json:"database"`
			SHA256      string `json:"sha256"`
			Mode        int    `json:"mode"`
			MinTurns    int    `json:"min_turns"`
			ExpectError string `json:"expect_error"`
		} `json:"corpora"`
		Output  string `json:"output"`
		Compare string `json:"compare"`
	}
	if err := decode([]byte(raw), &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Models) != 2 || len(config.Corpora) < 5 || config.Output == "" {
		t.Fatal("two models and all five modes required")
	}
	if config.Models[0].OfflineBaseline || !config.Models[1].OfflineBaseline {
		t.Fatal("one commander and one offline baseline required")
	}
	type point struct {
		Model, Database, Match string
		Turn, Orders           int
		Plan                   string
		Error                  string
	}
	var points []point
	modes := map[int]bool{}
	for _, corpus := range config.Corpora {
		modes[corpus.Mode] = true
		database, err := filepath.Abs(corpus.Database)
		if err != nil {
			t.Fatal(err)
		}
		content, err := os.ReadFile(database)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(content)
		if hex.EncodeToString(sum[:]) != corpus.SHA256 {
			t.Fatal("corpus identity changed", database)
		}
		if info, err := os.Stat(database + "-wal"); err == nil && info.Size() > 0 {
			t.Fatal("active/uncheckpointed corpus")
		} else if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		uri := url.URL{Scheme: "file", Path: database, RawQuery: "mode=ro&immutable=1"}
		db, err := sql.Open("sqlite", uri.String())
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		store := &Store{DB: db}
		records, err := readObjects(db, "SELECT body FROM records WHERE kind='turn' ORDER BY id")
		if err != nil {
			t.Fatal(err)
		}
		results, err := readObjects(db, "SELECT body FROM results")
		if err != nil {
			t.Fatal(err)
		}
		settled := map[string]bool{}
		for _, r := range results {
			settled[str(r["id"])] = true
		}
		histories := map[string][]Object{}
		for _, spec := range config.Models {
			if spec.OfflineBaseline {
				artifact, err := battlepolicy.LoadArtifact(spec.Path)
				if err != nil || artifact.Network.Config.PlanScope != "member" || "commander-policy-v2:"+hash(artifact) != spec.Version {
					t.Fatal("invalid matched offline baseline", err)
				}
				if _, err := NewLearned(spec.Path, corpus.Mode); err == nil || !strings.Contains(err.Error(), "independent member models are offline evaluation baselines") {
					t.Fatal("member baseline must not act as team commander", err)
				}
				t.Logf("mode=%d offline member baseline rejected as local commander", corpus.Mode)
				continue
			}
			model, err := NewLearned(spec.Path, corpus.Mode)
			if err != nil {
				t.Fatal(err)
			}
			if model.neural == nil || model.neural.Mixed == nil || model.Version() != spec.Version {
				t.Fatal("expected exact mixed artifact", spec.Path)
			}
			seen := map[string]bool{}
			maxTurn := 0
			orders := 0
			partialChecks := 0
			for _, record := range records {
				team := obj(record["team"])
				match := str(team["match_id"])
				turn := integer(team["turn"])
				if !settled[match] {
					t.Fatal("unsettled match", match)
				}
				if integer(team["mode"]) != corpus.Mode {
					t.Fatal("mixed corpus mode")
				}
				key := fmt.Sprintf("%s/%d", match, turn)
				if seen[key] {
					continue
				}
				seen[key] = true
				if histories[match] == nil {
					histories[match] = store.History(match)
					if err := store.Err(); err != nil {
						t.Fatal(err)
					}
				}
				var history []Object
				for _, entry := range histories[match] {
					if prior := obj(entry["team"]); prior != nil && integer(prior["turn"]) >= turn {
						continue
					}
					history = append(history, entry)
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				decision, err := model.Decide(ctx, team, history)
				cancel()
				if corpus.ExpectError != "" {
					var failure *neuralFailure
					if !errors.As(err, &failure) || failure.code != corpus.ExpectError {
						t.Fatal("missing exact refusal for incompatible public metadata", key, err)
					}
					points = append(points, point{Model: spec.Version, Database: corpus.Database, Match: match, Turn: turn, Error: failure.code})
					maxTurn = max(maxTurn, turn+1)
					continue
				}
				if err != nil {
					t.Fatal("shadow inference", corpus.Mode, key, err)
				}
				if decision.Strategy != "learned" || decision.Version != spec.Version || !yes(decision.Diagnostics["history_complete"]) || validatePlan(team, decision.Plan) != nil {
					t.Fatal("incomplete/invalid shadow decision", key)
				}
				// Reload the immutable model and JSON history as a restarted caller would.
				reloaded, err := NewLearned(spec.Path, corpus.Mode)
				if err != nil {
					t.Fatal(err)
				}
				var restoredTeam Object
				var restoredHistory []Object
				if err := decode(enc(team), &restoredTeam); err != nil {
					t.Fatal(err)
				}
				if err := decode(enc(history), &restoredHistory); err != nil {
					t.Fatal(err)
				}
				again, err := reloaded.Decide(context.Background(), restoredTeam, restoredHistory)
				if err != nil || !reflect.DeepEqual(decision, again) {
					t.Fatal("reload changed shadow decision", key, err)
				}
				// Same saved final plan is reused. Reserved actors model uncertain writes;
				// this only checks local recovery, not an acknowledgement from a server.
				saved := Object{"team": clone(team), "plan": decision.Plan, "strategy": "learned", "version": spec.Version}
				savedHistory := append(append([]Object(nil), history...), saved)
				reused, err := reloaded.Decide(context.Background(), team, savedHistory)
				if err != nil || !yes(reused.Diagnostics["reused_plan"]) || !reflect.DeepEqual(decision.Plan, reused.Plan) {
					t.Fatal("saved shadow plan not reused", key, err)
				}
				if len(decision.Plan.Orders) > 0 {
					current := clone(team)
					first := decision.Plan.Orders[0]
					member := obj(obj(current["members"])[first.Member])
					reserved := obj(member["reserved_actors"])
					if reserved == nil {
						reserved = Object{}
						member["reserved_actors"] = reserved
					}
					reserved[first.Actor] = "uncertain"
					remaining, err := reloaded.Decide(context.Background(), current, savedHistory)
					if err != nil || !yes(remaining.Diagnostics["reused_plan"]) || validatePlan(current, remaining.Plan) != nil {
						t.Fatal("partial shadow recovery failed", key, err)
					}
					if !reflect.DeepEqual(remaining.Plan.Orders, decision.Plan.Orders[1:]) {
						t.Fatal("partial recovery altered remaining slots", key)
					}
					for _, o := range remaining.Plan.Orders {
						if o.Member == first.Member && o.Actor == first.Actor {
							t.Fatal("reserved actor resubmitted")
						}
					}
					changed := clone(current)
					changed["observation_id"] = "changed-after-partial-write"
					if _, err := reloaded.Decide(context.Background(), changed, savedHistory); err == nil {
						t.Fatal("changed observation allowed replanning after uncertain write")
					}
					partialChecks++
				}
				points = append(points, point{Model: spec.Version, Database: corpus.Database, Match: match, Turn: turn, Orders: len(decision.Plan.Orders), Plan: hash(decision.Plan)})
				maxTurn = max(maxTurn, turn+1)
				orders += len(decision.Plan.Orders)
			}
			if maxTurn < corpus.MinTurns || corpus.ExpectError == "" && (orders == 0 || partialChecks == 0) || corpus.ExpectError != "" && (orders != 0 || partialChecks != 0) {
				t.Fatal("insufficient shadow coverage", maxTurn, corpus.MinTurns)
			}
			t.Logf("mode=%d model=%s fresh_observations=%d orders=%d max_turn=%d partial_recovery_checks=%d expected_error=%q; shadow only, no submitted commands or strength claim", corpus.Mode, spec.Version, len(seen), orders, maxTurn, partialChecks, corpus.ExpectError)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		after, err := os.ReadFile(database)
		if err != nil || sha256.Sum256(after) != sum {
			t.Fatal("shadow modified corpus", err)
		}
	}
	for mode := 1; mode <= 5; mode++ {
		if !modes[mode] {
			t.Fatal("missing mode", mode)
		}
	}
	if config.Compare != "" {
		previous, err := os.ReadFile(config.Compare)
		if err != nil {
			t.Fatal(err)
		}
		var expected []point
		if err := json.Unmarshal(previous, &expected); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(points, expected) {
			t.Fatal("separate process produced different plans")
		}
	}
	output, err := os.OpenFile(config.Output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(output).Encode(points); err != nil {
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
}
