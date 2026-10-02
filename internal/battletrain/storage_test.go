package battletrain

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestShardRoundTripAndCorruption(t *testing.T) {
	m := testModel(t)
	episode := testTrajectory(t, m, 5)
	dir := t.TempDir()
	manifest, e := SaveShard(dir, []Trajectory{episode})
	if e != nil {
		t.Fatal(e)
	}
	if manifest.TeamTurns != 5 || manifest.Episodes != 1 || manifest.Bytes <= 0 {
		t.Fatal("incomplete manifest")
	}
	loaded, got, e := LoadShard(dir, manifest.Digest)
	if e != nil {
		t.Fatal(e)
	}
	// Raw wire commands are deliberately json:"-" in BattleCandidate and
	// are reconstructed by the shared resolver, not loaded from training data.
	wantDigest, _ := Digest([]Trajectory{episode})
	gotDigest, _ := Digest(loaded)
	if wantDigest != gotDigest || !reflect.DeepEqual(loaded[0].Steps[0].Frame, episode.Steps[0].Frame) || !reflect.DeepEqual(got, manifest) {
		t.Fatal("training data changed during round trip")
	}
	again, e := SaveShard(dir, []Trajectory{episode})
	if e != nil || !reflect.DeepEqual(again, manifest) {
		t.Fatal("immutable shard could not be reused", e)
	}
	if _, _, e = LoadShard(dir, "../escape"); e == nil {
		t.Fatal("path traversal accepted")
	}
	path := filepath.Join(dir, manifest.Digest+".jsonl.gz")
	f, e := os.OpenFile(path, os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.WriteAt([]byte{255}, 10)
	f.Close()
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = LoadShard(dir, manifest.Digest); e == nil {
		t.Fatal("corrupt data accepted")
	}
	if _, e = SaveShard(dir, []Trajectory{episode}); e == nil {
		t.Fatal("corrupt existing immutable shard silently overwritten")
	}
}

func TestProvenanceAndTerminalValidation(t *testing.T) {
	m := testModel(t)
	for _, mutate := range []func(*Trajectory){
		func(tr *Trajectory) { tr.Steps[0].Observations[0].Battle.Participants[0].HP-- },
		func(tr *Trajectory) { tr.Steps[0].History.Observation.ID = "other-cutoff" },
		func(tr *Trajectory) { tr.Steps[0].Frame.Entities[0][5]++ },
		func(tr *Trajectory) {
			tr.Steps[1].Frame.EventSequence[0], tr.Steps[1].Frame.EventSequence[1] = tr.Steps[1].Frame.EventSequence[1], tr.Steps[1].Frame.EventSequence[0]
		},
		func(tr *Trajectory) {
			tr.Steps[1].History.Events[0].Effects[0], tr.Steps[1].History.Events[0].Effects[1] = tr.Steps[1].History.Events[0].Effects[1], tr.Steps[1].History.Events[0].Effects[0]
		},
		func(tr *Trajectory) { tr.FinalObservations = nil },
		func(tr *Trajectory) { tr.FinalHistory.Observation.ID = "different-final-cutoff" },
		func(tr *Trajectory) { tr.Bootstrap = .5 },
		func(tr *Trajectory) { tr.Steps[0].Reward = -1 },
	} {
		tr := testTrajectory(t, m, 2)
		mutate(&tr)
		if tr.Validate() == nil {
			t.Fatal("invalid provenance or terminal label accepted")
		}
	}
}
