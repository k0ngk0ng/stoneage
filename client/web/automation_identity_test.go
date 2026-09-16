package main

import (
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
)

func TestAutomationIdentityMatchesAllBindingComponents(t *testing.T) {
	base := automationIdentity{AccountID: "acct", CharacterID: "acct:2", CharacterName: "Hero", ServerID: "line-a"}
	for name, changed := range map[string]automationIdentity{
		"account":    {AccountID: "other", CharacterID: base.CharacterID, CharacterName: base.CharacterName, ServerID: base.ServerID},
		"slot":       {AccountID: base.AccountID, CharacterID: "acct:1", CharacterName: base.CharacterName, ServerID: base.ServerID},
		"name":       {AccountID: base.AccountID, CharacterID: base.CharacterID, CharacterName: "Other", ServerID: base.ServerID},
		"server":     {AccountID: base.AccountID, CharacterID: base.CharacterID, CharacterName: base.CharacterName, ServerID: "line-b"},
		"incomplete": {AccountID: base.AccountID, CharacterID: base.CharacterID, CharacterName: base.CharacterName},
	} {
		t.Run(name, func(t *testing.T) {
			if base.matches(changed) {
				t.Fatalf("identity unexpectedly matched %+v", changed)
			}
		})
	}
	if !base.matches(automationIdentity{AccountID: " acct ", CharacterID: "acct:2", CharacterName: " Hero ", ServerID: "line-a"}) {
		t.Fatal("whitespace normalization should preserve the same identity")
	}
}

func TestAutomationIdentityFromSnapshotRequiresCanonicalCharacterSlot(t *testing.T) {
	snapshot := aigame.Snapshot{
		Account:   "acct",
		Character: "Hero",
		Characters: []aigame.Character{
			{Slot: 0, Name: "Other"},
			{Slot: 2, Name: "Hero"},
		},
	}
	identity, ok := automationIdentityFromSnapshot(snapshot, "line-a")
	if !ok {
		t.Fatal("canonical character identity should be derived")
	}
	want := automationIdentity{AccountID: "acct", CharacterID: "acct:2", CharacterName: "Hero", ServerID: "line-a"}
	if !identity.matches(want) {
		t.Fatalf("identity=%+v want=%+v", identity, want)
	}

	snapshot.Characters = append(snapshot.Characters, aigame.Character{Slot: 1, Name: "Hero"})
	if _, ok := automationIdentityFromSnapshot(snapshot, "line-a"); ok {
		t.Fatal("ambiguous character name accepted")
	}
	snapshot.Characters = nil
	if _, ok := automationIdentityFromSnapshot(snapshot, "line-a"); ok {
		t.Fatal("slotless character must not become recoverable")
	}
}

func TestAutomationIdentityFromBindingUsesObservedNameOnlyAsFallback(t *testing.T) {
	binding := aimcp.Binding{AccountID: "acct", CharacterID: "acct:2"}
	snapshot := aigame.Snapshot{Account: "acct", Character: "Hero", Characters: []aigame.Character{{Slot: 2, Name: "Hero"}}}
	identity, ok := automationIdentityFromBinding(binding, snapshot, "line-a")
	if !ok || !identity.matches(automationIdentity{AccountID: "acct", CharacterID: "acct:2", CharacterName: "Hero", ServerID: "line-a"}) {
		t.Fatalf("identity=%+v ok=%v", identity, ok)
	}
	snapshot.Account = "other"
	if _, ok := automationIdentityFromBinding(binding, snapshot, "line-a"); ok {
		t.Fatal("binding accepted a different observed account")
	}
	snapshot.Account = "acct"
	if _, ok := automationIdentityFromBinding(binding, snapshot, ""); ok {
		t.Fatal("missing server line must not become recoverable")
	}
}
