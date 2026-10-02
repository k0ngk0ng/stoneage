package arenaagent

// Exercise persisted learned plans through the real runner, store and
// subprocess transport; these are execution tests, not strength evidence.
import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

func continuityRunnerView(t *testing.T, original Object, refresh, submitted bool) Object {
	t.Helper()
	var v aigame.BattleView
	if err := decode(enc(original), &v); err != nil {
		t.Fatal(err)
	}
	if refresh {
		v.Own.CombatStatsKnown = !v.Own.CombatStatsKnown
	}
	v.Battle.PlayerSubmitted = submitted
	v.Battle.Clock.Known = true
	v.Battle.Clock.ServerTurn = v.Turn
	v.Battle.Clock.DeadlineMS = 30000
	v.Battle.Clock.ServerNowMS = 0
	v.Battle.Clock.ReceivedAtMS = time.Now().UnixMilli()
	fresh := aigame.NewBattleView(aigame.Snapshot{Phase: aigame.PhaseBattle, Player: v.Own, Battle: v.Battle, Pets: v.Pets, Magic: v.Magic, Inventory: v.Inventory})
	fresh.Mode, fresh.CharacterID = v.Mode, v.CharacterID
	var result Object
	if err := decode(enc(fresh), &result); err != nil {
		t.Fatal(err)
	}
	result["event_cutoff"] = clone(obj(original["event_cutoff"]))
	return result
}

