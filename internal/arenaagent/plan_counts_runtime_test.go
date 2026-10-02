package arenaagent

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/battlenet"
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
)

// Explicit nonzero numerical fixtures, never claimed as trained candidates.
func neuralPlanCountsFixture(t *testing.T, mode int, features string) (*Learned, string) {
	t.Helper()
	legacy, path := neuralFixtureModel(t, mode, features)
	a := *legacy.neural
	c := a.Network.Config
	c.PlanFeatures = "target-counts-v1"
	m, err := battlenet.NewModel[float32](c, 4)
	if err != nil {
		t.Fatal(err)
	}
	for i := range m.Parameters["score.plan.w"].Values {
		m.Parameters["score.plan.w"].Values[i] = float32(i%11-5) * .3
	}
	a.Network, a.Architecture = m, battlepolicy.NetworkArchitecture(c)
	a.WeightsDigest, err = battlepolicy.NetworkDigest(m)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, enc(a), 0600); err != nil {
		t.Fatal(err)
	}
	l, err := NewLearned(path, mode)
	if err != nil {
		t.Fatal(err)
	}
	return l, path
}

func TestPlanCountsLocalCommanderHistoryAndRecovery(t *testing.T) {
	for _, features := range []string{battlepolicy.LegacyFeatureVersion, battlepolicy.RecipientFeatureVersion, battlepolicy.FeatureVersion} {
		t.Run(features, func(t *testing.T) {
			l, path := neuralPlanCountsFixture(t, 1, features)
			testNeuralMemoryWithModel(t, l, path)
		})
	}
	l, _ := neuralPlanCountsFixture(t, 2, battlepolicy.FeatureVersion)
	testNeuralObserverWithdrawal(t, l)
	l, _ = neuralPlanCountsFixture(t, 1, battlepolicy.FeatureVersion)
	testNeuralPartialHybridPlan(t, l)
}

func TestPlanCountsLocalCommanderFullTeamAndFailureContracts(t *testing.T) {
	for mode := 1; mode <= 5; mode++ {
		l, _ := neuralPlanCountsFixture(t, mode, battlepolicy.FeatureVersion)
		team, h := neuralFixtureTeam(t, mode, 0)
		before := string(enc(team))
		d, err := l.Decide(context.Background(), team, h)
		if err != nil || len(d.Plan.Orders) != mode*2 || validatePlan(team, d.Plan) != nil || str(d.Diagnostics["architecture"]) != "commander-policy-v3" {
			t.Fatal("v3 full-team contract", mode, d, err)
		}
		if string(enc(team)) != before {
			t.Fatal("mutated team")
		}
		nt, err := l.neuralTeam(team)
		if err != nil {
			t.Fatal(err)
		}
		history, err := neuralHistory(nt, h, "", 0, true)
		if err != nil {
			t.Fatal(err)
		}
		frame, err := battlepolicy.EncodeVersion(nt.views, history, l.neural.Features)
		if err != nil {
			t.Fatal(err)
		}
		g := &battlenet.Graph[float32]{}
		o, err := battlepolicy.Forward(context.Background(), l.neural.Network.Bind(g), frame, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		choices := map[Slot]string{}
		for i, slot := range frame.Slots {
			choices[Slot{nt.names[slot.Member], slot.Actor}] = slot.Candidates[o.Choices[i]].ID
		}
		if !reflect.DeepEqual(d.Plan, planFor(team, choices)) {
			t.Fatal("online/offline plan mismatch", mode)
		}
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err = l.Decide(cancelled, team, h); !errors.Is(err, context.Canceled) {
			t.Fatal("cancellation lost", err)
		}
		bad := clone(team)
		bad["missing_ids"] = []string{"missing"}
		_, err = l.Decide(context.Background(), bad, h)
		var failure *neuralFailure
		if !errors.As(err, &failure) || failure.code != "incomplete_team" || !strings.HasPrefix(err.Error(), "learned:") || strings.Contains(err.Error(), "v2") {
			t.Fatal("misleading architecture/failure", err)
		}
	}
}

func TestNeuralMissingMemberListNativeAndPersistedForms(t *testing.T) {
	legacy, _ := neuralFixtureModel(t, 2)
	counts, _ := neuralPlanCountsFixture(t, 2, battlepolicy.FeatureVersion)
	for _, l := range []*Learned{legacy, counts} {
		team, h := neuralFixtureTeam(t, 2, 0)
		for _, value := range []any{[]string{"missing"}, []any{"missing"}} {
			bad := clone(team)
			bad["missing_ids"] = value
			for _, form := range []Object{bad, clone(bad)} {
				_, err := l.Decide(context.Background(), form, h)
				var failure *neuralFailure
				if !errors.As(err, &failure) || failure.code != "incomplete_team" {
					t.Fatal("missing member ignored", l.Version(), err)
				}
			}
		}
		for _, value := range []any{"none", 0, Object{}} {
			bad := clone(team)
			bad["missing_ids"] = value
			_, err := l.Decide(context.Background(), bad, h)
			var failure *neuralFailure
			if !errors.As(err, &failure) || failure.code != "invalid_observation" {
				t.Fatal("malformed missing-member list accepted", err)
			}
		}
		for _, value := range []any{nil, []string{}, []any{}} {
			good := clone(team)
			good["missing_ids"] = value
			if _, err := l.Decide(context.Background(), good, h); err != nil {
				t.Fatal("complete team rejected", err)
			}
		}
	}
}
