package battletrain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/battlenet"
)

func demonstrationFixture(t *testing.T, tr Trajectory) Demonstration {
	t.Helper()
	hash := strings.Repeat("0", 64)
	d := Demonstration{Schema: DemonstrationSchema, Source: hash, Match: tr.Match, Group: hash, GroupKind: "recorded-roster-v1", Rules: tr.Rules, Platform: tr.Platform, Features: tr.Steps[0].Frame.Schema, Mode: tr.Mode, Side: tr.Side, Policy: tr.Policy, Strategy: "basic", Winner: tr.Winner, Reason: "defeat", Turns: len(tr.Steps), ResultDigest: hash}
	d.Roster = []string{"fixture-character", "fixture-enemy"}
	d.Group = DemonstrationGroup(d.Mode, d.Roster)
	for i, s := range tr.Steps {
		observations := append([]aigame.BattleView(nil), s.Observations...)
		for j := range observations {
			observations[j].CharacterID = "fixture-character"
			observations[j].Battle.Clock = aigame.BattleClock{RulesVersion: "stoneage-native-ladder-v1", RulesDigest: d.Rules, EnginePlatform: d.Platform}
		}
		history := s.History
		history.Observation = observations[0]
		step := DemonstratedTurn{RecordID: int64(i + 1), Frame: s.Frame, Choices: append([]int(nil), s.Choices...), Observations: observations, History: history}
		for j, slot := range s.Frame.Slots {
			step.Submissions = append(step.Submissions, DemonstratedSubmission{Member: slot.Member, Actor: slot.Actor, Status: "written", Selection: aigame.BattleSelection{MatchID: d.Match, Turn: int32(i), ObservationID: slot.Observation, CandidateID: slot.Candidates[s.Choices[j]].ID}})
		}
		d.Steps = append(d.Steps, step)
	}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDemonstrationUpdateMatchesExistingNumericsAndIgnoresOutcome(t *testing.T) {
	a, b, c := testModel(t), testModel(t), testModel(t)
	oa, ob, oc := &battlenet.Adam[float32]{}, &battlenet.Adam[float32]{}, &battlenet.Adam[float32]{}
	tr := teacherTrajectory(t, a, 5)
	d := demonstrationFixture(t, tr)
	losing := d
	losing.Winner = 1
	config := DefaultWarmupConfig().Update
	config.SequenceLength = 2
	before := teacherLoss(t, a, tr)
	valueHead := append([]float32(nil), a.Parameters["value.1.w"].Values...)
	for epoch := 0; epoch < 3; epoch++ {
		if _, err := Imitate(context.Background(), a, oa, []Trajectory{tr}, config); err != nil {
			t.Fatal(err)
		}
		r, err := ImitateDemonstrations(context.Background(), b, ob, []Demonstration{d}, config)
		if err != nil || r.Actions != 5 || r.TeamTurns != 5 || r.OptimizerUpdates != 1 || r.BeforePolicy == r.AfterPolicy {
			t.Fatalf("%+v %v", r, err)
		}
		if _, err := ImitateDemonstrations(context.Background(), c, oc, []Demonstration{losing}, config); err != nil {
			t.Fatal(err)
		}
		// Restore model and Adam between epochs; the uninterrupted copy must
		// remain exactly equal, including every optimizer moment.
		data, _ := json.Marshal(LearningState{Schema: 1, Model: b, Optimizer: ob})
		var restored LearningState
		if err := json.Unmarshal(data, &restored); err != nil {
			t.Fatal(err)
		}
		b, ob = restored.Model, restored.Optimizer
	}
	if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(oa, ob) || !reflect.DeepEqual(a, c) || !reflect.DeepEqual(oa, oc) {
		t.Fatal("source type/outcome/restoration changed the numerical update")
	}
	if teacherLoss(t, b, tr) >= before || !reflect.DeepEqual(valueHead, b.Parameters["value.1.w"].Values) {
		t.Fatal("did not learn choices or added value targets")
	}
	data, _ := json.Marshal(d)
	var disguised Trajectory
	if err := json.Unmarshal(data, &disguised); err != nil {
		t.Fatal(err)
	}
	if _, err := Train(context.Background(), b, ob, []Trajectory{disguised}, DefaultPPOConfig()); err == nil {
		t.Fatal("demonstration accepted as on-policy PPO")
	}
}

func TestDemonstrationRejectsBadEvidenceAtomically(t *testing.T) {
	for _, corruption := range []string{"intent", "choice", "frame", "history", "record", "rules", "member", "initial", "duplicate"} {
		t.Run(corruption, func(t *testing.T) {
			m := testModel(t)
			d := demonstrationFixture(t, teacherTrajectory(t, m, 3))
			s := &d.Steps[1]
			switch corruption {
			case "intent":
				s.Submissions[0].Status = "uncertain"
			case "choice":
				s.Choices[0] = -1
			case "frame":
				s.Frame.Entities[0][0] += 1
			case "history":
				s.History.Events = nil
			case "record":
				s.RecordID = d.Steps[0].RecordID
			case "rules":
				s.Observations[0].Battle.Clock.RulesDigest = ""
			case "member":
				s.Observations[0].CharacterID = "somebody-else"
			case "initial":
				s.InitialCursor = 100
			}
			ds := []Demonstration{d}
			if corruption == "duplicate" {
				ds = append(ds, d)
			}
			opt := &battlenet.Adam[float32]{}
			before, _ := ModelDigest(m)
			if _, err := ImitateDemonstrations(context.Background(), m, opt, ds, DefaultWarmupConfig().Update); err == nil {
				t.Fatal("invalid evidence accepted")
			}
			after, _ := ModelDigest(m)
			if before != after || opt.Step != 0 {
				t.Fatal("invalid source partially trained")
			}
		})
	}
}

func TestDemonstrationStorageAndCancellation(t *testing.T) {
	m := testModel(t)
	d := demonstrationFixture(t, teacherTrajectory(t, m, 4))
	path := filepath.Join(t.TempDir(), "demonstrations.jsonl")
	want, err := SaveDemonstrations(context.Background(), path, []Demonstration{d})
	if err != nil {
		t.Fatal(err)
	}
	loaded, manifest, err := LoadDemonstrations(context.Background(), path)
	// Candidate.Command is deliberately a non-serialized transport field.
	// Compare the complete serializable evidence and test numerical equality
	// separately, rather than requiring that private command strings survive.
	actualJSON, _ := json.Marshal(loaded)
	wantJSON, _ := json.Marshal([]Demonstration{d})
	if err != nil || !bytes.Equal(actualJSON, wantJSON) || !reflect.DeepEqual(manifest, want) || manifest.OnPolicyPPO {
		t.Fatalf("%+v %v", manifest, err)
	}
	if _, err := SaveDemonstrations(context.Background(), path, loaded); err == nil {
		t.Fatal("overwrote dataset")
	}
	data, _ := os.ReadFile(path)
	if err := os.WriteFile(path, append(data, ' '), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadDemonstrations(context.Background(), path); err == nil {
		t.Fatal("accepted tampered bytes")
	}
	config := DefaultWarmupConfig().Update
	config.BatchEpisodes = 1
	var d2 Demonstration
	// Deep-copy and remap all observed match identities without changing the
	// numerical frames, to exercise cancellation after an internal minibatch.
	raw, _ := json.Marshal(d)
	raw = []byte(strings.ReplaceAll(string(raw), `"test"`, `"second"`))
	if err := json.Unmarshal(raw, &d2); err != nil {
		t.Fatal(err)
	}
	probe := &imitationCancelCounter{Context: context.Background()}
	if _, err := ImitateDemonstrations(probe, testModel(t), &battlenet.Adam[float32]{}, []Demonstration{d, d2}, config); err != nil {
		t.Fatal(err)
	}
	ctx := &imitationCancelCounter{Context: context.Background(), Limit: probe.Calls * 3 / 4}
	opt := &battlenet.Adam[float32]{}
	before, _ := ModelDigest(m)
	if report, err := ImitateDemonstrations(ctx, m, opt, []Demonstration{d, d2}, config); !errors.Is(err, context.Canceled) || report.OptimizerUpdates < 1 {
		t.Fatal("did not cancel after an internal minibatch", report, err)
	}
	after, _ := ModelDigest(m)
	if before != after || opt.Step != 0 {
		t.Fatal("cancel leaked a partial epoch")
	}
}
