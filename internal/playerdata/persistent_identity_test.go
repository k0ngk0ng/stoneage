package playerdata

import (
	"bytes"
	"testing"
)

const testPersistentCharacterID = "pc1_0123456789abcdef0123456789abcdef"

func TestSnapshotReadsAndRoundTripsPersistentCharacterID(t *testing.T) {
	raw := []byte("Hero|lv=1|name=Hero\\ncharid=" + testPersistentCharacterID + "\\nfuture=retain\\n")
	document, err := ParseSave(raw)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := document.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.PersistentCharacterID != testPersistentCharacterID {
		t.Fatalf("persistent character ID = %q, want %q", snapshot.PersistentCharacterID, testPersistentCharacterID)
	}
	encoded, err := document.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, raw) {
		t.Fatalf("snapshot read changed archive: %q", encoded)
	}
	reloaded, err := ParseSave(encoded)
	if err != nil {
		t.Fatal(err)
	}
	reloadedSnapshot, err := reloaded.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if reloadedSnapshot.PersistentCharacterID != testPersistentCharacterID {
		t.Fatalf("round-tripped persistent character ID = %q, want %q", reloadedSnapshot.PersistentCharacterID, testPersistentCharacterID)
	}
}

func TestSnapshotLeavesOnlineRecordIdentityUnknown(t *testing.T) {
	record, err := ParseCharacter([]byte("name=Hero\ncharid=" + testPersistentCharacterID + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := SnapshotFromRecord(record, "online-revision")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.PersistentCharacterID != "" {
		t.Fatalf("online snapshot claimed persistent character ID %q", snapshot.PersistentCharacterID)
	}
}

func TestSnapshotIgnoresInvalidOrMissingPersistentCharacterID(t *testing.T) {
	values := []string{
		"",
		"pc1_",
		"pc1_0123456789abcdef0123456789abcde",   // 31 hex characters.
		"pc1_0123456789abcdef0123456789abcdef0", // 33 hex characters.
		"pc1_0123456789abcdef0123456789abcdefG",
		"pc1_0123456789abcdef0123456789ABCDEf",
		"pc2_0123456789abcdef0123456789abcdef",
	}
	for _, value := range values {
		t.Run(value, func(t *testing.T) {
			raw := []byte("Hero|lv=1|name=Hero\\n")
			if value != "" {
				raw = []byte("Hero|lv=1|name=Hero\\ncharid=" + value + "\\n")
			}
			document, err := ParseSave(raw)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := document.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.PersistentCharacterID != "" {
				t.Fatalf("invalid persistent character ID accepted: %q", snapshot.PersistentCharacterID)
			}
			encoded, err := document.Bytes()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(encoded, raw) {
				t.Fatalf("identity read repaired archive: %q", encoded)
			}
		})
	}
}
