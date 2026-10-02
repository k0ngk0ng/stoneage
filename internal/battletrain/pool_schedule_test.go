package battletrain

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"testing"
)

func TestPoolMixLegacyScheduleIdentity(t *testing.T) {
	p := buildPoolFixture(t)
	a := experimentCandidate(t, experimentFixture(t))
	c := DefaultEvaluationConfig()
	c.Points, c.PetPoints, c.Level, c.MaxTurns = 20, 0, 10, 20
	x, err := NewExperimentWithPoolMix(context.Background(), p.Environment, c, [3]int{6, 2, 2}, &p, &a, 2)
	if err != nil {
		t.Fatal(err)
	}
	// Reconstruct the previously emitted pool-first manifest. Loading and
	// training it must preserve its original order, including after resume.
	ids := map[string]bool{}
	for _, r := range p.Rosters {
		ids[r.ID()] = true
	}
	var pool, fresh, heldout []ExperimentFamily
	for _, f := range x.Families {
		if f.Split != "train" {
			heldout = append(heldout, f)
		} else if ids[(Roster{Players: f.Scenario.Builds[:1]}).ID()] {
			pool = append(pool, f)
		} else {
			fresh = append(fresh, f)
		}
	}
	x.Families = append(append(pool, fresh...), heldout...)
	id, err := Digest(x)
	if err != nil {
		t.Fatal(err)
	}
	// Recorded before interleaving was introduced, rather than generated from
	// the new implementation under test.
	if id != "b508c75110fc9c9959907085db09c23668c2731904de1c373567bc3847d711df" {
		t.Fatal("legacy experiment content changed", id)
	}
	path := t.TempDir() + "/old-experiment.json"
	if _, err = SaveExperiment(path, x); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadExperiment(path)
	if err != nil || !reflect.DeepEqual(x, loaded) {
		t.Fatal("legacy manifest reordered on load", err)
	}
	for game := uint64(0); game < 96; game++ {
		_, got := loaded.trainingScenario(731, game)
		if got != x.Families[(game/8)%6].Group {
			t.Fatalf("legacy game %d changed family", game)
		}
	}
}

func TestPoolMixInterleavedBatchCoverage(t *testing.T) {
	for mode := 1; mode <= 5; mode++ {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			p := buildPoolFixture(t)
			p.Config.Mode = mode
			rosters, err := initialRosters(p.Config)
			if err != nil {
				t.Fatal(err)
			}
			p.Rosters = rosters[:3]
			sort.Slice(p.Rosters, func(i, j int) bool { return p.Rosters[i].ID() < p.Rosters[j].ID() })
			ids := map[string]bool{}
			for _, r := range p.Rosters {
				ids[r.ID()] = true
			}
			c := DefaultEvaluationConfig()
			c.Mode, c.Points, c.PetPoints, c.Level, c.MaxTurns = mode, 20, 0, 10, 20
			x, err := NewExperimentWithPoolMix(context.Background(), p.Environment, c, [3]int{32, 2, 2}, &p, nil, 6)
			if err != nil {
				t.Fatal(err)
			}
			// A 128-game batch spans 16 complete eight-game families. Previously
			// alternating batches had 48/0 pool games. Both must now contain 24.
			for batch := 0; batch < 8; batch++ {
				poolGames := 0
				families := map[string]int{}
				for game := batch * 128; game < (batch+1)*128; game++ {
					s, group := x.trainingScenario(c.Seed, uint64(game))
					left := ids[(Roster{Players: s.Builds[:mode]}).ID()]
					right := ids[(Roster{Players: s.Builds[mode:]}).ID()]
					if left != right {
						t.Fatal("mixed sides within one family")
					}
					if left {
						poolGames++
					}
					families[group]++
				}
				if poolGames != 24 || len(families) != 16 {
					t.Fatalf("batch %d: pool games=%d, families=%d", batch, poolGames, len(families))
				}
				for _, count := range families {
					if count != 8 {
						t.Fatal("paired family split across batches", count)
					}
				}
			}
		})
	}
}

func TestInterleaveTrainingFamiliesBalancesCyclicWindows(t *testing.T) {
	for n := 2; n <= 40; n++ {
		for count := 1; count < n; count++ {
			pool, fresh := make([]ExperimentFamily, count), make([]ExperimentFamily, n-count)
			for i := range pool {
				pool[i].Group = fmt.Sprintf("pool-%d", i)
			}
			for i := range fresh {
				fresh[i].Group = fmt.Sprintf("fresh-%d", i)
			}
			out := interleaveTrainingFamilies(pool, fresh)
			if len(out) != n {
				t.Fatal("family count changed")
			}
			var gotPool, gotFresh []ExperimentFamily
			flags := make([]int, n)
			for i, f := range out {
				if f.Group[:4] == "pool" {
					gotPool = append(gotPool, f)
					flags[i] = 1
				} else {
					gotFresh = append(gotFresh, f)
				}
			}
			if !reflect.DeepEqual(pool, gotPool) || !reflect.DeepEqual(fresh, gotFresh) {
				t.Fatal("lost, duplicated or reordered a population")
			}
			// All cyclic windows, including batches that straddle the end of
			// a pass, differ from the requested family fraction by <1 family.
			for start := 0; start < n; start++ {
				seen := 0
				for length := 1; length <= n; length++ {
					seen += flags[(start+length-1)%n]
					delta := seen*n - length*count
					if delta <= -n || delta >= n {
						t.Fatalf("n=%d pool=%d window=%d/%d discrepancy=%d", n, count, start, length, delta)
					}
				}
			}
		}
	}
}
