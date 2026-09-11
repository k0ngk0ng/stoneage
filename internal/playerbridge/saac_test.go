package playerbridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/playerdata"
)

func TestSAACReadRoundTripPreservesRawBytes(t *testing.T) {
	q := newTestQueue(t)
	want := []byte{0x00, 'a', '-', '-', '-', '\n', 0x80, 0xff, '\n', 0x7f}
	done := startFakeSAAC(t, q, func(id string, request []byte) ([]byte, error) {
		fields, payload, err := parseTestRequest(id, request)
		if err != nil {
			return nil, err
		}
		if fields["op"] != "read" || fields["account"] != "alice" || fields["slot"] != "0" {
			return nil, fmt.Errorf("unexpected read request fields: %#v", fields)
		}
		if len(payload) != 0 {
			return nil, fmt.Errorf("read request has %d payload bytes", len(payload))
		}
		return buildTestResponse(id, "ok", "read", "alice", "ok", 0, len(want), true, true, want), nil
	})

	got, err := (SAAC{Queue: q}).Read(context.Background(), "alice", 0)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("Read() = %x, want %x", got, want)
	}
	waitForFakeSAAC(t, done)
}

func TestSAACWriteRoundTripSendsExpectedAndReplacementBytes(t *testing.T) {
	q := newTestQueue(t)
	expected := []byte{'o', 'l', 'd', 0x00, '-', '-', '-', '\n', 0x81}
	replacement := []byte{'n', 'e', 'w', '\n', 0xfe, '-', '-', '-', '\n'}
	done := startFakeSAAC(t, q, func(id string, request []byte) ([]byte, error) {
		fields, payload, err := parseTestRequest(id, request)
		if err != nil {
			return nil, err
		}
		if fields["op"] != "write" || fields["account"] != "alice" || fields["slot"] != "0" {
			return nil, fmt.Errorf("unexpected write request fields: %#v", fields)
		}
		if fields["expected-length"] != strconv.Itoa(len(expected)) || fields["new-length"] != strconv.Itoa(len(replacement)) {
			return nil, fmt.Errorf("unexpected write lengths: %#v", fields)
		}
		wantPayload := append(append([]byte(nil), expected...), replacement...)
		if !bytes.Equal(payload, wantPayload) {
			return nil, fmt.Errorf("write payload = %x, want %x", payload, wantPayload)
		}
		return buildTestResponse(id, "ok", "write", "alice", "ok", 0, len(replacement), true, true, nil), nil
	})

	if err := (SAAC{Queue: q}).Write(context.Background(), "alice", 0, expected, replacement); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	waitForFakeSAAC(t, done)
}

func TestSAACWriteMapsOnlineAndConflictToErrConflict(t *testing.T) {
	for _, code := range []string{"online", "conflict"} {
		t.Run(code, func(t *testing.T) {
			q := newTestQueue(t)
			done := startFakeSAAC(t, q, func(id string, request []byte) ([]byte, error) {
				fields, _, err := parseTestRequest(id, request)
				if err != nil {
					return nil, err
				}
				if fields["op"] != "write" || fields["account"] != "alice" || fields["slot"] != "0" {
					return nil, fmt.Errorf("unexpected write request fields: %#v", fields)
				}
				return buildTestResponse(id, "error", "write", "alice", code, 0, 0, false, false, nil), nil
			})

			err := (SAAC{Queue: q}).Write(context.Background(), "alice", 0, []byte("old"), []byte("new"))
			if !errors.Is(err, playerdata.ErrConflict) {
				t.Fatalf("Write() error = %v, want ErrConflict", err)
			}
			waitForFakeSAAC(t, done)
		})
	}
}

