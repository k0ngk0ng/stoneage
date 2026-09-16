package aigame

import (
	"sync"
	"testing"
	"time"
)

func TestSessionEventPublishCloseRace(t *testing.T) {
	const (
		iterations = 32
		publishers = 16
		closers    = 4
	)

	for iteration := 0; iteration < iterations; iteration++ {
		session := NewSession(nil, Config{EventBuffer: 1})
		// Keep every publisher blocked in the send until finish closes done.
		session.events <- Event{Function: "queued"}

		var publishWG sync.WaitGroup
		panics := make(chan any, publishers)
		for index := 0; index < publishers; index++ {
			publishWG.Add(1)
			go func(index int) {
				defer publishWG.Done()
				defer func() {
					if recovered := recover(); recovered != nil {
						panics <- recovered
					}
				}()
				session.applyAndPublish(Event{Function: "server", ID: uint32(index + 1), At: time.Now()})
			}(index)
		}

		deadline := time.Now().Add(time.Second)
		for {
			session.eventMu.Lock()
			registered := session.eventSenders == publishers
			session.eventMu.Unlock()
			if registered {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("iteration %d: publishers did not register", iteration)
			}
			time.Sleep(time.Millisecond)
		}

		var closeWG sync.WaitGroup
		for index := 0; index < closers; index++ {
			closeWG.Add(1)
			go func() {
				defer closeWG.Done()
				if err := session.Close(); err != nil {
					t.Errorf("close error: %v", err)
				}
			}()
		}
		closeWG.Wait()
		publishWG.Wait()
		// A reader callback arriving after Close must also be harmless.
		session.applyAndPublish(Event{Function: "late", At: time.Now()})
		close(panics)
		for recovered := range panics {
			t.Fatalf("iteration %d: event publisher panicked after close: %v", iteration, recovered)
		}

		if event, ok := <-session.events; !ok || event.Function != "queued" {
			t.Fatalf("iteration %d: queued event = %#v, open = %v", iteration, event, ok)
		}
		if _, ok := <-session.events; ok {
			t.Fatalf("iteration %d: event stream remained open", iteration)
		}
	}
}
