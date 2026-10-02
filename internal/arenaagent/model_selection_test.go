package arenaagent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
	"github.com/k0ngk0ng/stoneage/internal/battletrain"
)

// Only the registry's evidence-verifier result is substituted in positive
// transition tests. These are NOT native strength or arena certificates.
func selectionFixture(t *testing.T) (Config, battletrain.ChampionStatus, battletrain.ChampionStatus) {
	t.Helper()
	l, path := neuralFixtureModel(t, 1)
	root := t.TempDir()
	c := Config{Schema: 2, Mode: 1, Strategy: "learned", StateDir: filepath.Join(root, "state"), ChampionDirectory: filepath.Join(root, "champions"), Members: []MemberConfig{{ID: "one", Config: filepath.Join(root, "view"), Socket: filepath.Join(root, "socket")}}}
	x, err := battletrain.NewExperiment(context.Background(), l.neural.Environment, battletrain.DefaultEvaluationConfig(), [3]int{1, 1, 20})
	if err != nil {
		t.Fatal(err)
	}
	if err = battletrain.InitChampionRegistry(c.ChampionDirectory, x, battletrain.DefaultPromotionGate()); err != nil {
		t.Fatal(err)
	}
	a, err := battletrain.LoadChampionRegistry(context.Background(), c.ChampionDirectory)
	if err != nil {
		t.Fatal(err)
	}
	a.Champion, a.Model, a.Head = hash(l.neural), path, strings.Repeat("1", 64)
	b := a
	other, err := battlepolicy.LoadArtifact(path)
	if err != nil {
		t.Fatal(err)
	}
	other.Network.Parameters["score.1.w"].Values[0] += 1
	other.WeightsDigest, err = battlepolicy.NetworkDigest(other.Network)
	if err != nil {
		t.Fatal(err)
	}
	b.Model, b.Head, b.Champion = filepath.Join(root, "second-model.json"), strings.Repeat("2", 64), hash(other)
	if err = os.WriteFile(b.Model, enc(other), 0600); err != nil {
		t.Fatal(err)
	}
	return c, a, b
}

func fixedChampion(s battletrain.ChampionStatus) championLoader {
	return func(context.Context, string) (battletrain.ChampionStatus, error) { return s, nil }
}

