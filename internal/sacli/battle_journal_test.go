package sacli

import (
	"context"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"strings"
	"testing"
)

type journalGame struct{ Game }

func (journalGame) BattleJournal() aigame.BattleJournal {
	return aigame.BattleJournal{Battles: []aigame.BattleLog{{ID: 1, Turn: 2, Result: "进行中", Logs: []aigame.BattleLogEntry{{Turn: 2, Text: "玩家 → 敌人：攻击，体力 −100", Damage: 100}}}}}
}
func TestBattleLogCommandUsesSharedJournal(t *testing.T) {
	s := &Server{game: journalGame{}}
	r := s.commandBattleLog(context.Background(), Request{})
	if !r.OK || !strings.Contains(r.Text, "体力 −100") || !strings.Contains(string(r.Data), `"damage":100`) {
		t.Fatalf("response: %+v", r)
	}
}
