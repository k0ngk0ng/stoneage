package aiinitial

import (
	"bytes"
	"github.com/k0ngk0ng/stoneage/internal/characterbuild"
	"io"
	"strings"
	"testing"
)

func TestResolveCreationModes(t *testing.T) {
	for _, request := range []*Request{nil, {Mode: "birth"}} {
		got, err := Resolve(request, nil)
		if err != nil || got != nil {
			t.Fatalf("birth: %v %v", got, err)
		}
	}
	request := &Request{Mode: "custom", CharacterLevel: 70, Hometown: 3, Weights: characterbuild.Weights{Vital: 1, Strength: 3}, Pets: []Pet{{TemplateID: 42, Level: 60}}}
	got, err := Resolve(request, nil)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := got.Payload()
	if err != nil || payload != "1|70|1|3|0|0|1|42:60" {
		t.Fatalf("payload: %q %v", payload, err)
	}
	request.Pets[0].Level = 1
	if got.Pets[0].Level != 60 {
		t.Fatal("resolved state aliases request")
	}
}

func TestMountedInitialModesAndPayload(t *testing.T) {
	mount := true
	custom := &Request{Mode: "custom", CharacterLevel: 70, Hometown: 3, Weights: characterbuild.Weights{Vital: 1, Strength: 3}, Pets: []Pet{{TemplateID: 42, Level: 60}}, Mount: &mount}
	resolved, err := Resolve(custom, nil)
	if err != nil || resolved == nil || !resolved.Mount {
		t.Fatalf("mounted custom state: %+v %v", resolved, err)
	}
	payload, err := resolved.Payload()
	if err != nil || payload != "2|70|1|3|0|0|1|1|42:60" {
		t.Fatalf("mounted payload: %q %v", payload, err)
	}

	birth := &Request{Mode: "birth", Mount: &mount}
	if err := birth.Validate(); err != nil {
		t.Fatalf("birth mount reservation should allow an empty default-pet slot: %v", err)
	}
	if _, err := Resolve(birth, nil); err == nil {
		t.Fatal("birth mount resolved without a default pet")
	}
	birth.Pets = []Pet{{TemplateID: 42, Level: 1}}
	resolved, err = Resolve(birth, nil)
	if err != nil || resolved.CharacterLevel != 1 || resolved.Hometown != 0 || resolved.Weights != (characterbuild.Weights{Vital: 1, Strength: 1, Toughness: 1, Dexterity: 1}) || len(resolved.Pets) != 1 || resolved.Pets[0].Level != 1 || !resolved.Mount {
		t.Fatalf("mounted birth state: %+v %v", resolved, err)
	}

	for name, request := range map[string]*Request{
		"birth without mount and pets": {Mode: "birth", Pets: []Pet{{TemplateID: 42, Level: 1}}},
		"birth mount wrong pet level":  {Mode: "birth", Mount: &mount, Pets: []Pet{{TemplateID: 42, Level: 2}}},
		"birth mount too many pets":    {Mode: "birth", Mount: &mount, Pets: []Pet{{TemplateID: 42, Level: 1}, {TemplateID: 43, Level: 1}}},
		"custom mount without pets":    {Mode: "custom", Mount: &mount, CharacterLevel: 1, Weights: characterbuild.Weights{Vital: 1}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := request.Validate(); err == nil {
				t.Fatal("accepted invalid mounted initial state")
			}
		})
	}

	legacyBirth := &Request{Mode: "birth"}
	if got, err := Resolve(legacyBirth, nil); err != nil || got != nil {
		t.Fatalf("legacy birth state changed: %+v %v", got, err)
	}
}

func TestMountedRandomRequiresAPetAndCarriesMount(t *testing.T) {
	mount := true
	base := func(count Range) *Request {
		return &Request{Mode: "random", Mount: &mount, Random: &RandomRules{
			CharacterLevel: Range{Min: 10, Max: 10}, PetLevel: Range{Min: 5, Max: 5}, PetCount: count,
			PetTemplates: []int{42}, Hometowns: []int{0},
		}}
	}
	if err := base(Range{Min: 0, Max: 1}).Validate(); err == nil {
		t.Fatal("mounted random state accepted a zero pet lower bound")
	}
	resolved, err := Resolve(base(Range{Min: 1, Max: 1}), nil)
	if err != nil || resolved == nil || !resolved.Mount || len(resolved.Pets) != 1 || resolved.Pets[0].Level != 5 {
		t.Fatalf("mounted random state: %+v %v", resolved, err)
	}
}

func TestRandomBoundsAndEntropyFailure(t *testing.T) {
	request := &Request{Mode: "random", Random: &RandomRules{CharacterLevel: Range{10, 60}, PetLevel: Range{5, 30}, PetCount: Range{1, 2}, PetTemplates: []int{42, 43}, Hometowns: []int{0, 3}}}
	for i := 0; i < 100; i++ {
		got, err := Resolve(request, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got.CharacterLevel < 10 || got.CharacterLevel > 60 || (got.Hometown != 0 && got.Hometown != 3) || len(got.Pets) < 1 || len(got.Pets) > 2 {
			t.Fatalf("outside bounds: %+v", got)
		}
		for _, p := range got.Pets {
			if p.Level < 5 || p.Level > 30 || (p.TemplateID != 42 && p.TemplateID != 43) {
				t.Fatalf("pet outside bounds: %+v", p)
			}
		}
	}
	if _, err := Resolve(request, bytes.NewReader(nil)); err != io.EOF {
		t.Fatalf("entropy error: %v", err)
	}
	got, err := Resolve(request, strings.NewReader(strings.Repeat("\x00", 100)))
	if err != nil || got.CharacterLevel != 10 || got.Pets[0].Level != 5 {
		t.Fatalf("minimum: %+v %v", got, err)
	}
}
func TestRejectInvalidInitialState(t *testing.T) {
	requests := []*Request{
		{Mode: "birth", CharacterLevel: 2}, {Mode: "custom", CharacterLevel: 141, Weights: characterbuild.Weights{Vital: 1}},
		{Mode: "custom", CharacterLevel: 1}, {Mode: "custom", CharacterLevel: 1, Hometown: 4, Weights: characterbuild.Weights{Vital: 1}},
		{Mode: "random", Random: &RandomRules{CharacterLevel: Range{60, 10}, PetLevel: Range{1, 1}, PetCount: Range{0, 0}, Hometowns: []int{0}}},
		{Mode: "random", Random: &RandomRules{CharacterLevel: Range{1, 1}, PetLevel: Range{1, 1}, PetCount: Range{1, 1}, Hometowns: []int{0}}},
		{Mode: "random", Random: &RandomRules{CharacterLevel: Range{1, 1}, PetLevel: Range{1, 1}, PetCount: Range{0, 0}, Hometowns: []int{0, 0}}},
	}
	for i, r := range requests {
		if _, err := Resolve(r, nil); err == nil {
			t.Errorf("accepted invalid request %d", i)
		}
	}
}
