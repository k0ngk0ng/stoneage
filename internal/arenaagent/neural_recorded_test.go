package arenaagent

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"testing"
	"time"
)

// Recompute actual completed client decisions, including public projection and
// recurrent history reconstruction. This is not a transport latency benchmark.
// Run only after the collector exits; immutable SQLite deliberately ignores WAL.
func TestRecordedLearnedPlansReplay(t *testing.T) {
	raw := os.Getenv("STONEAGE_LEARNED_REPLAY")
	if raw == "" {
		t.Skip("completed collector database and its frozen model required")
	}
	var config struct {
		Directory                 string `json:"directory"`
		Model                     string `json:"model"`
		Mode                      int    `json:"mode"`
		RequireObserverWithdrawal bool   `json:"require_observer_withdrawal"`
		MinDecisionTurns          int    `json:"min_decision_turns"`
	}
	if err := decode([]byte(raw), &config); err != nil {
		t.Fatal(err)
	}
	database, err := filepath.Abs(filepath.Join(config.Directory, "arena.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(database); err != nil || !info.Mode().IsRegular() {
		t.Fatal("collector database missing", err)
	}
	if info, err := os.Stat(database + "-wal"); err == nil && info.Size() > 0 {
		t.Fatal("collector must exit and checkpoint its WAL before replay")
	} else if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	uri := url.URL{Scheme: "file", Path: database, RawQuery: "mode=ro&immutable=1"}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	store := &Store{DB: db}
	model, err := NewLearned(config.Model, config.Mode)
	if err != nil {
		t.Fatal(err)
	}
	records, err := readObjects(db, "SELECT body FROM records WHERE kind='turn' ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	histories := map[string][]Object{}
	settled, err := readObjects(db, "SELECT body FROM results")
	if err != nil {
		t.Fatal(err)
	}
	settledMatches := map[string]bool{}
	for _, result := range settled {
		if match := str(result["id"]); match != "" {
			settledMatches[match] = true
		}
	}
	var durations []time.Duration
	orders := 0
	observerWithdrawn, maxDecisionTurns := 0, 0
	for _, record := range records {
		team := obj(record["team"])
		match, turn := str(team["match_id"]), integer(team["turn"])
		if !settledMatches[match] {
			t.Fatal("replay coverage requires a settled match; partial collection is not a completed battle", match)
		}
		key := fmt.Sprintf("%s/%d", match, turn)
		if seen[key] {
			continue // Reused partial-submission plans are not fresh inference.
		}
		seen[key] = true
		if str(record["strategy"]) != "learned" || str(record["version"]) != model.Version() {
			t.Fatal("recorded policy differs from replay model", key)
		}
		if histories[match] == nil {
			histories[match] = store.History(match)
			if err := store.Err(); err != nil {
				t.Fatal(err)
			}
		}
		var history []Object
		for _, entry := range histories[match] {
			if previous := obj(entry["team"]); previous != nil && integer(previous["turn"]) >= turn {
				continue // Do not reuse the saved answer, or expose future plans.
			}
			history = append(history, entry)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		start := time.Now()
		decision, err := model.Decide(ctx, team, history)
		durations = append(durations, time.Since(start))
		cancel()
		if err != nil {
			t.Fatal("recorded public history cannot be reconstructed", key, err)
		}
		var want Plan
		if err := decode(enc(record["plan"]), &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(decision.Plan, want) {
			t.Fatalf("frozen model changed actual submitted plan %s: got=%+v want=%+v", key, decision.Plan, want)
		}
		actualDiagnostics, savedDiagnostics := clone(decision.Diagnostics), obj(record["diagnostics"])
		for _, field := range []string{"estimated_return", "policy_log_probability", "conditional_log_probabilities", "history_turns"} {
			if savedDiagnostics[field] == nil || !reflect.DeepEqual(actualDiagnostics[field], savedDiagnostics[field]) {
				t.Fatalf("recorded numerical output differs for %s field %s: actual=%v saved=%v", key, field, actualDiagnostics[field], savedDiagnostics[field])
			}
		}

		for name, member := range obj(team["members"]) {
			view := obj(member)
			if !yes(view["withdrawn"]) {
				continue
			}
			if integer(obj(view["battle"])["MyNo"])%10 == 0 {
				observerWithdrawn++
			}
			for _, order := range decision.Plan.Orders {
				if order.Member == name {
					t.Fatal("replayed order addressed a withdrawn member", key, name)
				}
			}
		}
		maxDecisionTurns = max(maxDecisionTurns, turn+1)
		orders += len(want.Orders)
		t.Logf("match=%s turn=%d orders=%d inference=%s", match, turn, len(want.Orders), durations[len(durations)-1])
	}
	if len(durations) == 0 || orders == 0 {
		t.Fatal("no actual learned plans replayed")
	}
	if config.RequireObserverWithdrawal && observerWithdrawn == 0 {
		t.Fatal("no fresh plan after the original history observer withdrew")
	}
	if maxDecisionTurns < config.MinDecisionTurns {
		t.Fatal("recorded matches did not reach required decision turn", maxDecisionTurns, config.MinDecisionTurns)
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	percentile := func(percent int) time.Duration { return durations[(len(durations)*percent+99)/100-1] }
	t.Logf("replayed mode=%d matches=%d plans=%d orders=%d platform=%s/%s p50=%s p95=%s max=%s; excludes DB read, model load and command transport", config.Mode, len(histories), len(durations), orders, runtime.GOOS, runtime.GOARCH, percentile(50), percentile(95), durations[len(durations)-1])
	t.Logf("coverage: max_decision_turns=%d fresh_plans_after_original_observer_withdrawal=%d", maxDecisionTurns, observerWithdrawn)
}
