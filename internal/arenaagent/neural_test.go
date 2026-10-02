package arenaagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/battleenv"
	"github.com/k0ngk0ng/stoneage/internal/battlenet"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

// Small numerical fixture, not a trained/certified model artifact.
func neuralFixtureModel(t *testing.T, mode int, features ...string) (*Learned, string) {
	t.Helper()
	c := battlepolicy.NetworkConfig()
	if len(features) > 0 {
		if !battlepolicy.SupportedFeatures(features[0]) {
			t.Fatal("unexpected fixture schema")
		}
		c.InputSchema = features[0]
		if features[0] == battlepolicy.LegacyFeatureVersion {
			c.InputSchema = ""
		}
	}
	c.Width, c.Heads, c.Layers = 8, 2, 1
	m, e := battlenet.NewModel[float32](c, 4)
	if e != nil {
		t.Fatal(e)
	}
	id, e := battlepolicy.NetworkDigest(m)
	if e != nil {
		t.Fatal(e)
	}
	hash := strings.Repeat("0", 64)
	a := battlepolicy.Artifact{Schema: 2, Architecture: "commander-policy-v2", Features: battlepolicy.NetworkFeatures(c), Actions: battlepolicy.ActionsForFeatures(battlepolicy.NetworkFeatures(c)), Status: "candidate", Environment: battleenv.Metadata{Rules: hash, Platform: "linux-amd64", Scenario: "controlled-battle-v8"}, Modes: []int{mode}, WeightsDigest: id, TrainingReport: hash, TrainingShards: []string{hash}, TrainingGroups: []string{hash}, Network: m}
	path := filepath.Join(t.TempDir(), "model.json")
	if e = os.WriteFile(path, enc(a), 0600); e != nil {
		t.Fatal(e)
	}
	l, e := NewLearned(path, mode)
	if e != nil {
		t.Fatal(e)
	}
	return l, path
}

func neuralFixtureTeam(t *testing.T, mode, turn int) (Object, []Object) {
	t.Helper()
	var roster []aigame.BattleParticipant
	for side := 0; side < 2; side++ {
		for i := 0; i < mode; i++ {
			roster = append(roster, aigame.BattleParticipant{BattleID: int32(side*10 + i), HP: int32(100 - turn), MaxHP: 100, Level: 35, Player: true})
			roster = append(roster, aigame.BattleParticipant{BattleID: int32(side*10 + i + 5), HP: 70, MaxHP: 90, Level: 35, Graphic: 100266, Name: "pet"})
		}
	}
	members, expected := Object{}, map[string]bool{}
	for i := 0; i < mode; i++ {
		s := aigame.Snapshot{Phase: aigame.PhaseBattle,
			Player: aigame.PlayerSnapshot{CombatStatsKnown: true, HP: 100, MaxHP: 100, MaxMP: 100, Attack: 50, Defense: 30, Quick: 20, Vital: 30, Strength: 30, Toughness: 30, Dexterity: 30, BattlePetSlotKnown: true, BattlePetSlot: 0},
			Battle: aigame.BattleSnapshot{Active: true, LadderID: "match", Turn: int32(turn), MyNo: int32(i), MyNoKnown: true, BPReceived: true, BCReceived: true, MyMP: 100, Participants: roster,
				Clock: aigame.BattleClock{RulesVersion: RulesVersion, RulesDigest: strings.Repeat("0", 64), EnginePlatform: "linux-amd64"}},
			Pets: []aigame.PetSnapshot{{Slot: 0, Name: "pet", Graphic: 100266, HP: 70, MaxHP: 90, UseFlag: 1, Skills: []aigame.PetSkillSnapshot{{Index: 0, ID: 1, Field: 0, Target: 0}, {Index: 1, ID: 2, Field: 0, Target: 2}}}},
		}
		v := aigame.NewBattleView(s)
		v.Mode, v.CharacterID = mode, fmt.Sprintf("char-%d", i)
		expected[v.CharacterID] = true
		var view Object
		if e := decode(enc(v), &view); e != nil {
			t.Fatal(e)
		}
		view["event_cutoff"] = Object{"stream": fmt.Sprintf("stream-%d", i), "cursor": 51 + turn*2, "gap": false}
		members[fmt.Sprintf("member-%d", i)] = view
	}
	team, e := teamObservation(clone(members), expected, mode)
	if e != nil {
		t.Fatal(e)
	}
	var events []aigame.BattleEvent
	if turn == 0 {
		events = []aigame.BattleEvent{{Sequence: 50, MatchID: "match", Function: "EN", Integers: []int32{1, 0}}, {Sequence: 51, MatchID: "match", Function: "B"}}
	} else {
		recipient := 10
		events = []aigame.BattleEvent{{Sequence: uint64(50 + turn*2), MatchID: "match", Function: "B", Effects: []aigame.BattleLogEntry{{Kind: "attack", Actor: 0, Target: 10, Damage: 1, Recipient: &recipient}}}, {Sequence: uint64(51 + turn*2), MatchID: "match", Function: "S", Raw: "BTIME"}}
	}
	var h []Object
	for _, event := range events {
		h = append(h, clone(Object{"member": "member-0", "stream": "stream-0", "event": event}))
	}
	return clone(team), h
}

