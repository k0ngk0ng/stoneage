package sacli

import (
	"context"
	"fmt"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/ainavigation"
)

// knowledge lazily loads the server tables used for warps and encounters.
func (s *Server) knowledge() (*aiknowledge.Knowledge, error) {
	s.knowledgeOnce.Do(func() {
		loaded, err := aiknowledge.Load(context.Background(), aiknowledge.Options{DataDir: s.config.MapDirectory})
		if err != nil {
			s.knowledgeErr = fmt.Errorf("load server data from %s: %w", s.config.MapDirectory, err)
			return
		}
		s.knowledgeData = loaded
	})
	if s.knowledgeErr != nil {
		return nil, s.knowledgeErr
	}
	return s.knowledgeData, nil
}

// commandEncounters lists the encounter areas recorded for the current floor,
// which is what tells a player (or a model) where walking can start a battle.
func (s *Server) commandEncounters(ctx context.Context, request Request) Response {
	snapshot, err := s.snapshot(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	if err := requireWorld(snapshot); err != nil {
		return actionFailure(err)
	}
	knowledge, err := s.knowledge()
	if err != nil {
		return failure(KindServer, "%v", err)
	}
	floor := int(snapshot.Position.Floor)
	lines := make([]string, 0, 8)
	for _, area := range knowledge.Encounters {
		if area.Floor != floor {
			continue
		}
		lines = append(lines, fmt.Sprintf("area %d: x=%d..%d y=%d..%d groups=%d chance=%d..%d max_enemies=%d",
			area.ID, area.Bounds.X, area.Bounds.X2, area.Bounds.Y, area.Bounds.Y2,
			len(area.GroupIDs), area.EncounterProbability.Min, area.EncounterProbability.Max, area.MaxEnemies))
	}
	if len(lines) == 0 {
		return Response{OK: true, Text: fmt.Sprintf("floor %d has no recorded encounter area; battles start on maps where walking can trigger one", floor)}
	}
	here, ok := knowledge.EncounterAt(floor, int(snapshot.Position.X), int(snapshot.Position.Y))
	status := "you are outside every encounter area"
	if ok {
		status = fmt.Sprintf("you are inside encounter area %d", here.ID)
	}
	return Response{OK: true, Text: fmt.Sprintf("encounter areas on floor %d:\n%s\n%s", floor, strings.Join(lines, "\n"), status)}
}

// walkIntoEncounter walks toward the nearest recorded encounter area on the
// floor and reports whether a battle started. It exists because battles only
// begin where the server's own encounter table says they can.
func (s *Server) commandSeekEncounter(ctx context.Context, request Request) Response {
	snapshot, err := s.snapshot(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	if err := requireWorld(snapshot); err != nil {
		return actionFailure(err)
	}
	knowledge, err := s.knowledge()
	if err != nil {
		return failure(KindServer, "%v", err)
	}
	floor := int(snapshot.Position.Floor)
	navigator, err := s.navigator()
	if err != nil {
		return failure(KindServer, "%v", err)
	}
	origin := ainavigation.Point{X: int(snapshot.Position.X), Y: int(snapshot.Position.Y)}
	best, bestDistance := ainavigation.Point{}, -1
	for _, area := range knowledge.Encounters {
		if area.Floor != floor {
			continue
		}
		for y := area.Bounds.Y; y <= area.Bounds.Y2; y++ {
			for x := area.Bounds.X; x <= area.Bounds.X2; x++ {
				if !navigator.Walkable(floor, x, y) {
					continue
				}
				distance := absInt(x-origin.X) + absInt(y-origin.Y)
				if bestDistance < 0 || distance < bestDistance {
					best, bestDistance = ainavigation.Point{X: x, Y: y}, distance
				}
			}
		}
	}
	if bestDistance < 0 {
		return actionFailure(fmt.Errorf("seek-encounter: floor %d has no reachable encounter area", floor))
	}
	if _, err := s.walkTo(ctx, best); err != nil {
		return actionFailure(fmt.Errorf("seek-encounter: walk to (%d,%d): %w", best.X, best.Y, err))
	}
	// Encounter rolls happen while the character moves inside the area, so
	// take a short walk within it and watch for the phase change.
	for attempt := 0; attempt < 6; attempt++ {
		current, err := s.snapshot(ctx)
		if err != nil {
			return sessionFailure(err)
		}
		if current.Phase == aigame.PhaseBattle && current.Battle.Active {
			return Response{OK: true, Text: fmt.Sprintf("battle started inside the encounter area\n%s", s.renderObservation(current)),
				Data: replyJSON(current)}
		}
		step := ainavigation.Point{X: int(current.Position.X), Y: int(current.Position.Y)}
		if best.X >= step.X {
			step.X++
		} else {
			step.X--
		}
		if !navigator.Walkable(floor, step.X, step.Y) {
			step = ainavigation.Point{X: best.X, Y: int(current.Position.Y) + 1}
		}
		if _, err := s.walkTo(ctx, step); err != nil {
			break
		}
	}
	final, err := s.snapshot(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	if final.Battle.Active {
		return Response{OK: true, Text: fmt.Sprintf("battle started\n%s", s.renderObservation(final)), Data: replyJSON(final)}
	}
	return Response{OK: false, Kind: KindAction, Text: fmt.Sprintf("walked to the encounter area at (%d,%d) but no battle started yet\n%s",
		best.X, best.Y, s.renderObservation(final)), Data: replyJSON(final),
		Error: "no encounter triggered yet; keep walking inside the area"}
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
