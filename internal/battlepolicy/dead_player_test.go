package battlepolicy

import (
	"context"
	"fmt"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/battlenet"
)

// Reproduce the complete BC + BP(menu off) + BA(player accepted) boundary
// seen in the dual-model arena, including the independent surviving-pet case.
func deadPlayerTeam(mode, side int, livePet bool) []aigame.BattleView {
	views := fixture(mode, side)
	id := int32(side * 10)
	for i := range views {
		for j := range views[i].Battle.Participants {
			p := &views[i].Battle.Participants[j]
			if p.BattleID == id || !livePet && p.BattleID == id+5 {
				p.Dead, p.HP = true, 0
			}
		}
	}
	b := views[0].Battle
	b.BPFlags = aigame.BattlePlayerMenuOff | aigame.BattlePetMenuOff
	b.PlayerSubmitted, b.BAReceived, b.AnimationFlags = true, true, 1<<uint(id)
	v := aigame.NewBattleView(aigame.Snapshot{Phase: aigame.PhaseBattle, Battle: b, Player: views[0].Own, Pets: views[0].Pets})
	v.Mode = mode
	views[0] = v
	return views
}

func TestDeadAcknowledgedPlayerKeepsTeamAndPetDecisions(t *testing.T) {
	for mode := 2; mode <= 5; mode++ {
		for side := 0; side < 2; side++ {
			for _, livePet := range []bool{false, true} {
				t.Run(fmt.Sprintf("%dv%d/side%d/pet%t", mode, mode, side, livePet), func(t *testing.T) {
					views := deadPlayerTeam(mode, side, livePet)
					for _, schema := range []string{LegacyFeatureVersion, RecipientFeatureVersion, FeatureVersion} {
						f, err := EncodeVersion(views, History{First: true}, schema)
						if err != nil {
							t.Fatal(err)
						}
						if err = f.Validate(); err != nil {
							t.Fatal(err)
						}
						want := 2 * (mode - 1)
						if livePet {
							want++
						}
						if len(f.Slots) != want || len(f.Entities) != 4*mode || f.Members[0] < 0 {
							t.Fatal("lost roster or independent actions")
						}
						for _, slot := range f.Slots {
							if slot.Member == 0 && slot.Actor == "player" {
								t.Fatal("duplicate dead-player command")
							}
						}
						cfg := NetworkConfig()
						cfg.Width, cfg.Heads, cfg.Layers = 8, 2, 1
						cfg.InputSchema = schema
						m, err := battlenet.NewModel[float32](cfg, 4)
						if err != nil {
							t.Fatal(err)
						}
						g := &battlenet.Graph[float32]{}
						out, err := Forward(context.Background(), m.Bind(g), f, g.New(1, cfg.Width, nil), nil, nil)
						if err != nil {
							t.Fatal(err)
						}
						selected, err := f.Selections(out.Choices)
						if err != nil {
							t.Fatal(err)
						}
						if len(selected[0]) != 0 && (!livePet || len(selected[0]) != 1) {
							t.Fatal("invalid owner selection", selected[0])
						}
						f.CompletedPlayers = nil
						if err = f.Validate(); err == nil {
							t.Fatal("missing action accepted without completion routing")
						}
					}
				})
			}
		}
	}
}

func TestDeadPlayerAcknowledgementDoesNotRelaxPartialSubmission(t *testing.T) {
	cases := map[string]func(*aigame.BattleSnapshot){
		"missing_BA":     func(b *aigame.BattleSnapshot) { b.BAReceived = false },
		"missing_bit":    func(b *aigame.BattleSnapshot) { b.AnimationFlags = 0 },
		"menu_available": func(b *aigame.BattleSnapshot) { b.BPFlags = 0 },
		"local_command":  func(b *aigame.BattleSnapshot) { b.LastCommand = "N" },
		"pet_submitted":  func(b *aigame.BattleSnapshot) { b.PetSubmitted = true },
		"movie":          func(b *aigame.BattleSnapshot) { b.Movie = true },
		"result":         func(b *aigame.BattleSnapshot) { b.Result = "escaped" },
		"missing_roster": func(b *aigame.BattleSnapshot) { b.BCReceived = false },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			v := deadPlayerTeam(2, 0, false)
			mutate(&v[0].Battle)
			if _, err := Encode(v, History{First: true}); err == nil {
				t.Fatal("unverified partial boundary accepted")
			}
		})
	}
	v := deadPlayerTeam(2, 0, false)
	for i := range v {
		v[i].Battle.Participants[0].Dead = false
		v[i].Battle.Participants[0].HP = 10
	}
	if _, err := Encode(v, History{First: true}); err == nil {
		t.Fatal("living submitted player accepted")
	}
}
