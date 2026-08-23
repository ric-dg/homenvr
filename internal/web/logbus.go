package web

import "sync"

// LogEvent is a single log line from any source.
type LogEvent struct {
	Name string `json:"name"`
	Line string `json:"line"`
}

// LogBroadcaster fans out log events to SSE subscribers.
type LogBroadcaster struct {
	mu  sync.Mutex
	subs map[chan LogEvent]struct{}
}

// NewLogBroadcaster creates a ready-to-use broadcaster.
func NewLogBroadcaster() *LogBroadcaster {
	return &LogBroadcaster{subs: make(map[chan LogEvent]struct{})}
}

// Subscribe returns a buffered channel that receives events until done is closed.
func (b *LogBroadcaster) Subscribe(done <-chan struct{}) <-chan LogEvent {
	ch := make(chan LogEvent, 64)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	go func() {
		<-done
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
		close(ch)
	}()
	return ch
}

// Send fans out an event to all subscribers. Slow subscribers miss events
// (non-blocking send) to never block the writer.
func (b *LogBroadcaster) Send(evt LogEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- evt:
		default:
		}
	}
}
