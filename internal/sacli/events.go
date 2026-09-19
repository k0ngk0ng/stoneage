package sacli

import (
	"sync"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

// eventLog keeps recent server events for `log` and wakes `wait` callers.
type eventLog struct {
	mu      sync.Mutex
	entries []aigame.Event
	limit   int
	notify  chan struct{}
}

func newEventLog(limit int) *eventLog {
	if limit <= 0 {
		limit = 256
	}
	return &eventLog{limit: limit, notify: make(chan struct{})}
}

func (log *eventLog) append(event aigame.Event) {
	log.mu.Lock()
	log.entries = append(log.entries, event)
	if len(log.entries) > log.limit {
		log.entries = append([]aigame.Event(nil), log.entries[len(log.entries)-log.limit:]...)
	}
	close(log.notify)
	log.notify = make(chan struct{})
	log.mu.Unlock()
}

// tail returns the last n events together with a channel that closes when a
// new event arrives.
func (log *eventLog) tail(n int) ([]aigame.Event, <-chan struct{}) {
	log.mu.Lock()
	defer log.mu.Unlock()
	entries := log.entries
	if n > 0 && len(entries) > n {
		entries = entries[len(entries)-n:]
	}
	return append([]aigame.Event(nil), entries...), log.notify
}

// lastSequence returns the sequence of the newest recorded event.
func (log *eventLog) lastSequence() uint64 {
	log.mu.Lock()
	defer log.mu.Unlock()
	if len(log.entries) == 0 {
		return 0
	}
	return log.entries[len(log.entries)-1].Sequence
}
