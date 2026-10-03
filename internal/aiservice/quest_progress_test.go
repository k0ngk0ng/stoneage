package aiservice

import (
	"context"
	"testing"
)

func TestQuestRequestProgressSurvivesDeliveryAndRequiresObservation(t *testing.T) {
	b, session := gameFixture(t)
	for _, state := range []string{"unknown", "not-started", "started", "delivered", "cleared"} {
		session.snapshot.AI.Received = state != "unknown"
		session.snapshot.AI.NowEvents[0], session.snapshot.AI.EndEvents[0] = 0, 0
		if state == "started" {
			session.snapshot.AI.NowEvents[0] = 1 << 3
		}
		if state == "delivered" {
			session.snapshot.AI.EndEvents[0] = 1 << 3
		}
		o, err := b.Observe(context.Background(), b.Binding)
		if err != nil {
			t.Fatal(err)
		}
		value, known := o.Flags["event:3"]
		if known != (state != "unknown") || value != (state == "started" || state == "delivered") {
			t.Fatalf("%s: known=%v value=%v", state, known, value)
		}
	}
}
