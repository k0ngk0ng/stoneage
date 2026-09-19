package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/battleauto"
)

// battleAutoRequest is the whole request: the panel sends the generation it
// was shown, exactly like every other control call. Seek asks the loop to walk
// between fights so encounters keep coming; it is a pointer because the panel
// default (and the deployed page before this field existed) is to walk.
type battleAutoRequest struct {
	Generation *uint64 `json:"generation"`
	Reason     string  `json:"reason,omitempty"`
	Seek       *bool   `json:"seek,omitempty"`
}

// startBattleAuto hands the session to the auto battle loop.
//
// This is deliberately not an AutomationExecutor mode. Auto battle has no
// task, no plan, no budget and no durable checkpoint: it answers the current
// turn with a heal or an attack and stops when the session does. Routing it
// through the task machinery would mean teaching its hard-coded mode lists
// about something that needs none of them; taking the control gate and running
// one loop is the whole feature.
func (handler *Handler) startBattleAuto(response http.ResponseWriter, request *http.Request, session *tcpSession) {
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", "POST")
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if session == nil || session.gate == nil {
		http.Error(response, "session is unavailable", http.StatusServiceUnavailable)
		return
	}
	var input battleAutoRequest
	decoder := json.NewDecoder(io.LimitReader(request.Body, 16*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		http.Error(response, "invalid auto battle request", http.StatusBadRequest)
		return
	}
	if input.Generation == nil || *input.Generation == 0 {
		http.Error(response, "generation is required", http.StatusBadRequest)
		return
	}

	state := session.gate.State()
	if state.Mode != aicontrol.Manual {
		http.Error(response, "character is already under automation control", http.StatusConflict)
		return
	}
	reason := strings.TrimSpace(input.Reason)
	if reason == "" {
		reason = "自动战斗中"
	}
	// The returned context is the run lifetime: a takeover or any other
	// ownership change cancels it and the loop exits on its own.
	started, runContext, err := session.gate.Switch(*input.Generation, aicontrol.Battle, reason)
	if err != nil {
		controlError(response, err)
		return
	}

	policy := battleauto.DefaultPolicy()
	policy.SeekEncounters = input.Seek == nil || *input.Seek
	runner := battleauto.Runner{
		Game:   &AutomationSession{ID: session.id, session: session, mode: aicontrol.Battle, generation: started.Generation},
		Tables: handler.recoveryTables(),
		Policy: policy,
		Log: func(format string, args ...any) {
			session.setAutomationNote(fmt.Sprintf(format, args...))
		},
	}
	if !session.setAutomation(runner, aicontrol.Battle, started.Generation) {
		_, _ = session.gate.Takeover("auto battle failed to start")
		http.Error(response, "auto battle failed to start", http.StatusConflict)
		return
	}
	go func() {
		_ = runner.Run(runContext)
		session.clearAutomation(started.Generation)
	}()

	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(response).Encode(handler.controlSnapshot(session))
}

// recoveryTables loads the game's own recovery tables once for the process. A
// deployment without the AI data directory still gets auto battle; it just
// attacks without healing, which is what an empty table means.
func (handler *Handler) recoveryTables() *aiknowledge.RecoveryTables {
	directory := strings.TrimSpace(handler.config.AutomationKnowledgeDataDir)
	if directory == "" {
		return nil
	}
	handler.recoveryOnce.Do(func() {
		handler.recoveryData, handler.recoveryErr = aiknowledge.LoadRecoveryTables(directory)
	})
	if handler.recoveryErr != nil {
		return nil
	}
	return handler.recoveryData
}

// setAutomationNote records the last decision so the control panel can show
// what the loop is doing without a separate channel.
func (s *tcpSession) setAutomationNote(note string) {
	s.automationMu.Lock()
	s.automationNote = strings.TrimSpace(note)
	s.automationMu.Unlock()
}

func (s *tcpSession) automationNoteText() string {
	s.automationMu.Lock()
	defer s.automationMu.Unlock()
	return s.automationNote
}
