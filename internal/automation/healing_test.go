package automation

import "testing"

func TestHealingCompletionRequiresLivePositiveFullHP(t *testing.T) {
	c := Condition{Kind: "character_hp_full"}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*Observation)
		want   bool
	}{
		{"full", func(*Observation) {}, true},
		{"injured", func(o *Observation) { o.Character.HP = 1 }, false},
		{"unknown", func(o *Observation) { o.Character.HP = 0; o.Character.MaxHP = 0 }, false},
		{"disconnected", func(o *Observation) { o.Connected = false }, false},
		{"not-ready", func(o *Observation) { o.Ready = false }, false},
		{"in-battle", func(o *Observation) { o.Battle = true }, false},
		{"dead", func(o *Observation) { o.Dead = true }, false},
	} {
		o := Observation{Connected: true, Ready: true, Character: Entity{HP: 29, MaxHP: 29}}
		tc.change(&o)
		if got := c.Match(o); got != tc.want {
			t.Fatalf("%s matched=%v", tc.name, got)
		}
	}
}

func TestHealthPercentageUsesObservedRoundedThreshold(t *testing.T) {
	c := Condition{Kind: "character_hp_percent", Value: 50}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	o := Observation{Connected: true, Ready: true, Character: Entity{HP: 14, MaxHP: 29}}
	if c.Match(o) {
		t.Fatal("14/29 accepted as half health")
	}
	o.Character.HP = 15
	if !c.Match(o) {
		t.Fatal("15/29 rejected")
	}
	o.Character.MaxHP = 0
	if c.Match(o) {
		t.Fatal("unknown maximum accepted")
	}
	for _, percent := range []int64{0, 101} {
		if err := (Condition{Kind: "character_hp_percent", Value: percent}).Validate(); err == nil {
			t.Fatal("invalid percentage accepted")
		}
	}
}
