package aiservice

import (
	"context"
	"encoding/json"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
)

// mailListEvidence records the exact connection and complete-table sequence
// that existed before an AB request was submitted. A cached table is retained
// only as baseline context; it can never by itself confirm the action.
type mailListEvidence struct {
	ProfileID                 string               `json:"profile_id"`
	AccountID                 string               `json:"account_id"`
	CharacterID               string               `json:"character_id"`
	Generation                uint64               `json:"generation"`
	SessionToken              string               `json:"session_token"`
	BeforeRevision            uint64               `json:"before_revision"`
	BeforeAddressBookRevision uint64               `json:"before_address_book_revision"`
	GameOutcomeConfirmed      bool                 `json:"game_outcome_confirmed"`
	After                     *mailListObservation `json:"after,omitempty"`
}

type mailListObservation struct {
	SessionToken        string                    `json:"session_token"`
	Revision            uint64                    `json:"revision"`
	AddressBookRevision uint64                    `json:"address_book_revision"`
	AddressBookKnown    bool                      `json:"address_book_known"`
	AddressBook         []aigame.AddressBookEntry `json:"address_book,omitempty"`
}

func isMailListAction(action aimcp.TypedAction) bool {
	return action.Kind == "mail" && action.Command == "list"
}

func copyAddressBook(entries []aigame.AddressBookEntry) []aigame.AddressBookEntry {
	if entries == nil {
		return nil
	}
	return append([]aigame.AddressBookEntry(nil), entries...)
}

func (b *GameBackend) prepareMailListEvidence(binding aimcp.Binding, snapshot aigame.Snapshot) (json.RawMessage, error) {
	evidence := mailListEvidence{
		ProfileID:                 binding.ProfileID,
		AccountID:                 binding.AccountID,
		CharacterID:               binding.CharacterID,
		Generation:                binding.Generation,
		SessionToken:              snapshot.SessionToken,
		BeforeRevision:            snapshot.Revision,
		BeforeAddressBookRevision: snapshot.AddressBookRevision,
		GameOutcomeConfirmed:      false,
	}
	return json.Marshal(evidence)
}

// reconcileMailListActions confirms only an unknown mail/list receipt whose
// current snapshot proves a later complete AB frame on the same connection.
// It never sends a packet. In particular, AddressBookKnown and ABI changes
// are insufficient because AddressBookRevision advances only for full AB.
func (b *GameBackend) reconcileMailListActions(ctx context.Context, binding aimcp.Binding, snapshot aigame.Snapshot, requestedHandle string) error {
	if b == nil || b.Receipts == nil || !snapshot.Connected || snapshot.SessionToken == "" {
		return nil
	}
	unknown, err := b.Receipts.Unknown(ctx, binding)
	if err != nil {
		return err
	}
	// Unknown is a bounded display list. A caller polling an older handle
	// must still be able to reconcile it after a long history of messages.
	if requestedHandle != "" {
		found := false
		for _, receipt := range unknown {
			found = found || receipt.Handle == requestedHandle
		}
		if !found {
			receipt, err := b.Receipts.Load(ctx, binding, requestedHandle)
			if err != nil {
				return err
			}
			unknown = append(unknown, receipt)
		}
	}
	for _, listed := range unknown {
		if listed.Status != aimcp.ReceiptUnknown {
			continue
		}
		action, receipt, err := b.Receipts.LoadAction(ctx, binding, listed.Handle)
		if err != nil {
			return err
		}
		if receipt.Status != aimcp.ReceiptUnknown || !isMailListAction(action) {
			continue
		}
		var evidence mailListEvidence
		if err := json.Unmarshal(receipt.Evidence, &evidence); err != nil {
			// An old or damaged receipt cannot establish delivery. Keep it
			// unknown and leave explicit review to the caller.
			continue
		}
		if evidence.ProfileID != binding.ProfileID || evidence.AccountID != binding.AccountID ||
			evidence.CharacterID != binding.CharacterID || evidence.Generation != binding.Generation ||
			evidence.SessionToken == "" || evidence.SessionToken != snapshot.SessionToken ||
			snapshot.AddressBookRevision == 0 || snapshot.AddressBookRevision <= evidence.BeforeAddressBookRevision ||
			snapshot.Revision <= evidence.BeforeRevision || !snapshot.AddressBookKnown {
			continue
		}
		after := &mailListObservation{
			SessionToken:        snapshot.SessionToken,
			Revision:            snapshot.Revision,
			AddressBookRevision: snapshot.AddressBookRevision,
			AddressBookKnown:    snapshot.AddressBookKnown,
			AddressBook:         copyAddressBook(snapshot.AddressBook),
		}
		evidence.GameOutcomeConfirmed = true
		evidence.After = after
		receipt.Status = aimcp.ReceiptConfirmed
		receipt.Reason = "server observed a new complete address-book table after mail/list submission"
		receipt.Evidence, err = json.Marshal(evidence)
		if err != nil {
			return err
		}
		if err := b.Receipts.Save(ctx, binding, receipt); err != nil {
			return err
		}
	}
	return nil
}
