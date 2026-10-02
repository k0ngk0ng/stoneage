package arenaagent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T, mode int) Object {
	t.Helper()
	views := Object{}
	ids := map[string]bool{}
	for n := 0; n < mode; n++ {
		id := fmt.Sprintf("char-%d", n)
		ids[id] = true
		candidates := []Object{{"id": "attack-11", "actor": "player", "kind": "attack", "target": 11, "index": -1, "ready": true}, {"id": "attack-10", "actor": "player", "kind": "attack", "target": 10, "index": -1, "ready": true}, {"id": "friendly", "actor": "player", "kind": "attack", "target": 1, "index": -1, "ready": true}, {"id": "guard", "actor": "player", "kind": "guard", "target": n, "index": -1, "ready": true}, {"id": "pet-wait", "actor": "pet", "kind": "wait", "target": -1, "index": -1, "ready": false}, {"id": "pet-attack", "actor": "pet", "kind": "skill", "skill_id": 1, "target": 10, "index": 0, "ready": false}}
		views[fmt.Sprintf("member-%d", n)] = Object{"schema_version": 1, "character_id": id, "match_id": "match", "turn": 4, "mode": mode, "observation_id": fmt.Sprintf("obs-%d", n), "battle": Object{"Active": true, "Ended": false, "MyNoKnown": true, "MyNo": n, "PlayerSubmitted": false, "PetSubmitted": false, "Clock": Object{"RulesVersion": RulesVersion}, "Participants": []Object{{"BattleID": 0, "HP": 100, "MaxHP": 100}, {"BattleID": 10, "HP": 30, "MaxHP": 100}}}, "candidates": candidates}
	}
	team, e := teamObservation(clone(views), ids, mode)
	if e != nil {
		t.Fatal(e)
	}
	return clone(team)
}
func TestJointPlanAllModes(t *testing.T) {
	for mode := 1; mode <= 5; mode++ {
		team := fixture(t, mode)
		original := string(enc(team))
		d, e := (Basic{}).Decide(context.Background(), team, nil)
		if e != nil || len(d.Plan.Orders) != mode*2 {
			t.Fatalf("mode %d: %v %+v", mode, e, d)
		}
		for _, o := range d.Plan.Orders {
			if o.Candidate != "attack-10" && o.Candidate != "pet-attack" {
				t.Fatal(o)
			}
		}
		if string(enc(team)) != original {
			t.Fatal("strategy mutated team")
		}
		if e = validatePlan(team, d.Plan); e != nil {
			t.Fatal(e)
		}
	}
}
func TestPlanFences(t *testing.T) {
	team := fixture(t, 2)
	obj(obj(team["members"])["member-0"])["reserved_actors"] = Object{"player": "uncertain"}
	d, e := (Basic{}).Decide(context.Background(), team, nil)
	if e != nil || len(d.Plan.Orders) != 3 {
		t.Fatal(e, d)
	}
	p := d.Plan
	p.Turn++
	if validatePlan(team, p) == nil {
		t.Fatal("accepted old turn")
	}
	p = d.Plan
	p.Orders = append([]Order{}, p.Orders...)
	p.Orders[1] = p.Orders[0]
	if validatePlan(team, p) == nil {
		t.Fatal("accepted duplicate")
	}
	p = d.Plan
	p.Orders = append([]Order{}, p.Orders...)
	p.Orders[0].Candidate = "invented"
	if validatePlan(team, p) == nil {
		t.Fatal("accepted invented action")
	}
	raw := clone(Object{"schema_version": 1, "match_id": d.Plan.Match, "turn": d.Plan.Turn, "observation_id": d.Plan.Observation, "orders": d.Plan.Orders})
	raw["extra"] = true
	if _, e = parsePlan(enc(raw), team); e == nil {
		t.Fatal("accepted extra schema field")
	}
}
func TestTeamRejectsMixedObservation(t *testing.T) {
	for _, change := range []Object{{"turn": 5}, {"match_id": "other"}, {"character_id": "enemy"}} {
		team := fixture(t, 2)
		views := obj(team["members"])
		for k, v := range change {
			obj(views["member-1"])[k] = v
		}
		if _, e := teamObservation(views, map[string]bool{"char-0": true, "char-1": true}, 2); e == nil {
			t.Fatal("mixed squad accepted")
		}
	}
}
func TestStoreRecoveryAndGaps(t *testing.T) {
	dir := t.TempDir()
	s, e := OpenStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	selection := Object{"match_id": "m", "turn": 1, "candidate_id": "a"}
	if !s.Reserve("one", selection, "player") {
		t.Fatal("not reserved")
	}
	s.DB.Close()
	s, e = OpenStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	if s.Reserve("one", selection, "player") {
		t.Fatal("repeated uncertain write")
	}
	s.Finish("one", selection, "player", Object{"ok": false, "kind": "unknown"})
	if s.Intent("one", "m", 1, "player") != "uncertain" {
		t.Fatal("unknown outcome released")
	}
	s.Finish("one", selection, "player", Object{"ok": false, "data": Object{"code": "stale_observation"}})
	if !s.Reserve("one", selection, "player") {
		t.Fatal("explicit prewrite rejection not released")
	}
	s.Finish("one", selection, "player", Object{"ok": true})
	batch := clone(Object{"stream": "s", "cursor": 1, "gap": false, "observation": Object{"match_id": "m"}, "events": []Object{{"sequence": 1, "match_id": "m", "at_ms": 1, "turn": 1}}})
	s.Ingest("one", batch)
	s.Ingest("one", batch)
	var n int
	if e = s.DB.QueryRow("SELECT count(*) FROM events").Scan(&n); e != nil || n != 1 {
		t.Fatal(e, n)
	}
	batch["gap"] = true
	obj(batch["observation"])["match_id"] = "new"
	s.Ingest("one", batch)
	if e = s.DB.QueryRow("SELECT count(*) FROM records WHERE kind='event_gap'").Scan(&n); e != nil || n != 2 {
		t.Fatal(e, n)
	}
	if e = s.Pin("m", "basic-v1"); e != nil {
		t.Fatal(e)
	}
	if s.Pin("m", "other") == nil {
		t.Fatal("policy not pinned")
	}
	s.DB.Close()
	if s.Reserve("one", Object{"match_id": "x", "turn": 2}, "player") || s.Err() == nil {
		t.Fatal("persistence failure allowed mutation")
	}
}
func TestOSLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner.lock")
	a, e := claim(path)
	if e != nil {
		t.Fatal(e)
	}
	if b, e := claim(path); e == nil {
		b.Close()
		t.Fatal("duplicate owner")
	}
	a.Close()
	b, e := claim(path)
	if e != nil {
		t.Fatal(e)
	}
	b.Close()
}
func trainingFixture(t *testing.T) (string, Object) {
	t.Helper()
	s, e := OpenStore(filepath.Join(t.TempDir(), "data"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	var team Object
	for n := 0; n < 12; n++ {
		team = fixture(t, 2)
		team["match_id"] = fmt.Sprintf("match-%d", n)
		d, e := (Basic{}).Decide(context.Background(), team, nil)
		if e != nil {
			t.Fatal(e)
		}
		s.Record("turn", Object{"team": team, "plan": d.Plan}, str(team["match_id"]), "")
		for _, o := range d.Plan.Orders {
			selection := Object{"match_id": team["match_id"], "turn": team["turn"], "candidate_id": o.Candidate}
			s.Reserve(o.Member, selection, o.Actor)
			s.Finish(o.Member, selection, o.Actor, Object{"ok": true})
		}
		s.Result(Object{"id": team["match_id"], "winner_side": n % 2, "reason": "defeat", "members": []Object{{"id": fmt.Sprintf("roster-%d", n/2)}}})
	}
	if e = s.Err(); e != nil {
		t.Fatal(e)
	}
	return filepath.Join(s.Directory, "arena.sqlite3"), team
}
func TestTrainingSharedFeaturesAndCoverage(t *testing.T) {
	db, team := trainingFixture(t)
	path := filepath.Join(t.TempDir(), "model.json")
	result, e := Train([]string{db}, path, 7, 20)
	if e != nil {
		t.Fatal(e)
	}
	if obj(result["training"])["held_out"] == nil {
		t.Fatal("missing held out")
	}
	l, e := NewLearned(path, 2)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	d, e := l.Decide(ctx, team, nil)
	if e != nil || validatePlan(team, d.Plan) != nil {
		t.Fatal(e)
	}
	if _, e = NewLearned(path, 1); e == nil {
		t.Fatal("untrained mode accepted")
	}
	team["rules_version"] = "new"
	if _, e = l.Decide(ctx, team, nil); e == nil {
		t.Fatal("new rules accepted")
	}
	if _, e = Train([]string{db}, path, 1, 5); e == nil {
		t.Fatal("overwrote model")
	}
	if _, e = Evaluate([]string{db}, path); e != nil {
		t.Fatal(e)
	}
	rows, _, e := readExamples([]string{db})
	if e != nil || len(rows) != 12 {
		t.Fatal(e, len(rows))
	}
}
func TestFeaturesExcludePrivateAndFuture(t *testing.T) {
	team := fixture(t, 2)
	d, _ := (Basic{}).Decide(context.Background(), team, nil)
	choices := map[Slot]string{}
	for _, o := range d.Plan.Orders {
		choices[Slot{o.Member, o.Actor}] = o.Candidate
	}
	first := features(team, choices)
	team["opponent_private_attack"] = 99999
	team["future_result"] = Object{"winner_side": 1}
	if !reflect.DeepEqual(first, features(team, choices)) {
		t.Fatal("private/future data leaked")
	}
	choices[Slot{"member-1", "player"}] = "attack-11"
	if features(team, choices)["focus:enemy"] >= first["focus:enemy"] {
		t.Fatal("no joint focus feature")
	}
}
func TestChatCompletionsAndDeadline(t *testing.T) {
	team := fixture(t, 1)
	d, _ := (Basic{}).Decide(context.Background(), team, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(time.Second):
			}
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("missing key")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(enc(Object{"choices": []Object{{"finish_reason": "stop", "message": Object{"content": string(enc(d.Plan))}}}}))
	}))
	defer srv.Close()
	t.Setenv("ARENA_TEST_KEY", "test-key")
	l, e := NewLLM(Object{"endpoint": srv.URL, "model": "fixture", "api_key_env": "ARENA_TEST_KEY"})
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	v, e := l.Decide(ctx, team, nil)
	if e != nil || validatePlan(team, v.Plan) != nil {
		t.Fatal(e)
	}
	l.endpoint = srv.URL + "/slow"
	short, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	start := time.Now()
	if _, e = l.Decide(short, team, nil); e == nil || time.Since(start) > 500*time.Millisecond {
		t.Fatal("HTTP deadline not enforced", e)
	}
	l.budget = 16000
	history := []Object{}
	for i := 0; i < 20; i++ {
		history = append(history, Object{"turn": i, "raw": strings.Repeat("x", 10000), "summary": "old"})
	}
	if _, e := l.payload(team, history, nil); e == nil {
		t.Fatal("oversized full history was silently shortened")
	}
}
func TestPluginHelper(t *testing.T) {
	if os.Args[len(os.Args)-1] != "arena-block" {
		return
	}
	select {
	case <-time.After(30 * time.Second):
	}
	os.Exit(0)
}
func TestPluginCancellation(t *testing.T) {
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}

	p := Plugin{clone(Object{"id": "custom", "version": "1", "modes": []int{1}, "command": []string{exe, "-test.run=TestPluginHelper", "--", "arena-block"}})}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, e = p.Decide(ctx, fixture(t, 1), nil)
	if e == nil {
		t.Fatal("invalid helper output accepted")
	}
}

