package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

func (handler *Handler) battleEvents(response http.ResponseWriter, request *http.Request, session *tcpSession) {
	if request.Method != http.MethodGet {
		response.Header().Set("Allow", "GET")
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cursor := uint64(0)
	if raw := request.URL.Query().Get("cursor"); raw != "" {
		var err error
		cursor, err = strconv.ParseUint(raw, 10, 64)
		if err != nil {
			http.Error(response, "invalid cursor", http.StatusBadRequest)
			return
		}
	}
	session.authoritativeMu.RLock()
	observer := session.authoritative
	session.authoritativeMu.RUnlock()
	if observer == nil {
		http.Error(response, "session observer unavailable", http.StatusServiceUnavailable)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	handler.writeJSON(response, http.StatusOK, observer.BattleEvents(request.URL.Query().Get("stream"), cursor))
}

// Shared typed battle surface. Like the existing manual Web controls, writes
// must own the current generation; merely knowing a candidate ID grants no control.
func (handler *Handler) battleView(response http.ResponseWriter, request *http.Request, session *tcpSession) {
	response.Header().Set("Cache-Control", "no-store")
	if request.Method == http.MethodGet {
		snapshot, err := session.observeAuthoritative(request.Context())
		if err != nil {
			http.Error(response, "session observer unavailable", http.StatusServiceUnavailable)
			return
		}
		handler.writeJSON(response, http.StatusOK, aigame.NewBattleView(snapshot))
		return
	}
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", "GET, POST")
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input struct {
		Generation uint64                 `json:"generation"`
		Selection  aigame.BattleSelection `json:"selection"`
	}
	d := json.NewDecoder(http.MaxBytesReader(response, request.Body, 4096))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || input.Generation == 0 {
		http.Error(response, "selection and current generation required", http.StatusBadRequest)
		return
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		http.Error(response, "one selection required", http.StatusBadRequest)
		return
	}
	err := session.dispatch(request.Context(), input.Generation, aicontrol.Manual, func(ctx context.Context) error {
		session.authoritativeMu.RLock()
		observer := session.authoritative
		session.authoritativeMu.RUnlock()
		if observer == nil {
			return aigame.ErrClosed
		}
		snapshot, err := observer.Observe(ctx)
		if err != nil {
			return err
		}
		if snapshot.Ladder != nil && snapshot.Battle.LadderID != "" && snapshot.Ladder.Snapshot.Self.Strategy != "manual" {
			return aicontrol.ErrMode
		}
		action, err := aigame.ResolveBattleSelection(snapshot, input.Selection)
		if err != nil {
			return err
		}
		return observer.ExecuteExpected(ctx, snapshot.Revision, action)
	})
	if err != nil {
		code, status := "outcome_unknown", http.StatusBadGateway
		switch {
		case errors.Is(err, aigame.ErrStaleRevision):
			code, status = "stale_observation", http.StatusConflict
		case errors.Is(err, aigame.ErrBattleNotReady), errors.Is(err, aigame.ErrWrongPhase):
			code, status = "not_ready", http.StatusConflict
		case errors.Is(err, aigame.ErrInvalidAction):
			code, status = "invalid_candidate", http.StatusBadRequest
		case errors.Is(err, aicontrol.ErrMode), errors.Is(err, aicontrol.ErrOwner):
			code, status = "automation_conflict", http.StatusConflict
		case errors.Is(err, aicontrol.ErrStale), errors.Is(err, aicontrol.ErrClosed):
			code, status = "stale_control", http.StatusConflict
		}
		handler.writeJSON(response, status, map[string]any{"ok": false, "code": code})
		return
	}
	handler.writeJSON(response, http.StatusOK, map[string]any{"status": "written", "selection": input.Selection})
}
