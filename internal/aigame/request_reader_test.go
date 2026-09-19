package aigame

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/server/go/namedproto"
)

// A request issued after the character entered the world must not read the
// socket itself: the background reader owns it by then, and two readers on
// one bufio.Reader interleave packets. That race made CharLogout's reply
// unreadable ("truncated Ringo bit stream") during the 2026-09-18 sactl run.
// This server keeps sending unrelated chat while the logout reply is pending,
// so a direct read would consume one of those packets instead.
func TestRequestAfterEnterWaitsForTheBackgroundReader(t *testing.T) {
	client, peer := net.Pipe()
	serverDone := make(chan error, 1)
	go func() {
		defer peer.Close()
		if _, err := peer.Write([]byte{'L', 0}); err != nil {
			serverDone <- err
			return
		}
		buffer := make([]byte, 8192)
		readMessage := func() (namedproto.Message, error) {
			n, err := peer.Read(buffer)
			if err != nil {
				return namedproto.Message{}, err
			}
			raw, err := namedproto.DecodePacket(buffer[:n])
			if err != nil {
				return namedproto.Message{}, err
			}
			return namedproto.ParseMessage(raw)
		}
		writeResponse := func(id uint32, function string, fields []string) error {
			raw, err := namedproto.RawMessage(id, function, fields)
			if err != nil {
				return err
			}
			packet, err := namedproto.EncodePacket(raw)
			if err != nil {
				return err
			}
			_, err = peer.Write(packet)
			return err
		}
		// Handshake.
		message, err := readMessage()
		if err != nil {
			serverDone <- err
			return
		}
		if err := writeResponse(message.ID, "ClientLogin", []string{namedproto.EncodeString([]byte("ok"))}); err != nil {
			serverDone <- err
			return
		}
		message, err = readMessage()
		if err != nil {
			serverDone <- err
			return
		}
		list := namedproto.EncodeString([]byte("Hero|0\\z0\\z1\\z10\\z100\\z20\\z30\\z4\\z0\\z50\\z50\\z50\\z50\\z0\\zHero\\zhome"))
		if err := writeResponse(message.ID, "CharList", []string{namedproto.EncodeString([]byte("successful")), list}); err != nil {
			serverDone <- err
			return
		}
		message, err = readMessage()
		if err != nil {
			serverDone <- err
			return
		}
		if message.Function != "CharLogin" {
			serverDone <- fmt.Errorf("expected CharLogin, got %s", message.Function)
			return
		}
		if err := writeResponse(message.ID, "CharLogin", []string{namedproto.EncodeString([]byte("successful")), namedproto.EncodeString(nil)}); err != nil {
			serverDone <- err
			return
		}

		// World traffic the background reader must consume, then the reply to
		// the next request. Both are written while the client is idle.
		time.Sleep(20 * time.Millisecond)
		if err := writeResponse(9001, "TK", []string{
			namedproto.EncodeInt(0),
			namedproto.EncodeString([]byte("P|hello")),
			namedproto.EncodeInt(0),
		}); err != nil {
			serverDone <- err
			return
		}

		message, err = readMessage()
		if err != nil {
			serverDone <- err
			return
		}
		if message.Function != "CharLogout" {
			serverDone <- fmt.Errorf("expected CharLogout, got %s", message.Function)
			return
		}
		if err := writeResponse(message.ID, "CharLogout", []string{
			namedproto.EncodeString([]byte("successful")),
			namedproto.EncodeString([]byte("Hero")),
		}); err != nil {
			serverDone <- err
			return
		}
		serverDone <- nil
	}()

	config := Config{Address: "pipe", Dial: func(context.Context, string) (net.Conn, error) { return client, nil }}
	session, err := Login(context.Background(), config, Credentials{Account: "acct", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := session.Enter(context.Background(), "Hero"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := session.Logout(ctx); err != nil {
		t.Fatalf("logout after entering the world: %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}
