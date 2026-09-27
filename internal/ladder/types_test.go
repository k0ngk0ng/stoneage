package ladder

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHistoricalReceiptKeepsBootIdentityThroughClientJSON(t *testing.T) {
	e, err := Decode(`LADDER|{"version":1,"ok":true,"replay":true,"applied_revision":12,"revision":24,"server_boot":"current","receipt_boot":"previous","snapshot":{"phase":"idle"}}`)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(e.Clone())
	if err != nil {
		t.Fatal(err)
	}
	var result Envelope
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result.ServerBoot != "current" || result.ReceiptBoot != "previous" || !result.Replay || result.AppliedRevision != 12 || result.Revision != 24 || result.Snapshot.Room != nil {
		t.Fatalf("historical receipt lost context: %+v", result)
	}
}

func TestRequestFencesAndEncoding(t *testing.T) {
	for _, r := range []Request{
		{ID: "id", Operation: "queue"},
		{ID: "id|other", Operation: "status"},
		{ID: "id", Revision: 1, Operation: "create", Argument: "6"},
		{ID: "id", Revision: 1, Operation: "invite", Argument: "80"},
		{ID: "id", Revision: 1, Operation: "invite", Argument: "0"},
		{ID: "id", Revision: 1, Operation: "invite", Argument: "00:pc1_a"},
		{ID: "id", Revision: 1, Operation: "invite", Argument: "0:pc1_a:pc1_b"},
		{ID: "id", Revision: 1, Operation: "loadout", Argument: "-1"},
		{ID: "id", Revision: 1, Operation: "accept", Argument: "name\nS|AI"},
		{ID: "id", Revision: 1, Operation: "queue", Argument: "forged"},
		{ID: "id", Revision: 1, Operation: "settle", Argument: "victory"},
	} {
		if _, err := r.Wire(); err == nil {
			t.Fatalf("accepted invalid request %+v", r)
		}
	}
	wire, err := (Request{ID: "req_1", Revision: 45, Operation: "invite", Argument: "0:pc1_a"}).Wire()
	if err != nil || wire != "LADDER|1|req_1|45|invite|0:pc1_a" {
		t.Fatalf("wire=%q error=%v", wire, err)
	}
}

func TestContactReadDecodesNamesAndKeepsSelectedIdentity(t *testing.T) {
	if _, err := (Request{ID: "cards", Operation: "contacts"}).Wire(); err != nil {
		t.Fatal(err)
	}
	e, err := Decode(`LADDER|{"version":1,"event":"contacts_lookup","contacts":[{"slot":3,"id":"pc1_a","name_hex":"b0a2b2bc","online":true}]}`)
	if err != nil || len(e.Contacts) != 1 || e.Contacts[0].Name != "阿布" || e.Contacts[0].ID != "pc1_a" {
		t.Fatalf("%+v %v", e, err)
	}
	copy := e.Clone()
	copy.Contacts[0].ID = "pc1_b"
	if e.Contacts[0].ID != "pc1_a" {
		t.Fatal("clone rewrote selected identity")
	}
}

func TestWireNamesDecodeOnceAndSnapshotsAreIndependent(t *testing.T) {
	player := `{"id":"pc1_a","name_hex":"b0a2b2bc","power":400}`
	wire := `LADDER|{"version":1,"ok":true,"revision":14,"snapshot":{"self":` + player + `,"room":{"members":[` + player + `]},"match":{"teams":[{"members":[` + player + `]}]},"result":{"members":[` + player + `]},"invitations":[{"from":` + player + `}]}}`
	e, err := Decode(wire)
	if err != nil {
		t.Fatal(err)
	}
	if e.Snapshot.Self.Name != "阿布" || e.Snapshot.Result.Members[0].Name != "阿布" || e.Snapshot.Invitations[0].From.Name != "阿布" {
		t.Fatalf("names: %+v", e)
	}
	copy := e.Clone()
	copy.Snapshot.Room.Members[0].Name = "changed"
	copy.Snapshot.Match.Teams[0].Members[0].Name = "changed"
	copy.Snapshot.Result.Members[0].Name = "changed"
	copy.Snapshot.Invitations[0].From.Name = "changed"
	if e.Snapshot.Room.Members[0].Name != "阿布" || e.Snapshot.Match.Teams[0].Members[0].Name != "阿布" || e.Snapshot.Result.Members[0].Name != "阿布" || e.Snapshot.Invitations[0].From.Name != "阿布" {
		t.Fatal("clone mutated original")
	}
	for _, value := range []string{strings.Replace(wire, `"version":1`, `"version":2`, 1), strings.Replace(wire, "b0a2b2bc", "not-hex", 1), "LADDER|broken"} {
		if _, err := Decode(value); err == nil {
			t.Fatal("invalid envelope accepted")
		}
	}
}
