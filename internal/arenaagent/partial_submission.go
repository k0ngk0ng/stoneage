package arenaagent

import "github.com/k0ngk0ng/stoneage/internal/aigame"

// An uncertain local intent is enough to prohibit replacing the remainder of
// a team plan. A dead player's server-completed bit alone is not a local order.
// This rule is shared by saved-plan recovery and the runner's fallback boundary.
func teamHasSubmittedOrReservedOrders(team Object) (bool, error) {
	members := obj(team["members"])
	if len(members) == 0 {
		return false, neuralError("invalid_observation", "missing controlled team observation")
	}
	for _, name := range sortedKeys(members) {
		member := obj(members[name])
		if member == nil || obj(member["battle"]) == nil {
			return false, neuralError("invalid_observation", "missing typed battle observation")
		}
		var battle aigame.BattleSnapshot
		if err := decode(enc(member["battle"]), &battle); err != nil {
			return false, neuralError("invalid_observation", "invalid typed battle observation")
		}
		reserved := obj(member["reserved_actors"])
		if raw := member["reserved_actors"]; raw != nil && reserved == nil {
			return false, neuralError("invalid_observation", "invalid reserved actor state")
		}
		if battle.PlayerSubmitted && !battle.DeadPlayerCommandComplete() || battle.PetSubmitted || len(reserved) > 0 {
			return true, nil
		}
	}
	return false, nil
}
