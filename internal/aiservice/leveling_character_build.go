package aiservice

import (
	"context"
	"errors"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
)

// LevelingCharacterBuild applies the immutable administrator-selected build
// between acknowledged leveling actions. It uses the same durable single-point
// receipts as other game operations; it does not choose goals or change policy.
type LevelingCharacterBuild struct {
	Backend *GameBackend
}

func (p *LevelingCharacterBuild) PrepareCharacter(ctx context.Context, s aigame.Snapshot) (bool, error) {
	if p == nil || p.Backend == nil || ctx == nil {
		return false, aimcp.ErrBackend
	}
	b := p.Backend
	if err := b.check(b.Binding); err != nil {
		return false, err
	}
	if !characterBuildOwner(b.Owner) || b.CharacterBuild == nil || b.Receipts == nil {
		return false, errors.New("configured character build and receipt store required")
	}
	if err := b.CharacterBuild.Validate(); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if s.Account != b.Binding.AccountID || s.Character != b.Binding.CharacterName {
		return false, aimcp.ErrInvalidBinding
	}
	if !s.Connected || s.Phase != aigame.PhaseWorld || !s.Player.HasStatus || s.Player.HP <= 0 ||
		s.Battle.Active || s.Trade.Active || s.Trade.Pending {
		return false, errors.New("character build requires a living idle character outside battle and trade")
	}
	if err := b.reconcileStatAllocations(ctx, s); err != nil {
		return false, err
	}
	pending, err := b.Receipts.HasUnknownStateChange(ctx, b.Binding, "")
	if err != nil {
		return false, err
	}
	if pending {
		// A same-session allocation can wait for fresh read-only evidence.
		// Unknown actions from a prior lease/process must remain paused.
		handle := b.liveStatAllocationHandle()
		other, err := b.Receipts.HasUnknownStateChange(ctx, b.Binding, handle)
		if err != nil {
			return false, err
		}
		if other {
			return false, errors.New("unresolved prior game action blocks automatic character build")
		}
		if handle == "" {
			// Another observer just confirmed the receipt. Re-observe on the
			// next tick instead of acting on this older snapshot.
			return false, nil
		}
		return false, p.refresh(ctx)
	}
	if s.Player.StatPointsKnown && s.Player.UnspentStatPoints >= 0 && s.Player.UnspentStatPoints <= int32(b.CharacterBuild.ReservePoints) {
		return true, nil
	}
	index, allowed := b.nextBuildAttribute(s)
	if !allowed {
		// In particular, a level-up invalidates the old available-point
		// count. Read-only queries cannot spend or recreate those points.
		return false, p.refresh(ctx)
	}
	r, err := b.gameAction(ctx, b.Binding, aimcp.TypedAction{Kind: "allocate-stat", Index: int32(index), ExpectedRevision: s.Revision}, true)
	if errors.Is(err, aigame.ErrStaleRevision) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if r.Handle == "" {
		return false, errors.New("character allocation has no durable receipt")
	}
	// Even a synchronous server update is checked by the next observation.
	// No second game action is allowed in this coordinator tick.
	return false, nil
}

func (p *LevelingCharacterBuild) refresh(ctx context.Context) error {
	if p.Backend.OwnStateRefresh == nil {
		return errors.New("character build requires an own-state refresh adapter")
	}
	_, err := p.Backend.Observe(ctx, p.Backend.Binding)
	return err
}

func (b *GameBackend) liveStatAllocationHandle() string {
	b.statMu.Lock()
	defer b.statMu.Unlock()
	for handle, evidence := range b.statAllocations {
		if evidence.Generation == b.Binding.Generation && evidence.CharacterID == b.Binding.CharacterID {
			return handle
		}
	}
	return ""
}
