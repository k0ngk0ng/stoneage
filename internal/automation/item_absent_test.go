package automation

import "testing"

func TestItemAbsentRequiresAuthoritativeInventory(t *testing.T) {
	condition := Condition{Kind: "item_absent", ID: "item:2417"}
	base := Observation{
		Connected: true,
		Ready:     true,
		Flags:     map[string]bool{"inventory:known": true},
		Inventory: map[string]int{},
	}
	for _, tc := range []struct {
		name        string
		observation Observation
		want        bool
	}{
		{
			name:        "missing key",
			observation: base,
			want:        true,
		},
		{
			name: "explicit zero",
			observation: func() Observation {
				o := base
				o.Inventory = map[string]int{"item:2417": 0}
				return o
			}(),
			want: true,
		},
		{
			name: "positive count",
			observation: func() Observation {
				o := base
				o.Inventory = map[string]int{"item:2417": 1}
				return o
			}(),
			want: false,
		},
		{
			name: "invalid negative count",
			observation: func() Observation {
				o := base
				o.Inventory = map[string]int{"item:2417": -1}
				return o
			}(),
			want: false,
		},
		{
			name: "inventory unknown",
			observation: func() Observation {
				o := base
				o.Flags = map[string]bool{}
				return o
			}(),
			want: false,
		},
		{
			name: "disconnected",
			observation: func() Observation {
				o := base
				o.Connected = false
				return o
			}(),
			want: false,
		},
		{
			name: "not ready",
			observation: func() Observation {
				o := base
				o.Ready = false
				return o
			}(),
			want: false,
		},
		{
			name:        "empty unknown snapshot",
			observation: Observation{},
			want:        false,
		},
		{
			name: "nil inventory despite known flag",
			observation: func() Observation {
				o := base
				o.Inventory = nil
				return o
			}(),
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := condition.Match(tc.observation); got != tc.want {
				t.Fatalf("item_absent match=%v want=%v observation=%+v", got, tc.want, tc.observation)
			}
		})
	}
}

func TestItemAbsentRejectsInvalidConditionFields(t *testing.T) {
	for _, condition := range []Condition{
		{Kind: "item_absent"},
		{Kind: "item_absent", ID: "item:2417", Value: 1},
		{Kind: "item_absent", ID: "item:2417", Value: -1},
	} {
		if err := condition.Validate(); err == nil {
			t.Fatalf("invalid item_absent condition accepted: %+v", condition)
		}
	}
	for _, condition := range []Condition{
		{Kind: "item_absent", ID: "item:2417"},
		{Kind: "item_absent", ID: "item:2417", Value: 0},
	} {
		if err := condition.Validate(); err != nil {
			t.Fatalf("valid item_absent condition rejected: %+v: %v", condition, err)
		}
	}
}
