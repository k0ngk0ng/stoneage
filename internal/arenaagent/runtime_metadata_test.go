package arenaagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func metadataFixture(rules bool) Object {
	clock := Object{"RulesVersion": RulesVersion}
	if rules {
		clock["RulesDigest"] = strings.Repeat("a", 64)
		clock["EnginePlatform"] = "linux-arm64"
	}
	return Object{"schema_version": 1, "match_id": "metadata-match", "turn": 0, "battle": Object{"Clock": clock}}
}

func TestOptionalRulesWaitForProjectionAndPreserveAbsence(t *testing.T) {
	for _, available := range []bool{false, true} {
		calls := 0
		v, err := waitBattleMetadata(context.Background(), false, 60*time.Millisecond, func(context.Context) (Object, error) {
			calls++
			return metadataFixture(available && calls >= 2), nil
		})
		if err != nil || calls < 2 || (str(obj(obj(v["battle"])["Clock"])["RulesDigest"]) != "") != available {
			t.Fatal("optional wait lost delayed metadata or invented provenance", available, calls, v, err)
		}
	}
}

func TestOptionalRulesNeverHideRequiredClockErrorsOrCancellation(t *testing.T) {
	transportErr := errors.New("transport failed")
	_, err := waitBattleMetadata(context.Background(), false, 0, func(context.Context) (Object, error) { return nil, transportErr })
	if !errors.Is(err, transportErr) {
		t.Fatal(err)
	}
	for _, requiredRules := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
		_, err := waitBattleMetadata(ctx, requiredRules, 0, func(context.Context) (Object, error) {
			v := metadataFixture(false)
			if !requiredRules {
				obj(obj(v["battle"])["Clock"])["RulesVersion"] = "unsupported"
			}
			return v, nil
		})
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("missing required metadata accepted", requiredRules, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	_, err = waitBattleMetadata(ctx, false, 0, func(context.Context) (Object, error) {
		cancel()
		return metadataFixture(true), nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation returned a successful projection", err)
	}
}

func TestAllCommanderStrategiesCollectRulesBeforeAtomicBatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-backed CLI fixture")
	}
	neural, _ := neuralFixtureModel(t, 1)
	for _, strategy := range []Strategy{Basic{}, &LLM{}, &Learned{mode: 1}, &Hybrid{Local: &Learned{mode: 1}}, neural, &Hybrid{Local: neural}} {
		t.Run(strategy.ID(), func(t *testing.T) {
			dir := t.TempDir()
			store, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer store.DB.Close()
			view := metadataFixture(true)
			batch := Object{"stream": "metadata-stream", "cursor": 0, "gap": false, "events": []Object{}, "observation": view}
			files := map[string][]byte{
				"view.json":  enc(Object{"ok": true, "data": view}),
				"batch.json": enc(Object{"ok": true, "data": batch}),
				"sactl": []byte(`#!/bin/sh
set -eu
cd "$(dirname "$0")"
shift 5
case "$1" in
 query)
  case "$2" in
   BTIME) touch clock-requested ;;
   BTRULES) test -f clock-requested; touch rules-requested ;;
   *) exit 1 ;;
  esac
  echo '{"ok":true}' ;;
 battle-state)
  test -f rules-requested
  touch projection-observed
  cat view.json ;;
 battle-events)
  test -f projection-observed
  cat batch.json ;;
 *) exit 1 ;;
esac
`),
			}
			for name, body := range files {
				if err := os.WriteFile(filepath.Join(dir, name), body, 0700); err != nil {
					t.Fatal(err)
				}
			}
			r := &Runner{strategy: strategy, store: store}
			m := &member{cfg: MemberConfig{ID: "member"}, binary: filepath.Join(dir, "sactl"), store: store}
			got, err := r.collect(context.Background(), m)
			if err != nil || str(obj(obj(got["battle"])["Clock"])["RulesDigest"]) != strings.Repeat("a", 64) || str(obj(got["event_cutoff"])["stream"]) != "metadata-stream" {
				t.Fatal("commander skipped metadata or lost atomic observation", got, err)
			}
		})
	}
}
