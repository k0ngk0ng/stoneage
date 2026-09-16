package aileveling

import (
	"encoding/json"

	"github.com/k0ngk0ng/stoneage/internal/automation"
)

// Persist selectors with the encounter step so an explicit resume after a
// process restart cannot silently discard the chosen area or supply policy.
type planSettings struct {
	AreaID     int                        `json:"area_id,omitempty"`
	Parameters map[string]json.RawMessage `json:"parameters,omitempty"`
}

func (c *Coordinator) settingsForPlan(plan automation.Plan) (runSettings, error) {
	settings := c.runSettings(plan.ID)
	if len(plan.Steps) == 0 {
		return settings, ErrInvalidRequest
	}
	var saved planSettings
	if err := json.Unmarshal(plan.Steps[0].Action.Arguments, &saved); err != nil {
		return settings, err
	}
	// Old checkpoints contain {} and may still have in-memory selectors.
	if saved.AreaID != 0 {
		settings.areaID = saved.AreaID
	}
	if saved.Parameters != nil {
		settings.parameters = cloneParameters(saved.Parameters)
	}
	return settings, nil
}
