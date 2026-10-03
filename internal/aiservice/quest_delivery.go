package aiservice

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

type QuestPetDeliverySkill struct{ NPC *NPCSkill }
type petDeliveryReceipt struct {
	Version   int           `json:"version"`
	Phase     string        `json:"phase,omitempty"`
	Gold      int64         `json:"gold"`
	Reward    int64         `json:"reward"`
	Items     map[int32]int `json:"items"`
	Consumed  map[int32]int `json:"consumed"`
	Protected []string      `json:"protected"`
	Delivered []string      `json:"delivered"`
}

func (s *QuestPetDeliverySkill) ValidateSkill(ctx context.Context, a automation.Action) error {
	if s == nil || s.NPC == nil || a.Skill != "pet.deliver" || a.MaximumCost != 0 {
		return errors.New("pet.deliver requires a reviewed zero-cost NPC delivery")
	}
	args, err := decodeNPCWindow(a.Arguments)
	if err != nil {
		return err
	}
	if args.PetCollection == "" || len(args.PetIDs) > 0 {
		return npcInvalid("pet.deliver requires a completed task pet collection")
	}
	spec, err := s.NPC.lookup(a.Arguments)
	if err != nil {
		return err
	}
	w, err := resolveNPCWindow(spec, args.WindowSequence)
	if err != nil {
		return err
	}
	c, err := resolveNPCChoice(w.Choices, args.Choice)
	if err != nil {
		return err
	}
	if c.PetDelivery == nil || c.PetDelivery.GoldReward <= 0 {
		return npcInvalid("pet delivery has no reviewed gold reward")
	}
	a.Skill = "npc.dialogue"
	return s.NPC.ValidateSkill(ctx, a)
}
func (*QuestPetDeliverySkill) Execute(context.Context, automation.Action) error {
	return errors.New("pet delivery requires a durable task checkpoint")
}
func receiptConfirmed(r petDeliveryReceipt, o aigame.Snapshot) bool {
	if r.Phase != "" && r.Phase != "submitted" {
		return false
	}
	if r.Version != 1 || r.Gold < 0 || r.Gold > 1000000000 || r.Reward <= 0 || r.Reward > 1000000000 || validatePetDeliveryIDs(r.Delivered) != nil || len(r.Protected)+len(r.Delivered) > 5 || collectionWorld(o) != nil || !o.AI.Received || !o.AI.ItemsKnown || len(r.Consumed) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, id := range append(slices.Clone(r.Protected), r.Delivered...) {
		if id == "" || seen[id] {
			return false
		}
		seen[id] = true
	}
	for id, count := range r.Items {
		if id <= 0 || count < 1 || count > 20 {
			return false
		}
	}
	for id, count := range r.Consumed {
		if id <= 0 || count < 1 || count > r.Items[id] {
			return false
		}
	}
	if int64(o.Player.Gold) != r.Gold+r.Reward {
		return false
	}
	owned, err := collectionOwned(o)
	if err != nil || len(owned) != len(r.Protected) {
		return false
	}
	for _, id := range r.Protected {
		if _, ok := owned[id]; !ok {
			return false
		}
	}
	for _, id := range r.Delivered {
		if _, ok := owned[id]; ok {
			return false
		}
	}
	items := map[int32]int{}
	for _, item := range o.AI.Items {
		items[item.TemplateID]++
	}
	for id, before := range r.Items {
		if items[id] != before-r.Consumed[id] {
			return false
		}
		delete(items, id)
	}
	return len(items) == 0
}
func (s *QuestPetDeliverySkill) CanResumeStep(ctx context.Context, a automation.Action, p automation.StepProgress) error {
	if err := s.ValidateSkill(ctx, a); err != nil {
		return err
	}
	var r petDeliveryReceipt
	if err := json.Unmarshal(p.State, &r); err != nil {
		return err
	}
	o, err := refreshOwnIdentity(ctx, s.NPC, false)
	if err != nil {
		return err
	}
	if r.Version == 1 && r.Phase == "preparing" {
		return collectionWorld(o)
	}
	if !receiptConfirmed(r, o) {
		return errors.New("pet delivery result is unconfirmed; final confirmation will not be repeated")
	}
	return nil
}
func (s *QuestPetDeliverySkill) ExecuteStep(ctx context.Context, a automation.Action, scope automation.StepExecution) error {
	if err := s.ValidateSkill(ctx, a); err != nil {
		return err
	}
	if scope.Save == nil || scope.PlanID == "" || scope.StepID == "" {
		return errors.New("pet delivery checkpoint missing")
	}
	args, _ := decodeNPCWindow(a.Arguments)
	collection, ok := scope.Results[args.PetCollection]
	if !ok || !collection.Confirmed || validatePetDeliveryIDs(collection.PetIDs) != nil {
		return errors.New("task collection output unavailable")
	}
	var r petDeliveryReceipt
	if len(scope.Progress.State) > 0 {
		if err := json.Unmarshal(scope.Progress.State, &r); err != nil {
			return err
		}
		if r.Version != 1 || r.Phase != "" && r.Phase != "preparing" && r.Phase != "submitted" {
			return errors.New("invalid pet delivery checkpoint")
		}
		if r.Phase != "preparing" && !slices.Equal(r.Delivered, collection.PetIDs) {
			return errors.New("pet delivery identities changed")
		}
	}
	if len(scope.Progress.State) == 0 || r.Phase == "preparing" {
		// Preflight refusals (for example a full wallet) remain resumable.
		// Only the later submitted receipt authorizes the destructive WN.
		preparing, _ := json.Marshal(petDeliveryReceipt{Version: 1, Phase: "preparing"})
		if err := scope.Save(ctx, automation.StepProgress{State: preparing}); err != nil {
			return err
		}
		before, err := refreshOwnIdentity(ctx, s.NPC, false)
		if err != nil {
			return err
		}
		spec, err := s.NPC.lookup(a.Arguments)
		if err != nil {
			return err
		}
		window, err := resolveNPCWindow(spec, args.WindowSequence)
		if err != nil {
			return err
		}
		choice, err := resolveNPCChoice(window.Choices, args.Choice)
		if err != nil {
			return err
		}
		if err := choice.PetDelivery.validate(before, collection.PetIDs); err != nil {
			return err
		}
		r = petDeliveryReceipt{Version: 1, Phase: "submitted", Gold: int64(before.Player.Gold), Reward: choice.PetDelivery.GoldReward, Items: map[int32]int{}, Consumed: choice.PetDelivery.RequiredItems, Delivered: slices.Clone(collection.PetIDs)}
		for _, item := range before.AI.Items {
			r.Items[item.TemplateID]++
		}
		for _, pet := range before.Pets {
			if !slices.Contains(r.Delivered, pet.StableID) {
				r.Protected = append(r.Protected, pet.StableID)
			}
		}
		raw, _ := json.Marshal(r)
		if err := scope.Save(ctx, automation.StepProgress{State: raw}); err != nil {
			return err
		}
		args.PetCollection = ""
		args.PetIDs = slices.Clone(r.Delivered)
		a.Arguments, _ = json.Marshal(args)
		a.Skill = "npc.dialogue"
		a.ExpectedRevision = before.Revision
		// A persisted receipt means that a future invocation only checks the
		// result. It never repeats this destructive confirmation.
		game := &AutomationGame{Backend: s.NPC.Backend, Skills: SkillSet{"npc.dialogue": s.NPC}}
		if err := game.Execute(ctx, a); err != nil {
			return err
		}
	}
	wait, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		o, err := refreshOwnIdentity(wait, s.NPC, false)
		if err != nil {
			return err
		}
		if receiptConfirmed(r, o) {
			raw, _ := json.Marshal(r)
			persist, stop := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			defer stop()
			return scope.Save(persist, automation.StepProgress{State: raw, Confirmed: true})
		}
		select {
		case <-wait.Done():
			return errors.New("pet delivery reward or consumption not confirmed")
		case <-time.After(100 * time.Millisecond):
		}
	}
}