func neuralTurnRecord(team Object, d Decision) Object {
	return clone(Object{"turn": team["turn"], "team": team, "plan": d.Plan, "strategy": d.Strategy, "version": d.Version, "diagnostics": d.Diagnostics})
}

func TestNeuralSchemaRoutingAndJointModes(t *testing.T) {
	for mode := 1; mode <= 5; mode++ {
		l, path := neuralFixtureModel(t, mode)
		team, h := neuralFixtureTeam(t, mode, 0)
		before := string(enc(team))
		d, e := l.Decide(context.Background(), team, h)
		if e != nil {
			t.Fatal(mode, e)
		}
		if len(d.Plan.Orders) != mode*2 || validatePlan(team, d.Plan) != nil || d.Diagnostics["estimated_return"] == nil || d.Diagnostics["estimated_win_probability"] != nil || !yes(d.Diagnostics["history_complete"]) {
			t.Fatal("invalid neural decision", d)
		}
		if before != string(enc(team)) {
			t.Fatal("strategy mutated live observation")
		}
		if _, e = NewLearned(path, mode%5+1); e == nil {
			t.Fatal("untrained team size accepted")
		}
	}
}

func TestNeuralCommanderContinuesAfterHistoryObserverWithdraws(t *testing.T) {
	l, _ := neuralFixtureModel(t, 2)
	testNeuralObserverWithdrawal(t, l)
}

func testNeuralObserverWithdrawal(t *testing.T, l *Learned) {
	t.Helper()
	zero, history := neuralFixtureTeam(t, 2, 0)
	initial, err := l.Decide(context.Background(), zero, history)
	if err != nil {
		t.Fatal(err)
	}
	history = append(history, neuralTurnRecord(zero, initial))
	for turn := 1; turn <= 2; turn++ {
		team, events := neuralFixtureTeam(t, 2, turn)
		history = append(history, events...)
		for name, value := range obj(team["members"]) {
			original := obj(value)
			var v aigame.BattleView
			if err := decode(enc(original), &v); err != nil {
				t.Fatal(err)
			}
			var roster []aigame.BattleParticipant
			for _, p := range v.Battle.Participants {
				if p.BattleID != 0 && p.BattleID != 5 {
					roster = append(roster, p)
				}
			}
			v.Battle.Participants = roster
			if v.Battle.MyNo == 0 {
				v.Battle.BPFlags = aigame.BattlePlayerMenuOff | aigame.BattlePetMenuOff
			}
			fresh := aigame.NewBattleView(aigame.Snapshot{Phase: aigame.PhaseBattle, Battle: v.Battle, Player: v.Own, Pets: v.Pets})
			fresh.Mode, fresh.CharacterID = v.Mode, v.CharacterID
			var next Object
			if err := decode(enc(fresh), &next); err != nil {
				t.Fatal(err)
			}
			next["event_cutoff"] = original["event_cutoff"]
			obj(team["members"])[name] = next
		}
		var err error
		team, err = teamObservation(obj(team["members"]), map[string]bool{"char-0": true, "char-1": true}, 2)
		if err != nil {
			t.Fatal(err)
		}
		d, err := l.Decide(context.Background(), team, history)
		if err != nil || len(d.Plan.Orders) != 2 || !yes(d.Diagnostics["history_complete"]) {
			t.Fatalf("turn %d failed after observer knockout: %+v %v", turn, d, err)
		}
		for _, order := range d.Plan.Orders {
			if order.Member != "member-1" {
				t.Fatal("issued command to withdrawn member")
			}
		}
		history = append(history, neuralTurnRecord(team, d))
	}
}

