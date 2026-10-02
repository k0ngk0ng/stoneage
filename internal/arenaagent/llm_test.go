package arenaagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func commanderProvider(t *testing.T, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request, body Object
		if json.NewDecoder(r.Body).Decode(&request) != nil || len(objects(request["messages"])) != 2 || decode([]byte(str(objects(request["messages"])[1]["content"])), &body) != nil {
			t.Error("invalid provider request")
			w.WriteHeader(400)
			return
		}
		decision, err := (Basic{}).Decide(r.Context(), obj(body["team"]), nil)
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		_, _ = w.Write(enc(Object{"choices": []Object{{"finish_reason": "stop", "message": Object{"role": "assistant", "content": string(enc(decision.Plan))}}}}))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestLLMCompleteContextExactEnvelopeLimit(t *testing.T) {
	team, _ := neuralFixtureTeam(t, 5, 42)
	team["history_record_cutoff"] = 100
	var history []Object
	for turn := 0; turn < 42; turn++ {
		past, _ := neuralFixtureTeam(t, 5, turn)
		obj(obj(past["members"])["member-0"])["public_description"] = strings.Repeat("战斗记录\n\"", 3000)
		decision, err := (Basic{}).Decide(context.Background(), past, nil)
		if err != nil {
			t.Fatal(err)
		}
		record := neuralTurnRecord(past, decision)
		record["record_id"] = turn + 1
		record["diagnostics"] = Object{"provider_prompt": "DO_NOT_RECURSIVELY_INCLUDE_PROMPTS", "estimated_return": .2}
		history = append(history, record)
	}
	before := hash(Object{"team": team, "history": history})
	l, err := NewLLM(Object{"endpoint": "http://localhost/v1/chat/completions", "model": "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := l.payload(team, history, nil)
	if err != nil {
		t.Fatal(err)
	}
	var body Object
	if err = decode([]byte(str(payload["messages"].([]Object)[1]["content"])), &body); err != nil {
		t.Fatal(err)
	}
	retained := objects(body["history"])
	if len(retained) != 42 || integer(obj(retained[0]["team"])["turn"]) != 0 || integer(obj(retained[41]["team"])["turn"]) != 41 || strings.Contains(string(enc(payload)), "DO_NOT_RECURSIVELY_INCLUDE_PROMPTS") {
		t.Fatal("lost battle history or recursive provider context")
	}
	size := len(enc(payload))
	l.budget = size
	if _, err = l.payload(team, history, nil); err != nil {
		t.Fatal("exact envelope should fit", err)
	}
	l.budget = size - 1
	var failure *llmFailure
	if _, err = l.payload(team, history, nil); !errors.As(err, &failure) || failure.code != "context_limit" {
		t.Fatal("envelope overhead not bounded", err)
	}
	if before != hash(Object{"team": team, "history": history}) {
		t.Fatal("context builder mutated evidence")
	}
}

func TestLLMHistoryUsesRecordAndEachObserverBoundary(t *testing.T) {
	team, _ := neuralFixtureTeam(t, 2, 1)
	team["history_record_cutoff"] = 3
	obj(obj(obj(team["members"])["member-1"])["event_cutoff"])["cursor"] = 51
	history := []Object{}
	for member := 0; member < 2; member++ {
		for sequence := 50; sequence <= 54; sequence++ {
			history = append(history, Object{"member": fmt.Sprintf("member-%d", member), "stream": fmt.Sprintf("stream-%d", member), "event": Object{"sequence": sequence, "match_id": "match", "at_ms": 1000 - sequence}})
		}
	}
	old, _ := neuralFixtureTeam(t, 2, 0)
	d, _ := (Basic{}).Decide(context.Background(), old, nil)
	record := neuralTurnRecord(old, d)
	record["record_id"] = 1
	history = append(history, record)
	future := clone(record)
	future["record_id"] = 4
	future["future_secret"] = "hidden"
	history = append(history, future)
	history = append(history, Object{"record_id": 2, "member": "member-0", "event_gap": Object{"stream": "stream-0", "cursor": 53, "gap": true}})
	history = append(history, Object{"record_id": 3, "member": "member-0", "event_gap": Object{"stream": "stream-0", "cursor": 54, "gap": true}})
	out, err := llmHistory(team, history)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	gaps := 0
	for _, r := range out {
		if obj(r["event"]) != nil {
			counts[str(r["member"])]++
		}
		if obj(r["event_gap"]) != nil {
			gaps++
		}
		if r["future_secret"] != nil {
			t.Fatal("future record leaked")
		}
	}
	if len(out) != 8 || counts["member-0"] != 4 || counts["member-1"] != 2 || gaps != 1 {
		t.Fatal("wrong independent boundaries", len(out), counts, gaps)
	}
}

func TestLLMRejectsAmbiguousProviderResponses(t *testing.T) {
	team := fixture(t, 1)
	d, _ := (Basic{}).Decide(context.Background(), team, nil)
	valid := Object{"choices": []Object{{"finish_reason": "stop", "message": Object{"content": string(enc(d.Plan))}}}}
	for _, kind := range []string{"two-choices", "missing-finish", "length", "refusal", "refusal-object", "duplicate-envelope", "duplicate-plan", "escaped-duplicate", "invalid-plan", "oversize", "http-secret"} {
		t.Run(kind, func(t *testing.T) {
			body := clone(valid)
			choice := objects(body["choices"])[0]
			message := obj(choice["message"])
			raw := []byte(nil)
			switch kind {
			case "two-choices":
				body["choices"] = []Object{choice, choice}
			case "missing-finish":
				delete(choice, "finish_reason")
			case "length":
				choice["finish_reason"] = "length"
			case "refusal":
				message["refusal"] = "no"
			case "refusal-object":
				message["refusal"] = Object{"reason": "no"}
			case "duplicate-envelope":
				raw = []byte(`{"choices":[],"choices":` + string(enc(body["choices"])) + `}`)
			case "duplicate-plan":
				message["content"] = strings.Replace(string(enc(d.Plan)), `"turn":4`, `"turn":4,"turn":4`, 1)
			case "escaped-duplicate":
				message["content"] = strings.Replace(string(enc(d.Plan)), `"turn":4`, `"turn":4,"\u0074urn":4`, 1)
			case "invalid-plan":
				message["content"] = `{"orders":[]}`
			case "oversize":
				raw = []byte(strings.Repeat("x", (1<<20)+1))
			}
			if raw == nil {
				raw = enc(body)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if kind == "http-secret" {
					w.WriteHeader(500)
					_, _ = w.Write([]byte("SECRET_PROVIDER_RESPONSE"))
					return
				}
				_, _ = w.Write(raw)
			}))
			defer server.Close()
			l, e := NewLLM(Object{"endpoint": server.URL + "?key=SECRET_PROVIDER_URL", "model": "test"})
			if e != nil {
				t.Fatal(e)
			}
			_, e = l.Decide(context.Background(), team, nil)
			var failure *llmFailure
			if !errors.As(e, &failure) || strings.Contains(e.Error(), "SECRET") {
				t.Fatal("unsafe/accepted response", e)
			}
		})
	}
}

func TestLLMBodyDeadlinePreservesCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	l, e := NewLLM(Object{"endpoint": server.URL, "model": "fixture"})
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, e = l.Decide(ctx, fixture(t, 1), nil)
	if !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal("lost body cancellation", e)
	}
}

