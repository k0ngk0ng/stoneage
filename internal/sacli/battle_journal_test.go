package sacli

import (
	"context"
	"encoding/json"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"strings"
	"testing"
)

type journalGame struct{ Game }

func (journalGame) BattleJournal() aigame.BattleJournal {
	recipient, guardian := 0, 15
	return aigame.BattleJournal{Battles: []aigame.BattleLog{{ID: 1, Turn: 2, Result: "进行中", Logs: []aigame.BattleLogEntry{{Turn: 2, Text: "玩家 → 敌人：攻击，体力 −100", Damage: 100, Recipient: &recipient, Guardian: &guardian}}}}}
}
func TestBattleLogCommandUsesSharedJournal(t *testing.T) {
	s := &Server{game: journalGame{}}
	r := s.commandBattleLog(context.Background(), Request{})
	if !r.OK || !strings.Contains(r.Text, "体力 −100") || !strings.Contains(string(r.Data), `"damage":100`) {
		t.Fatalf("response: %+v", r)
	}
	var journal aigame.BattleJournal
	if err := json.Unmarshal(r.Data, &journal); err != nil {
		t.Fatal(err)
	}
	entry := journal.Battles[0].Logs[0]
	if entry.Recipient == nil || *entry.Recipient != 0 || entry.Guardian == nil || *entry.Guardian != 15 {
		t.Fatal("structured recipient dropped", entry)
	}
}
