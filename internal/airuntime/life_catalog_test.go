package airuntime

import (
	"strings"
	"testing"
)

func TestLifeActivityCatalogIsReviewedAndDistinct(t *testing.T) {
	presets := ListLifeActivityPresets()
	if len(presets) != 24 {
		t.Fatalf("default preset count = %d, want 24", len(presets))
	}
	seen := make(map[string]bool, len(presets))
	for _, preset := range presets {
		if preset.Kind == "" || preset.Label == "" || preset.Description == "" || preset.Completion == "" || preset.Fallback == "" {
			t.Fatalf("incomplete life preset: %+v", preset)
		}
		if seen[preset.Kind] {
			t.Fatalf("duplicate life preset kind %q", preset.Kind)
		}
		seen[preset.Kind] = true
		if !ValidLifeActivity(preset.Kind) {
			t.Fatalf("catalog kind is not valid: %q", preset.Kind)
		}
	}
	for _, legacy := range []string{"idle", "rest", "wander", "chat", "leveling", "quest"} {
		if !seen[legacy] {
			t.Fatalf("legacy activity %q missing from default pool", legacy)
		}
	}
	for _, required := range []string{
		"observe", "village-explore", "chat-nearby", "mail-check", "mail-send",
		"pet-care", "inventory-sort", "memory-plan", "schedule-reminder", "stat-allocation",
	} {
		if !seen[required] {
			t.Fatalf("required life activity %q missing from default pool", required)
		}
	}
}

func TestLifeActivityCatalogLookupAndCopy(t *testing.T) {
	preset, ok := LookupLifeActivityPreset("mail")
	if !ok || preset.Kind != "mail-check" || preset.Label == "" {
		t.Fatalf("mail alias lookup = %+v, %v", preset, ok)
	}
	if alias, ok := LookupLifeActivity("allocate-stat"); !ok || alias.Kind != "stat-allocation" {
		t.Fatalf("allocate-stat alias lookup = %+v, %v", alias, ok)
	}
	if _, ok := LookupLifeActivityPreset("not-a-life-activity"); ok || ValidLifeActivity("not-a-life-activity") {
		t.Fatal("unknown life activity accepted")
	}

	first := ListLifeActivityPresets()
	first[0].Kind = "changed"
	first[0].Description = "changed"
	again, ok := LookupLifeActivityPreset("idle")
	if !ok || again.Kind != "idle" || again.Description == "changed" {
		t.Fatalf("catalog result aliases mutable data: %+v, %v", again, ok)
	}
	if got := len(DefaultLifeActivityPresets()); got != len(ListLifeActivities()) {
		t.Fatalf("catalog aliases disagree: %d", got)
	}
}

func TestLifeActivityValidationUsesCatalogAndCanonicalDuplicates(t *testing.T) {
	goal := Goal{Kind: "life", Life: &LifePolicy{Activities: DefaultLifeActivityKinds()}}
	if err := goal.ValidateLife(); err != nil {
		t.Fatalf("full default pool rejected: %v", err)
	}
	if got := goal.LifeActivities(); len(got) != len(DefaultLifeActivityKinds()) {
		t.Fatalf("default life pool count = %d", len(got))
	}
	goal.Life.Activities = []string{"mail", "mail-check"}
	if err := goal.ValidateLife(); err == nil {
		t.Fatal("canonical duplicate aliases accepted")
	}
	goal.Life.Activities = append([]string(nil), DefaultLifeActivityKinds()...)
	goal.Life.Activities[0] = "idle"
	copy := goal.LifeActivities()
	copy[0] = "rest"
	if goal.Life.Activities[0] != "idle" {
		t.Fatal("life activity selection aliases returned slice")
	}
}

func TestLifeActivityDefaultPoolIsCatalogWhenUnset(t *testing.T) {
	goal := Goal{Kind: "life", Life: &LifePolicy{}}
	got := goal.LifeActivities()
	want := DefaultLifeActivityKinds()
	if len(got) != len(want) {
		t.Fatalf("unset life pool count = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unset life pool[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestLifeActivityDescriptionsMatchMCPContracts(t *testing.T) {
	byKind := make(map[string]LifeActivityPreset)
	for _, preset := range ListLifeActivityPresets() {
		byKind[preset.Kind] = preset
	}
	checks := map[string][]string{
		"inventory-sort":    {"kind=item", "command=move", "expected_revision"},
		"mail-check":        {"kind=mail", "command=list", "AddressBookKnown"},
		"mail-contact":      {"kind=mail", "command=add", "x/y"},
		"mail-send":         {"kind=mail", "command=send", "地址簿"},
		"memory-recall":     {"game_memory_list"},
		"memory-plan":       {"game_memory_write"},
		"schedule-review":   {"game_schedule_list"},
		"schedule-reminder": {"game_schedule_create", "幂等"},
		"stat-allocation":   {"build:allocation_allowed", "stat_points", "build_next_stat", "allocate-stat"},
		"leveling":          {"game_start_leveling", "game_task_status"},
		"quest":             {"game_query_knowledge", "game_start_task", "game_task_status"},
	}
	for kind, markers := range checks {
		preset, ok := byKind[kind]
		if !ok {
			t.Fatalf("contract check references missing activity %q", kind)
		}
		text := preset.Description + "\n" + preset.Completion + "\n" + preset.Fallback
		for _, marker := range markers {
			if !strings.Contains(text, marker) {
				t.Errorf("%s description omitted contract marker %q: %s", kind, marker, text)
			}
		}
	}
}