func TestNeuralMemoryPollingRestartAndFutureEventIsolation(t *testing.T) {
	for _, version := range []string{battlepolicy.FeatureVersion, battlepolicy.RecipientFeatureVersion, battlepolicy.LegacyFeatureVersion} {
		t.Run(version, func(t *testing.T) { testNeuralVersionedMemory(t, version) })
	}
}

func testNeuralVersionedMemory(t *testing.T, version string) {
	l, path := neuralFixtureModel(t, 1, version)
	testNeuralMemoryWithModel(t, l, path)
}

func testNeuralMemoryWithModel(t *testing.T, l *Learned, path string) {
	t.Helper()
	zero, h := neuralFixtureTeam(t, 1, 0)
	d0, e := l.Decide(context.Background(), zero, h)
	if e != nil {
		t.Fatal(e)
	}
	for range 2 {
		d, e := l.Decide(context.Background(), zero, h)
		if e != nil || !reflect.DeepEqual(d, d0) {
			t.Fatal("poll advanced recurrent state", e)
		}
	}
	h = append(h, neuralTurnRecord(zero, d0))
	one, events := neuralFixtureTeam(t, 1, 1)
	recipient, guardian := 0, 15
	obj(events[0]["event"])["effects"] = []aigame.BattleLogEntry{{Kind: "attack", Actor: 0, Target: 10, Damage: 25, Flags: 1536, Recipient: &recipient, Guardian: &guardian}}
	h = append(h, events...)
	d1, e := l.Decide(context.Background(), one, h)
	if e != nil {
		t.Fatal(e)
	}
	// Validate the online reconstruction against the same typed offline path.
	var memory []float32
	var cursor uint64
	stream := ""
	for _, team := range []Object{zero, one} {
		nt, e := l.neuralTeam(team)
		if e != nil {
			t.Fatal(e)
		}
		history, e := neuralHistory(nt, h, stream, cursor, stream == "")
		if e != nil {
			t.Fatal(e)
		}
		f, e := battlepolicy.EncodeVersion(nt.views, history, l.neural.Features)
		if e != nil {
			t.Fatal(e)
		}
		g := &battlenet.Graph[float32]{}
		o, e := battlepolicy.Forward(context.Background(), l.neural.Network.Bind(g), f, g.New(1, l.neural.Network.Config.Width, memory), nil, nil)
		if e != nil {
			t.Fatal(e)
		}
		memory = append([]float32(nil), o.Memory.Data...)
		stream, cursor = nt.cutoff.Stream, nt.cutoff.Cursor
		if integer(team["turn"]) == 1 {
			choices := map[Slot]string{}
			for i, slot := range f.Slots {
				choices[Slot{nt.names[slot.Member], slot.Actor}] = slot.Candidates[o.Choices[i]].ID
			}
			if d1.Diagnostics["estimated_return"].(float32) != o.Value.Data[0] || !reflect.DeepEqual(d1.Plan, planFor(team, choices)) {
				t.Fatal("online plan/value differs from continuous typed inference")
			}
		}
	}
	// This event has arrived later in the store but belongs beyond the saved
	// cutoff; it must not leak into the current decision even with a false turn.
	h = append(h, clone(Object{"member": "member-0", "stream": "stream-0", "event": aigame.BattleEvent{Sequence: 54, MatchID: "match", Turn: 0, Effects: []aigame.BattleLogEntry{{Kind: "attack", Actor: 10, Target: 0, Damage: 99999}}}}))
	fresh, e := NewLearned(path, 1)
	if e != nil {
		t.Fatal(e)
	}
	d, e := fresh.Decide(context.Background(), one, h)
	if e != nil || !reflect.DeepEqual(d, d1) {
		t.Fatal("restart/future event changed reconstructed decision", e)
	}
	// Loading pins the bytes. Replacing the model file cannot change a running match.
	if e = os.WriteFile(path, []byte("{}"), 0600); e != nil {
		t.Fatal(e)
	}
	d, e = fresh.Decide(context.Background(), one, h)
	if e != nil || !reflect.DeepEqual(d, d1) {
		t.Fatal("active model was reloaded from disk", e)
	}
}

