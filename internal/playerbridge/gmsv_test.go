package playerbridge

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestGMSVResponseIdentity(t *testing.T) {
	for _, mode := range []string{"valid", "missing_id", "wrong_id", "missing_protocol", "wrong_protocol", "duplicate_id"} {
		t.Run(mode, func(t *testing.T) {
			q := newTestQueue(t)
			done := startFakeSAAC(t, q, func(id string, request []byte) ([]byte, error) {
				fields, err := parseLines(request, true)
				if err != nil {
					return nil, err
				}
				// Raw CP936 bytes and plus signs must survive protocol encoding.
				if fields["character"] != "\xce\xda+\xc1\xa6" {
					return nil, fmt.Errorf("character bytes changed")
				}
				response := "protocol=1\nid=" + id + "\nok=1\n"
				switch mode {
				case "missing_id":
					response = "protocol=1\nok=1\n"
				case "wrong_id":
					response = "protocol=1\nid=other\nok=1\n"
				case "missing_protocol":
					response = "id=" + id + "\nok=1\n"
				case "wrong_protocol":
					response = strings.Replace(response, "protocol=1", "protocol=2", 1)
				case "duplicate_id":
					response += "id=" + id + "\n"
				}
				return []byte(response), nil
			})
			_, err := (GMSV{Queue: q}).Call(context.Background(), map[string]string{"action": "snapshot", "character": "\xce\xda+\xc1\xa6"})
			if (err == nil) != (mode == "valid") {
				t.Fatalf("mode %s: %v", mode, err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAccountAcceptsNativePunctuation(t *testing.T) {
	if !validAccount("player.one-2_3") {
		t.Fatal("valid native account rejected")
	}
	if validAccount("player/name") {
		t.Fatal("path separator accepted")
	}
}
