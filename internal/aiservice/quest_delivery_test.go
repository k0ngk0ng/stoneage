package aiservice

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

func deliveryOutcome() (petDeliveryReceipt, aigame.Snapshot) {
	r := petDeliveryReceipt{Version: 1, Gold: 100, Reward: 500, Items: map[int32]int{20001: 1, 1234: 2}, Consumed: map[int32]int{20001: 1}, Protected: []string{"original"}, Delivered: []string{"caught"}}
	o := aigame.Snapshot{Connected: true, Phase: aigame.PhaseWorld, Player: aigame.PlayerSnapshot{HasStatus: true, HP: 10, Gold: 600}, AI: aigame.AIObservation{Received: true, ItemsKnown: true, Items: []aigame.AIInventoryItem{{Slot: 5, TemplateID: 1234}, {Slot: 6, TemplateID: 1234}}}, Pets: []aigame.PetSnapshot{{StableID: "original", IdentityKnown: true, SpeciesIDKnown: true, EventFlagKnown: true}}}
	return r, o
}

func TestPetDeliveryReceiptRequiresExactRewardAndConsumption(t *testing.T) {
	for _, mode := range []string{"confirmed", "missing-reward", "extra-reward", "lost-original", "voucher-retained", "other-item-consumed", "unknown-items", "unknown-pet", "missing-consumed-baseline", "duplicate-protected", "overlap"} {
		t.Run(mode, func(t *testing.T) {
			r, o := deliveryOutcome()
			switch mode {
			case "missing-reward":
				o.Player.Gold = 100
			case "extra-reward":
				o.Player.Gold++
			case "lost-original":
				o.Pets = nil
			case "voucher-retained":
				o.AI.Items = append(o.AI.Items, aigame.AIInventoryItem{TemplateID: 20001})
			case "other-item-consumed":
				o.AI.Items = o.AI.Items[:1]
			case "unknown-items":
				o.AI.ItemsKnown = false
			case "unknown-pet":
				o.Pets[0].IdentityKnown = false
			case "missing-consumed-baseline":
				delete(r.Items, 20001)
			case "duplicate-protected":
				r.Protected = append(r.Protected, "original")
			case "overlap":
				r.Delivered = []string{"original"}
			}
			if receiptConfirmed(r, o) != (mode == "confirmed") {
				t.Fatal("incorrect receipt outcome", mode)
			}
		})
	}
}

type deliveryReceiptGame struct {
	*petDeliverySession
	onConfirm     func() error
	confirmations int
}

func (g *deliveryReceiptGame) ExecuteExpected(ctx context.Context, rev uint64, a aigame.Action) error {
	if a.Kind == aigame.ActionWindow {
		g.confirmations++
		if g.onConfirm != nil {
			if err := g.onConfirm(); err != nil {
				return err
			}
		}
	}
	if err := g.petDeliverySession.ExecuteExpected(ctx, rev, a); err != nil {
		return err
	}
	if a.Kind == aigame.ActionWindow {
		g.snapshot.Player.Gold += 500
		g.snapshot.Pets = g.snapshot.Pets[2:]
		g.snapshot.AI.Pets = slices.Clone(g.snapshot.Pets)
		g.snapshot.AI.Items = nil
		g.snapshot.ActiveWindow = nil
	}
	return nil
}

func TestPetDeliveryPersistsBeforeFinalOKAndNeverRepeatsAfterLostReply(t *testing.T) {
	for _, mode := range []string{"confirmed", "disk-failure", "uncertain", "capacity"} {
		t.Run(mode, func(t *testing.T) {
			npc, base, spec := npcSkillFixture(t, true)
			d, o, ids := petDeliveryFixture()
			d.ConfirmationButton, d.GoldReward = 1, 500
			spec.WindowType = 0
			spec.Choices = map[int]NPCChoice{1: {Button: 1, PetDelivery: d}, 32: {Button: 32}}
			npc.Registry = MustNPCRegistry([]NPCSpec{spec})
			base.snapshot.Pets, base.snapshot.AI = o.Pets, o.AI
			base.snapshot.AI.GoldLimitKnown, base.snapshot.AI.GoldLimit = true, 1000000
			if mode == "capacity" {
				base.snapshot.AI.GoldLimit = 500
			}
			base.snapshot.ActiveWindow = &aigame.WindowSnapshot{Type: 0, Sequence: 100, ObjectID: 42, Data: d.AcceptText, ButtonType: 1, Open: true}
			g := &deliveryReceiptGame{petDeliverySession: &petDeliverySession{npcSkillSession: base}}
			npc.Backend.Session = g
			skill := &QuestPetDeliverySkill{NPC: npc}
			a := automation.Action{Skill: "pet.deliver", ExpectedRevision: 12, Arguments: json.RawMessage(`{"npc":"trainer","window_sequence":100,"choice":1,"pet_collection":"collect"}`)}
			var saved automation.StepProgress
			scope := automation.StepExecution{PlanID: "p", StepID: "deliver", Results: map[string]automation.StepProgress{"collect": {PetIDs: ids, Confirmed: true}}, Save: func(_ context.Context, p automation.StepProgress) error {
				if mode == "disk-failure" {
					return errors.New("disk failure")
				}
				saved = p.Clone()
				return nil
			}}
			g.onConfirm = func() error {
				if len(saved.State) == 0 || saved.Confirmed {
					t.Fatal("final confirmation preceded receipt")
				}
				var receipt petDeliveryReceipt
				if json.Unmarshal(saved.State, &receipt) != nil || receipt.Phase != "submitted" {
					t.Fatal("submitted boundary not durable", string(saved.State))
				}
				if mode == "uncertain" {
					return errors.New("lost confirmation reply")
				}
				return nil
			}
			err := skill.ExecuteStep(context.Background(), a, scope)
			if (err == nil) != (mode == "confirmed") {
				t.Fatal(err)
			}
			if mode == "disk-failure" {
				if g.confirmations != 0 {
					t.Fatal("destructive write without checkpoint")
				}
				return
			}
			if mode == "capacity" {
				if g.confirmations != 0 {
					t.Fatal("over-capacity reward submitted")
				}
				if err := skill.CanResumeStep(context.Background(), a, saved); err != nil {
					t.Fatal("pre-write refusal poisoned recovery", err)
				}
				g.snapshot.AI.GoldLimit = 1000000
				scope.Progress = saved.Clone()
				if err := skill.ExecuteStep(context.Background(), a, scope); err != nil {
					t.Fatal(err)
				}
				if !saved.Confirmed || g.confirmations != 1 {
					t.Fatal("recovered delivery not confirmed")
				}
				return
			}
			if saved.Confirmed != (mode == "confirmed") {
				t.Fatal(saved)
			}
			// On resume an uncertain result is rejected without another WN.
			if err := skill.CanResumeStep(context.Background(), a, saved); (err == nil) != (mode == "confirmed") {
				t.Fatal(err)
			}
			if mode == "confirmed" {
				scope.Progress = saved
				if err := skill.ExecuteStep(context.Background(), a, scope); err != nil {
					t.Fatal(err)
				}
			}
			if g.confirmations != 1 {
				t.Fatal("delivery replayed", g.confirmations)
			}
		})
	}
}
