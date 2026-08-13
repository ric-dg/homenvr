// Package watchdog implements the health monitor, mirroring v1 watchdog.py:
// it probes the go2rtc API and every camera's mic TCP ports, tracks downtime
// per check, and logs (and optionally ntfy-pushes) an alert after a check has
// been failing continuously for monitor.alert_after_sec. monitor.* settings
// are re-read from config every cycle.
package watchdog

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/ric-dg/homenvr/internal/config"
)

// Logger receives alert lines (the supervisor's ServiceLog implements it).
type Logger interface {
	Logf(format string, args ...any)
}

// Watcher runs the health checks. Use one instance per supervisor.
type Watcher struct {
	cfg   *config.File
	alert Logger
	http  *http.Client
}

// New builds a watcher that reads config via cfg and writes alerts to alert.
func New(cfg *config.File, alert Logger) *Watcher {
	return &Watcher{
		cfg:   cfg,
		alert: alert,
		http:  &http.Client{Timeout: 4 * time.Second},
	}
}

// probes abstracts the network health checks so the watcher logic can be
// tested without real sockets (the production Watcher implements it).
type probes interface {
	portOK(port int) bool
	go2rtcAPI(port int) bool
	go2rtcStream(port int, name string) bool
}

// Run checks forever until ctx is cancelled, sleeping monitor.interval_sec
// between cycles (v1 behavior: the interval is re-read each loop).
func (w *Watcher) Run(ctx context.Context) {
	tracker := NewTracker(w.alert.Logf, w.ntfy, 0)
	for {
		cfg := w.cfg.Get()
		if cfg.Monitor.Enabled {
			tracker.AlertAfter = time.Duration(cfg.Monitor.AlertAfterSec) * time.Second
			w.cycle(cfg, tracker, w)
		}
		interval := time.Duration(cfg.Monitor.IntervalSec) * time.Second
		if interval <= 0 {
			interval = 10 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// cycle gathers one result per check and feeds the tracker.
func (w *Watcher) cycle(cfg *config.Config, tracker *Tracker, p probes) {
	api := cfg.Go2rtc.APIPort
	results := map[string]bool{
		"go2rtc:api": p.go2rtcAPI(api),
	}
	for _, cam := range cfg.Cameras {
		results["stream:"+cam.Name] = p.go2rtcStream(api, cam.Name)
		if cam.Mic.Enabled {
			results["daemon:"+cam.Name+":live"] = p.portOK(cam.Mic.LivePort)
			results["daemon:"+cam.Name+":rec"] = p.portOK(cam.Mic.RecPort)
			results["daemon:"+cam.Name+":ctl"] = p.portOK(cam.Mic.CtlPort)
		}
	}
	tracker.Observe(time.Now(), results)
}

// portOK reports whether 127.0.0.1:port accepts a TCP connection (v1
// watchdog.py port_ok, timeout 3s).
func (w *Watcher) portOK(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 3*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// go2rtcAPI reports whether the go2rtc API endpoint answers 200 (v1
// go2rtc_api_ok).
func (w *Watcher) go2rtcAPI(apiPort int) bool {
	resp, err := w.http.Get(fmt.Sprintf("http://127.0.0.1:%d/api/streams", apiPort))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// go2rtcStream reports whether the named stream currently has producers (v1
// go2rtc_ok).
func (w *Watcher) go2rtcStream(apiPort int, name string) bool {
	resp, err := w.http.Get(fmt.Sprintf("http://127.0.0.1:%d/api/streams", apiPort))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var streams map[string]struct {
		Producers []any `json:"producers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&streams); err != nil {
		return false
	}
	stream, ok := streams[name]
	return ok && len(stream.Producers) > 0
}

// ntfy sends a push notification to ntfy.sh/{topic}; failures are ignored
// (v1 ntfy).
func (w *Watcher) ntfy(msg string) {
	cfg := w.cfg.Get()
	topic := strings.TrimSpace(cfg.Monitor.NtfyTopic)
	if topic == "" {
		return
	}
	resp, err := w.http.Post(fmt.Sprintf("https://ntfy.sh/%s", topic),
		"text/plain", strings.NewReader(msg))
	if err == nil {
		resp.Body.Close()
	}
}
