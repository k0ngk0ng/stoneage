package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"time"
)

type adminRuntimeLifecycle interface {
	RestoreActive(context.Context) error
	Close() error
}

// Keep the control plane available during recovery, so a slow game login
// cannot prevent an operator from pausing another player.
func serveAdminWithLifecycle(ctx context.Context, server *http.Server, listener net.Listener, runtime adminRuntimeLifecycle) error {
	defer listener.Close()
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	restoreCtx, cancelRestore := context.WithCancel(ctx)
	defer cancelRestore()
	restoreDone := make(chan struct{})
	go func() {
		defer close(restoreDone)
		if runtime != nil && restoreCtx.Err() == nil {
			if err := runtime.RestoreActive(restoreCtx); err != nil && restoreCtx.Err() == nil {
				log.Printf("AI startup recovery incomplete: %v", err)
			}
		}
	}()
	var serveErr error
	select {
	case serveErr = <-serveDone:
	case <-ctx.Done():
	}
	cancelRestore()
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	shutdownErr := server.Shutdown(shutdownCtx)
	if shutdownErr != nil {
		_ = server.Close()
	}
	var runtimeErr error
	if runtime != nil {
		// Runtime.Close revokes game capabilities before cancelling Codex.
		// Its lifetime is not tied directly to the signal context.
		runtimeErr = runtime.Close()
	}
	<-restoreDone
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	return errors.Join(serveErr, shutdownErr, runtimeErr)
}
