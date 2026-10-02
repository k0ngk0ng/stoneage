package aigame

import "testing"

func TestMagicIdentityAndUnequip(t *testing.T) {
	s := &Session{state: newGameState(false)}
	const prefix = "J1|1|8|0|1|name\\zpipe|memo|"
	s.applyEvent(stringEvent("S", prefix+"id=10|"))
	m := s.Snapshot().Magic[0]
	if !m.IDKnown || m.ID != 10 || m.MP != 8 || m.Name != "name|pipe" {
		t.Fatal(m)
	}
	v := decisionFixture()
	v.Magic = []MagicSnapshot{m}
	for _, c := range NewBattleView(v).Candidates {
		if c.Kind == "magic" && (!c.MagicIDKnown || c.MagicID != 10 || c.MPCost != 8) {
			t.Fatal("candidate lost atomic spell metadata", c)
		}
	}
	for _, suffix := range []string{"", "id=x|", "id=-1|", "id=2147483648|", "id=10|id=20|"} {
		s.applyEvent(stringEvent("S", prefix+suffix))
		if got := s.Snapshot().Magic[0]; got.IDKnown || got.ID != 0 {
			t.Fatal("old or invalid metadata retained earlier spell ID", got)
		}
	}
	s.applyEvent(stringEvent("S", prefix+"id=0|"))
	if !s.Snapshot().Magic[0].IDKnown {
		t.Fatal("zero is a valid spell ID")
	}
	s.applyEvent(stringEvent("S", "J1|0|"))
	m = s.Snapshot().Magic[0]
	if m.UseFlag != 0 || m.IDKnown || m.Name != "" {
		t.Fatal("unequip retained spell", m)
	}
	v.Magic = []MagicSnapshot{m}
	for _, c := range NewBattleView(v).Candidates {
		if c.Kind == "magic" {
			t.Fatal("unequipped spell remains executable")
		}
	}
}
