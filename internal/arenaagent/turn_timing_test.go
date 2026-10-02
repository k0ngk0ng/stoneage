package arenaagent

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Exercise the real runner, subprocess transport, SQLite and dispatch. The
// subprocess supplies public fixture replies; it is not a game/strength test.
func timingRunner(t *testing.T, reject bool) (*Runner, map[string]Object) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell subprocess fixture")
	}
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DB.Close() })
	binary := filepath.Join(dir, "fixture-sactl")
	script := `#!/bin/sh
set -eu
fixture_directory="$2"
shift 5
case "$1" in
 query) echo '{"ok":true}' ;;
 battle-state) cat "$fixture_directory/view.json" ;;
 battle-events) sleep 0.03; cat "$fixture_directory/events.json" ;;
 battle-act) sleep 0.02; cat "$fixture_directory/result.json" ;;
 *) exit 1 ;;
esac
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	r := &Runner{store: store, strategy: Basic{}, config: Config{Mode: 2}, ids: map[string]string{}}
	team := fixture(t, 2)
	var members []Object
	statuses := map[string]Object{}
	for i := 0; i < 2; i++ {
		name := fmt.Sprintf("member-%d", i)
		id := fmt.Sprintf("char-%d", i)
		r.ids[name] = id
		members = append(members, Object{"id": id})
		v := obj(obj(team["members"])[name])
		obj(v["battle"])["Clock"] = Object{"Known": true, "RulesVersion": RulesVersion, "RulesDigest": "fixture-rules", "EnginePlatform": "fixture-platform", "DeadlineMS": 30000, "ServerNowMS": 0, "ReceivedAtMS": time.Now().UnixMilli()}
		for _, c := range objects(v["candidates"]) {
			c["ready"] = true
		}
		memberDir := filepath.Join(dir, name)
		if err := os.Mkdir(memberDir, 0700); err != nil {
			t.Fatal(err)
		}
		result := Object{"ok": true}
		if reject {
			result = Object{"ok": false, "data": Object{"code": "stale_observation"}}
		}
		for file, data := range map[string]Object{
			"view.json":   {"ok": true, "data": v},
			"events.json": {"ok": true, "data": Object{"stream": "fixture-stream", "cursor": 0, "events": []Object{}, "observation": v}},
			"result.json": result,
		} {
			if err := os.WriteFile(filepath.Join(memberDir, file), enc(data), 0600); err != nil {
				t.Fatal(err)
			}
		}
		r.members = append(r.members, &member{cfg: MemberConfig{ID: name, Config: memberDir}, binary: binary, store: store})
	}
	for name := range r.ids {
		statuses[name] = clone(Object{"phase": "battle", "match": Object{"id": "match", "mode": 2, "side": 0, "teams": []Object{{"members": members}}}})
	}
	return r, statuses
}

func TestTurnTimingIncludesObservationDecisionAndDispatch(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(fmt.Sprintf("rejected=%t", reject), func(t *testing.T) {
			r, statuses := timingRunner(t, reject)
			r.strategy = turnDecisionFunc(func(ctx context.Context, team Object, history []Object) (Decision, error) {
				if err := sleep(ctx, 20*time.Millisecond); err != nil {
					return Decision{}, err
				}
				return (Basic{}).Decide(ctx, team, history)
			})
			if err := r.battle(context.Background(), statuses); err != nil {
				t.Fatal(err)
			}
			rows, err := readObjects(r.store.DB, "SELECT body FROM records WHERE kind='turn_timing'")
			if err != nil || len(rows) != 1 {
				t.Fatal(err, rows)
			}
			v := rows[0]
			if str(v["outcome"]) != "dispatch_complete" || str(v["requested_strategy"]) != "fixture-policy" || str(v["decision_strategy"]) != "basic" || integer(v["planned_orders"]) != 4 || str(v["plan_digest"]) == "" || yes(v["decision_deadline_exceeded"]) {
				t.Fatal("incomplete attempt identity", v)
			}
			durations := obj(v["durations_ms"])
			if len(durations) != 5 || num(durations["observation"]) < 30 || num(durations["decision"]) < 20 || num(durations["dispatch"]) < 40 {
				t.Fatal("timing omitted delayed real runner stages", v)
			}
			total := 0.
			for _, d := range durations {
				total += num(d)
			}
			if math.Abs(total-num(v["total_ms"])) > 1e-5 {
				t.Fatal("stages do not partition end-to-end elapsed time", v)
			}
			var written, submissions int
			if err := r.store.DB.QueryRow("SELECT count(*) FROM records WHERE kind='submission'").Scan(&submissions); err != nil || submissions != 4 {
				t.Fatal(err, submissions)
			}
			if err := r.store.DB.QueryRow("SELECT count(*) FROM battle_intents WHERE status='written'").Scan(&written); err != nil || written != map[bool]int{false: 4, true: 0}[reject] {
				t.Fatal("dispatch timing changed receipt/intent semantics", err, written)
			}
			for _, row := range r.store.History("match") {
				if row["durations_ms"] != nil || row["total_ms"] != nil {
					t.Fatal("runtime timing leaked into model history")
				}
			}
		})
	}
}

func TestTurnTimingCancellationDoesNotLookLikeSuccessfulSubmission(t *testing.T) {
	r, statuses := timingRunner(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.strategy = turnDecisionFunc(func(context.Context, Object, []Object) (Decision, error) {
		cancel()
		return Decision{}, context.Canceled
	})
	if err := r.battle(ctx, statuses); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	rows, err := readObjects(r.store.DB, "SELECT body FROM records WHERE kind='turn_timing'")
	if err != nil || len(rows) != 1 || str(rows[0]["outcome"]) != "canceled" || str(rows[0]["last_stage"]) != "decision" || str(rows[0]["plan_digest"]) != "" {
		t.Fatal("canceled attempt was dropped or mislabeled", err, rows)
	}
	var actions int
	if err := r.store.DB.QueryRow("SELECT count(*) FROM records WHERE kind IN ('turn','submission','strategy_fallback')").Scan(&actions); err != nil || actions != 0 {
		t.Fatal("cancellation created a decision, fallback or submission", err, actions)
	}
}

func TestTurnTimingFallbackKeepsRequestedAndExecutedStrategy(t *testing.T) {
	r, statuses := timingRunner(t, false)
	r.strategy = turnDecisionFunc(func(context.Context, Object, []Object) (Decision, error) {
		return Decision{}, errors.New("fixture strategy failure")
	})
	if err := r.battle(context.Background(), statuses); err != nil {
		t.Fatal(err)
	}
	rows, err := readObjects(r.store.DB, "SELECT body FROM records WHERE kind='turn_timing'")
	if err != nil || len(rows) != 1 || str(rows[0]["requested_strategy"]) != "fixture-policy" || str(rows[0]["decision_strategy"]) != "basic" || str(rows[0]["decision_version"]) != "basic-v1" || str(rows[0]["outcome"]) != "dispatch_complete" {
		t.Fatal("fallback mislabeled as requested policy", err, rows)
	}
	var fallbacks, written int
	if err := r.store.DB.QueryRow("SELECT count(*) FROM records WHERE kind='strategy_fallback'").Scan(&fallbacks); err != nil || fallbacks != 1 {
		t.Fatal(err, fallbacks)
	}
	if err := r.store.DB.QueryRow("SELECT count(*) FROM battle_intents WHERE status='written'").Scan(&written); err != nil || written != 4 {
		t.Fatal("timing prevented fallback dispatch", err, written)
	}
}

func TestTurnTimingLateDecisionIsNotDispatched(t *testing.T) {
	r, statuses := timingRunner(t, false)
	// Use a short real server-clock budget, without replacing the runner clock.
	for _, member := range r.members {
		for _, name := range []string{"view.json", "events.json"} {
			path := filepath.Join(member.cfg.Config, name)
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var reply Object
			if err := decode(b, &reply); err != nil {
				t.Fatal(err)
			}
			view := obj(reply["data"])
			if name == "events.json" {
				view = obj(view["observation"])
			}
			clock := obj(obj(view["battle"])["Clock"])
			clock["DeadlineMS"], clock["ReceivedAtMS"] = 6000, time.Now().UnixMilli()
			if err := os.WriteFile(path, enc(reply), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	r.strategy = turnDecisionFunc(func(ctx context.Context, team Object, history []Object) (Decision, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("missing decision budget")
		}
		// A broken strategy ignores cancellation and returns a valid plan late.
		// The runner must still refuse to submit it, as before timing was added.
		timer := time.NewTimer(time.Until(deadline.Add(1100 * time.Millisecond)))
		defer timer.Stop()
		<-timer.C
		return (Basic{}).Decide(context.Background(), team, history)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.battle(ctx, statuses); err != nil {
		t.Fatal(err)
	}
	rows, err := readObjects(r.store.DB, "SELECT body FROM records WHERE kind='turn_timing'")
	if err != nil || len(rows) != 1 || str(rows[0]["outcome"]) != "late_decision" || !yes(rows[0]["decision_deadline_exceeded"]) || str(rows[0]["last_stage"]) != "decision" || str(rows[0]["plan_digest"]) == "" {
		t.Fatal("late valid plan mislabeled as a completed dispatch", err, rows)
	}
	var actions int
	if err := r.store.DB.QueryRow("SELECT count(*) FROM records WHERE kind IN ('turn','submission','strategy_fallback')").Scan(&actions); err != nil || actions != 0 {
		t.Fatal("late plan produced game actions", err, actions)
	}
}
