package arenaagent

import (
	"context"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

func TestNeuralDeadPlayerAcknowledgementAndRetry(t *testing.T) {
	l, _ := neuralFixtureModel(t, 2)
	initial, h := neuralFixtureTeam(t, 2, 0)
	d, err := l.Decide(context.Background(), initial, h)
	if err != nil {
		t.Fatal(err)
	}
	h = append(h, neuralTurnRecord(initial, d))
	team, events := neuralFixtureTeam(t, 2, 1)
	h = append(h, events...)
	members := obj(team["members"])
	for name, raw := range members {
		old := obj(raw)
		var v aigame.BattleView
		if err := decode(enc(old), &v); err != nil {
			t.Fatal(err)
		}
		for i := range v.Battle.Participants {
			p := &v.Battle.Participants[i]
			if p.BattleID == 0 || p.BattleID == 5 {
				p.Dead, p.HP = true, 0
			}
		}
		if v.Battle.MyNo == 0 {
			v.Battle.PlayerSubmitted, v.Battle.BAReceived, v.Battle.AnimationFlags = true, true, 1
			v.Battle.BPFlags = aigame.BattlePlayerMenuOff | aigame.BattlePetMenuOff
		}
		next := aigame.NewBattleView(aigame.Snapshot{Phase: aigame.PhaseBattle, Battle: v.Battle, Player: v.Own, Pets: v.Pets})
		next.Mode, next.CharacterID = 2, v.CharacterID
		var object Object
		if err := decode(enc(next), &object); err != nil {
			t.Fatal(err)
		}
		object["event_cutoff"] = old["event_cutoff"]
		members[name] = object
	}
	team, err = teamObservation(members, map[string]bool{"char-0": true, "char-1": true}, 2)
	if err != nil {
		t.Fatal(err)
	}
	// Include a stale pre-write attempt. A server-completed dead player's bit
	// must not prevent retry, but any reserved/local live action still must.
	stale := clone(team)
	stale["observation_id"] = "old-attempt"
	h = append(h, Object{"team": stale, "strategy": "learned", "version": l.Version()})
	d, err = l.Decide(context.Background(), team, h)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Plan.Orders) != 2 || validatePlan(team, d.Plan) != nil {
		t.Fatal("missing surviving teammate plan", d)
	}
	for _, o := range d.Plan.Orders {
		if o.Member == "member-0" {
			t.Fatal("command addressed to handled dead actor")
		}
	}
	for _, name := range []string{"member-0", "member-1"} {
		partial := clone(team)
		v := obj(obj(partial["members"])[name])
		v["reserved_actors"] = Object{"player": "reserved"}
		if _, err = l.Decide(context.Background(), partial, h); err == nil {
			t.Fatal("reserved actor allowed replan", name)
		}
	}
	partial := clone(team)
	obj(obj(obj(partial["members"])["member-1"])["battle"])["PlayerSubmitted"] = true
	if _, err = l.Decide(context.Background(), partial, h); err == nil {
		t.Fatal("live partial submission allowed replan")
	}
}
