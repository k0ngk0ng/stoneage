package aiservice

import (
	"context"
	"errors"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

func TestDeparturePreviewUsesCurrentLevelAndLeavesSkillUnchanged(t *testing.T) {
	danger, area := unsafeTravelArea(99, 10, 1, 0, 3, 0, 4, 6)
	for _, test := range []struct {
		name          string
		level, height int
		known, want   bool
	}{
		{"low-level", 1, 1, true, false}, {"unknown-encounter", 20, 1, false, false}, {"sufficient-level", 20, 1, true, true}, {"safe-detour", 1, 3, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			knowledge := &aiknowledge.Knowledge{Encounters: []aiknowledge.EncounterArea{danger}}
			if test.known {
				knowledge.Leveling = []aiknowledge.LevelingArea{area}
			}
			tiles := safeTravelTiles(t, safeTravelFloor(10, 5, test.height))
			// No session is installed: any observation or gameplay call would fail.
			skill := &MovementSkill{Backend: &GameBackend{Knowledge: knowledge}, Navigator: tiles, SafeTravel: true}
			game := &AutomationGame{Skills: SkillSet{"move": skill}}
			observed := automation.Observation{Connected: true, Ready: true, Floor: 10, Character: automation.Entity{Level: test.level, HP: 10, MaxHP: 10}}
			err := game.ValidateDeparture(context.Background(), crossMapAction(12, 10, 4, 0), observed)
			if (err == nil) != test.want {
				t.Fatalf("route feasibility=%v want=%v", err, test.want)
			}
			if skill.Navigator != tiles || !skill.SafeTravel {
				t.Fatal("preview changed shared movement skill")
			}
		})
	}
}

func TestDeparturePreviewChecksCrossMapReachabilityAndCancellation(t *testing.T) {
	knowledge := &aiknowledge.Knowledge{}
	skill := &MovementSkill{Backend: &GameBackend{Knowledge: knowledge}, Navigator: safeTravelTiles(t, safeTravelFloor(10, 3, 1), safeTravelFloor(11, 3, 1)), SafeTravel: true}
	observed := automation.Observation{Connected: true, Ready: true, Floor: 10, Character: automation.Entity{Level: 10, HP: 10, MaxHP: 10}}
	action := crossMapAction(12, 11, 2, 0)
	if err := skill.ValidateDeparture(context.Background(), action, observed); err == nil {
		t.Fatal("unreachable map accepted")
	}
	knowledge.Warps = []aiknowledge.MapWarp{{Type: "NONE", Time: "NULL", Attribute: "NULL", From: aiknowledge.Point{Floor: 10, X: 2}, To: aiknowledge.Point{Floor: 11}}}
	if err := skill.ValidateDeparture(context.Background(), action, observed); err != nil {
		t.Fatalf("reachable map rejected: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := skill.ValidateDeparture(ctx, action, observed); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}
