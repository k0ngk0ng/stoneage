package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/websession"
)

const agentLeaseIdleTimeout = 90 * time.Second

// webAgentLease authorizes one incarnation of one authenticated game session.
// The capability never confers an AI identity or funding privilege. Only the
// private listener can issue it; the public Handler cannot route these APIs.
type webAgentLease struct {
	session     *tcpSession
	identity    automationIdentity
	identityMu  sync.Mutex
	incarnation string
	generation  uint64
	ctx         context.Context
	lastSeen    atomic.Int64
}

func (lease *webAgentLease) touch() { lease.lastSeen.Store(time.Now().UnixNano()) }

func (lease *webAgentLease) snapshot(ctx context.Context) (aigame.Snapshot, error) {
	var snapshot aigame.Snapshot
	err := lease.session.dispatch(ctx, lease.generation, aicontrol.Agent, func(ctx context.Context) error {
		var err error
		snapshot, err = lease.validate(ctx)
		return err
	})
	return snapshot, err
}

// validate is called while holding the session's control gate. In particular,
// observation is fenced as strictly as a write, without consuming browser events.
func (lease *webAgentLease) validate(ctx context.Context) (aigame.Snapshot, error) {
	if err := lease.ctx.Err(); err != nil {
		return aigame.Snapshot{}, err
	}
	snapshot, err := lease.session.observeAuthoritative(ctx)
	if err != nil {
		return aigame.Snapshot{}, err
	}
	identity, ok := automationIdentityFromSnapshot(snapshot, lease.session.serverLineID())
	if !snapshot.Connected || (snapshot.Phase != aigame.PhaseWorld && snapshot.Phase != aigame.PhaseBattle) || !ok || snapshot.SessionToken != lease.incarnation || !lease.matchesObservedIdentity(identity) {
		return aigame.Snapshot{}, aicontrol.ErrStale
	}
	return snapshot, nil
}

// matchesObservedIdentity binds the optional native persistent character ID
// once it arrives in S(AI). The first attach can legitimately race that
// observation, so an empty lease ID may be completed by the first valid
// observed ID. Once either side has established an ID, later observations
// must match it exactly; an empty observed value cannot weaken an existing
// binding.
func (lease *webAgentLease) matchesObservedIdentity(observed automationIdentity) bool {
	if lease == nil {
		return false
	}
	lease.identityMu.Lock()
	defer lease.identityMu.Unlock()
	if lease.identity.matches(observed) {
		return true
	}
	if lease.identity.PersistentCharacterID != "" || observed.PersistentCharacterID == "" {
		return false
	}
	completed := lease.identity
	completed.PersistentCharacterID = observed.PersistentCharacterID
	if !completed.matches(observed) {
		return false
	}
	lease.identity = completed
	return true
}

// InternalAgentHandler is mounted ONLY on a filesystem-protected Unix socket.
// A session ID alone is not an Agent credential on the public HTTP interface.
func (handler *Handler) InternalAgentHandler() http.Handler {
	return http.HandlerFunc(handler.serveInternalAgent)
}

func decodeAgentInput(w http.ResponseWriter, r *http.Request, value any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("one JSON object required")
	}
	return nil
}