func TestLLMAndHybridAllModesHistoryRestartAndPartialPlan(t *testing.T) {
	for mode := 1; mode <= 5; mode++ {
		for _, kind := range []string{"llm", "hybrid"} {
			t.Run(fmt.Sprintf("%s/%d", kind, mode), func(t *testing.T) {
				var calls atomic.Int32
				server := commanderProvider(t, &calls)
				cfg := Object{"endpoint": server.URL, "model": "fixture"}
				local, path := neuralFixtureModel(t, mode)
				newStrategy := func() Strategy {
					l, e := NewLLM(cfg)
					if e != nil {
						t.Fatal(e)
					}
					if kind == "llm" {
						return l
					}
					model, e := NewLearned(path, mode)
					if e != nil {
						t.Fatal(e)
					}
					return &Hybrid{model, l}
				}
				_ = local
				strategy := newStrategy()
				directory := filepath.Join(t.TempDir(), "store")
				store, e := OpenStore(directory)
				if e != nil {
					t.Fatal(e)
				}
				var team Object
				var final Decision
				for turn := 0; turn < 2; turn++ {
					var events []Object
					team, events = neuralFixtureTeam(t, mode, turn)
					for _, entry := range events {
						ev := obj(entry["event"])
						store.Ingest(str(entry["member"]), clone(Object{"stream": entry["stream"], "cursor": ev["sequence"], "events": []Object{ev}}))
					}
					history, cutoff := store.HistorySnapshot("match")
					team["history_record_cutoff"] = cutoff
					final, e = strategy.Decide(context.Background(), team, history)
					if e != nil || validatePlan(team, final.Plan) != nil {
						t.Fatal("decision", e)
					}
					if kind == "hybrid" && (obj(final.Diagnostics["local_proposal"]) == nil || str(final.Diagnostics["proposal_id"]) == "") {
						t.Fatal("missing immutable learned proposal")
					}
					store.Record("turn", neuralTurnRecord(team, final), "match", "")
				}
				member := "member-0"
				view := obj(obj(team["members"])[member])
				candidate := ""
				for _, order := range final.Plan.Orders {
					if order.Member == member && order.Actor == "player" {
						candidate = order.Candidate
					}
				}
				selection := Object{"match_id": "match", "turn": 1, "observation_id": view["observation_id"], "candidate_id": candidate}
				if !store.Reserve(member, selection, "player") {
					t.Fatal(store.Err())
				}
				if e = store.DB.Close(); e != nil {
					t.Fatal(e)
				}
				store, e = OpenStore(directory)
				if e != nil {
					t.Fatal(e)
				}
				defer store.DB.Close()
				history, cutoff := store.HistorySnapshot("match")
				team["history_record_cutoff"] = cutoff
				obj(view["battle"])["PlayerSubmitted"] = true
				view["reserved_actors"] = Object{"player": "uncertain"}
				restarted := newStrategy()
				got, e := restarted.Decide(context.Background(), team, history)
				if e != nil || calls.Load() != 2 || len(got.Plan.Orders) != mode*2-1 || !yes(got.Diagnostics["reused_plan"]) {
					t.Fatal("restart reran inference/changed final plan", e, calls.Load())
				}
				if !neuralPlanSubset(got.Plan, final.Plan) || !reflect.DeepEqual(got.Diagnostics["local_proposal"], final.Diagnostics["local_proposal"]) {
					t.Fatal("recovery lost final orders/provenance")
				}
				changed := clone(team)
				changed["observation_id"] = "changed"
				if _, e = restarted.Decide(context.Background(), changed, history); e == nil || calls.Load() != 2 {
					t.Fatal("unsafe partial replanning", e)
				}
				cfg["model"] = "different"
				if _, e = newStrategy().Decide(context.Background(), team, history); e == nil || calls.Load() != 2 {
					t.Fatal("changed provider accepted saved final plan", e)
				}
			})
		}
	}
}
