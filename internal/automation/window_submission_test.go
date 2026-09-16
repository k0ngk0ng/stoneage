package automation

import (
	"context"
	"errors"
	"testing"
)

func TestInformationalWindowRequiresSubmissionAndDoesNotReplayUnknown(t *testing.T) {
	for _, confirmed := range []bool{true, false} {
		name := "unknown"
		if confirmed {
			name = "submitted"
		}
		t.Run(name, func(t *testing.T) {
			e, g, p := fixture(t)
			g.observation.Inventory["item:2415"] = 1
			g.observation.Flags["now:2"] = true
			success := []Condition{{Kind: "window_submitted", ID: "himiko", Value: 231}}
			p.Completion = success
			p.Steps[0].Success = success
			g.execute = func(Action) error {
				if !confirmed {
					return errors.New("uncertain write")
				}
				g.observation.Revision++
				g.observation.SubmittedWindows = map[string]int{"himiko": 231}
				return nil
			}
			ctx := context.Background()
			started, err := e.Start(ctx, p)
			if err != nil || started.Status != Running {
				t.Fatalf("flower must not skip confirmation: %+v %v", started, err)
			}
			_, err = e.Tick(ctx, p.ID)
			if confirmed && err != nil {
				t.Fatal(err)
			}
			if !confirmed && err == nil {
				t.Fatal("uncertain write unexpectedly succeeded")
			}
			if confirmed {
				c, err := e.Tick(ctx, p.ID)
				if err != nil || c.Status != Completed || c.Confirmation == nil || c.Confirmation.SubmittedWindows["himiko"] != 231 {
					t.Fatalf("local submission missing from checkpoint: %+v %v", c, err)
				}
			} else {
				if _, err := e.Resume(ctx, p.ID); err == nil {
					t.Fatal("unknown write resumed without evidence")
				}
			}
			_, _ = e.Tick(ctx, p.ID)
			if len(g.actions) != 1 {
				t.Fatalf("window sent %d times", len(g.actions))
			}
		})
	}
}
