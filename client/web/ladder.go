package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/battleauto"
	"github.com/k0ngk0ng/stoneage/internal/ladder"
)

type ladderHTTPAction struct {
	Generation uint64         `json:"generation"`
	Request    ladder.Request `json:"request"`
}

// ladder is a typed bridge over the existing authenticated game connection.
// The authority validates membership, revisions and receipts. Writes retain
// the same generation fence as ordinary manual commands; waiting for the
// authoritative receipt must not hold that fence or block takeover.
func (handler *Handler) ladder(response http.ResponseWriter, request *http.Request, session *tcpSession) {
	response.Header().Set("Cache-Control", "no-store")
	session.authoritativeMu.RLock()
	observer, observerErr := session.authoritative, session.authoritativeErr
	session.authoritativeMu.RUnlock()
	if observer == nil || observerErr != nil {
		http.Error(response, "session observer unavailable", http.StatusServiceUnavailable)
		return
	}
	switch request.Method {
	case http.MethodGet:
		query := request.URL.Query()
		cursor, err := strconv.ParseUint(query.Get("cursor"), 10, 64)
		if err != nil && query.Get("cursor") != "" {
			http.Error(response, "invalid ladder cursor", http.StatusBadRequest)
			return
		}
		wait := time.Duration(0)
		if query.Get("wait") != "" {
			wait, err = time.ParseDuration(query.Get("wait"))
			if err != nil || wait < 0 || wait > 30*time.Second {
				http.Error(response, "wait must be between 0 and 30s", http.StatusBadRequest)
				return
			}
		}
		before, err := observer.Observe(request.Context())
		if err != nil {
			http.Error(response, err.Error(), http.StatusServiceUnavailable)
			return
		}
		if before.Ladder == nil {
			handler.writeJSON(response, http.StatusConflict, map[string]string{"code": "ladder_snapshot_required"})
			return
		}
		batch := observer.LadderEvents(query.Get("stream"), cursor)
		if wait > 0 {
			ctx, cancel := context.WithTimeout(request.Context(), wait)
			defer cancel()
			batch, err = observer.WaitLadderEvents(ctx, query.Get("stream"), cursor)
		}
		if err != nil || request.Context().Err() != nil {
			if request.Context().Err() == nil {
				http.Error(response, err.Error(), http.StatusServiceUnavailable)
			}
			return
		}
		current := observer.Snapshot()
		strategies, _ := battleauto.NewStrategies(nil, handler.config.LadderStrategies...)
		handler.writeJSON(response, http.StatusOK, struct {
			ladder.Events
			controlResponse
			Pets       []aigame.PetSnapshot      `json:"pets"`
			Strategies []battleauto.StrategyInfo `json:"strategies"`
		}{batch, handler.controlSnapshot(session), current.Pets, strategies.List()})
	case http.MethodPost:
		var input ladderHTTPAction
		decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			http.Error(response, "invalid ladder action", http.StatusBadRequest)
			return
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF || input.Generation == 0 {
			http.Error(response, "one action and current generation are required", http.StatusBadRequest)
			return
		}
		if _, err := input.Request.Wire(); err != nil {
			http.Error(response, err.Error(), http.StatusBadRequest)
			return
		}
		if input.Request.Operation == "ready" || input.Request.Operation == "queue" {
			handle, _, _ := session.automationStatus()
			if runner, ok := handle.(battleauto.Runner); ok && runner.Policy.SeekEncounters {
				http.Error(response, "请先停止自动寻敌，再准备竞技场", http.StatusConflict)
				return
			}
		}
		if input.Request.Operation == "strategy" && input.Request.Argument != "manual" {
			strategies, err := battleauto.NewStrategies(nil, handler.config.LadderStrategies...)
			if err != nil || !strategies.Has(input.Request.Argument) {
				http.Error(response, "strategy is not installed", http.StatusBadRequest)
				return
			}
		}
		ctx, cancel := context.WithTimeout(request.Context(), 8*time.Second)
		defer cancel()
		err := session.dispatch(ctx, input.Generation, aicontrol.Manual, func(ctx context.Context) error {
			return observer.Do(ctx, aigame.Ladder(input.Request))
		})
		if errors.Is(err, aicontrol.ErrStale) || errors.Is(err, aicontrol.ErrOwner) || errors.Is(err, aicontrol.ErrClosed) {
			controlError(response, err)
			return
		}
		if errors.Is(err, aigame.ErrInvalidAction) || errors.Is(err, aigame.ErrWrongPhase) {
			http.Error(response, err.Error(), http.StatusConflict)
			return
		}
		var reply ladder.Envelope
		if err == nil {
			reply, err = observer.WaitLadderReply(ctx, input.Request)
		}
		if err != nil {
			// Never turn an unconfirmed socket write into a failed operation.
			// Both clients keep this exact request for reconciliation/retry.
			handler.writeJSON(response, http.StatusGatewayTimeout, struct {
				Code    string         `json:"code"`
				Request ladder.Request `json:"request"`
			}{"outcome_unknown", input.Request})
			return
		}
		if reply.OK && !reply.HistoricalReceipt() && (input.Request.Operation == "ready" || input.Request.Operation == "strategy") {
			session.automationMu.Lock()
			if session.gate.State().Generation == input.Generation {
				session.ladderAutoSuppressed = false
			}
			session.automationMu.Unlock()
		}
		automationError := ""
		if reply.OK {
			if err := handler.ensureLadderAuto(request.Context(), session, input.Generation); err != nil {
				automationError = err.Error()
			}
		}
		handler.writeJSON(response, http.StatusOK, struct {
			ladder.Envelope
			controlResponse
			AutomationError string `json:"automation_error,omitempty"`
		}{reply, handler.controlSnapshot(session), automationError})
	default:
		response.Header().Set("Allow", "GET, POST")
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// Only the typed browser API opts into hosting the policy. Raw protocol
// sessions (including sactl over HTTP) retain their own single writer.
func (handler *Handler) ensureLadderAuto(ctx context.Context, session *tcpSession, generation uint64) error {
	observed, err := session.observeAuthoritative(ctx)
	if err != nil || observed.Ladder == nil {
		return err
	}
	s := observed.Ladder.Snapshot
	handle, mode, automationGen := session.automationStatus()
	runner, isLoop := handle.(battleauto.Runner)
	eligible := s.AutoBattleEligible()
	if isLoop && mode == aicontrol.Battle && runner.LadderOnly && (!eligible || s.Self.Strategy == "manual") {
		if _, _, err := session.gate.Switch(generation, aicontrol.Manual, "竞技场手动控制"); err != nil {
			return err
		}
		session.clearAutomation(automationGen)
		return nil
	}
	session.automationMu.Lock()
	suppressed := session.ladderAutoSuppressed
	session.automationMu.Unlock()
	if !eligible || s.Self.Strategy == "manual" || suppressed {
		return nil
	}
	strategies, err := battleauto.NewStrategies(nil, handler.config.LadderStrategies...)
	if err != nil {
		return err
	}
	if !strategies.Has(s.Self.Strategy) {
		return errors.New("已选竞技场策略尚未安装，请切换到手动或已安装策略")
	}
	control := session.gate.State()
	if control.Mode == aicontrol.Battle && isLoop {
		return nil
	}
	if control.Mode != aicontrol.Manual {
		return aicontrol.ErrOwner
	}
	return handler.startBattleLoop(session, generation, aicontrol.Battle, "竞技场自动战斗", false, true)
}

// A nonnil request means the write's result is unknown; callers must retain
// that exact request for retry, including when local takeover already worked.
func (handler *Handler) setLadderManual(ctx context.Context, session *tcpSession, generation uint64) (*ladder.Request, error) {
	snapshot, err := session.observeAuthoritative(ctx)
	if err != nil {
		return nil, err
	}
	if snapshot.Ladder == nil || !snapshot.Ladder.Snapshot.ReservesCharacter() || snapshot.Ladder.Snapshot.Self.Strategy == "manual" {
		return nil, nil
	}
	id, err := ladder.NewRequestID()
	if err != nil {
		return nil, err
	}
	r := ladder.Request{ID: id, Revision: snapshot.Ladder.Revision, Operation: "strategy", Argument: "manual"}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	session.authoritativeMu.RLock()
	observer := session.authoritative
	session.authoritativeMu.RUnlock()
	if observer == nil {
		return nil, aigame.ErrClosed
	}
	if err = session.dispatch(ctx, generation, aicontrol.Manual, func(ctx context.Context) error { return observer.Do(ctx, aigame.Ladder(r)) }); err != nil {
		if errors.Is(err, aicontrol.ErrStale) || errors.Is(err, aicontrol.ErrOwner) || errors.Is(err, aicontrol.ErrClosed) || errors.Is(err, aigame.ErrInvalidAction) || errors.Is(err, aigame.ErrWrongPhase) {
			return nil, err
		}
		return &r, err
	}
	reply, err := observer.WaitLadderReply(ctx, r)
	if err != nil {
		return &r, err
	}
	if !reply.OK {
		return nil, errors.New(reply.Code)
	}
	return nil, nil
}

func (session *tcpSession) ladderBlocksWorldAutomation(ctx context.Context) bool {
	snapshot, err := session.observeAuthoritative(ctx)
	return err == nil && snapshot.Ladder != nil && snapshot.Ladder.Snapshot.ReservesCharacter()
}
