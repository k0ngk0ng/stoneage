package battletrain

import (
	"fmt"
	"reflect"
)

// BalancedPairing crosses two seeds, roster ownership and arena side. Empty
// keeps the historical four-game, fixed-roster schedule exactly.
const BalancedPairing = "roster-side-v1"

func validatePairing(pairing string) error {
	if pairing != "" && pairing != BalancedPairing {
		return fmt.Errorf("unsupported battle pairing %q", pairing)
	}
	return nil
}

func familyGames(pairing string) int {
	if pairing == BalancedPairing {
		return 8
	}
	return 4
}

func pairingSwap(pairing string, game uint64) bool {
	if pairing == BalancedPairing {
		return (game%2)^((game/2)%2) == 1
	}
	return game%2 == 1
}

func experimentSchema(pairing string) string {
	if pairing == BalancedPairing {
		return "commander-experiment-v2"
	}
	return "commander-experiment-v1"
}

func evaluationSchema(pairing string) string {
	if pairing == BalancedPairing {
		return "commander-evaluation-v3"
	}
	return "commander-evaluation-v2"
}

func championSchema(pairing string) string {
	if pairing == BalancedPairing {
		return "native-champion-registry-v2"
	}
	return "native-champion-registry-v1"
}

// Also validate unbound reports, whose group suite cannot be reconstructed
// from an embedded experiment. Evidence verification additionally reconstructs
// excluded-family selection from the frozen models and config.
func validateBalancedGames(games []EvaluationGame) error {
	groups := map[string][]EvaluationGame{}
	for _, g := range games {
		groups[g.Opponent+":"+g.Group] = append(groups[g.Opponent+":"+g.Group], g)
	}
	for _, group := range groups {
		if len(group) != 8 || group[0].Scenario.Seed == group[4].Scenario.Seed {
			return fmt.Errorf("balanced evaluation requires eight games and two distinct seeds per family")
		}
		for i, game := range group {
			want := group[0].Scenario
			want.Seed = group[(i/4)*4].Scenario.Seed
			if pairingSwap(BalancedPairing, uint64(i)) {
				swapScenario(&want)
			}
			if game.CandidateSide != i%2 || game.OpponentPolicy != group[0].OpponentPolicy || !reflect.DeepEqual(want, game.Scenario) {
				return fmt.Errorf("evaluation does not cross roster ownership and arena side")
			}
		}
	}
	return nil
}
