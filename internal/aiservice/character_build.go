package aiservice

import (
	"context"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
)

func (b *GameBackend) nextBuildAttribute(s aigame.Snapshot) (int, bool) {
	if !s.Connected || s.Phase != aigame.PhaseWorld || !s.Player.HasStatus ||
		s.Player.HP <= 0 || s.Battle.Active || s.Trade.Active || s.Trade.Pending {
		return 0, false
	}
	return b.CharacterBuild.NextAttribute(
		[4]int32{s.Player.Vital, s.Player.Strength, s.Player.Toughness, s.Player.Dexterity},
		s.Player.UnspentStatPoints, s.Player.StatPointsKnown)
}

func characterBuildOwner(owner aicontrol.Mode) bool {
	return owner == aicontrol.Agent || owner == aicontrol.Leveling
}

func (b *GameBackend) projectCharacterBuild(ctx context.Context, o *aimcp.Observation, s aigame.Snapshot) error {
	if !characterBuildOwner(b.Owner) || b.CharacterBuild == nil || b.CharacterBuild.Validate() != nil {
		return nil
	}
	o.Flags["build:configured"] = true
	o.OwnProgress["build_reserve_points"] = b.CharacterBuild.ReservePoints
	if b.Receipts == nil {
		return nil
	}
	pending, err := b.Receipts.HasUnknownStateChange(ctx, b.Binding, "")
	if err != nil {
		return err
	}
	if pending {
		return nil
	}
	active, err := b.Active(ctx)
	if err != nil {
		return err
	}
	if len(active) != 0 {
		return nil
	}
	if s.Connected && s.Phase == aigame.PhaseWorld && s.Player.HasStatus && s.Player.HP > 0 &&
		!s.Battle.Active && !s.Trade.Active && !s.Trade.Pending && s.Player.StatPointsKnown &&
		s.Player.UnspentStatPoints >= 0 && s.Player.UnspentStatPoints <= int32(b.CharacterBuild.ReservePoints) {
		o.Flags["build:settled"] = true
	}
	if index, ok := b.nextBuildAttribute(s); ok {
		o.Flags["build:allocation_allowed"] = true
		o.OwnProgress["build_next_stat"] = index
	}
	return nil
}
