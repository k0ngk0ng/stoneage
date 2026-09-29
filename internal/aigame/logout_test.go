package aigame

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

// A native in-place logout must send EOF, never the record-point packet,
// and must wait for the peer before reporting success.
func TestLogoutInPlaceNativeEOF(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	peer, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	session := NewSession(conn, Config{})
	session.state.snapshot.Phase = PhaseWorld
	session.startReader()
	defer session.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- session.LogoutInPlace(ctx) }()
	peer.SetReadDeadline(time.Now().Add(time.Second))
	bytes, err := io.ReadAll(peer)
	if err != nil || len(bytes) != 0 {
		t.Fatalf("logout sent a packet instead of EOF: bytes=%d err=%v", len(bytes), err)
	}
	select {
	case err := <-done:
		t.Fatalf("completed before peer close: %v", err)
	default:
	}
	peer.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