func TestNeuralRejectsRelabeledLegacyWeights(t *testing.T) {
	_, path := neuralFixtureModel(t, 1, battlepolicy.LegacyFeatureVersion)
	artifact, err := battlepolicy.LoadArtifact(path)
	if err != nil {
		t.Fatal(err)
	}
	artifact.Features = battlepolicy.FeatureVersion
	if err = os.WriteFile(path, enc(artifact), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = NewLearned(path, 1); err == nil {
		t.Fatal("legacy weights silently interpreted as v7")
	}
}

func TestNeuralPartialSubmissionReusesFinalHybridPlan(t *testing.T) {
	l, _ := neuralFixtureModel(t, 1)
	testNeuralPartialHybridPlan(t, l)
}

func testNeuralPartialHybridPlan(t *testing.T, l *Learned) {
	t.Helper()
	team, h := neuralFixtureTeam(t, 1, 0)
	d, e := l.Decide(context.Background(), team, h)
	if e != nil {
		t.Fatal(e)
	}
	// A hybrid's final plan may differ from the neural proposal. It must be
	// continued as a whole, without another LLM request after partial writes.
	proposal := d
	d.Plan.Orders = append([]Order(nil), d.Plan.Orders...)
	for i := range d.Plan.Orders {
		if d.Plan.Orders[i].Actor == "pet" {
			for _, id := range sortedKeys(slots(team)[Slot{"member-0", "pet"}]) {
				if id != d.Plan.Orders[i].Candidate {
					d.Plan.Orders[i].Candidate = id
					break
				}
			}
		}
	}
	if reflect.DeepEqual(d.Plan, proposal.Plan) {
		t.Fatal("fixture needs a provider plan different from the local proposal")
	}
	providerPlan, historySize := d.Plan, len(h)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var request Object
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		messages := objects(request["messages"])
		var payload Object
		if len(messages) != 2 || decode([]byte(str(messages[1]["content"])), &payload) != nil {
			t.Error("missing structured provider context")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		local := obj(payload["local_proposal"])
		var received Plan
		if decode(enc(local["plan"]), &received) != nil || !reflect.DeepEqual(received, proposal.Plan) || str(local["version"]) != l.Version() || len(arr(payload["history"])) != historySize || obj(local["diagnostics"])["estimated_return"] == nil {
			t.Error("provider did not receive the real neural proposal and history")
		}
		_, _ = w.Write(enc(Object{"choices": []Object{{"finish_reason": "stop", "message": Object{"content": string(enc(providerPlan))}}}}))
	}))
	defer server.Close()
	model, e := NewLLM(Object{"endpoint": server.URL, "model": "test-provider"})
	if e != nil {
		t.Fatal(e)
	}
	hybrid := &Hybrid{Local: l, Model: model}
	final, e := hybrid.Decide(context.Background(), team, h)
	if e != nil || !reflect.DeepEqual(final.Plan, d.Plan) || final.Version != hybrid.Version() || requests.Load() != 1 {
		t.Fatal("provider plan was not accepted", e, final)
	}
	d = final
	h = append(h, neuralTurnRecord(team, d))
	partial := clone(team)
	v := obj(obj(partial["members"])["member-0"])
	obj(v["battle"])["PlayerSubmitted"] = true
	v["reserved_actors"] = Object{"player": "uncertain"}
	got, e := hybrid.Decide(context.Background(), partial, h)
	if e != nil || len(got.Plan.Orders) != 1 || got.Plan.Orders[0].Actor != "pet" || !yes(got.Diagnostics["reused_plan"]) || requests.Load() != 1 {
		t.Fatal("hybrid did not reuse pending order", e, got)
	}
	for _, o := range d.Plan.Orders {
		if o.Actor == "pet" && o != got.Plan.Orders[0] {
			t.Fatal("remaining pet order changed")
		}
	}
	if _, e = l.Decide(context.Background(), partial, h[:len(h)-1]); e == nil {
		t.Fatal("partial turn without saved full plan accepted")
	}
}

