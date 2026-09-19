package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/battleauto"
)

// battleAutoRequest is the whole request: the panel sends the generation it
// was shown, exactly like every other control call. Seek asks the loop to walk
// between fights so encounters keep coming; it is a pointer because absent
// means no, and an older page must keep the loop off the character.
//
// Mode is which claim the loop takes on the character. Auto battle answers the
// turn it is in and nothing else; leveling is the same loop plus the walk, and
// it exists because the task-based leveling executor is a separate, heavier
// thing that a deployment may not have configured at all.
type battleAutoRequest struct {
	Generation *uint64 `json:"generation"`
	Reason     string  `json:"reason,omitempty"`
	Seek       *bool   `json:"seek,omitempty"`
	Mode       string  `json:"mode,omitempty"`
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

	mode := aicontrol.Battle
	switch strings.ToLower(strings.TrimSpace(input.Mode)) {
	case "", string(aicontrol.Battle):
		mode = aicontrol.Battle
	case string(aicontrol.Leveling):
		mode = aicontrol.Leveling
	default:
		http.Error(response, "unsupported loop mode", http.StatusBadRequest)
		return
	}
	state := session.gate.State()
	if state.Mode != aicontrol.Manual {
		http.Error(response, "character is already under automation control", http.StatusConflict)
		return
	}
	// A loop drives a character in the world. Taking the lease before the
	// character is in it leaves the page unable to log in -- the login packet
	// is a manual one -- until somebody hands control back.
	observed, observeErr := session.observeAuthoritative(request.Context())
	if observeErr != nil {
		http.Error(response, "session state is unavailable", http.StatusServiceUnavailable)
		return
	}
	if observed.Phase != aigame.PhaseWorld {
		http.Error(response, "character is not in the world yet", http.StatusConflict)
		return
	}
	reason := strings.TrimSpace(input.Reason)
	if reason == "" {
		reason = "自动战斗中"
		if mode == aicontrol.Leveling {
			reason = "自动练级中"
		}
	}
	// The returned context is the run lifetime: a takeover or any other
	// ownership change cancels it and the loop exits on its own.
	started, runContext, err := session.gate.Switch(*input.Generation, mode, reason)
	if err != nil {
		controlError(response, err)
		return
	}

	policy := battleauto.DefaultPolicy()
	// Walking outside a battle is opt-in for the battle claim -- it drives the
	// character, and a player who only wants the turns answered must keep their
	// own movement -- and the whole point of the leveling claim.
	policy.SeekEncounters = mode == aicontrol.Leveling || (input.Seek != nil && *input.Seek)
	runner := battleauto.Runner{
		Game:   &AutomationSession{ID: session.id, session: session, mode: mode, generation: started.Generation},
		Tables: handler.recoveryTables(),
		Policy: policy,
		Log: func(format string, args ...any) {
			session.setAutomationNote(fmt.Sprintf(format, args...))
		},
		State: session.setAutomationState,
	}
	if !session.setAutomation(runner, mode, started.Generation) {
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

// setAutomationState keeps the loop's counters for the control panel: whether
// it is in a fight right now, how many it has answered and how those went.
func (s *tcpSession) setAutomationState(state battleauto.State) {
	s.automationMu.Lock()
	s.automationState = state
	s.automationStateKnown = true
	s.automationMu.Unlock()
}

func (s *tcpSession) automationStateSnapshot() (battleauto.State, bool) {
	s.automationMu.Lock()
	defer s.automationMu.Unlock()
	return s.automationState, s.automationStateKnown
}
