package aigame

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

func worldTestSession(t *testing.T) (*Session, net.Conn) {
	t.Helper()
	client, peer := net.Pipe()
	session := NewSession(client, Config{})
	session.stateMu.Lock()
	session.state.snapshot.Phase = PhaseWorld
	session.state.snapshot.Position = Point{Floor: 1, X: 2, Y: 3}
	session.stateMu.Unlock()
	t.Cleanup(func() {
		_ = session.Close()
		_ = peer.Close()
	})
	return session, peer
}

func TestExecuteExpectedWritesAtExpectedRevision(t *testing.T) {
	session, peer := worldTestSession(t)
	expected := session.Snapshot().Revision
	result := make(chan error, 1)
	go func() { result <- session.ExecuteExpected(context.Background(), expected, Move(2, 3, "ab")) }()

	packet := make([]byte, 4096)
	if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	n, err := peer.Read(packet)
	if err != nil {
		t.Fatal(err)
	}
	event, err := decodeEvent(packet[:n])
	if err != nil {
		t.Fatal(err)
	}
	if event.Function != "W" || len(event.Fields) != 3 || event.Fields[2].String() != "ab" {
		t.Fatalf("wire action: %+v", event)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if got := session.Snapshot(); got.LastFunction != "W" || got.LastError != "" {
		t.Fatalf("action projection: %+v", got)
	}
}

func TestExecuteExpectedRejectsStaleRevisionWithoutWriting(t *testing.T) {
	session, peer := worldTestSession(t)
	expected := session.Snapshot().Revision
	session.applyEvent(stringEvent("TK", "server update"))

	err := session.ExecuteExpected(context.Background(), expected, Move(2, 3, "a"))
	if !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("stale revision error = %v", err)
	}
	if err := peer.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := peer.Read(make([]byte, 4096)); !errors.Is(err, net.ErrClosed) && !isTimeout(err) {
		t.Fatalf("stale action wrote packet: %v", err)
	}
}

func TestExecuteExpectedConcurrentWithStateUpdates(t *testing.T) {
	session, peer := worldTestSession(t)
	expected := session.Snapshot().Revision
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		packet := make([]byte, 4096)
		for {
			if _, err := peer.Read(packet); err != nil {
				return
			}
		}
	}()

	const actions = 16
	var wg sync.WaitGroup
	errorsSeen := make(chan error, actions)
	for i := 0; i < actions; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := session.ExecuteExpected(context.Background(), expected, Move(2, 3, "a"))
			if err != nil && !errors.Is(err, ErrStaleRevision) {
				errorsSeen <- err
			}
		}()
	}
	// Compete with submissions while keeping all projection access on the
	// same path as the asynchronous network reader.
	for i := 0; i < actions; i++ {
		session.applyEvent(stringEvent("TK", "server update"))
	}
	wg.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Errorf("unexpected concurrent action error: %v", err)
	}
	_ = session.Close()
	<-readerDone
}

func isTimeout(err error) bool {
	type timeout interface{ Timeout() bool }
	v, ok := err.(timeout)
	return ok && v.Timeout()
}
