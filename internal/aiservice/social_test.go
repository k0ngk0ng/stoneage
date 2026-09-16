package aiservice

import (
	"context"
	"testing"
)

func TestSocialSettingsDistinguishUnknownAndDisabled(t *testing.T) {
	b, session := gameFixture(t)
	o, err := b.Observe(context.Background(), b.Binding)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := o.Flags["social:trade"]; ok {
		t.Fatal("unknown setting became disabled fact")
	}
	session.snapshot.Player.SocialFlagsKnown = true
	session.snapshot.Player.SocialFlags = 33
	o, err = b.Observe(context.Background(), b.Binding)
	if err != nil || !o.Flags["social:known"] || !o.Flags["social:party"] || !o.Flags["social:trade"] {
		t.Fatalf("settings: %+v %v", o.Flags, err)
	}
	if value, ok := o.Flags["social:duel"]; !ok || value {
		t.Fatal("known disabled duel not represented")
	}
}
