package aiservice

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/k0ngk0ng/stoneage/internal/automation"
)

var ErrTravelHealingLimit = errors.New("travel item recovery limit reached")

type MovementHealthRecovery interface{ Heal(context.Context) error }

// TravelItemRecovery consumes at most one reviewed item per call. Movement
// rechecks actual health before every subsequent W/warp; base HP never proves
// readiness. Missing supplies and ambiguous outcomes stop the journey.
type TravelItemRecovery struct{ Healing *ItemHealingSkill }

func (r *TravelItemRecovery) Heal(ctx context.Context) error {
	if r == nil || r.Healing == nil || r.Healing.Backend == nil {
		return ErrHealingItemUnavailable
	}
	b := r.Healing.Backend
	snapshot, err := refreshInventoryIdentity(ctx, NewNPCSkill(b, nil))
	if err != nil {
		return err
	}
	if !snapshot.Player.HasStatus || snapshot.Player.MaxHP <= 0 || snapshot.Player.HP <= 0 || snapshot.Player.HP > snapshot.Player.MaxHP {
		return ErrTravelHealingRequired
	}
	if snapshot.Player.HP >= snapshot.Player.MaxHP/2+snapshot.Player.MaxHP%2 {
		return nil
	}
	aliases := make([]string, 0, len(r.Healing.Contracts))
	for alias, c := range r.Healing.Contracts {
		if !c.Verified || c.BaseHP <= 0 || b.Knowledge == nil || c.SourceFingerprint != b.Knowledge.Fingerprint() {
			continue
		}
		for _, item := range snapshot.AI.Items {
			if item.TemplateID == c.TemplateID && item.Slot >= 5 && item.Slot < 20 {
				aliases = append(aliases, alias)
				break
			}
		}
	}
	if len(aliases) == 0 {
		return ErrHealingItemUnavailable
	}
	// Stable selection from the configured aliases. No unreviewed replacement,
	// purchase, or guessed template/slot mapping is allowed.
	sort.Strings(aliases)
	args, err := json.Marshal(map[string]string{"item": aliases[0]})
	if err != nil {
		return err
	}
	// Never retry this whole call: an error may follow a successful ID write.
	return r.Healing.Execute(ctx, automation.Action{Skill: "item.heal", ExpectedRevision: snapshot.Revision, Arguments: args})
}
