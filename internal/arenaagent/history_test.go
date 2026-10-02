package arenaagent

import (
	"reflect"
	"testing"
)

func TestHistoryRetainsStreamsGapsAndObservationCutoffsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	s, e := OpenStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	batch := func(stream string, sequence int, gap bool) Object {
		return clone(Object{"stream": stream, "cursor": sequence, "gap": gap,
			"observation": Object{"match_id": "match"},
			"events":      []Object{{"sequence": sequence, "at_ms": sequence, "match_id": "match", "turn": 0}}})
	}
	s.Ingest("member", batch("before-reconnect", 1, false))
	cutoff := Object{"stream": "before-reconnect", "cursor": 1, "gap": false}
	s.Record("turn", Object{"turn": 0, "team": Object{"members": Object{"member": Object{"event_cutoff": cutoff}}}}, "match", "")
	// More data arrives after the saved decision, followed by a fresh stream
	// whose sequence starts at 1 again. Neither may alter the old cutoff.
	s.Ingest("member", batch("before-reconnect", 2, false))
	s.Ingest("member", batch("after-reconnect", 1, true))
	if e = s.Err(); e != nil {
		t.Fatal(e)
	}
	before := s.History("match")
	if e = s.DB.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = OpenStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	after := s.History("match")
	if s.Err() != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("restart changed history", s.Err())
	}
	seen := map[string]int{}
	gaps, turns := 0, 0
	for _, entry := range after {
		if event := obj(entry["event"]); event != nil {
			seen[str(entry["stream"])]++
			if str(entry["member"]) != "member" {
				t.Fatal("event observer identity lost")
			}
		}
		if gap := obj(entry["event_gap"]); gap != nil {
			gaps++
			if str(entry["member"]) != "member" || str(gap["stream"]) != "after-reconnect" || integer(gap["cursor"]) != 1 {
				t.Fatal("gap metadata lost", entry)
			}
		}
		if team := obj(entry["team"]); team != nil {
			turns++
			got := obj(obj(obj(team["members"])["member"])["event_cutoff"])
			if !reflect.DeepEqual(got, clone(cutoff)) {
				t.Fatal("new events overwrote decision cutoff")
			}
		}
	}
	if gaps != 1 || turns != 1 || seen["before-reconnect"] != 2 || seen["after-reconnect"] != 1 {
		t.Fatal("history lost evidence or conflated streams", gaps, turns, seen)
	}
}
