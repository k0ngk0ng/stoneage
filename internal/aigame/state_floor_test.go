package aigame

import "testing"

func TestFloorChangeInvalidatesVisibleActors(t *testing.T) {
	state := newGameState(true)
	applyEventLocked(&state, stringEvent("S", "C100|30|30|4|5"))
	state.actors[17] = ActorSnapshot{ID: 17, Name: "Old village NPC", X: 8, Y: 9}

	// An ordinary owner-coordinate refresh must preserve nearby actors.
	applyEventLocked(&state, stringEvent("S", "C100|30|30|5|5"))
	if len(state.actors) != 1 {
		t.Fatal("same-floor coordinate refresh discarded visible actors")
	}
	applyEventLocked(&state, stringEvent("S", "C200|30|30|8|9"))
	if len(state.actors) != 0 || state.snapshot.Position.Floor != 200 {
		t.Fatal("map change retained actors whose records belong to the old floor")
	}
}
