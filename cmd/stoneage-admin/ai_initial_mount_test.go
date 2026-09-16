package main

import (
	"context"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aiinitial"
	"github.com/k0ngk0ng/stoneage/internal/playerdata"
)

type initialMountProvider struct{ defaults int }

func (p *initialMountProvider) Validate(context.Context, aiinitial.Resolved) error { return nil }
func (p *initialMountProvider) Apply(context.Context, string, int, aiinitial.Resolved) (playerdata.Snapshot, error) {
	panic("unexpected mutation")
}
func (p *initialMountProvider) DefaultMount(context.Context) (aiinitial.Pet, error) {
	p.defaults++
	return aiinitial.Pet{TemplateID: 42, Level: 1}, nil
}

func TestNewAICreationsMountByDefaultAndPreserveOptOut(t *testing.T) {
	provider := &initialMountProvider{}
	adapter := &aiPlayerProvisionerAdapter{initializer: provider}
	got, err := adapter.initialRequest(context.Background(), nil)
	if err != nil || got.Mount == nil || !*got.Mount || len(got.Pets) != 1 || got.Pets[0].TemplateID != 42 {
		t.Fatalf("default birth: %+v %v", got, err)
	}
	disabled := false
	requested := &aiinitial.Request{Mode: "birth", Mount: &disabled}
	got, err = adapter.initialRequest(context.Background(), requested)
	if err != nil || *got.Mount || len(got.Pets) != 0 || provider.defaults != 1 {
		t.Fatalf("opt out: %+v %v", got, err)
	}
	if len(requested.Pets) != 0 {
		t.Fatal("caller request mutated")
	}
	enabled := true
	chosen := &aiinitial.Request{Mode: "birth", Mount: &enabled, Pets: []aiinitial.Pet{{TemplateID: 77, Level: 1}}}
	got, err = adapter.initialRequest(context.Background(), chosen)
	if err != nil || got.Pets[0].TemplateID != 77 || provider.defaults != 1 {
		t.Fatalf("explicit birth pet was replaced: %+v %v", got, err)
	}
	adapter.initializer = nil
	if _, err := adapter.initialRequest(context.Background(), nil); err == nil {
		t.Fatal("silently created walking AI without mount service")
	}
}
