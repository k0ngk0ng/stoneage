package automation

import "testing"

func TestBattleConditionRequiresConnectedBattle(t *testing.T) {
	c := Condition{Kind: "battle"}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, o := range []Observation{
		{},
		{Connected: true, Ready: true},
		{Battle: true},
		{Connected: true, Battle: true},
	} {
		if got := c.Match(o); got != (o.Connected && o.Battle) {
			t.Fatalf("observation %+v: match=%v", o, got)
		}
	}
}
