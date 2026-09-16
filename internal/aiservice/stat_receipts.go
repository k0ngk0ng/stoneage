package aiservice

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
)

type statAllocationState struct {
	Revision   uint64   `json:"revision"`
	Points     int32    `json:"points"`
	Attributes [4]int32 `json:"attributes"`
}

type statAllocationEvidence struct {
	Generation           uint64               `json:"generation"`
	CharacterID          string               `json:"character_id"`
	RequestedAction      aimcp.TypedAction    `json:"requested_action"`
	GameOutcomeConfirmed bool                 `json:"game_outcome_confirmed"`
	Before               statAllocationState  `json:"before"`
	After                *statAllocationState `json:"after,omitempty"`
}

func statState(s aigame.Snapshot) statAllocationState {
	return statAllocationState{Revision: s.Revision, Points: s.Player.UnspentStatPoints,
		Attributes: [4]int32{s.Player.Vital, s.Player.Strength, s.Player.Toughness, s.Player.Dexterity}}
}

// Called under statMu. Save the baseline before the only possible write.
// Only this live backend retains the receipt as reconcilable: a new process
// or character lease cannot infer delivery from coincidentally matching stats.
func (b *GameBackend) prepareStatAllocation(ctx context.Context, s aigame.Snapshot, a aimcp.TypedAction, r *aimcp.ActionReceipt) error {
	pending, err := b.Receipts.HasUnknownStateChange(ctx, b.Binding, r.Handle)
	if err != nil {
		return err
	}
	if pending {
		r.Status = aimcp.ReceiptFailed
		r.Reason = "another action has an uncertain outcome; allocation was not submitted"
		if err := b.Receipts.Save(ctx, b.Binding, *r); err != nil {
			return err
		}
		return errors.New(r.Reason)
	}
	evidence := statAllocationEvidence{Generation: b.Binding.Generation, CharacterID: b.Binding.CharacterID, RequestedAction: a, Before: statState(s)}
	r.Evidence, err = json.Marshal(evidence)
	if err != nil {
		return err
	}
	if err := b.Receipts.Save(ctx, b.Binding, *r); err != nil {
		return err
	}
	if b.statAllocations == nil {
		b.statAllocations = make(map[string]statAllocationEvidence)
	}
	b.statAllocations[r.Handle] = evidence
	return nil
}

func (b *GameBackend) reconcileStatAllocations(ctx context.Context, s aigame.Snapshot) error {
	b.statMu.Lock()
	defer b.statMu.Unlock()
	if len(b.statAllocations) == 0 || !s.Connected || !s.Player.HasStatus || !s.Player.StatPointsKnown {
		return nil
	}
	if err := b.check(b.Binding); err != nil {
		return err
	}
	after := statState(s)
	for handle, evidence := range b.statAllocations {
		if evidence.Generation != b.Binding.Generation || evidence.CharacterID != b.Binding.CharacterID {
			// A reused backend must not apply evidence to a new ownership
			// lease. Keep the durable receipt unknown for explicit review.
			delete(b.statAllocations, handle)
			continue
		}
		before := evidence.Before
		index := evidence.RequestedAction.Index
		if index < 0 || index > 3 || before.Points <= 0 || after.Points != before.Points-1 ||
			after.Revision <= before.Revision || s.AIObservationRevision <= before.Revision {
			continue
		}
		matches := true
		for i, value := range before.Attributes {
			want := int64(value)
			if int32(i) == index {
				want++
			}
			matches = matches && value > 0 && int64(after.Attributes[i]) == want
		}
		if !matches {
			continue
		}
		r, err := b.Receipts.Load(ctx, b.Binding, handle)
		if err != nil {
			return err
		}
		if r.Status != aimcp.ReceiptUnknown {
			delete(b.statAllocations, handle)
			continue
		}
		evidence.GameOutcomeConfirmed, evidence.After = true, &after
		r.Status, r.Reason = aimcp.ReceiptConfirmed, "server confirmed one point spent and the selected base attribute increased by one"
		r.Evidence, err = json.Marshal(evidence)
		if err != nil {
			return err
		}
		if err := b.Receipts.Save(ctx, b.Binding, r); err != nil {
			return err
		}
		delete(b.statAllocations, handle)
	}
	return nil
}
