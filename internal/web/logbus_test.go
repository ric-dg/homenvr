package web

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestLogBroadcaster_Send(t *testing.T) {
	b := NewLogBroadcaster()
	done := make(chan struct{})
	defer close(done)
	ch := b.Subscribe(done)

	b.Send(LogEvent{Name: "service", Line: "hello"})

	select {
	case evt := <-ch:
		if evt.Name != "service" || evt.Line != "hello" {
			t.Errorf("got %+v, want name=service line=hello", evt)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestLogBroadcaster_MultipleSubscribers(t *testing.T) {
	b := NewLogBroadcaster()
	done1, done2 := make(chan struct{}), make(chan struct{})
	defer close(done1)
	defer close(done2)
	ch1 := b.Subscribe(done1)
	ch2 := b.Subscribe(done2)

	b.Send(LogEvent{Name: "alert", Line: "warning"})

	for _, ch := range []<-chan LogEvent{ch1, ch2} {
		select {
		case evt := <-ch:
			if evt.Line != "warning" {
				t.Errorf("got line %q, want warning", evt.Line)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out")
		}
	}
}

func TestLogBroadcaster_Unsubscribe(t *testing.T) {
	b := NewLogBroadcaster()
	done := make(chan struct{})
	ch := b.Subscribe(done)
	close(done)

	// Give the goroutine time to clean up.
	time.Sleep(10 * time.Millisecond)

	b.Send(LogEvent{Name: "service", Line: "after close"})

	// Channel should be closed and empty.
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected closed channel, got event")
		}
	case <-time.After(100 * time.Millisecond):
		// fine — no event received
	}
}

func TestLogBroadcaster_SlowSubscriberDrops(t *testing.T) {
	b := NewLogBroadcaster()
	done := make(chan struct{})
	defer close(done)
	ch := b.Subscribe(done)

	// Don't read from ch — buffer fills up (64), then drops.
	for i := 0; i < 100; i++ {
		b.Send(LogEvent{Name: "service", Line: fmt.Sprintf("line %d", i)})
	}

	// Should have at most 64 events (buffer size).
	count := 0
	for {
		select {
		case <-ch:
			count++
		default:
			goto done
		}
	}
done:
	if count != 64 {
		t.Errorf("got %d events, want 64 (buffer size)", count)
	}
}

func TestLogBroadcaster_NoSubscribers(t *testing.T) {
	b := NewLogBroadcaster()
	// Should not panic with no subscribers.
	b.Send(LogEvent{Name: "service", Line: "nobody listening"})
}

func TestLogBroadcaster_ConcurrentSend(t *testing.T) {
	b := NewLogBroadcaster()
	done := make(chan struct{})
	defer close(done)
	ch := b.Subscribe(done)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				b.Send(LogEvent{Name: "service", Line: "x"})
			}
		}(i)
	}
	wg.Wait()

	// Drain and count — should not panic or deadlock.
	count := 0
	for {
		select {
		case <-ch:
			count++
		default:
			return
		}
	}
}
