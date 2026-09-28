package aigame

import (
	"context"
	"net"
	"runtime"
	"sync/atomic"
	"testing"
)

type closeCountingConn struct {
	net.Conn
	closed atomic.Int32
}

func (c *closeCountingConn) Close() error { c.closed.Add(1); return nil }

func TestFinishedHandshakeSurvivesCommandCancellation(t *testing.T) {
	// Force cancellation before a watcher goroutine gets its first time slice.
	previous := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previous)
	for i := 0; i < 100; i++ {
		connection := &closeCountingConn{}
		session := &Session{conn: connection}
		ctx, cancel := context.WithCancel(context.Background())
		stop := session.watchContext(ctx)
		stop()
		cancel()
		runtime.Gosched()
		if connection.closed.Load() != 0 {
			t.Fatal("finished handshake closed when command returned")
		}
	}
}

func TestCancelledHandshakeClosesBeforeReturning(t *testing.T) {
	connection := &closeCountingConn{}
	session := &Session{conn: connection}
	ctx, cancel := context.WithCancel(context.Background())
	stop := session.watchContext(ctx)
	cancel()
	// AfterFunc may already be running. Stop must wait if so, and never leave a
	// callback that can close a subsequent connection after cleanup completes.
	stop()
	count := connection.closed.Load()
	runtime.Gosched()
	if connection.closed.Load() != count {
		t.Fatal("late close after handshake cleanup")
	}
}
