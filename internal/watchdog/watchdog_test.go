package watchdog

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ric-dg/homenvr/internal/config"
)

// Compile-time check: the production watcher must satisfy the probe interface.
var _ probes = (*Watcher)(nil)

type fakeProbes struct {
	ports map[int]bool
	api   bool
	stream map[string]bool
}

func (f fakeProbes) portOK(port int) bool { return f.ports[port] }
func (f fakeProbes) go2rtcAPI(int) bool    { return f.api }
func (f fakeProbes) go2rtcStream(_ int, name string) bool {
	return f.stream[name]
}

func TestTrackerLifecycle(t *testing.T) {
	var lines []string
	var notified []string
	tr := NewTracker(func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) },
		func(m string) { notified = append(notified, m) }, 300*time.Second)

	now := time.Unix(1000, 0)
	tr.Observe(now, map[string]bool{"go2rtc:api": false})
	if len(lines) != 1 || lines[0] != "check failed: go2rtc:api" {
		t.Fatalf("first fail lines = %v", lines)
	}
	if len(notified) != 0 {
		t.Fatal("alerted before alert_after_sec")
	}

	tr.Observe(now.Add(100*time.Second), map[string]bool{"go2rtc:api": false})
	if len(notified) != 0 {
		t.Fatal("alerted before alert_after_sec elapsed")
	}

	tr.Observe(now.Add(310*time.Second), map[string]bool{"go2rtc:api": false})
	if len(notified) != 1 || notified[0] != "[cctv] go2rtc:api DOWN for 310s" {
		t.Fatalf("notified = %v", notified)
	}
	if len(lines) != 2 || !strings.HasPrefix(lines[1], "ALERT: ") {
		t.Fatalf("alert line missing: %v", lines)
	}

	// Repeated failure must not re-alert.
	tr.Observe(now.Add(600*time.Second), map[string]bool{"go2rtc:api": false})
	if len(notified) != 1 {
		t.Fatalf("re-alerted: %v", notified)
	}

	// Recovery logs and clears.
	tr.Observe(now.Add(700*time.Second), map[string]bool{"go2rtc:api": true})
	if len(lines) != 3 || !strings.HasPrefix(lines[2], "recovered: go2rtc:api (down 700s)") {
		t.Fatalf("recovery line = %v", lines)
	}
	if len(tr.Down()) != 0 {
		t.Fatal("still down after recovery")
	}
}

func TestTrackerPerCheckState(t *testing.T) {
	var notified []string
	tr := NewTracker(func(string, ...any) {}, func(m string) { notified = append(notified, m) }, time.Second)
	now := time.Unix(0, 0)
	tr.Observe(now, map[string]bool{"a": false, "b": false})
	tr.Observe(now.Add(2*time.Second), map[string]bool{"a": false, "b": false})
	if len(notified) != 2 {
		t.Fatalf("expected 2 alerts, got %v", notified)
	}
	tr.Observe(now.Add(3*time.Second), map[string]bool{"a": true, "b": true})
	if len(tr.Down()) != 0 {
		t.Fatal("checks still down after recovery")
	}
}

func TestCycleBuildsChecks(t *testing.T) {
	cfg := config.Defaults()
	cfg.Cameras = []config.Camera{config.DefaultCamera(), config.DefaultCamera()}
	cfg.Cameras[0].Name = "brio"
	cfg.Cameras[1].Name = "garage"
	cfg.Cameras[1].Mic.Enabled = false

	allDown := fakeProbes{
		ports:  map[int]bool{9010: true, 9011: true, 9012: true},
		api:    true,
		stream: map[string]bool{"brio": true, "garage": true},
	}
	var lines []string
	tr := NewTracker(func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }, nil, time.Hour)
	w := &Watcher{}
	w.cycle(&cfg, tr, allDown)
	if len(lines) != 0 {
		t.Fatalf("healthy config produced failures: %v", lines)
	}

	// Mic ports down for brio must surface as daemon checks only for the mic-enabled camera.
	downPorts := fakeProbes{
		ports:  map[int]bool{9011: true, 9012: true},
		api:    true,
		stream: map[string]bool{"brio": true, "garage": true},
	}
	tr = NewTracker(func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }, nil, time.Hour)
	w.cycle(&cfg, tr, downPorts)
	if len(lines) != 1 || lines[0] != "check failed: daemon:brio:live" {
		t.Fatalf("expected only daemon:brio:live failure, got %v", lines)
	}
}
