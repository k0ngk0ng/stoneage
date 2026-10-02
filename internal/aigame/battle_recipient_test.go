package aigame

import (
	"encoding/json"
	"testing"
)

func TestJournalDamageRecipient(t *testing.T) {
	// -1 denotes an absent field, not a valid battle slot.
	for _, tc := range []struct {
		name, fields        string
		recipient, guardian int
	}{
		{"ordinary", "a0|rA|f2|dA", 10, -1},
		{"zero_target", "aA|r0|f2|dA", 0, -1},
		{"guardian", "a0|rA|f202|dA|gF", 15, 15},
		{"zero_guardian", "aA|r5|f202|dA|g0", 0, 0},
		{"reflection", "a0|rA|f402|dA", 0, -1},
		{"reflection_after_guardian", "a0|rA|f602|dA|gF", 0, 15},
		{"reflection_without_guardian_slot", "a0|rA|f602|dA", 0, -1},
		{"skill_number_is_not_guardian", "a0|rA|f2|dA|g5", 10, -1},
		{"dodge", "a0|rA|f20|d0", -1, -1},
		{"guarded_dodge", "a0|rA|f220|d0|gF", -1, 15},
		{"vanish", "a0|rA|f1000|d0", -1, -1},
		{"missing_guardian", "a0|rA|f202|dA", -1, -1},
		{"bad_guardian", "a0|rA|f202|dA|gZZ", -1, -1},
		{"out_of_range_guardian", "a0|rA|f202|dA|g14", -1, -1},
		{"missing_actor_reflection", "rA|f402|dA", -1, -1},
		{"bad_actor_reflection", "aZZ|rA|f402|dA", -1, -1},
		{"out_of_range_actor_reflection", "a14|rA|f402|dA", -1, -1},
		{"bad_target", "a0|rZZ|f2|dA", -1, -1},
		{"empty_target", "a0|r|f2|dA", -1, -1},
		{"out_of_range_target", "a0|r14|f2|dA", -1, -1},
		{"missing_flags", "a0|rA|dA|gF", -1, -1},
		{"bad_flags", "a0|rA|fZZ|dA|gF", -1, -1},
		{"negative_flags", "a0|rA|f-1|dA|gF", -1, -1},
		{"overflow_flags", "a0|rA|fFFFFFFFF|dA|gF", -1, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := battleJournal{}
			j.record(Event{Function: "EN", Fields: []Field{{Kind: FieldInt, Int: 1}}})
			j.record(stringEvent("B", "BH|"+tc.fields+"|FF|"))
			logs := j.battles[0].Logs
			if len(logs) != 2 {
				t.Fatalf("missing hit: %+v", logs)
			}
			e := logs[1]
			for _, field := range []struct {
				name string
				got  *int
				want int
			}{
				{"recipient", e.Recipient, tc.recipient}, {"guardian", e.Guardian, tc.guardian},
			} {
				if field.want < 0 && field.got != nil || field.want >= 0 && (field.got == nil || *field.got != field.want) {
					t.Fatalf("%s: got %v, want %d; entry %+v", field.name, field.got, field.want, e)
				}
				data, err := json.Marshal(e)
				if err != nil {
					t.Fatal(err)
				}
				var object map[string]json.RawMessage
				if err = json.Unmarshal(data, &object); err != nil {
					t.Fatal(err)
				}
				_, present := object[field.name]
				if present != (field.want >= 0) {
					t.Fatalf("zero/absent JSON semantics: %s", data)
				}
			}
		})
	}
}

func TestJournalRecipientCounterAndMultihit(t *testing.T) {
	j := battleJournal{}
	j.record(Event{Function: "EN", Fields: []Field{{Kind: FieldInt, Int: 1}}})
	j.record(stringEvent("B", "BH|a0|rA|f202|dA|gF|r0|f402|counter5|rB|f2|d3|FF|"))
	logs := j.battles[0].Logs[1:]
	if len(logs) != 3 {
		t.Fatal(logs)
	}
	// The counter's attacker is the preceding intended defender, as encoded
	// by the existing movie protocol, not the original attacker's slot.
	for i, want := range []int{15, 10, 11} {
		if logs[i].Recipient == nil || *logs[i].Recipient != want {
			t.Fatalf("hit %d: %+v", i, logs[i])
		}
	}
	if logs[1].Kind != "counter" || logs[1].Actor != 10 || logs[2].Actor != 0 || logs[2].Guardian != nil {
		t.Fatal(logs)
	}
	j.record(stringEvent("B", "BH|a0|rZZ|f2|d1|r0|f402|counter1|FF|"))
	logs = j.battles[0].Logs
	if logs[len(logs)-1].Recipient != nil {
		t.Fatal("malformed counter actor invented slot zero")
	}
	j.record(stringEvent("B", "BH|a0|f2|d1|FF|"))
	last := j.battles[0].Logs[len(j.battles[0].Logs)-1]
	if last.Kind != "unknown" || last.Recipient != nil || last.Guardian != nil {
		t.Fatal("missing target invented hit", last)
	}
}

func TestDamageRecipientSnapshotsAreImmutable(t *testing.T) {
	s := &Session{}
	s.applyEvent(Event{Function: "EN", Fields: []Field{{Kind: FieldInt, Int: 1}}})
	s.applyEvent(stringEvent("B", "BH|a0|rA|f602|dA|gF|FF|"))
	check := func(e BattleLogEntry) {
		t.Helper()
		if e.Recipient == nil || *e.Recipient != 0 || e.Guardian == nil || *e.Guardian != 15 {
			t.Fatalf("aliased or missing recipients: %+v", e)
		}
	}
	for i := 0; i < 2; i++ {
		journal := s.BattleJournal().Battles[0].Logs[1]
		check(journal)
		*journal.Recipient, *journal.Guardian = 7, 8
		batch := s.BattleEvents("", 0)
		found := false
		for _, event := range batch.Events {
			for _, effect := range event.Effects {
				if effect.Kind == "attack" {
					check(effect)
					*effect.Recipient, *effect.Guardian = 9, 10
					found = true
				}
			}
		}
		if !found {
			t.Fatal("missing public event")
		}
	}
}