func TestSAACRejectsMismatchedResponseIdentity(t *testing.T) {
	tests := []struct {
		name string
		edit func(id string) (responseID, responseOp, responseAccount string, responseSlot int)
	}{
		{
			name: "id",
			edit: func(id string) (string, string, string, int) { return id + "-wrong", "read", "alice", 0 },
		},
		{
			name: "op",
			edit: func(id string) (string, string, string, int) { return id, "write", "alice", 0 },
		},
		{
			name: "account",
			edit: func(id string) (string, string, string, int) { return id, "read", "bob", 0 },
		},
		{
			name: "slot",
			edit: func(id string) (string, string, string, int) { return id, "read", "alice", 1 },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			q := newTestQueue(t)
			wantPayload := []byte("raw archive")
			done := startFakeSAAC(t, q, func(id string, request []byte) ([]byte, error) {
				fields, payload, err := parseTestRequest(id, request)
				if err != nil {
					return nil, err
				}
				if fields["op"] != "read" || fields["account"] != "alice" || fields["slot"] != "0" || len(payload) != 0 {
					return nil, fmt.Errorf("unexpected read request: %#v payload=%x", fields, payload)
				}
				responseID, responseOp, responseAccount, responseSlot := test.edit(id)
				return buildTestResponse(responseID, "ok", responseOp, responseAccount, "ok", responseSlot, len(wantPayload), true, true, wantPayload), nil
			})

			_, err := (SAAC{Queue: q}).Read(context.Background(), "alice", 0)
			if !errors.Is(err, playerdata.ErrUnavailable) {
				t.Fatalf("Read() error = %v, want ErrUnavailable", err)
			}
			waitForFakeSAAC(t, done)
		})
	}
}

func TestSAACRejectsResponseWithIncorrectReadLength(t *testing.T) {
	q := newTestQueue(t)
	payload := []byte("short archive")
	done := startFakeSAAC(t, q, func(id string, request []byte) ([]byte, error) {
		fields, requestPayload, err := parseTestRequest(id, request)
		if err != nil {
			return nil, err
		}
		if fields["op"] != "read" || fields["account"] != "alice" || fields["slot"] != "0" || len(requestPayload) != 0 {
			return nil, fmt.Errorf("unexpected read request: %#v payload=%x", fields, requestPayload)
		}
		return buildTestResponse(id, "ok", "read", "alice", "ok", 0, len(payload)+1, true, true, payload), nil
	})

	_, err := (SAAC{Queue: q}).Read(context.Background(), "alice", 0)
	if !errors.Is(err, playerdata.ErrUnavailable) {
		t.Fatalf("Read() error = %v, want ErrUnavailable", err)
	}
	waitForFakeSAAC(t, done)
}

func TestSAACRejectsInvalidArgumentsBeforeQueueIO(t *testing.T) {
	q := newTestQueue(t)
	tooLarge := bytes.Repeat([]byte{'x'}, playerdata.MaxSaveSize+1)
	tests := []struct {
		name string
		call func(SAAC) error
	}{
		{
			name: "invalid account",
			call: func(s SAAC) error { _, err := s.Read(context.Background(), "bad/account", 0); return err },
		},
		{
			name: "invalid slot",
			call: func(s SAAC) error { _, err := s.Read(context.Background(), "alice", 2); return err },
		},
		{
			name: "empty expected",
			call: func(s SAAC) error { return s.Write(context.Background(), "alice", 0, nil, []byte("new")) },
		},
		{
			name: "empty replacement",
			call: func(s SAAC) error { return s.Write(context.Background(), "alice", 0, []byte("old"), nil) },
		},
		{
			name: "expected over capacity",
			call: func(s SAAC) error { return s.Write(context.Background(), "alice", 0, tooLarge, []byte("new")) },
		},
		{
			name: "replacement over capacity",
			call: func(s SAAC) error { return s.Write(context.Background(), "alice", 0, []byte("old"), tooLarge) },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(SAAC{Queue: q}); err == nil {
				t.Fatal("call unexpectedly succeeded")
			}
			entries, err := os.ReadDir(q.Requests)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasSuffix(entry.Name(), ".req") {
					t.Fatalf("invalid call created request %s", entry.Name())
				}
			}
		})
	}
}