func TestRunnerPreservesPartialStrategyPlanBoundary(t *testing.T) {
	for _, kind := range []string{"learned", "llm", "hybrid"} {
		for _, refresh := range []bool{false, true} {
			for _, intent := range []string{"uncertain", "written", "rejected"} {
				t.Run(fmt.Sprintf("%s/refresh=%t/intent=%s", kind, refresh, intent), func(t *testing.T) {
					ctx := context.Background()
					r, statuses := timingRunner(t, false)
					// Preserve the native public readiness transition: the pet becomes
					// ready after the player's write. This is a transport fixture, not
					// a battle-engine or strategy-strength test.
					script := `#!/bin/sh
set -eu
fixture_directory="$2"
shift 5
case "$1" in
 query) echo '{"ok":true}' ;;
 battle-state)
   if [ -f "$fixture_directory/player-written" ]; then
     cat "$fixture_directory/after-player.json"
   else
     cat "$fixture_directory/view.json"
   fi ;;
 battle-events) cat "$fixture_directory/events.json" ;;
 battle-act)
   case "$2" in *player:*) : > "$fixture_directory/player-written" ;; esac
   echo '{"ok":true}' ;;
 *) exit 1 ;;
esac
`
					if err := os.WriteFile(r.members[0].binary, []byte(script), 0700); err != nil {
						t.Fatal(err)
					}
					model, _ := neuralFixtureModel(t, 2)
					var strategy Strategy = model
					if kind != "learned" {
						var calls atomic.Int32
						server := commanderProvider(t, &calls)
						llm, err := NewLLM(Object{"endpoint": server.URL, "model": "fixture"})
						if err != nil {
							t.Fatal(err)
						}
						strategy = llm
						if kind == "hybrid" {
							strategy = &Hybrid{model, llm}
						}
					}
					r.strategy = strategy
					initial, events := neuralFixtureTeam(t, 2, 0)
					views := Object{}
					for name, value := range obj(initial["members"]) {
						views[name] = continuityRunnerView(t, obj(value), false, false)
					}
					initial, err := teamObservation(views, r.expected(), 2)
					if err != nil {
						t.Fatal(err)
					}
					for _, entry := range events {
						ev := obj(entry["event"])
						r.store.Ingest(str(entry["member"]), clone(Object{"stream": entry["stream"], "cursor": ev["sequence"], "events": []Object{ev}}))
					}
					history, cutoff := r.store.HistorySnapshot("match")
					initial["history_record_cutoff"] = cutoff
					first, err := strategy.Decide(ctx, initial, history)
					if err != nil || validatePlan(initial, first.Plan) != nil || len(first.Plan.Orders) != 4 {
						t.Fatal("real learned fixture must produce full plan", err)
					}
					r.store.Record("turn", neuralTurnRecord(initial, first), "match", "")
					var selection Object
					for _, order := range first.Plan.Orders {
						if order.Member == "member-0" && order.Actor == "player" {
							selection = Object{"match_id": first.Plan.Match, "turn": first.Plan.Turn, "observation_id": obj(views[order.Member])["observation_id"], "candidate_id": order.Candidate}
						}
					}
					if selection == nil || !r.store.Reserve("member-0", selection, "player") {
						t.Fatal("initial intent", r.store.Err())
					}
					if intent == "written" {
						r.store.Finish("member-0", selection, "player", Object{"ok": true})
					} else if intent == "rejected" {
						r.store.Finish("member-0", selection, "player", Object{"ok": false, "data": Object{"code": "stale_observation"}})
					}
					currentViews := Object{}
					for _, member := range r.members {
						name := member.cfg.ID
						view := continuityRunnerView(t, obj(views[name]), refresh && name == "member-0", intent != "rejected" && name == "member-0")
						afterPlayer := continuityRunnerView(t, view, false, true)
						if str(afterPlayer["observation_id"]) != str(view["observation_id"]) {
							t.Fatal("submission must not change combat observation identity")
						}
						boundary := obj(view["event_cutoff"])
						batch := Object{"stream": boundary["stream"], "cursor": boundary["cursor"], "gap": false, "events": []Object{}, "observation": view}
						for file, body := range map[string]Object{"view.json": {"ok": true, "data": view}, "after-player.json": {"ok": true, "data": afterPlayer}, "events.json": {"ok": true, "data": batch}} {
							if err := os.WriteFile(filepath.Join(member.cfg.Config, file), enc(body), 0600); err != nil {
								t.Fatal(err)
							}
						}
						currentViews[name] = clone(view)
					}
					partial, err := teamObservation(currentViews, r.expected(), 2)
					if err != nil || (str(partial["observation_id"]) != str(initial["observation_id"])) != refresh {
						t.Fatal("fixture identity is not the intended public refresh", err)
					}
					if intent != "rejected" {
						obj(currentViews["member-0"])["reserved_actors"] = Object{"player": intent}
					}
					history, cutoff = r.store.HistorySnapshot("match")
					partial["history_record_cutoff"] = cutoff
					expected, strategyErr := strategy.Decide(ctx, partial, history)
					unsafe := refresh && intent != "rejected"
					if unsafe {
						var failure *neuralFailure
						if !errors.As(strategyErr, &failure) || failure.code != "changed_observation" {
							t.Fatal("fixture must hit actual learned continuity rejection", strategyErr)
						}
					} else if strategyErr != nil || expected.Strategy != kind {
						t.Fatal("safe continuation/replan must remain learned", strategyErr)
					}
					var before int64
					if err := r.store.DB.QueryRow("SELECT COALESCE(MAX(id),0) FROM records").Scan(&before); err != nil {
						t.Fatal(err)
					}
					runErr := r.battle(ctx, statuses)
					if r.store.Err() != nil {
						t.Fatal(r.store.Err())
					}
					var forbidden int
					if err := r.store.DB.QueryRow("SELECT count(*) FROM records WHERE id>? AND kind IN ('turn','submission_intent','submission','strategy_fallback')", before).Scan(&forbidden); err != nil {
						t.Fatal(err)
					}
					if unsafe {
						var failure *neuralFailure
						if !errors.As(runErr, &failure) || failure.code != "changed_observation" || forbidden != 0 {
							t.Fatalf("partial final plan replaced at runner boundary: err=%v new_action_records=%d", runErr, forbidden)
						}
						if got := r.store.Intent("member-0", "match", 0, "player"); got != intent {
							t.Fatal("original submission fence changed", got)
						}
						return
					}
					if runErr != nil {
						t.Fatal("safe runner path failed", runErr)
					}
					rows, err := readObjects(r.store.DB, "SELECT body FROM records WHERE kind='turn' ORDER BY id")
					if err != nil || len(rows) != 2 || str(rows[1]["strategy"]) != kind {
						t.Fatal("safe runner path did not commit learned decision", err, len(rows))
					}
					var actual Plan
					if decode(enc(rows[1]["plan"]), &actual) != nil || !reflect.DeepEqual(actual, expected.Plan) {
						t.Fatal("runner changed authoritative remaining/replanned orders")
					}
					var sent int
					if err := r.store.DB.QueryRow("SELECT count(*) FROM records WHERE id>? AND kind='submission'", before).Scan(&sent); err != nil || sent != len(expected.Plan.Orders) {
						t.Fatal("safe controls must actually reach dispatch", sent, len(expected.Plan.Orders), err)
					}
					wanted := map[Slot]string{}
					for _, order := range expected.Plan.Orders {
						wanted[Slot{order.Member, order.Actor}] = order.Candidate
					}
					for _, row := range r.store.History("match") {
						receipt := obj(row["submission"])
						if int64(integer(row["record_id"])) <= before || receipt == nil {
							continue
						}
						key := Slot{str(row["member"]), str(receipt["actor"])}
						if wanted[key] == "" || str(obj(receipt["selection"])["candidate_id"]) != wanted[key] || !yes(obj(receipt["response"])["ok"]) {
							t.Fatal("dispatch changed or duplicated a saved order", row)
						}
						delete(wanted, key)
					}
					if len(wanted) != 0 || r.store.Err() != nil {
						t.Fatal("missing actual dispatch receipts", wanted, r.store.Err())
					}
				})
			}
		}
	}
}
