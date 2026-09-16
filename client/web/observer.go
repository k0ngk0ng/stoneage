package main

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

// webObserverConn lets aigame's validated action encoder write through the
// Web bridge's already-open socket. It is deliberately not a second network
// connection: Close only retires the parser adapter and never closes the
// tcpSession's upstream connection.
type webObserverConn struct {
	session *tcpSession

	mu     sync.RWMutex
	closed bool
}

func (connection *webObserverConn) Read([]byte) (int, error) {
	return 0, net.ErrClosed
}

func (connection *webObserverConn) Write(packet []byte) (int, error) {
	if connection == nil || connection.session == nil {
		return 0, net.ErrClosed
	}
	connection.mu.RLock()
	if connection.closed {
		connection.mu.RUnlock()
		return 0, net.ErrClosed
	}
	err := connection.session.writeUpstream(packet)
	connection.mu.RUnlock()
	if err != nil {
		// tcpSession.write intentionally leaves terminal handling to its
		// caller because an HTTP write can fail while Gate.Dispatch still owns
		// the gate mutex. The aigame encoder also holds its state read lock while
		// invoking this Write method, and finish closes that observer. Defer the
		// terminal transition to another goroutine so the caller can release its
		// read lock before the observer is closed.
		go connection.session.finish(err.Error())
		return 0, err
	}
	return len(packet), nil
}

func (connection *webObserverConn) Close() error {
	if connection == nil {
		return nil
	}
	connection.mu.Lock()
	connection.closed = true
	connection.mu.Unlock()
	return nil
}

func (connection *webObserverConn) LocalAddr() net.Addr  { return observerAddr("stoneage-web") }
func (connection *webObserverConn) RemoteAddr() net.Addr { return observerAddr("stoneage-upstream") }
func (connection *webObserverConn) SetDeadline(_ time.Time) error {
	return nil
}
func (connection *webObserverConn) SetReadDeadline(_ time.Time) error {
	return nil
}
func (connection *webObserverConn) SetWriteDeadline(_ time.Time) error {
	return nil
}

type observerAddr string

func (address observerAddr) Network() string { return "tcp" }
func (address observerAddr) String() string  { return string(address) }

func (session *tcpSession) observeAuthoritative(ctx context.Context) (aigame.Snapshot, error) {
	if session == nil {
		return aigame.Snapshot{}, errors.New("authoritative observer is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return aigame.Snapshot{}, err
	}
	session.authoritativeMu.RLock()
	observer, observerErr := session.authoritative, session.authoritativeErr
	session.authoritativeMu.RUnlock()
	if observerErr != nil {
		return aigame.Snapshot{}, observerErr
	}
	if observer == nil {
		return aigame.Snapshot{}, errors.New("authoritative observer is unavailable")
	}
	return observer.Observe(ctx)
}

func (session *tcpSession) applyAuthoritativeClientPacket(packet []byte) {
	if session == nil {
		return
	}
	session.authoritativeMu.RLock()
	observer := session.authoritative
	session.authoritativeMu.RUnlock()
	if observer == nil {
		return
	}
	// Browser-originated packets are already handled by the Web protocol
	// bridge.  An identity parse failure must not turn a valid upstream server
	// stream into an observer error; it simply leaves the binding unavailable
	// until the next valid login/character packet arrives.
	_ = observer.ApplyClientPacket(packet)
}