func selectionClient(t *testing.T, c *Config, status battletrain.ChampionStatus) {
	t.Helper()
	c.Sactl = filepath.Join(filepath.Dir(c.StateDir), "fixture-client")
	// Use the member config path only as a fixture file prefix. No live daemon
	// or game endpoint is involved. Every external command is recorded.
	script := `#!/bin/sh
printf '%s %s\n' "$6" "$7" >> "$2.calls"
case "$6" in
 query) printf '{"ok":true,"data":{}}\n' ;;
 battle-state) cat "$2" ;;
 arena)
  case "$7" in
   status) printf '{"ok":true,"data":{"revision":1}}\n' ;;
   queue) cat "$2.queue" ;;
   *) exit 2 ;;
  esac ;;
 *) exit 3 ;;
esac
`
	if err := os.WriteFile(c.Sactl, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	view := Object{"schema_version": 1, "battle": Object{"Clock": Object{"RulesVersion": RulesVersion, "RulesDigest": status.Registry.Environment.Rules, "EnginePlatform": status.Registry.Environment.Platform}}}
	for _, m := range c.Members {
		if err := os.WriteFile(m.Config, enc(Object{"ok": true, "data": view}), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(m.Config+".queue", enc(Object{"ok": true, "data": Object{}}), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func selectionRunner(t *testing.T, c Config, a battletrain.ChampionStatus) *Runner {
	t.Helper()
	s, selected, err := selectChampion(context.Background(), c, fixedChampion(a))
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(c.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{config: c, store: store, strategy: s, selection: selected, ids: map[string]string{"one": "char-0"}, Output: &bytes.Buffer{}, loadChampion: fixedChampion(a)}
	for _, m := range c.Members {
		r.members = append(r.members, &member{cfg: m, binary: c.Sactl, store: store})
	}
	return r
}

func selectionStatuses(phase, match string) map[string]Object {
	s := Object{"phase": phase, "self": Object{"id": "char-0", "strategy": "manual", "ready": true},
		"room": Object{"id": "room", "mode": 1, "leader_id": "char-0", "members": []Object{{"id": "char-0"}}}}
	if match != "" {
		s["match"] = Object{"id": match, "mode": 1, "side": 0, "teams": []Object{{"members": []Object{{"id": "char-0"}}}, {"members": []Object{{"id": "opponent"}}}}}
	}
	return map[string]Object{"one": clone(s)}
}

func TestChampionSelectionQueueMatchRestartAndRollback(t *testing.T) {
	ctx := context.Background()
	c, a, b := selectionFixture(t)
	selectionClient(t, &c, a)
	r := selectionRunner(t, c, a)
	if err := r.pinMatch("unsaved"); err == nil {
		t.Fatal("attached new champion to an unrecorded active match")
	}
	if err := r.prepare(ctx, selectionStatuses("room", "")); err != nil {
		t.Fatal(err)
	}
	firstVersion := r.strategy.Version()
	if !r.selectionSaved || r.selection.Artifact != a.Champion {
		t.Fatal("queue did not persist selected model")
	}
	r.loadChampion = func(context.Context, string) (battletrain.ChampionStatus, error) {
		t.Fatal("read registry while queued or in a match")
		return b, nil
	}
	for _, phase := range []string{"queued", "countdown", "battle", "result"} {
		if err := r.prepare(ctx, selectionStatuses(phase, "")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.validateMatch(selectionStatuses("battle", "first")["one"]); err != nil {
		t.Fatal(err)
	}
	// Losing the source model/registry cannot change an already queued match.
	bytesA, err := os.ReadFile(a.Model)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(a.Model); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(c.ChampionDirectory, c.ChampionDirectory+".offline"); err != nil {
		t.Fatal(err)
	}
	r.Close()
	r, err = newRunner(ctx, c)
	if err != nil {
		t.Fatal("could not recover cached model without source", err)
	}
	defer r.Close()
	r.ids["one"] = "char-0"
	if r.strategy.Version() != firstVersion || r.selection.Artifact != a.Champion {
		t.Fatal("restart selected different model")
	}
	if _, err = r.validateMatch(selectionStatuses("battle", "first")["one"]); err != nil {
		t.Fatal(err)
	}
	r.loadChampion = fixedChampion(b)
	if err = r.prepare(ctx, selectionStatuses("room", "")); err != nil {
		t.Fatal(err)
	}
	if r.strategy.Version() == firstVersion || r.selection.Artifact != b.Champion {
		t.Fatal("next queue did not select promoted model")
	}
	if err = r.pinMatch("second"); err != nil {
		t.Fatal(err)
	}
	if err = r.pinMatch("first"); err == nil {
		t.Fatal("changed an already pinned match")
	}
	if err = os.WriteFile(a.Model, bytesA, 0600); err != nil {
		t.Fatal(err)
	}
	a.Head = strings.Repeat("3", 64) // Verified rollback selects the earlier A.
	r.loadChampion = fixedChampion(a)
	if err = r.prepare(ctx, selectionStatuses("room", "")); err != nil {
		t.Fatal(err)
	}
	if r.strategy.Version() != firstVersion || r.selection.Head != a.Head {
		t.Fatal("rollback not applied to new queue")
	}
	if err = r.pinMatch("third"); err != nil {
		t.Fatal(err)
	}
	var models, pinned int
	if err = r.store.DB.QueryRow("SELECT count(*) FROM commander_models").Scan(&models); err != nil {
		t.Fatal(err)
	}
	if err = r.store.DB.QueryRow("SELECT count(*) FROM records WHERE kind='match_model'").Scan(&pinned); err != nil {
		t.Fatal(err)
	}
	if models != 2 || pinned != 3 {
		t.Fatal("model copies or per-match evidence not preserved", models, pinned)
	}
}

func TestChampionSelectionKeepsUncertainQueueAndRejectsUnavailableSource(t *testing.T) {
	ctx := context.Background()
	c, a, _ := selectionFixture(t)
	selectionClient(t, &c, a)
	r := selectionRunner(t, c, a)
	defer r.Close()
	if err := r.prepare(ctx, selectionStatuses("room", "")); err != nil {
		t.Fatal(err)
	}
	old := r.strategy.Version()
	r.store.SetPending("one", Object{"operation": "queue", "args": nil, "request_id": "prior-queue", "revision": 1})
	reads := 0
	r.loadChampion = func(context.Context, string) (battletrain.ChampionStatus, error) {
		reads++
		return battletrain.ChampionStatus{}, errors.New("source is unavailable")
	}
	if err := r.prepare(ctx, selectionStatuses("room", "")); err != nil || reads != 0 || r.strategy.Version() != old || r.store.Pending("one") != nil {
		t.Fatal("uncertain request did not retain/reconcile original model", err, reads)
	}
	before, _ := os.ReadFile(c.Members[0].Config + ".calls")
	if err := r.prepare(ctx, selectionStatuses("room", "")); err == nil || reads != 1 {
		t.Fatal("silently queued with cached/basic model when next selection failed", err)
	}
	after, _ := os.ReadFile(c.Members[0].Config + ".calls")
	if !bytes.Equal(before, after) || r.strategy.Version() != old {
		t.Fatal("failed selection made a game request or replaced the model")
	}
}

func TestChampionSelectionRejectsWrongServerAndDamagedCache(t *testing.T) {
	ctx := context.Background()
	c, a, b := selectionFixture(t)
	selectionClient(t, &c, a)
	r := selectionRunner(t, c, a)
	if err := r.prepare(ctx, selectionStatuses("room", "")); err != nil {
		t.Fatal(err)
	}
	old := r.strategy.Version()
	r.loadChampion = fixedChampion(b)
	view := Object{"schema_version": 1, "battle": Object{"Clock": Object{"RulesVersion": RulesVersion, "RulesDigest": strings.Repeat("f", 64), "EnginePlatform": a.Registry.Environment.Platform}}}
	if err := os.WriteFile(c.Members[0].Config, enc(Object{"ok": true, "data": view}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.prepare(ctx, selectionStatuses("room", "")); err == nil || r.strategy.Version() != old {
		t.Fatal("incompatible server accepted/changed model", err)
	}
	calls, _ := os.ReadFile(c.Members[0].Config + ".calls")
	if strings.Count(string(calls), "arena queue\n") != 1 {
		t.Fatal("queued on mismatched server")
	}
	if _, err := r.store.DB.Exec("UPDATE commander_models SET body='{}'"); err != nil {
		t.Fatal(err)
	}
	r.Close()
	if restored, err := newRunner(ctx, c); err == nil {
		restored.Close()
		t.Fatal("corrupt cache silently replaced with new champion")
	}
}

func TestChampionConfigAndRealVerifierBoundaries(t *testing.T) {
	c, a, _ := selectionFixture(t)
	path := filepath.Join(filepath.Dir(c.StateDir), "team.json")
	for _, strategy := range []string{"learned", "hybrid"} {
		c.Strategy = strategy
		if err := os.WriteFile(path, enc(c), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err != nil {
			t.Fatal(err)
		}
		if _, err := makeStrategy(c); err == nil {
			t.Fatal("registry bypassed evidence verification")
		}
	}
	c.Strategy = "learned"
	for _, change := range []func(*Config){func(v *Config) { v.Schema = 1 }, func(v *Config) { v.Model = a.Model }, func(v *Config) { v.Strategy = "basic" }} {
		bad := c
		change(&bad)
		if err := os.WriteFile(path, enc(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err == nil {
			t.Fatal("ambiguous or old config accepted", bad)
		}
	}
	if _, _, err := configuredStrategy(context.Background(), c); err == nil {
		t.Fatal("real empty registry treated as approved champion")
	}
	// An arbitrary pointer to a fixture model is not a verified promotion.
	if err := os.WriteFile(filepath.Join(c.ChampionDirectory, "champion.json"), enc(Object{"event": a.Head}), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := configuredStrategy(context.Background(), c); err == nil {
		t.Fatal("forged/missing event accepted by real verifier")
	}
	bad := a
	bad.Champion = strings.Repeat("f", 64)
	if _, _, err := selectChampion(context.Background(), c, fixedChampion(bad)); err == nil {
		t.Fatal("changed artifact accepted after verification")
	}
	c.Mode = 2
	if _, _, err := selectChampion(context.Background(), c, fixedChampion(a)); err == nil {
		t.Fatal("wrong registry mode accepted")
	}
}

func TestHybridChampionRecoveryPinsBothModels(t *testing.T) {
	c, a, _ := selectionFixture(t)
	c.Strategy = "hybrid"
	c.LLM = Object{"endpoint": "http://127.0.0.1:8000/v1/chat/completions", "model": "fixture-model"}
	r := selectionRunner(t, c, a)
	if err := r.store.saveSelection(r.selection, r.strategy); err != nil {
		t.Fatal(err)
	}
	version := r.strategy.Version()
	r.Close()
	r, err := newRunner(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if r.strategy.ID() != "hybrid" || r.strategy.Version() != version {
		t.Fatal("hybrid recovery changed either model")
	}
	r.Close()
	fixed := c
	fixed.ChampionDirectory, fixed.Model = "", a.Model
	if r, err := newRunner(context.Background(), fixed); err == nil {
		r.Close()
		t.Fatal("fixed-model config bypassed persisted queue selection")
	}
	c.LLM["model"] = "different-model"
	if r, err := newRunner(context.Background(), c); err == nil {
		r.Close()
		t.Fatal("changed LLM configuration during recovery")
	}
}

func TestChampionSelectionPersistenceFailureStopsQueue(t *testing.T) {
	c, a, _ := selectionFixture(t)
	selectionClient(t, &c, a)
	r := selectionRunner(t, c, a)
	defer r.Close()
	// A real SQLite failure must roll back the model and pointer together,
	// then latch the error before any queue command can run.
	if _, err := r.store.DB.Exec(`CREATE TRIGGER fail_selection BEFORE INSERT ON commander_selection BEGIN SELECT RAISE(ABORT,'fixture disk failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := r.prepare(context.Background(), selectionStatuses("room", "")); err == nil || r.selectionSaved {
		t.Fatal("queued despite failed persistence", err)
	}
	var models int
	if err := r.store.DB.QueryRow("SELECT count(*) FROM commander_models").Scan(&models); err != nil || models != 0 {
		t.Fatal("failed pointer commit left a model committed", models, err)
	}
	calls, _ := os.ReadFile(c.Members[0].Config + ".calls")
	if strings.Contains(string(calls), "arena queue") {
		t.Fatal("queue request escaped failed persistence")
	}
	if r.store.Err() == nil {
		t.Fatal("persistence failure was not latched")
	}
}
