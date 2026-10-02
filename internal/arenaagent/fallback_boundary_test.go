package arenaagent

import (
	"context"
	"errors"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

// Check the runner decision boundary and actual SQLite diagnostics. This
// complements the real learned/subprocess recovery fixture in runner_continuity_test.go.
func TestFallbackRequiresUnsubmittedVerifiedTeam(t *testing.T) {
	for _, name := range []string{"fresh", "player_submitted", "pet_submitted", "uncertain", "written", "dead_completed", "dead_reserved", "invalid_battle", "invalid_reservation"} {
		t.Run(name, func(t *testing.T) {
			team, _ := neuralFixtureTeam(t, 1, 0)
			member := obj(obj(team["members"])["member-0"])
			battle := obj(member["battle"])
			switch name {
			case "player_submitted":
				battle["PlayerSubmitted"] = true
			case "pet_submitted":
				battle["PetSubmitted"] = true
			case "uncertain", "written":
				member["reserved_actors"] = Object{"player": name}
			case "dead_completed", "dead_reserved":
				var typed aigame.BattleSnapshot
				if err := decode(enc(battle), &typed); err != nil {
					t.Fatal(err)
				}
				typed.PlayerSubmitted, typed.BAReceived = true, true
				typed.BPFlags = aigame.BattlePlayerMenuOff
				typed.AnimationFlags = 1
				for i := range typed.Participants {
					if typed.Participants[i].BattleID == typed.MyNo {
						typed.Participants[i].Dead = true
						typed.Participants[i].HP = 0
					}
				}
				if !typed.DeadPlayerCommandComplete() {
					t.Fatal("fixture must use the actual server-completed dead-player exception")
				}
				if decode(enc(typed), &battle) != nil {
					t.Fatal("typed fixture projection")
				}
				member["battle"] = battle
				if name == "dead_reserved" {
					member["reserved_actors"] = Object{"pet": "uncertain"}
				}
			case "invalid_battle":
				member["battle"] = "not a typed observation"
			case "invalid_reservation":
				member["reserved_actors"] = "uncertain"
			}
			store, err := OpenStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.DB.Close()
			original := neuralError("fixture_decision_failure", "fixture strategy failure")
			r := &Runner{store: store, strategy: turnDecisionFunc(func(context.Context, Object, []Object) (Decision, error) {
				return Decision{}, original
			})}
			decision, err := r.decideForTurn(context.Background(), context.Background(), team, nil)
			allowed := name == "fresh" || name == "dead_completed"
			if allowed {
				if err != nil || decision.Strategy != "basic" || validatePlan(team, decision.Plan) != nil {
					t.Fatal("legitimate fallback was disabled", err, decision)
				}
			} else {
				var failure *neuralFailure
				if !errors.As(err, &failure) || len(decision.Plan.Orders) != 0 {
					t.Fatal("unverified/partially submitted team received a replacement plan", err, decision)
				}
				if name != "invalid_battle" && name != "invalid_reservation" && !errors.Is(err, original) {
					t.Fatal("original strategy failure identity lost", err)
				}
			}
			var fallbacks, rejections int
			if err := store.DB.QueryRow("SELECT count(*) FROM records WHERE kind='strategy_fallback'").Scan(&fallbacks); err != nil {
				t.Fatal(err)
			}
			if err := store.DB.QueryRow("SELECT count(*) FROM records WHERE kind='strategy_rejected'").Scan(&rejections); err != nil {
				t.Fatal(err)
			}
			wantFallbacks, wantRejections := 0, 1
			if allowed {
				wantFallbacks, wantRejections = 1, 0
			}
			if fallbacks != wantFallbacks || rejections != wantRejections || store.Err() != nil {
				t.Fatal("fallback/rejection evidence does not match actual decision", fallbacks, rejections, store.Err())
			}
		})
	}
}
