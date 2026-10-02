package aigame

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativePetCapacityProjection(t *testing.T) {
	root := os.Getenv("STONEAGE_BATTLE_PARITY_DIR")
	if root == "" {
		t.Skip("explicit native parity captures required")
	}
	f, err := os.Open(filepath.Join(root, "pet-capacity", "stdout.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	n := 0
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		if !strings.HasPrefix(scan.Text(), "CAPACITY|") {
			continue
		}
		var c struct {
			Slots, Transmigration, Index int32
			Accepted                     bool
			Status                       string
		}
		if err = json.Unmarshal(scan.Bytes()[9:], &c); err != nil {
			t.Fatal(err)
		}
		raw, err := hex.DecodeString(c.Status)
		if err != nil {
			t.Fatal(err)
		}
		s := &Session{state: newGameState(false)}
		s.applyEvent(stringEvent("S", string(raw)))
		p := s.Snapshot().Pets[0]
		if p.Slot != 0 || !p.SkillSlotsKnown || p.SkillSlots != c.Slots || !p.TransmigrationKnown || p.Transmigration != c.Transmigration || p.BattleSkillIndexAllowed(c.Index) != c.Accepted {
			t.Fatalf("public K projection differs from native command gate: %+v / %+v", p, c)
		}
		n++
	}
	if err = scan.Err(); err != nil || n != 6 {
		t.Fatal("incomplete native capacity capture", n, err)
	}
}
