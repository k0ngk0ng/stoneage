package playerdata

import (
	"bytes"
	"strings"
	"testing"
)

// Literal SAAC envelope, following makeSaveCharString in saac/char.c.
// The inner item/pet pipes are escaped once, not independently JSON-encoded.
const fixture = `Hero|lv=12|lv=12\ngld=45\nname=Hero\nownt=\nitem5=id=100\zbi=200\zna=Stone\zcustom=77\z\npet0=lv:1\zname:Pet\zownt:\zpsk0:10\z\npoolitem0=id=101\zna=Ore\z\npoolpet0=name:BankPet\zownt:\z\nfuture=retain\nDATAEND=1\n`

func TestSaveRoundTripAndTargetedEdit(t *testing.T) {
	d, err := ParseSave([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	before, err := d.Bytes()
	if err != nil || string(before) != fixture {
		t.Fatalf("round trip: %q %v", before, err)
	}
	if err = d.Character.SetInteger("gld", 900); err != nil {
		t.Fatal(err)
	}
	got, err := d.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(fixture, "gld=45", "gld=900", 1)
	if string(got) != want {
		t.Fatalf("edit changed unrelated fields: %q", got)
	}
	reloaded, err := ParseSave(got)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Revision == d.Revision {
		t.Fatal("revision did not change")
	}
	for _, key := range []string{"item5", "poolitem0", "pet0", "poolpet0", "future"} {
		a, _ := d.Character.Raw(key)
		b, _ := reloaded.Character.Raw(key)
		if !bytes.Equal(a, b) {
			t.Fatalf("lost %s", key)
		}
	}
}

func TestPetSkillAndItemEditPreserveOtherAttributes(t *testing.T) {
	d, _ := ParseSave([]byte(fixture))
	raw, _ := d.Character.Raw("pet0")
	pet, err := ParsePet(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err = pet.SetInteger("psk1", 99); err != nil {
		t.Fatal(err)
	}
	pet.Delete("psk0")
	if string(pet.Bytes()) != "lv:1|name:Pet|ownt:|psk1:99|" {
		t.Fatalf("pet: %q", pet.Bytes())
	}
	if err = d.Character.SetRaw("pet0", pet.Bytes()); err != nil {
		t.Fatal(err)
	}
	raw, _ = d.Character.Raw("item5")
	item, err := ParseItem(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err = item.SetText("na", "石头", 32); err != nil {
		t.Fatal(err)
	}
	name, err := item.Text("na")
	if err != nil || name != "石头" {
		t.Fatalf("name=%q err=%v", name, err)
	}
	if value, _ := item.Integer("custom"); value != 77 {
		t.Fatal("lost custom item attribute")
	}
}

func TestPetPackedGrowthAttributesUseSignedInt32Fixture(t *testing.T) {
	pet, err := ParsePet([]byte("lv:1|name:Pet|ownt:|dmswc:777|lvup:-760981768|llt:5|slt:3|"))
	if err != nil {
		t.Fatal(err)
	}
	var possession Possession
	if err = readPet(pet.Bytes(), &possession); err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{
		"growth_vi":  0xd2,
		"growth_str": 0xa4,
		"growth_tou": 0x56,
		"growth_dx":  0xf8,
	}
	got := map[string]int64{}
	for _, attribute := range possession.Attributes {
		got[attribute.Key] = attribute.Value
		if attribute.Key == "lvup" {
			t.Fatal("packed lvup leaked into pet attributes")
		}
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("%s = %d, want %d; attributes=%+v", key, got[key], value, possession.Attributes)
		}
	}
	if err = SetPetGrowth(pet, "growth_str", 9); err != nil {
		t.Fatal(err)
	}
	packed, err := pet.Integer("lvup")
	if err != nil || packed != -771139848 {
		t.Fatalf("packed lvup = %d, err=%v, want -771139848", packed, err)
	}
	if err = readPet(pet.Bytes(), &possession); err != nil {
		t.Fatal(err)
	}
	for _, attribute := range possession.Attributes {
		if attribute.Key == "growth_vi" && attribute.Value != 0xd2 ||
			attribute.Key == "growth_str" && attribute.Value != 9 ||
			attribute.Key == "growth_tou" && attribute.Value != 0x56 ||
			attribute.Key == "growth_dx" && attribute.Value != 0xf8 {
			t.Fatalf("growth byte changed unexpectedly: %+v", possession.Attributes)
		}
	}
}

func TestSnapshotCapacitiesFollowTransmigration(t *testing.T) {
	for _, test := range []struct {
		transmigration string
		want           int
	}{
		{transmigration: "0", want: 5},
		{transmigration: "1", want: 7},
		{transmigration: "5", want: 15},
		{transmigration: "99", want: 15},
	} {
		t.Run(test.transmigration, func(t *testing.T) {
			raw := []byte("Hero|lv=1|trn=" + test.transmigration + "\nname=Hero\n")
			document, err := ParseSave(raw)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := document.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Capacities["item_inventory"] != 15 || snapshot.Capacities["item_warehouse"] != 30 || snapshot.Capacities["pet_inventory"] != 5 {
				t.Fatalf("fixed capacities = %+v", snapshot.Capacities)
			}
			if snapshot.Capacities["pet_warehouse"] != test.want {
				t.Fatalf("pet warehouse capacity = %d, want %d", snapshot.Capacities["pet_warehouse"], test.want)
			}
		})
	}
}

func TestPetDefinitionsExposeGrowthBytesAndBoundRank(t *testing.T) {
	for _, field := range []string{"growth_vi", "growth_str", "growth_tou", "growth_dx"} {
		if err := ValidateAttribute("pet", field, 255); err != nil {
			t.Fatalf("ValidateAttribute(%q, 255) = %v", field, err)
		}
		if err := ValidateAttribute("pet", field, 256); err == nil {
			t.Fatalf("ValidateAttribute(%q, 256) succeeded", field)
		}
	}
	if err := ValidateAttribute("pet", "lvup", 0); err == nil {
		t.Fatal("ValidateAttribute(lvup) succeeded for packed pet field")
	}
	if err := ValidateAttribute("pet", "llt", 5); err != nil {
		t.Fatalf("ValidateAttribute(llt, 5) = %v", err)
	}
	if err := ValidateAttribute("pet", "llt", 6); err == nil {
		t.Fatal("ValidateAttribute(llt, 6) succeeded")
	}
}

func TestCP936TrailBytesAreNotDelimitersOrEscapes(t *testing.T) {
	// Both pairs are valid CP936 characters whose trail byte is ASCII punctuation.
	data := []byte{'n', 'a', 'm', 'e', '=', 0x81, 0x5c, 0x81, 0x7c, '\n', 'g', 'l', 'd', '=', '1', '\n'}
	r, err := ParseCharacter(data)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(r.Bytes(), data) {
		t.Fatal("changed CP936 bytes")
	}
	encoded := escape(data)
	want := append([]byte("name="), 0x81, 0x5c, 0x81, 0x7c)
	want = append(want, []byte(`\ngld=1\n`)...)
	if !bytes.Equal(encoded, want) {
		t.Fatalf("escape=%x want=%x", encoded, want)
	}
	decoded, err := unescape(want)
	if err != nil || !bytes.Equal(decoded, data) {
		t.Fatalf("decode=%x err=%v", decoded, err)
	}
	pet, err := ParsePet(append([]byte{'n', 'a', 'm', 'e', ':', 0x81, 0x7c}, []byte("|lv:1|")...))
	if err != nil {
		t.Fatal(err)
	}
	if n, err := pet.Integer("lv"); err != nil || n != 1 {
		t.Fatalf("pet lv=%d err=%v", n, err)
	}
}

func TestRejectMalformedAndAmbiguousSaves(t *testing.T) {
	for _, value := range []string{"", "a|b", "a|b|name=x|extra", "a|b|name=x\\", "a|b|name=x\\q", "a|b|name=x\\ngld=1\\ngld=2", "a|b|name=\x81", "a|b|name=x\x00"} {
		if _, err := ParseSave([]byte(value)); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
	r, _ := ParseCharacter([]byte("name=Hero\ngld=1\n"))
	if err := r.SetRaw("name", []byte("X\ngld=999")); err == nil {
		t.Fatal("accepted injected field")
	}
	if err := r.SetInteger("gld", 1<<32); err == nil {
		t.Fatal("accepted integer overflow")
	}
	if err := r.SetText("name", "石头", 3); err == nil {
		t.Fatal("ignored CP936 byte limit")
	}
	if err := r.SetText("name", "🙂", 32); err == nil {
		t.Fatal("accepted unrepresentable name")
	}
}

func TestSaveCapacityCheckedAfterEscaping(t *testing.T) {
	d, _ := ParseSave([]byte(fixture))
	if err := d.Character.SetRaw("future", []byte(strings.Repeat("|", 40000))); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Bytes(); err == nil {
		t.Fatal("accepted oversized escaped save")
	}
}
