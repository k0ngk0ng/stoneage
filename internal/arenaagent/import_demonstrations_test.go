package arenaagent

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/battlenet"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
	"github.com/k0ngk0ng/stoneage/internal/battletrain"
)

func recordedFixture(t *testing.T, mutate func(*sql.DB)) string {
	t.Helper()
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Pin("match", "basic:basic-v1"); err != nil {
		t.Fatal(err)
	}
	for turn := 0; turn < 3; turn++ {
		team, events := neuralFixtureTeam(t, 2, turn)
		for _, event := range events {
			store.Ingest("member-0", clone(Object{"stream": "stream-0", "cursor": obj(event["event"])["sequence"], "events": []Object{obj(event["event"])}}))
		}
		decision, err := (Basic{}).Decide(context.Background(), team, nil)
		if err != nil {
			t.Fatal(err)
		}
		store.Record("turn", neuralTurnRecord(team, decision), "match", "")
		store.Record("turn", neuralTurnRecord(team, decision), "match", "") // repeated polling does not double training weight
		for _, order := range decision.Plan.Orders {
			view := obj(obj(team["members"])[order.Member])
			selection := clone(Object{"match_id": "match", "turn": turn, "observation_id": view["observation_id"], "candidate_id": order.Candidate})
			if !store.Reserve(order.Member, selection, order.Actor) {
				t.Fatal(store.Err())
			}
			store.Finish(order.Member, selection, order.Actor, Object{"ok": true})
		}
	}
	store.Result(Object{"id": "match", "mode": 2, "turns": 3, "winner_side": 1, "reason": "defeat", "members": []Object{{"id": "char-0"}, {"id": "char-1"}, {"id": "enemy-0"}, {"id": "enemy-1"}}})
	if store.Err() != nil {
		t.Fatal(store.Err())
	}
	if mutate != nil {
		mutate(store.DB)
	}
	if _, err := store.DB.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	if err := store.DB.Close(); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "arena.sqlite3")
}

func TestImportDemonstrationsReconstructsConfirmedWholePlans(t *testing.T) {
	database := recordedFixture(t, nil)
	before, _ := recordedDatabaseHash(database)
	output := filepath.Join(t.TempDir(), "data.jsonl")
	report, err := ImportDemonstrations(context.Background(), []string{database}, output, battlepolicy.FeatureVersion)
	if err != nil || report.Imported != 1 || len(report.Excluded) != 0 || report.Dataset.Turns != 3 || report.Dataset.Actions != 12 {
		t.Fatalf("%+v %v", report, err)
	}
	after, _ := recordedDatabaseHash(database)
	if before != after {
		t.Fatal("source changed")
	}
	ds, _, err := battletrain.LoadDemonstrations(context.Background(), output)
	if err != nil {
		t.Fatal(err)
	}
	if ds[0].Steps[0].InitialCursor != 49 || ds[0].Winner != 1 || ds[0].Source != before {
		t.Fatal("lost initial boundary, losing result or provenance")
	}
	for _, step := range ds[0].Steps {
		for _, event := range step.History.Events {
			if event.Sequence > step.History.Cursor {
				t.Fatal("future event included")
			}
		}
	}
	var out bytes.Buffer
	if err := Main(context.Background(), []string{"import-demonstrations", "--database", database, "--output", filepath.Join(t.TempDir(), "cli.jsonl")}, "test", &out); err != nil || !bytes.Contains(out.Bytes(), []byte(`"imported_matches":1`)) {
		t.Fatalf("%s %v", out.String(), err)
	}
}