func TestNeuralStaleUnsubmittedPlanCanReplan(t *testing.T) {
	l, _ := neuralFixtureModel(t, 1)
	team, history := neuralFixtureTeam(t, 1, 0)
	want, err := l.Decide(context.Background(), team, history)
	if err != nil {
		t.Fatal(err)
	}
	stale := clone(team)
	stale["observation_id"] = "previous-observation"
	history = append(history, neuralTurnRecord(stale, want))
	got, err := l.Decide(context.Background(), team, history)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("unsubmitted stale attempt prevented fresh inference", err, got)
	}
	for _, state := range []string{"PlayerSubmitted", "PetSubmitted", "reserved"} {
		t.Run(state, func(t *testing.T) {
			partial := clone(team)
			view := obj(obj(partial["members"])["member-0"])
			if state == "reserved" {
				view["reserved_actors"] = Object{"player": "uncertain"}
			} else {
				obj(view["battle"])[state] = true
			}
			_, err := l.Decide(context.Background(), partial, history)
			var failure *neuralFailure
			if !errors.As(err, &failure) || failure.code != "changed_observation" {
				t.Fatal("changed partially submitted plan was not rejected", err)
			}
		})
	}
}

func TestNeuralHistoryAndRuleFailuresAreExplicit(t *testing.T) {
	for _, kind := range []string{"rules", "platform", "cutoff", "gap", "missing_event", "duplicate_event", "missing_turn", "reconnected"} {
		t.Run(kind, func(t *testing.T) {
			l, _ := neuralFixtureModel(t, 1)
			team, h := neuralFixtureTeam(t, 1, 0)
			v := obj(obj(team["members"])["member-0"])
			switch kind {
			case "rules":
				obj(obj(v["battle"])["Clock"])["RulesDigest"] = strings.Repeat("1", 64)
			case "platform":
				obj(obj(v["battle"])["Clock"])["EnginePlatform"] = "linux-arm64"
			case "cutoff":
				delete(v, "event_cutoff")
			case "gap":
				h = append(h, Object{"member": "member-0", "event_gap": Object{"stream": "stream-0", "cursor": 51}})
			case "missing_event":
				h = h[:1]
			case "duplicate_event":
				h = append(h, h[1])
			case "missing_turn":
				team, h = neuralFixtureTeam(t, 1, 1)
			case "reconnected":
				obj(v["event_cutoff"])["stream"] = "new-stream"
			}
			_, e := l.Decide(context.Background(), team, h)
			var failure *neuralFailure
			if !errors.As(e, &failure) || failure.code == "" {
				t.Fatal("expected explicit compatibility/history failure", e)
			}
		})
	}
}

func TestDecodeNeuralModelRejectsUnknownSchemaFields(t *testing.T) {
	_, path := neuralFixtureModel(t, 1)
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var v Object
	if e = json.Unmarshal(b, &v); e != nil {
		t.Fatal(e)
	}
	v["ignore_rules"] = true
	if e = os.WriteFile(path, enc(v), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = NewLearned(path, 1); e == nil {
		t.Fatal("unknown model compatibility override accepted")
	}
}

func TestCapabilityWaitObservesActualRulesResponse(t *testing.T) {
	queries := 0
	view, e := waitServerCapabilities(context.Background(), true, func(context.Context) (Object, error) {
		queries++
		clock := Object{"RulesVersion": RulesVersion}
		if queries == 2 {
			clock["RulesDigest"] = strings.Repeat("0", 64)
			clock["EnginePlatform"] = "linux-amd64"
		}
		return Object{"schema_version": 1, "battle": Object{"Clock": clock}}, nil
	})
	if e != nil || queries != 2 || str(obj(obj(view["battle"])["Clock"])["RulesDigest"]) == "" {
		t.Fatal("query write mistaken for response", queries, e)
	}
	l, _ := neuralFixtureModel(t, 1)
	wrapped := &clockChecked{inner: l}
	if localModel(wrapped) != l {
		t.Fatal("simulation wrapper hid neural preflight requirements")
	}
}