func (handler *Handler) serveInternalAgent(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get("Origin") != "" {
		http.Error(w, "browser access forbidden", http.StatusForbidden)
		return
	}
	if r.URL.Path == "/internal/agent/attach" {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		handler.attachAgent(w, r)
		return
	}
	switch r.URL.Path {
	case "/internal/agent/observe", "/internal/agent/execute", "/internal/agent/detach":
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
	case "/internal/agent/watch":
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
	default:
		http.NotFound(w, r)
		return
	}
	auth := r.Header.Get("Authorization")
	token := strings.TrimPrefix(auth, "Bearer ")
	if auth == token || len(token) != 43 {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	handler.agentMu.Lock()
	lease := handler.agentLeases[token]
	handler.agentMu.Unlock()
	if lease == nil {
		w.WriteHeader(http.StatusGone)
		return
	}
	if _, err := lease.snapshot(r.Context()); err != nil {
		handler.releaseAgent(token, lease)
		w.WriteHeader(http.StatusGone)
		return
	}
	lease.touch()
	lease.session.touch()
	switch r.URL.Path {
	case "/internal/agent/detach":
		handler.releaseAgent(token, lease)
		w.WriteHeader(http.StatusNoContent)
	case "/internal/agent/observe":
		snapshot, err := lease.snapshot(r.Context())
		if err != nil {
			handler.releaseAgent(token, lease)
			w.WriteHeader(http.StatusGone)
			return
		}
		handler.writeJSON(w, http.StatusOK, websession.ObserveResponse{Snapshot: snapshot, SessionToken: snapshot.SessionToken})
	case "/internal/agent/execute":
		var input websession.ExecuteRequest
		if err := decodeAgentInput(w, r, &input); err != nil || input.Validate() != nil {
			http.Error(w, "invalid action request", http.StatusBadRequest)
			return
		}
		// No retry: a failed write may already have reached the game server.
		err := lease.session.dispatch(r.Context(), lease.generation, aicontrol.Agent, func(ctx context.Context) error {
			if _, err := lease.validate(ctx); err != nil {
				return err
			}
			lease.session.authoritativeMu.RLock()
			observer := lease.session.authoritative
			lease.session.authoritativeMu.RUnlock()
			if observer == nil {
				return aicontrol.ErrClosed
			}
			return observer.ExecuteExpected(ctx, input.Revision, input.Action)
		})
		if err != nil {
			if errors.Is(err, aicontrol.ErrStale) || errors.Is(err, aicontrol.ErrClosed) || errors.Is(err, aicontrol.ErrOwner) || lease.ctx.Err() != nil {
				handler.releaseAgent(token, lease)
				w.WriteHeader(http.StatusGone)
			} else {
				status, code := agentActionError(err)
				handler.writeJSON(w, status, map[string]string{"code": code})
			}
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case "/internal/agent/watch":
		timer := time.NewTimer(20 * time.Second)
		defer timer.Stop()
		select {
		case <-r.Context().Done():
			return
		case <-lease.ctx.Done():
			handler.releaseAgent(token, lease)
			w.WriteHeader(http.StatusGone)
			return
		case <-lease.session.closed:
			handler.releaseAgent(token, lease)
			w.WriteHeader(http.StatusGone)
			return
		case <-timer.C:
		}
		if _, err := lease.snapshot(r.Context()); err != nil {
			handler.releaseAgent(token, lease)
			w.WriteHeader(http.StatusGone)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (handler *Handler) attachAgent(w http.ResponseWriter, r *http.Request) {
	var input websession.AttachRequest
	if err := decodeAgentInput(w, r, &input); err != nil || input.Validate() != nil || !agentIdentity(input.Identity).canonicalCharacterID() {
		http.Error(w, "complete session binding required", http.StatusBadRequest)
		return
	}
	session, ok := handler.sessions.get(input.SessionID)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	var before aigame.Snapshot
	var boundIdentity automationIdentity
	err := session.dispatch(r.Context(), input.Generation, aicontrol.Manual, func(ctx context.Context) error {
		if session.gate == nil {
			return aicontrol.ErrClosed
		}
		var err error
		before, err = session.observeAuthoritative(ctx)
		if err != nil {
			return err
		}
		identity, ok := automationIdentityFromSnapshot(before, session.serverLineID())
		if !before.Connected || (before.Phase != aigame.PhaseWorld && before.Phase != aigame.PhaseBattle) || before.SessionToken == "" || !ok || !matchesAgentIdentity(agentIdentity(input.Identity), identity) {
			return aicontrol.ErrOwner
		}
		boundIdentity = identity
		return nil
	})
	if err != nil {
		http.Error(w, "session binding unavailable", http.StatusConflict)
		return
	}
	// Do not replace paused tasks, even though manual packet sends are permitted
	// while paused. The human must explicitly take over that task first.
	if session.gate.State().Mode != aicontrol.Manual {
		w.WriteHeader(http.StatusConflict)
		return
	}
	var random [32]byte
	if _, err = rand.Read(random[:]); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	token := base64.RawURLEncoding.EncodeToString(random[:])
	state, ctx, err := session.gate.Switch(input.Generation, aicontrol.Agent, "Agent 控制中")
	if err != nil {
		controlError(w, err)
		return
	}
	lease := &webAgentLease{session: session, identity: boundIdentity.normalized(), incarnation: before.SessionToken, generation: state.Generation, ctx: ctx}
	lease.touch()
	if _, err = lease.snapshot(r.Context()); err != nil {
		handler.releaseAgent(token, lease)
		w.WriteHeader(http.StatusConflict)
		return
	}
	// Install lifecycle metadata so the existing Web control UI shows the owner.
	if !session.setAutomation(lease, aicontrol.Agent, state.Generation) {
		handler.releaseAgent(token, lease)
		w.WriteHeader(http.StatusConflict)
		return
	}
	handler.agentMu.Lock()
	if handler.agentLeases == nil {
		handler.agentLeases = make(map[string]*webAgentLease)
	}
	handler.agentLeases[token] = lease
	handler.agentMu.Unlock()
	lease.identityMu.Lock()
	replyIdentity := lease.identity
	lease.identityMu.Unlock()
	go handler.watchAgentLifetime(token, lease)
	handler.writeJSON(w, http.StatusCreated, struct {
		Token      string             `json:"token"`
		Generation uint64             `json:"generation"`
		Identity   automationIdentity `json:"identity"`
	}{token, state.Generation, replyIdentity})
}

func (handler *Handler) releaseAgent(token string, lease *webAgentLease) {
	handler.agentMu.Lock()
	if handler.agentLeases[token] == lease {
		delete(handler.agentLeases, token)
	}
	handler.agentMu.Unlock()
	// CAS prevents a late detach/expiry from undoing human takeover or a new task.
	_, _, _ = lease.session.gate.Switch(lease.generation, aicontrol.Manual, "Agent 控制已结束")
	lease.session.clearAutomation(lease.generation)
}

func (handler *Handler) watchAgentLifetime(token string, lease *webAgentLease) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	defer handler.releaseAgent(token, lease)
	for {
		select {
		case <-lease.ctx.Done():
			return
		case <-lease.session.closed:
			return
		case <-handler.stop:
			return
		case <-ticker.C:
			if time.Since(time.Unix(0, lease.lastSeen.Load())) > agentLeaseIdleTimeout {
				return
			}
			if _, err := lease.snapshot(context.Background()); err != nil {
				return
			}
		}
	}
}

func agentIdentity(identity websession.Identity) automationIdentity {
	return automationIdentity{AccountID: identity.AccountID, CharacterID: identity.CharacterID, CharacterName: identity.CharacterName, ServerID: identity.ServerID, PersistentCharacterID: identity.PersistentCharacterID}
}

// Only proven pre-submission failures are classified as rejected. A transport
// failure must retain the existing unknown-receipt reconciliation semantics.
func agentActionError(err error) (int, string) {
	switch {
	case errors.Is(err, aigame.ErrStaleRevision):
		return http.StatusConflict, "stale_revision"
	case errors.Is(err, aigame.ErrInvalidAction), errors.Is(err, aigame.ErrTextEncoding):
		return http.StatusConflict, "invalid_action"
	case errors.Is(err, aigame.ErrWrongPhase):
		return http.StatusConflict, "wrong_phase"
	case errors.Is(err, aigame.ErrBattleNotReady):
		return http.StatusConflict, "battle_not_ready"
	default:
		return http.StatusBadGateway, "outcome_unknown"
	}
}

// The server may deliver persistent metadata after CharLogin. A missing
// expected persistent ID can be enriched only within the already-bound login
// incarnation; account, slot, name and line must still match in full.
func matchesAgentIdentity(expected, observed automationIdentity) bool {
	if expected.PersistentCharacterID == "" {
		expected.PersistentCharacterID = observed.PersistentCharacterID
	}
	return expected.matches(observed)
}