func TestImportDemonstrationsRejectsIncompleteSource(t *testing.T) {
	for name, statement := range map[string]string{
		"uncertain_submission":                "UPDATE battle_intents SET status='uncertain' WHERE turn=1",
		"unreconstructable_or_unwritten_plan": "DELETE FROM battle_intents WHERE turn=1 AND actor='pet'",
		"missing_decision_turn":               "DELETE FROM records WHERE kind='turn' AND json_extract(body,'$.team.turn')=1",
		"unsettled_match":                     "DELETE FROM results",
		"missing_pinned_policy":               "DELETE FROM match_policies",
		"noncombat_or_invalid_result":         "UPDATE results SET body=json_set(body,'$.winner_side','unknown')",
		"gap_or_fallback":                     "INSERT INTO records(at_ms,kind,match_id,member,body) VALUES(1,'event_gap','match','member-0','{}')",
	} {
		t.Run(name, func(t *testing.T) {
			db := recordedFixture(t, func(db *sql.DB) {
				if _, err := db.Exec(statement); err != nil {
					t.Fatal(err)
				}
			})
			output := filepath.Join(t.TempDir(), "never.jsonl")
			r, err := ImportDemonstrations(context.Background(), []string{db}, output, battlepolicy.FeatureVersion)
			if err == nil || r.Excluded[name] != 1 || r.Imported != 0 {
				t.Fatalf("%+v %v", r, err)
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatal("published incomplete source")
			}
		})
	}
}

func TestImportDemonstrationsRejectsWALAndDuplicateSources(t *testing.T) {
	db := recordedFixture(t, nil)
	output := filepath.Join(t.TempDir(), "data.jsonl")
	if err := os.WriteFile(db+"-wal", []byte("pending"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportDemonstrations(context.Background(), []string{db}, output, battlepolicy.FeatureVersion); err == nil || !strings.Contains(err.Error(), "checkpoint") {
		t.Fatal(err)
	}
	if err := os.Remove(db + "-wal"); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportDemonstrations(context.Background(), []string{db, db}, output, battlepolicy.FeatureVersion); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatal(err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("duplicate published")
	}
}

func TestNativeDemonstrationsTrainAndRestore(t *testing.T) {
	database := os.Getenv("STONEAGE_DEMONSTRATION_DATABASE")
	if database == "" {
		t.Skip("stopped native commander database required")
	}
	output := filepath.Join(t.TempDir(), "recorded.jsonl")
	r, err := ImportDemonstrations(context.Background(), []string{database}, output, battlepolicy.FeatureVersion)
	if err != nil || r.Imported == 0 || len(r.Excluded) != 0 {
		t.Fatalf("%+v %v", r, err)
	}
	ds, manifest, err := battletrain.LoadDemonstrations(context.Background(), output)
	if err != nil {
		t.Fatal(err)
	}
	config := battlepolicy.NetworkConfig()
	a, err := battlenet.NewModel[float32](config, 901)
	if err != nil {
		t.Fatal(err)
	}
	b, err := battlenet.NewModel[float32](config, 901)
	if err != nil {
		t.Fatal(err)
	}
	oa, ob := &battlenet.Adam[float32]{}, &battlenet.Adam[float32]{}
	update := battletrain.DefaultWarmupConfig().Update
	update.BatchEpisodes = 1
	for epoch := 0; epoch < 2; epoch++ {
		r, err := battletrain.ImitateDemonstrations(context.Background(), a, oa, ds, update)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := battletrain.ImitateDemonstrations(context.Background(), b, ob, ds, update); err != nil {
			t.Fatal(err)
		}
		var restored battletrain.LearningState
		if err := decode(enc(battletrain.LearningState{Schema: 1, Model: b, Optimizer: ob}), &restored); err != nil {
			t.Fatal(err)
		}
		b, ob = restored.Model, restored.Optimizer
		if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(oa, ob) || r.BeforePolicy == r.AfterPolicy {
			t.Fatal("recorded learning/restoration differs")
		}
		t.Logf("native epoch=%d parameters=%d report=%s", epoch+1, a.Count(), enc(r))
	}
	id, _ := battletrain.Digest(battletrain.LearningState{Schema: 1, Model: a, Optimizer: oa})
	t.Logf("source dataset=%s, restored learning_state=%s; no model-strength claim", enc(manifest), id)
}
