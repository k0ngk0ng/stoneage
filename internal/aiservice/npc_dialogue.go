package aiservice

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

const npcNextPage = 32

// npc.dialogue consumes a reviewed paginated MESSAGE window. NEXT must be
// explicitly catalogued as a free action; the final choice retains its quote.
// Only a fresh server WN permits another submission, never an unrelated event.
func validateDialogueChoices(window NPCWindowSpec, final NPCChoice) error {
	next, err := resolveNPCChoice(window.Choices, json.RawMessage("32"))
	if err != nil {
		return err
	}
	quote, err := verifiedNPCQuote(next)
	if err != nil || window.Type != 0 || next.Button != npcNextPage || next.Data != "" || quote != 0 {
		return npcInvalid("paginated dialogue requires a reviewed, free MESSAGE NEXT choice")
	}
	if final.Button != 1 && final.Button != 4 {
		return npcInvalid("paginated dialogue final choice must be OK or YES")
	}
	return nil
}

func (s *NPCSkill) executeDialogue(ctx context.Context, action automation.Action, spec NPCSpec, args npcWindowArguments) error {
	window, err := resolveNPCWindow(spec, args.WindowSequence)
	if err != nil {
		return err
	}
	final, err := resolveNPCChoice(window.Choices, args.Choice)
	if err != nil {
		return err
	}
	expected := action.ExpectedRevision
	for page := 0; page < 32; page++ {
		current, err := s.observe(ctx)
		if err != nil {
			return err
		}
		if current.Revision != expected {
			return aigame.ErrStaleRevision
		}
		if err := validateWindowSnapshot(spec, args, current); err != nil {
			return err
		}
		active := current.ActiveWindow
		if active.ButtonType&npcNextPage == 0 {
			if active.ButtonType&int32(final.Button) == 0 {
				return npcInvalid("dialogue does not offer the reviewed final button")
			}
			return s.executeWindow(ctx, expected, action.MaximumCost, spec, args)
		}
		nextArgs := args
		nextArgs.Choice = json.RawMessage("32")
		nextArgs.PetIDs = nil
		if err := s.executeWindow(ctx, expected, 0, spec, nextArgs); err != nil {
			return err
		}
		// A sent page request is uncertain until WN replaces the submitted
		// window. Do not resend it on timeout, reconnect or unrelated updates.
		waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		for {
			fresh, observeErr := s.observe(waitCtx)
			if observeErr != nil {
				cancel()
				return observeErr
			}
			if fresh.ActiveWindow != nil && fresh.ActiveWindow.Open && !fresh.ActiveWindow.Submitted {
				if err := validateWindowSnapshot(spec, args, fresh); err != nil {
					cancel()
					return err
				}
				expected = fresh.Revision
				break
			}
			select {
			case <-waitCtx.Done():
				cancel()
				return fmt.Errorf("dialogue page acknowledgement missing: %w", waitCtx.Err())
			case <-time.After(25 * time.Millisecond):
			}
		}
		cancel()
	}
	return npcInvalid("dialogue exceeded 32 pages; final choice was not sent")
}
