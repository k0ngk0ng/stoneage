package sacli

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLogoutOptionsCannotFallBackOnOldDaemon(t *testing.T) {
	if err := os.MkdirAll("../../build", 0755); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("../../build", "logout-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	for _, args := range [][]string{{"--in-place"}, {"--record-point"}, {"--typo"}, {"--in-place", "--record-point"}} {
		socket := filepath.Join(dir, "s.sock")
		listener, err := net.Listen("unix", socket)
		if err != nil {
			t.Fatal(err)
		}
		received := make(chan Request, 1)
		go func() {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
			var request Request
			if json.NewDecoder(conn).Decode(&request) != nil {
				return
			}
			received <- request
			// Emulate old Dispatch: bare logout accepts and ignores ALL args.
			response := Response{OK: true}
			if request.Command != "logout" {
				response = failure(KindUsage, "unknown command %q; run help", request.Command)
			}
			json.NewEncoder(conn).Encode(response)
		}()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		response, err := Call(ctx, socket, Request{Command: "logout", Args: args})
		cancel()
		listener.Close()
		if err != nil {
			t.Fatal(err)
		}
		if response.OK || !strings.Contains(response.Error, "no logout was performed") {
			t.Fatalf("unsafe old-daemon response: %+v", response)
		}
		if request := <-received; request.Command == "logout" {
			t.Fatal("old daemon would move player to record point")
		}
	}
}
