package arenaagent

import "time"

// A timing describes one eligible decision attempt, including retries and
// failures. Durations use Go's monotonic clock. Dispatch completion says nothing
// about whether the server accepted or settled every planned order.
type turnTiming struct {
	Schema                   int                `json:"schema_version"`
	Turn                     int                `json:"turn"`
	Observation              string             `json:"observation_id"`
	RequestedStrategy        string             `json:"requested_strategy"`
	DecisionStrategy         string             `json:"decision_strategy,omitempty"`
	DecisionVersion          string             `json:"decision_version,omitempty"`
	Plan                     string             `json:"plan_digest,omitempty"`
	Orders                   int                `json:"planned_orders"`
	ReusedPlan               bool               `json:"reused_plan"`
	Outcome                  string             `json:"outcome"`
	LastStage                string             `json:"last_stage"`
	RemainingBeforeHistoryMS float64            `json:"remaining_before_history_ms"`
	DecisionBudgetMS         float64            `json:"decision_budget_ms"`
	DecisionDeadlineExceeded bool               `json:"decision_deadline_exceeded"`
	Durations                map[string]float64 `json:"durations_ms"`
	TotalMS                  float64            `json:"total_ms"`
	started, last            time.Time
}

func newTurnTiming(started time.Time, team Object, strategy string, remaining float64, deadline time.Time) *turnTiming {
	t := &turnTiming{
		Schema: 1, Turn: integer(team["turn"]), Observation: str(team["observation_id"]),
		RequestedStrategy: strategy, Outcome: "error", LastStage: "observation",
		RemainingBeforeHistoryMS: remaining, DecisionBudgetMS: float64(time.Until(deadline)) / float64(time.Millisecond),
		Durations: map[string]float64{}, started: started, last: started,
	}
	t.next("history")
	return t
}

func (t *turnTiming) next(stage string) {
	now := time.Now()
	t.Durations[t.LastStage] += float64(now.Sub(t.last)) / float64(time.Millisecond)
	t.last, t.LastStage = now, stage
}

func (t *turnTiming) finish() {
	now := time.Now()
	t.Durations[t.LastStage] += float64(now.Sub(t.last)) / float64(time.Millisecond)
	t.TotalMS = float64(now.Sub(t.started)) / float64(time.Millisecond)
}