func TestMain(m *testing.M) {
	if os.Getenv("ARENA_FAKE_SACTL") == "1" {
		fmt.Println(`{"ok":true}`)
		os.Exit(0)
	}
	os.Exit(m.Run())
}
func TestIdentityLockAcrossSockets(t *testing.T) {
	t.Setenv("ARENA_FAKE_SACTL", "1")
	dir := t.TempDir()
	store, e := OpenStore(filepath.Join(dir, "state"))
	if e != nil {
		t.Fatal(e)
	}
	defer store.DB.Close()
	exe, _ := os.Executable()
	members := []*member{}
	for i := 0; i < 2; i++ {
		socket := filepath.Join(dir, fmt.Sprintf("%d.sock", i))
		config := filepath.Join(dir, fmt.Sprintf("%d.toml", i))
		raw := fmt.Sprintf("socket_path = %q\ntransport = \"http\"\nweb_base_url = \"https://game.example\"\naccount = \"fixture\"\ncharacter = \"fixture\"\n", socket)
		if e = os.WriteFile(config, []byte(raw), 0600); e != nil {
			t.Fatal(e)
		}
		members = append(members, &member{cfg: MemberConfig{ID: fmt.Sprint(i), Config: config, Socket: socket}, binary: exe, ownership: filepath.Join(dir, "ownership"), store: store})
	}
	defer members[0].close()
	defer members[1].close()
	if e = members[0].start(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e = members[1].start(context.Background()); e == nil {
		t.Fatal("same identity acquired two sockets")
	}
}
func TestHybridFallbackAndProviderRedirect(t *testing.T) {
	db, team := trainingFixture(t)
	model := filepath.Join(t.TempDir(), "model.json")
	if _, e := Train([]string{db}, model, 1, 5); e != nil {
		t.Fatal(e)
	}
	learned, e := NewLearned(model, 2)
	if e != nil {
		t.Fatal(e)
	}
	leaked := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	llm, e := NewLLM(Object{"endpoint": srv.URL, "model": "fixture"})
	if e != nil {
		t.Fatal(e)
	}
	h := Hybrid{learned, llm}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	d, e := h.Decide(ctx, team, nil)
	if e != nil || str(d.Diagnostics["fallback"]) != "learned" || leaked {
		t.Fatal(e, d.Diagnostics, leaked)
	}
}
func TestInitProtectsExistingConfiguration(t *testing.T) {
	base, err := os.MkdirTemp("", "arena-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	dir := filepath.Join(base, "team")
	v, e := Init(dir, 2)
	if e != nil {
		t.Fatal(e)
	}
	c, e := LoadConfig(str(v["config"]))
	if e != nil || len(c.Members) != 2 || c.Strategy != "basic" {
		t.Fatal(e, c)
	}
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	for _, configured := range []string{"", "sactl", filepath.Join(dir, "custom-sactl")} {
		c.Sactl = configured
		path := filepath.Join(dir, "migration.json")
		if e = os.WriteFile(path, enc(c), 0600); e != nil {
			t.Fatal(e)
		}
		loaded, e := LoadConfig(path)
		if e != nil {
			t.Fatal(e)
		}
		want := configured
		if configured == "" || configured == "sactl" {
			want = exe
		}
		if loaded.Sactl != want {
			t.Fatalf("configured executable %q resolved to %q; want %q", configured, loaded.Sactl, want)
		}
	}
	if _, e = Init(dir, 1); e == nil {
		t.Fatal("overwrote configuration")
	}
	for _, m := range c.Members {
		info, e := os.Stat(m.Config)
		if e != nil {
			t.Fatal(e)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatal("nonprivate config")
		}
	}
}
