// Package aicontrol serializes ownership changes with outbound game actions.
// A generation is a fencing token, not an authentication credential. Callers
// must authenticate the user and bind them to the character independently.
package aicontrol

import (
	"context"
	"errors"
	"sync"
)

type Mode string

const (
	Manual   Mode = "manual"
	Quest    Mode = "quest"
	Leveling Mode = "leveling"
	Agent    Mode = "agent"
	Paused   Mode = "paused"
	// Battle is the player-facing auto battle: it answers each turn with a
	// heal or an attack. It is not a task run, so it owns the session without
	// a plan or a durable checkpoint.
	Battle Mode = "battle"
)

var (
	ErrStale  = errors.New("control generation changed")
	ErrOwner  = errors.New("gameplay is controlled by another owner")
	ErrClosed = errors.New("character control is closed")
	ErrMode   = errors.New("invalid control mode")
)

type State struct {
	Mode       Mode   `json:"mode"`
	Generation uint64 `json:"generation"`
	Reason     string `json:"reason,omitempty"`
}

// Gate must be the only path to the character's game connection. The send
// callback must honor its context and a short write deadline. Switch waits
// for an already-started write; a game command already sent cannot be undone.
type Gate struct {
	mu          sync.Mutex
	interruptMu sync.Mutex
	state       State
	ctx         context.Context
	cancel      context.CancelFunc
	closed      bool
}

func New() *Gate {
	ctx, cancel := context.WithCancel(context.Background())
	return &Gate{state: State{Mode: Manual, Generation: 1}, ctx: ctx, cancel: cancel}
}

func (g *Gate) State() State { g.mu.Lock(); defer g.mu.Unlock(); return g.state }

// Switch is compare-and-swap so two tabs cannot both start different plans.
// Takeover intentionally does not need the old token: it is an authenticated
// owner's unconditional request to regain control.
func (g *Gate) Switch(expected uint64, mode Mode, reason string) (State, context.Context, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return g.state, nil, ErrClosed
	}
	if expected != g.state.Generation {
		return g.state, nil, ErrStale
	}
	if mode != Manual && mode != Paused && mode != Quest && mode != Leveling && mode != Agent && mode != Battle {
		return g.state, nil, ErrMode
	}
	g.change(mode, reason)
	return g.state, g.ctx, nil
}

func (g *Gate) Takeover(reason string) (State, error) {
	// Cancellation must not wait behind a blocked protocol write. The
	// ownership change still serializes with that write before returning.
	g.interrupt()
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return g.state, ErrClosed
	}
	g.change(Manual, reason)
	return g.state, nil
}

func (g *Gate) change(mode Mode, reason string) {
	g.interruptMu.Lock()
	defer g.interruptMu.Unlock()
	g.cancel()
	g.ctx, g.cancel = context.WithCancel(context.Background())
	g.state = State{Mode: mode, Generation: g.state.Generation + 1, Reason: reason}
}

func (g *Gate) interrupt() {
	g.interruptMu.Lock()
	g.cancel()
	g.interruptMu.Unlock()
}

// Dispatch validates ownership at the last possible moment, while holding
// the same lock as Switch. It never queues or retries non-idempotent actions.
func (g *Gate) Dispatch(ctx context.Context, generation uint64, owner Mode, send func(context.Context) error) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return ErrClosed
	}
	if generation != g.state.Generation {
		return ErrStale
	}
	// Auto battle answers turns and nothing else, so the player keeps the rest
	// of the client: walking, chat, panels and items all stay theirs, and only
	// the battle command bar is taken away (the page does that). Holding the
	// whole session the way the task modes do would leave them unable to move
	// after starting it.
	if owner != g.state.Mode && !(owner == Manual && (g.state.Mode == Paused || g.state.Mode == Battle)) {
		return ErrOwner
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := g.ctx.Err(); err != nil {
		return err
	}
	if send == nil {
		return errors.New("missing game action")
	}
	actionCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(g.ctx, cancel)
	defer stop()
	defer cancel()
	return send(actionCtx)
}

func (g *Gate) Close() {
	g.interrupt()
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.closed {
		g.closed = true
		g.cancel()
		g.state.Generation++
	}
}
