package aiservice

import (
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
)

func TestProjectObservationCopiesAddressBook(t *testing.T) {
	snapshot := aigame.Snapshot{
		AddressBookKnown: true,
		AddressBook: []aigame.AddressBookEntry{{Index: 3, Use: true, Online: true, Level: 12, DuelPoint: 7, Graphic: 88, Name: "Alice", Transmigration: 2}},
	}
	observation := ProjectObservation(aimcp.Binding{CharacterID: "character-1"}, snapshot)
	if !observation.AddressBookKnown || len(observation.AddressBook) != 1 {
		t.Fatalf("address book projection = %+v", observation)
	}
	entry := observation.AddressBook[0]
	if entry.Index != 3 || !entry.Use || !entry.Online || entry.Level != 12 || entry.DuelPoint != 7 || entry.Graphic != 88 || entry.Name != "Alice" || entry.Transmigration != 2 {
		t.Fatalf("address book entry projection = %+v", entry)
	}
	snapshot.AddressBook[0].Name = "changed"
	if observation.AddressBook[0].Name != "Alice" {
		t.Fatal("address book projection aliases source snapshot")
	}
}
