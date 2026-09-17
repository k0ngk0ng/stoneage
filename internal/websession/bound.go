package websession

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

// BoundSession is a character-scoped lease on an already existing Web
// session. Bind never creates a browser session and never owns the Web
// session's public HTTP lifecycle; Close only releases the private lease.
// This is the adapter for callers which already have a human-owned Web
// session and only need typed agent operations while the lease is active.
type BoundSession struct {
	client     *Client
	token      string
	generation uint64
	identity   Identity

	mu          sync.Mutex
	leaseDone   chan struct{}
	leaseClosed bool
	watchCancel context.CancelFunc
	closeOnce   sync.Once
	closeErr    error
}

// Bind claims one existing Web session and returns a lease-only game
// adapter. The request's session ID, generation and full identity remain
// process-owned inputs; no model-facing caller can choose a different
// session through this method.
func (client *Client) Bind(ctx context.Context, request AttachRequest) (*BoundSession, error) {
	if client == nil || client.control == nil {
		return nil, ErrInvalidConfig
	}
	if ctx == nil {
		ctx = context.Background()
	}
	response, err := client.Attach(ctx, request)
	if err != nil {
		return nil, err
	}
	bound := &BoundSession{
		client:     client,
		token:      response.Token,
		generation: response.Generation,
		identity:   response.Identity,
		leaseDone:  make(chan struct{}),
	}
	bound.startWatch()
	return bound, nil
}

// UsesControlGate marks this adapter as already fenced by the Web Gate. The
// service layer must not wrap ExecuteExpected in a second gate dispatch.
func (session *BoundSession) UsesControlGate() {}

func (session *BoundSession) Observe(ctx context.Context) (aigame.Snapshot, error) {
	if session == nil || session.client == nil {
		return aigame.Snapshot{}, ErrClosed
	}
	token, active := session.lease()
	if !active {
		return aigame.Snapshot{}, ErrLeaseUnavailable
	}
	response, err := session.client.Observe(ctx, token)
	if err != nil {
		session.handleLeaseError(err)
		return aigame.Snapshot{}, err
	}
	if response.SessionToken != "" {
		response.Snapshot.SessionToken = response.SessionToken
	}
	return response.Snapshot, nil
}

func (session *BoundSession) ExecuteExpected(ctx context.Context, revision uint64, action aigame.Action) error {
	if session == nil || session.client == nil {
		return ErrClosed
	}
	token, active := session.lease()
	if !active {
		return ErrLeaseUnavailable
	}
	err := session.client.Execute(ctx, token, ExecuteRequest{Revision: revision, Action: action})
	if err != nil {
		session.handleLeaseError(err)
	}
	return err
}

// LeaseDone closes when the Web server revokes the lease or this adapter is
// closed. It never represents the lifetime of the caller's browser session.
func (session *BoundSession) LeaseDone() <-chan struct{} {
	if session == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return session.leaseDone
}

// Close releases only the private agent lease. In particular, it does not
// poll the browser event stream and does not DELETE the Web session.
func (session *BoundSession) Close() error {
	if session == nil {
		return nil
	}
	session.closeOnce.Do(func() {
		session.mu.Lock()
		token := session.token
		cancel := session.watchCancel
		session.token = ""
		if !session.leaseClosed {
			session.leaseClosed = true
			close(session.leaseDone)
		}
		session.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		if token != "" {
			ctx, cancelDetach := context.WithTimeout(context.Background(), 5*time.Second)
			session.closeErr = session.client.Detach(ctx, token)
			cancelDetach()
		}
	})
	return session.closeErr
}

func (session *BoundSession) lease() (string, bool) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.leaseClosed || session.token == "" {
		return "", false
	}
	return session.token, true
}

func (session *BoundSession) startWatch() {
	ctx, cancel := context.WithCancel(context.Background())
	session.mu.Lock()
	session.watchCancel = cancel
	token := session.token
	session.mu.Unlock()
	go func() {
		for {
			err := session.client.watch(ctx, token)
			if err == nil {
				continue
			}
			if ctx.Err() != nil {
				return
			}
			// A failed watch cannot prove that the lease remains fenced. Stop
			// all typed operations immediately; callers can reconcile the
			// pending receipt before deciding what to do next.
			session.markLeaseLost()
			return
		}
	}()
}

func (session *BoundSession) handleLeaseError(err error) {
	if errors.Is(err, ErrLeaseRevoked) || errors.Is(err, ErrUnauthorized) {
		session.markLeaseLost()
	}
}

func (session *BoundSession) markLeaseLost() {
	session.mu.Lock()
	if session.leaseClosed {
		session.mu.Unlock()
		return
	}
	session.leaseClosed = true
	session.token = ""
	close(session.leaseDone)
	cancel := session.watchCancel
	session.watchCancel = nil
	session.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

var _ interface {
	Observe(context.Context) (aigame.Snapshot, error)
	ExecuteExpected(context.Context, uint64, aigame.Action) error
	LeaseDone() <-chan struct{}
	UsesControlGate()
	Close() error
} = (*BoundSession)(nil)
