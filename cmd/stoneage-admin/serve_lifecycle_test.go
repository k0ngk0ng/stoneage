package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

type lifecycleTestRuntime struct {
	entered chan struct{}
	stopped chan struct{}
	closed  atomic.Int32
	err     error
}

func (r *lifecycleTestRuntime) RestoreActive(ctx context.Context) error {
	close(r.entered)
	if r.err == nil {
		<-ctx.Done()
	}
	close(r.stopped)
	return r.err
}
func (r *lifecycleTestRuntime) Close() error { r.closed.Add(1); return nil }

func TestAdminServesDuringRestoreAndClosesRuntimeOnShutdown(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending recovery", true: "failed recovery"}[failed], func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runtime := &lifecycleTestRuntime{entered: make(chan struct{}), stopped: make(chan struct{})}
			if failed {
				runtime.err = errors.New("test recovery failure")
			}
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })}
			done := make(chan error, 1)
			go func() { done <- serveAdminWithLifecycle(ctx, server, listener, runtime) }()
			select {
			case <-runtime.entered:
			case <-time.After(time.Second):
				t.Fatal("recovery did not start")
			}
			client := &http.Client{Timeout: time.Second}
			response, err := client.Get("http://" + listener.Addr().String())
			if err != nil {
				t.Fatal("control plane unavailable during recovery:", err)
			}
			response.Body.Close()
			if response.StatusCode != http.StatusNoContent {
				t.Fatal("unexpected control response")
			}
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("shutdown did not finish")
			}
			if runtime.closed.Load() != 1 {
				t.Fatal("runtime was not closed exactly once")
			}
			select {
			case <-runtime.stopped:
			default:
				t.Fatal("restore outlived shutdown")
			}
		})
	}
}
