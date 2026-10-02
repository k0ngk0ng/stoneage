package aigame

import (
	"errors"
	"fmt"
	"testing"

	"github.com/k0ngk0ng/stoneage/server/go/namedproto"
)

func TestPetCapacitySeparateFromOwnedSlot(t *testing.T) {
	s := &Session{state: newGameState(false)}
	full := func(capacity, rebirth string) {
		s.applyEvent(stringEvent("S", fmt.Sprintf("K2|1|100|80|100|0|0|10|100|3|20|21|22|95|10|20|30|40|%s|1|%s|pet|nickname|", capacity, rebirth)))
	}
	full("3", "0")
	p := s.Snapshot().Pets[0]
	if p.Slot != 2 || p.SkillSlots != 3 || !p.SkillSlotsKnown || !p.TransmigrationKnown || p.Transmigration != 0 || p.Wind != 40 || p.Loyalty != 95 || p.Name != "pet" {
		t.Fatal("full K capacity, owned slot or field order lost", p)
	}
	if !p.BattleSkillIndexAllowed(2) || p.BattleSkillIndexAllowed(3) {
		t.Fatal("untransmigrated native capacity ignored")
	}
	// Every missing numeric mask bit used to shift capacity/names. Include
	// all of them in one update and retain the existing continuity identity.
	mask := int32(4096 | 8192 | 16384 | 32768 | 65536 | 131072 | 262144 | 524288 | 1048576)
	s.applyEvent(stringEvent("S", "K2|"+namedproto.EncodeInt(mask)+"|96|40|30|20|10|5|0|new\\zname|new nickname|"))
	q := s.Snapshot().Pets[0]
	if q.Slot != 2 || q.Identity != p.Identity || q.SkillSlots != 5 || !q.SkillSlotsKnown || q.Loyalty != 96 || q.Earth != 40 || q.Water != 30 || q.Fire != 20 || q.Wind != 10 || q.ChangeNameFlag != 0 || q.Name != "new|name" || q.FreeName != "new nickname" {
		t.Fatal("masked K fields shifted or replaced identity", q)
	}
	for _, invalid := range []string{"", "-1", "8", "bogus", "2147483648"} {
		s.applyEvent(stringEvent("S", "K2|"+namedproto.EncodeInt(131072)+"|"+invalid+"|"))
		if s.Snapshot().Pets[0].SkillSlotsKnown {
			t.Fatal("invalid capacity considered known", invalid)
		}
	}
	full("0", "1")
	p = s.Snapshot().Pets[0]
	if !p.BattleSkillIndexAllowed(6) || p.BattleSkillIndexAllowed(7) || p.BattleSkillIndexAllowed(-1) {
		t.Fatal("native transmigration exception or hard limit wrong", p)
	}
	full("0", "0")
	if s.Snapshot().Pets[0].BattleSkillIndexAllowed(0) {
		t.Fatal("known zero capacity interpreted as unknown")
	}
	full("0", "")
	if p = s.Snapshot().Pets[0]; p.TransmigrationKnown || !p.BattleSkillIndexAllowed(0) {
		t.Fatal("unknown transmigration treated as zero", p)
	}
}

func TestPetCapacityGatesCandidatesAndSharedWrites(t *testing.T) {
	s := decisionFixture()
	p := &s.Pets[0]
	p.SkillSlots, p.SkillSlotsKnown, p.TransmigrationKnown = 1, true, true
	p.Skills = append(p.Skills, PetSkillSnapshot{Index: 1, ID: 3, Name: "break", Field: 1, Target: 6})
	for _, c := range NewBattleView(s).Candidates {
		if c.Actor == "pet" && c.Kind == "skill" && c.Index >= 1 {
			t.Fatal("unusable native skill offered to commander", c)
		}
	}
	s.Battle.PlayerSubmitted = true
	state := newGameState(false)
	state.snapshot = s
	state.replacePet(0, *p)
	if _, _, err := validateActionLocked(&state, Battle("W|1|A")); !errors.Is(err, ErrInvalidAction) {
		t.Fatal("raw CLI/typed Web action bypassed shared capacity gate", err)
	}
	for _, cmd := range []string{"W|0|A", "W|FF|FF"} {
		if _, _, err := validateActionLocked(&state, Battle(cmd)); err != nil {
			t.Fatal("valid skill/default rejected", cmd, err)
		}
	}
	p.Transmigration = 1
	state.replacePet(0, *p)
	if _, _, err := validateActionLocked(&state, Battle("W|1|A")); err != nil {
		t.Fatal("transmigrated pet incorrectly restricted", err)
	}
}
