package admin

import (
	"context"
	"net/http"

	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

type aiLifeStateReader interface {
	ListAgentNotes(context.Context, string, int) ([]airuntime.AgentNote, error)
	ListSchedules(context.Context, string, ...int) ([]airuntime.Schedule, error)
}

// This view uses the authenticated admin boundary and deliberately excludes
// token attempts, generated Codex configuration and schedule claim tokens.
func (server *Server) aiLifeStateAPI(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		playerJSONError(w, http.StatusMethodNotAllowed, "请求方式不支持")
		return
	}
	if !aiIDPattern.MatchString(id) {
		playerJSONError(w, http.StatusNotFound, "AI 玩家不存在")
		return
	}
	if _, err := server.aiStore.GetProfile(r.Context(), id); err != nil {
		aiStoreError(w, err)
		return
	}
	store, ok := server.aiStore.(aiLifeStateReader)
	if !ok {
		playerJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	// Ask for one extra row to make truncation explicit rather than claiming
	// a bounded display is the complete history.
	notes, err := store.ListAgentNotes(r.Context(), id, 51)
	if err != nil {
		aiStoreError(w, err)
		return
	}
	schedules, err := store.ListSchedules(r.Context(), id, 51)
	if err != nil {
		aiStoreError(w, err)
		return
	}
	notesMore, schedulesMore := len(notes) > 50, len(schedules) > 50
	if notesMore {
		notes = notes[:50]
	}
	if schedulesMore {
		schedules = schedules[:50]
	}
	if notes == nil {
		notes = []airuntime.AgentNote{}
	}
	if schedules == nil {
		schedules = []airuntime.Schedule{}
	}
	playerJSON(w, http.StatusOK, map[string]any{
		"available": true, "notes": notes, "schedules": schedules,
		"notes_truncated": notesMore, "schedules_truncated": schedulesMore,
	})
}
