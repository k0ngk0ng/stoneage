package battletrain

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/k0ngk0ng/stoneage/internal/battlenet"
)

// Checkpoints reference immutable objects. Old games, the rule/config spec
// and scorer weights are not copied into every per-game progress snapshot.
type buildSearchDisk struct {
	Schema                string   `json:"schema"`
	Spec                  string   `json:"spec"`
	Stage                 string   `json:"stage"`
	Generation            int      `json:"generation"`
	Pending               []Roster `json:"pending"`
	Search                []string `json:"search"`
	Validation            []string `json:"validation"`
	Test                  []string `json:"test"`
	Scorer                string   `json:"scorer,omitempty"`
	ScorerTrainingRosters []string `json:"scorer_training_rosters,omitempty"`
	Selected              string   `json:"selected,omitempty"`
}
type buildEvaluationDisk struct {
	Roster Roster   `json:"roster"`
	Games  []string `json:"games"`
}

func persistBuildSearch(root string, s BuildSearchState, cache map[string]bool) (buildSearchDisk, error) {
	d := buildSearchDisk{Schema: "build-search-state-v1", Stage: s.Stage, Generation: s.Generation, Pending: s.Pending, ScorerTrainingRosters: s.ScorerTrainingRosters, Selected: s.Selected}
	dir := filepath.Join(root, "search-objects")
	if e := os.MkdirAll(dir, 0700); e != nil {
		return d, e
	}
	put := func(value any) (string, error) {
		id, e := Digest(value)
		if e != nil {
			return "", e
		}
		if cache[id] {
			return id, nil
		}
		if e = writeObject(filepath.Join(dir, id+".json"), value); e != nil {
			return "", e
		}
		cache[id] = true
		return id, nil
	}
	var e error
	d.Spec, e = put(s.Spec)
	if e != nil {
		return d, e
	}
	if s.Scorer != nil {
		d.Scorer, e = put(s.Scorer)
		if e != nil {
			return d, e
		}
	}
	for i, list := range [][]BuildEvaluation{s.Search, s.Validation, s.Test} {
		var ids []string
		for _, result := range list {
			r := buildEvaluationDisk{Roster: result.Roster}
			for _, game := range result.Games {
				id, e := put(game)
				if e != nil {
					return d, e
				}
				r.Games = append(r.Games, id)
			}
			id, e := put(r)
			if e != nil {
				return d, e
			}
			ids = append(ids, id)
		}
		switch i {
		case 0:
			d.Search = ids
		case 1:
			d.Validation = ids
		case 2:
			d.Test = ids
		}
	}
	return d, nil
}

func expandBuildSearch(root string, d buildSearchDisk) (BuildSearchState, error) {
	s := BuildSearchState{Stage: d.Stage, Generation: d.Generation, Pending: d.Pending, ScorerTrainingRosters: d.ScorerTrainingRosters, Selected: d.Selected}
	if d.Schema != "build-search-state-v1" || len(d.Search) > 896 || len(d.Validation) > 16 || len(d.Test) > 2 {
		return s, fmt.Errorf("invalid build search disk schema/dimensions")
	}
	get := func(id string, value any) error {
		if !digest(id) {
			return fmt.Errorf("invalid search object identity")
		}
		if e := readObject(filepath.Join(root, "search-objects", id+".json"), value, 16<<20); e != nil {
			return e
		}
		actual, e := Digest(value)
		if e != nil {
			return e
		}
		if actual != id {
			return fmt.Errorf("search object checksum mismatch")
		}
		return nil
	}
	if e := get(d.Spec, &s.Spec); e != nil {
		return s, e
	}
	if d.Scorer != "" {
		var scorer battlenet.BuildScorer[float32]
		if e := get(d.Scorer, &scorer); e != nil {
			return s, e
		}
		s.Scorer = &scorer
	}
	gameCount := 0
	for i, list := range [][]string{d.Search, d.Validation, d.Test} {
		var results []BuildEvaluation
		for _, id := range list {
			var r buildEvaluationDisk
			if e := get(id, &r); e != nil {
				return s, e
			}
			gameCount += len(r.Games)
			if gameCount > 200000 {
				return s, fmt.Errorf("search object game limit exceeded")
			}
			result := BuildEvaluation{Roster: r.Roster}
			for _, id := range r.Games {
				var game BuildGame
				if e := get(id, &game); e != nil {
					return s, e
				}
				result.Games = append(result.Games, game)
			}
			results = append(results, result)
		}
		switch i {
		case 0:
			s.Search = results
		case 1:
			s.Validation = results
		case 2:
			s.Test = results
		}
	}
	return s, nil
}
