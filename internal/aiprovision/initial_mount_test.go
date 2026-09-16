package aiprovision

import (
	"context"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aiinitial"
)

func TestMountedBirthUsesDurableInitialization(t *testing.T) {
	ctx := context.Background()
	connector := &fakeConnector{sessions: []*fakeHeadlessSession{{}}}
	fixture := newProviderFixture(t, connector)
	p, err := New(Config{ProviderConfig: ProviderConfig{Auth: fixture.auth, Secrets: fixture.secrets, Game: connector}, Profiles: fixture.profiles})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Provider().Close()
	enabled := true
	initializer := &fakeInitializer{}
	request := CreateRequest{ProfileID: "mounted-birth", CharacterName: "Scout", Initializer: initializer, InitialState: &aiinitial.Request{Mode: "birth", Mount: &enabled, Pets: []aiinitial.Pet{{TemplateID: 42, Level: 1}}}}
	if _, err := p.CreateAI(ctx, request); err != nil {
		t.Fatal(err)
	}
	record, err := p.InitialState(ctx, request.ProfileID)
	if err != nil || record == nil || record.Status != "published" || !record.Resolved.Mount || record.Resolved.CharacterLevel != 1 || initializer.calls != 1 {
		t.Fatalf("birth skipped initialization: %+v %v", record, err)
	}
}

func TestRandomMountChecksWholeCandidateRange(t *testing.T) {
	ctx := context.Background()
	fixture := newProviderFixture(t, &fakeConnector{})
	p, err := New(Config{ProviderConfig: ProviderConfig{Auth: fixture.auth, Secrets: fixture.secrets, Game: &fakeConnector{}}, Profiles: fixture.profiles})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Provider().Close()
	enabled := true
	initializer := &fakeInitializer{}
	request := &aiinitial.Request{Mode: "random", Mount: &enabled, Random: &aiinitial.RandomRules{CharacterLevel: aiinitial.Range{Min: 35, Max: 60}, PetLevel: aiinitial.Range{Min: 1, Max: 30}, PetCount: aiinitial.Range{Min: 1, Max: 2}, PetTemplates: []int{42, 43}, Hometowns: []int{0}}}
	if _, err := p.reserveInitial(ctx, "mounted-random", request, initializer); err != nil {
		t.Fatal(err)
	}
	if len(initializer.validated) != 3 {
		t.Fatalf("validation count: %d", len(initializer.validated))
	}
	for i, probe := range initializer.validated[:2] {
		if !probe.Mount || probe.CharacterLevel != 35 || len(probe.Pets) != 1 || probe.Pets[0].TemplateID != 42+i || probe.Pets[0].Level != 30 {
			t.Fatalf("candidate probe: %+v", probe)
		}
	}
}
