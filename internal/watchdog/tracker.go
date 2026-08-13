package watchdog

import (
	"fmt"
	"time"
)

// Tracker is the down/alert state machine behind the watcher, extracted so it
// is testable with a fake clock and injected log/notify callbacks.
//
// Semantics (v1 watchdog.py):
//   - ok         -> log "recovered: <name> (down <n>s)" if it was down
//   - first fail -> log "check failed: <name>", start the clock
//   - fail >= AlertAfter and not yet alerted -> log "ALERT: [cctv] ...", notify
type Tracker struct {
	Log        func(string, ...any)
	Notify     func(string)
	AlertAfter time.Duration

	down    map[string]time.Time
	alerted map[string]bool
}

// NewTracker builds a tracker. alertAfter may be 0 and set per cycle.
func NewTracker(log func(string, ...any), notify func(string), alertAfter time.Duration) *Tracker {
	return &Tracker{
		Log:        log,
		Notify:     notify,
		AlertAfter: alertAfter,
		down:       map[string]time.Time{},
		alerted:    map[string]bool{},
	}
}

// Observe advances the state machine with one result per check name.
func (t *Tracker) Observe(now time.Time, results map[string]bool) {
	for name, ok := range results {
		if ok {
			if since, was := t.down[name]; was {
				t.Log("recovered: %s (down %ds)", name, int(now.Sub(since).Seconds()))
			}
			delete(t.down, name)
			delete(t.alerted, name)
			continue
		}
		if _, was := t.down[name]; !was {
			t.down[name] = now
			t.Log("check failed: %s", name)
		}
		if dur := now.Sub(t.down[name]); dur >= t.AlertAfter && !t.alerted[name] {
			t.alerted[name] = true
			msg := fmt.Sprintf("[cctv] %s DOWN for %ds", name, int(dur.Seconds()))
			t.Log("ALERT: %s", msg)
			if t.Notify != nil {
				t.Notify(msg)
			}
		}
	}
}

// Down reports the currently-failing checks and when each started failing.
func (t *Tracker) Down() map[string]time.Time {
	out := make(map[string]time.Time, len(t.down))
	for k, v := range t.down {
		out[k] = v
	}
	return out
}
