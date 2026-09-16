package automation

import "testing"

func TestBackpackFreeSlotsRequiresAuthoritativeCapacity(t *testing.T) {
	c := Condition{Kind: "backpack_free_slots", Value: 2}
	for _, tc := range []struct {
		name               string
		used               int
		known, flag, match bool
	}{
		{"empty", 0, true, true, true},
		{"exact", 13, true, true, true},
		{"insufficient", 14, true, true, false},
		{"unknown-count", 0, false, true, false},
		{"unknown-inventory", 0, true, false, false},
		{"invalid-negative", -1, true, true, false},
		{"invalid-overflow", 16, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := Observation{Flags: map[string]bool{"inventory:known": tc.flag}, OwnProgress: map[string]int{}}
			if tc.known {
				o.OwnProgress["backpack_used_slots"] = tc.used
			}
			if c.Match(o) != tc.match {
				t.Fatal("unproven capacity satisfied condition")
			}
		})
	}
	for _, value := range []int64{-1, 16} {
		c.Value = value
		if c.Validate() == nil || c.Match(Observation{Flags: map[string]bool{"inventory:known": true}, OwnProgress: map[string]int{"backpack_used_slots": 0}}) {
			t.Fatal("invalid requested capacity accepted")
		}
	}
}
