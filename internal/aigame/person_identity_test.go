package aigame

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

const personRecord = "1|G|10|20|0|100|1|0|Friend||||||0|0"

func personMetadata(kind, record string) Event {
	return stringEvent("S", "AIPERSON|1|"+kind+"|42|"+testPersistentCharacterID+"|"+hex.EncodeToString([]byte(record)))
}

func TestVisibleIdentityBindsExactNextRecordAndExpires(t *testing.T) {
	for _, mode := range []string{"exact", "wrong-bytes", "intervening", "duplicate", "invalid", "overflow", "npc", "duplicate-record"} {
		t.Run(mode, func(t *testing.T) {
			state := newGameState(true)
			record := personRecord
			if mode == "npc" {
				record = "4" + record[1:]
			}
			applyEventLocked(&state, personMetadata("C", record))
			switch mode {
			case "wrong-bytes":
				record = strings.Replace(record, "Friend", "Other", 1)
			case "intervening":
				applyEventLocked(&state, stringEvent("S", "P2|50"))
			case "duplicate":
				applyEventLocked(&state, personMetadata("C", record))
			case "invalid":
				applyEventLocked(&state, stringEvent("S", "AIPERSON|bad"))
			case "overflow":
				for i := 0; i < 128; i++ {
					applyEventLocked(&state, personMetadata("C", record))
				}
			case "duplicate-record":
				record += "," + record
			}
			applyEventLocked(&state, stringEvent("C", record))
			if got := state.actors[42].PersistentCharacterID; (got == testPersistentCharacterID) != (mode == "exact") {
				t.Fatalf("identity=%q", got)
			}
			// A replacement full record with the same object index is a fresh actor.
			applyEventLocked(&state, stringEvent("C", personRecord))
			if state.actors[42].PersistentCharacterID != "" {
				t.Fatal("object reuse retained identity")
			}
		})
	}
}

func TestVisibleIdentityLifecycleAndMultipleRecords(t *testing.T) {
	state := newGameState(true)
	second := strings.Replace(personRecord, "|G|", "|H|", 1)
	secondID := "pc1_abcdef0123456789abcdef0123456789"
	applyEventLocked(&state, personMetadata("C", personRecord))
	applyEventLocked(&state, stringEvent("S", "AIPERSON|1|C|43|"+secondID+"|"+hex.EncodeToString([]byte(second))))
	applyEventLocked(&state, stringEvent("C", personRecord+","+second))
	if state.actors[42].PersistentCharacterID != testPersistentCharacterID || state.actors[43].PersistentCharacterID != secondID {
		t.Fatal("bulk attribution failed")
	}
	applyEventLocked(&state, stringEvent("CA", "G|11|20|0|0"))
	if state.actors[42].PersistentCharacterID != testPersistentCharacterID {
		t.Fatal("movement lost same actor identity")
	}
	applyEventLocked(&state, stringEvent("CD", "G"))
	if _, ok := state.actors[42]; ok {
		t.Fatal("deleted actor remains")
	}
	applyEventLocked(&state, stringEvent("S", "C100|30|30|1|1"))
	if len(state.actors) != 0 {
		t.Fatal("map change retained people")
	}
}

func TestPartyIdentityFullUpdatesAndLifecycle(t *testing.T) {
	state := newGameState(true)
	record := "N0|1|42|20|100|80|30|Friend"
	applyEventLocked(&state, personMetadata("N", record))
	applyEventLocked(&state, stringEvent("S", record))
	member := state.party[0]
	if member.ID != 42 || member.Level != 20 || member.HP != 80 || member.Name != "Friend" || member.PersistentCharacterID != testPersistentCharacterID {
		t.Fatalf("full native party row: %+v", member)
	}
	// An unpaired partial update cannot retain attribution from an older row.
	applyEventLocked(&state, stringEvent("S", "N0|g|70")) // native bit16 HP
	if state.party[0].PersistentCharacterID != "" {
		t.Fatal("unpaired update retained identity")
	}
	for _, event := range []Event{stringEvent("S", "N0|0|"), stringEvent("CharLogout", "successful"), stringEvent("CharLogin", "successful")} {
		applyEventLocked(&state, personMetadata("N", record))
		applyEventLocked(&state, stringEvent("S", record))
		applyEventLocked(&state, event)
		if len(state.party) != 0 {
			t.Fatalf("%s retained party identity", event.Function)
		}
	}
	// A metadata object ID different from the native N row is never accepted.
	applyEventLocked(&state, personMetadata("N", strings.Replace(record, "42", "43", 1)))
	applyEventLocked(&state, stringEvent("S", strings.Replace(record, "42", "43", 1)))
	if state.party[0].PersistentCharacterID != "" {
		t.Fatal("mismatched object accepted")
	}
}

func TestPersonIdentityRejectsMalformedMetadata(t *testing.T) {
	for _, raw := range []string{"AIPERSON|2|C|42|" + testPersistentCharacterID + "|61", "AIPERSON|1|CA|42|" + testPersistentCharacterID + "|61", "AIPERSON|1|C|-1|" + testPersistentCharacterID + "|61", "AIPERSON|1|C|42|invalid|61", fmt.Sprintf("AIPERSON|1|N|42|%s|xx", testPersistentCharacterID)} {
		if parsePersonIdentity(raw) != nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}

func TestVisiblePersonDoesNotRetroactivelyAttributeChat(t *testing.T) {
	state := newGameState(true)
	tk := Event{Function: "TK", Fields: []Field{{Kind: FieldInt, Int: 42}, {Kind: FieldString, Text: []byte("hello")}, {Kind: FieldInt, Int: 0}}}
	applyEventLocked(&state, tk)
	applyEventLocked(&state, personMetadata("C", personRecord))
	applyEventLocked(&state, stringEvent("C", personRecord))
	applyEventLocked(&state, tk)
	if len(state.chat) != 2 {
		t.Fatalf("chat count=%d", len(state.chat))
	}
	for _, message := range state.chat {
		if message.SpeakerCharacterID != "" {
			t.Fatal("visible actor identity substituted for chat-time evidence")
		}
	}
}

func TestPartySlotReuseDoesNotCarryPreviousMemberDetails(t *testing.T) {
	state := newGameState(true)
	applyEventLocked(&state, stringEvent("S", "N0|1|42|20|100|80|30|OldPerson"))
	applyEventLocked(&state, stringEvent("S", "N0|2|43|"))
	member := state.party[0]
	if member.ID != 43 || member.Name != "" || member.HP != 0 || member.Level != 0 || member.PersistentCharacterID != "" {
		t.Fatalf("slot reused old details: %+v", member)
	}
}
