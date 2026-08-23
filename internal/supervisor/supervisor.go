package supervisor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ric-dg/homenvr/internal/config"
	"github.com/ric-dg/homenvr/internal/mic"
	"github.com/ric-dg/homenvr/internal/proc"
	"github.com/ric-dg/homenvr/internal/recorder"
	"github.com/ric-dg/homenvr/internal/retention"
	"github.com/ric-dg/homenvr/internal/sleepctx"
	"github.com/ric-dg/homenvr/internal/watchdog"
	"github.com/ric-dg/homenvr/internal/web"
)

// LogSender is the subset of LogBroadcaster the supervisor needs.
type LogSender interface {
	Send(evt web.LogEvent)
}

// Supervisor owns the long-running child processes: go2rtc (whose exec
// streams pull in ffmpeg per camera) and, in later increments, the feed
// processes for mic capture, motion gating and recording. This loop mirrors
// v1 run.ps1: reload config, regenerate go2rtc.yaml when the relevant keys
// change (and restart go2rtc), supervise go2rtc, and rotate logs.
type Supervisor struct {
	ConfigPath string
	YAMLPath   string

	log            *ServiceLog
	cfg            *config.File
	go2rtc         *proc.Child
	lastHash       string
	lastMode       string
	started        time.Time
	system         web.SystemInfo
	lastCamStart   time.Time

	mu      sync.Mutex // guards runners
	runners map[string]context.CancelFunc
	paused  bool

	logBus LogSender
}

// New loads the config (required) and writes the initial go2rtc.yaml.
func New(cfgPath, yamlPath string) (*Supervisor, error) {
	cf, err := config.NewFile(cfgPath)
	if err != nil {
		return nil, err
	}
	cfg := cf.Get()
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "homenvrd: config warning: %v\n", err)
	}
	log, err := NewServiceLog(cfg.Paths.LogDir, cfg.Log.MaxMB, cfg.Log.Keep)
	if err != nil {
		return nil, err
	}
	s := &Supervisor{ConfigPath: cfgPath, YAMLPath: yamlPath, cfg: cf, log: log, started: time.Now(), runners: map[string]context.CancelFunc{}}
	log.Logf("service starting")
	s.writeYAML()
	return s, nil
}

// Log exposes the service log (used by subsystems for shared logging).
func (s *Supervisor) Log() *ServiceLog { return s.log }

// SetLogBus wires the log broadcaster so service.log and alert.log lines
// are pushed via SSE. Call before Run().
func (s *Supervisor) SetLogBus(bus LogSender) {
	s.logBus = bus
	s.log.OnWrite = func(line string) {
		bus.Send(web.LogEvent{Name: "service", Line: line})
	}
}

// Cfg returns the current effective config.
func (s *Supervisor) Cfg() *config.Config { return s.cfg.Get() }

// CfgFile returns the hot-reloading config handle (used by the web panel for
// live state and the web port).
func (s *Supervisor) CfgFile() *config.File { return s.cfg }

// Status implements web.StatusProvider, snapshotting go2rtc and the camera
// runners for the control panel.
func (s *Supervisor) Status() web.Status {
	cfg := s.cfg.Get()
	st := web.Status{
		Uptime:     time.Since(s.started),
		ConfigPath: s.ConfigPath,
		Mode:       cfg.Record.Mode,
		LivePort:   cfg.Go2rtc.APIPort,
		System:     web.CollectSystemInfo(),
	}
	if s.go2rtc != nil && !s.go2rtc.Exited() {
		st.Go2rtc = web.ProcInfo{Running: true, PID: s.go2rtc.Pid(), Uptime: s.go2rtc.Uptime()}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cam := range cfg.Cameras {
		_, active := s.runners[cam.Name]
		st.Cameras = append(st.Cameras, web.CamInfo{
			Name: cam.Name, Active: active, Mic: cam.Mic.Enabled, Mode: cfg.Record.Mode,
		})
	}
	return st
}

// Run supervises until ctx is cancelled. Ticks every 5 seconds, as in v1.
// It also runs the watchdog and retention loops as goroutines.
func (s *Supervisor) Run(ctx context.Context) error {
	s.log.Logf("supervisor running (config=%s, yaml=%s)", s.ConfigPath, s.YAMLPath)
	s.lastMode = s.cfg.Get().Record.Mode

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.runWatchdog(ctx)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.runRetention(ctx)
	}()
	if s.logBus != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.watchGo2rtcLogs(ctx)
		}()
	}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		s.tick(ctx)
		select {
		case <-ctx.Done():
			s.Shutdown()
			wg.Wait()
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// runWatchdog starts the health monitor, logging alerts to alert.log.
func (s *Supervisor) runWatchdog(ctx context.Context) {
	cfg := s.cfg.Get()
	alert, err := NewRotatingLog(cfg.Paths.LogDir, "alert.log", cfg.Log.MaxMB, cfg.Log.Keep)
	if err != nil {
		s.log.Logf("watchdog: alert log: %v", err)
		return
	}
	if s.logBus != nil {
		bus := s.logBus
		alert.OnWrite = func(line string) {
			bus.Send(web.LogEvent{Name: "alert", Line: line})
		}
	}
	w := watchdog.New(s.cfg, alert)
	s.log.Logf("watchdog started")
	w.Run(ctx)
	s.log.Logf("watchdog stopped")
}

// runRetention deletes recordings older than record.retain_hours, running
// every 600 seconds as in v1.
func (s *Supervisor) runRetention(ctx context.Context) {
	ticker := time.NewTicker(600 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.retentionOnce(now)
		}
	}
}

func (s *Supervisor) retentionOnce(now time.Time) {
	cfg := s.cfg.Get()
	seen := map[string]bool{}
	var dirs []string
	for _, cam := range cfg.Cameras {
		if cam.Record.OutDir == "" || seen[cam.Record.OutDir] {
			continue
		}
		seen[cam.Record.OutDir] = true
		dirs = append(dirs, cam.Record.OutDir)
		if cfg.Record.Mode == "combined" {
			combined := filepath.Join(cam.Record.OutDir, "combined")
			if !seen[combined] {
				seen[combined] = true
				dirs = append(dirs, combined)
			}
		}
	}
	retention.Run(func(msg string) { s.log.Logf("%s", msg) }, dirs, cfg.Record.RetainHours, cfg.Record.RetainMB, now)
	s.cfg.SignalRecording()
}

// RunRetentionNow runs the retention cleanup immediately (web panel
// "Run retention" action), bypassing the 600s ticker.
func (s *Supervisor) RunRetentionNow() {
	s.log.Logf("retention: manual run requested")
	s.retentionOnce(time.Now())
}

func (s *Supervisor) tick(ctx context.Context) {
	cfg := s.cfg.Get()
	if changed, err := s.cfg.Refresh(); err != nil {
		s.log.Logf("config reload failed: %v (keeping last good config)", err)
	} else if changed {
		cfg = s.cfg.Get()
		s.log.UpdateLogDir(cfg.Paths.LogDir, cfg.Log.MaxMB, cfg.Log.Keep)
		s.log.Logf("config reloaded")
	}
	s.log.UpdateLogDir(cfg.Paths.LogDir, cfg.Log.MaxMB, cfg.Log.Keep)
	for _, n := range RotateManagedLogs(cfg.Paths.LogDir, cfg.Log.MaxMB, cfg.Log.Keep) {
		s.log.Logf("rotated %s", n)
	}

	h := GenHash(cfg)
	if h != s.lastHash {
		s.lastHash = h
		s.writeYAML()
		s.log.Logf("config changed -> regenerated %s, restarting go2rtc", s.YAMLPath)
		if s.go2rtc != nil {
			s.go2rtc.Stop()
		}
		s.go2rtc = nil
	}

	if s.go2rtc != nil && s.go2rtc.Exited() {
		s.log.Logf("go2rtc exited code=%d (uptime %s)", s.go2rtc.ExitCode(), s.go2rtc.Uptime())
		s.go2rtc = nil
	}
	if s.go2rtc == nil {
		s.startGo2rtc(cfg)
	}

	if cfg.Record.Mode != s.lastMode {
		if s.lastMode != "" {
			s.log.Logf("recording mode changed %q -> %q, restarting cameras", s.lastMode, cfg.Record.Mode)
			s.mu.Lock()
			for name, cancel := range s.runners {
				cancel()
				delete(s.runners, name)
			}
			s.mu.Unlock()
		}
		s.lastMode = cfg.Record.Mode
	}

	s.reconcileCameras(cfg)

	sys := web.CollectSystemInfo()
	if sys.Memory.UsedPct > 85 {
		s.dropLowestPriority(cfg)
	}
}

// reconcileCameras starts a runner goroutine for each newly-seen camera and
// cancels runners whose camera disappeared from config. Runners supervise the
// per-camera mic feeder and recording loop; they re-read config internally, so
// only the camera set (add/remove) needs reconciliation here.
func (s *Supervisor) reconcileCameras(cfg *config.Config) {
	s.mu.Lock()
	paused := s.paused
	s.mu.Unlock()
	if paused {
		return
	}
	seen := map[string]bool{}
	started := false
	for _, cam := range cfg.Cameras {
		seen[cam.Name] = true
		s.mu.Lock()
		_, ok := s.runners[cam.Name]
		s.mu.Unlock()
		if ok {
			continue
		}
		if started {
			continue
		}
		if elapsed := time.Since(s.lastCamStart); elapsed < 2*time.Second {
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		s.mu.Lock()
		s.runners[cam.Name] = cancel
		s.mu.Unlock()
		s.lastCamStart = time.Now()
		s.log.Logf("camera %q runner starting", cam.Name)
		go s.runCamera(cam.Name, ctx)
		started = true
	}
	s.mu.Lock()
	for name, cancel := range s.runners {
		if seen[name] {
			continue
		}
		s.log.Logf("camera %q removed from config -> stopping runner", name)
		cancel()
		delete(s.runners, name)
	}
	s.mu.Unlock()
}

// runCamera supervises one camera's feed processes for as long as the camera
// exists, mirroring v1's per-camera main.py: the mic feeder and the recording
// loop run concurrently, and a crashed loop is restarted after a 2s backoff.
func (s *Supervisor) runCamera(name string, ctx context.Context) {
	for {
		cfg := s.cfg.Get()
		if cfg.Camera(name) == nil {
			return
		}
		ffmpeg := cfg.Tools.FFmpeg
		logDir := cfg.Paths.LogDir

		var wg sync.WaitGroup
		if cfg.Camera(name).Mic.Enabled {
			wg.Add(1)
			go func() {
				defer wg.Done()
				mic.New(s.cfg, name, s.log, ffmpeg, logDir).Run(ctx)
			}()
		}
		if cfg.Record.Mode == "none" {
			s.log.Logf("camera %q: live-only mode (no recording)", name)
			<-ctx.Done()
			wg.Wait()
			return
		}
		recDone := make(chan struct{})
		go func() {
			defer close(recDone)
			recorder.New(s.cfg, name, s.log, ffmpeg, logDir).Run(ctx)
		}()
		<-recDone
		if ctx.Err() != nil {
			wg.Wait()
			return
		}
		s.log.Logf("camera %q recording loop exited, restarting in 2s", name)
		if !sleepctx.Sleep(ctx, 2*time.Second) {
			wg.Wait()
			return
		}
	}
}

func (s *Supervisor) startGo2rtc(cfg *config.Config) {
	bin := cfg.Tools.Go2rtc
	if bin == "" {
		s.log.Logf("go2rtc binary not found (tools.go2rtc empty and not on PATH)")
		return
	}
	proc.KillByName(filepath.Base(bin))
	ch := proc.NewChild("go2rtc", cfg.Paths.LogDir)
	if err := ch.Start([]string{bin, "-c", s.YAMLPath}); err != nil {
		s.log.Logf("go2rtc start failed: %v", err)
		return
	}
	s.go2rtc = ch
	s.log.Logf("go2rtc started pid=%d", ch.Pid())
}

func (s *Supervisor) writeYAML() {
	cfg := s.cfg.Get()
	if err := os.MkdirAll(filepath.Dir(s.YAMLPath), 0o755); err != nil {
		s.log.Logf("yaml dir: %v", err)
		return
	}
	if err := os.WriteFile(s.YAMLPath, []byte(BuildYAML(cfg)), 0o644); err != nil {
		s.log.Logf("yaml write: %v", err)
		return
	}
	s.lastHash = GenHash(cfg)
}

// Shutdown stops children in order: go2rtc first, then the per-camera feed
// processes (mic + recording runners), then the watchdog.
func (s *Supervisor) Shutdown() {
	s.log.Logf("service stopping")
	if s.go2rtc != nil {
		s.go2rtc.Stop()
	}
	s.mu.Lock()
	for name, cancel := range s.runners {
		s.log.Logf("camera %q runner stopping", name)
		cancel()
	}
	s.runners = map[string]context.CancelFunc{}
	s.mu.Unlock()
	s.log.Logf("service stopped")
}

func (s *Supervisor) StopAll() {
	s.log.Logf("stop-all: stopping go2rtc and all camera runners")
	if s.go2rtc != nil {
		s.go2rtc.Stop()
		s.go2rtc = nil
	}
	s.mu.Lock()
	for name, cancel := range s.runners {
		s.log.Logf("stop-all: camera %q runner stopping", name)
		cancel()
	}
	s.runners = map[string]context.CancelFunc{}
	s.paused = true
	s.mu.Unlock()
	s.log.Logf("stop-all: done")
}

func (s *Supervisor) StartAll() {
	s.log.Logf("start-all: resuming camera reconciliation")
	s.mu.Lock()
	s.paused = false
	s.lastMode = ""
	s.mu.Unlock()
}

func (s *Supervisor) dropLowestPriority(cfg *config.Config) {
	type candidate struct {
		name     string
		priority int
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var worst *candidate
	for name := range s.runners {
		cam := cfg.Camera(name)
		if cam == nil {
			continue
		}
		p := cam.Priority
		if p == 0 {
			p = 5
		}
		if worst == nil || p < worst.priority {
			worst = &candidate{name: name, priority: p}
		}
	}
	if worst == nil || worst.priority >= 5 {
		return
	}
	s.log.Logf("memory pressure (%.0f%%) -> dropping camera %q (priority %d)",
		CollectMemoryPct(), worst.name, worst.priority)
	if cancel, ok := s.runners[worst.name]; ok {
		cancel()
		delete(s.runners, worst.name)
	}
}

func CollectMemoryPct() float64 {
	sys := web.CollectSystemInfo()
	return sys.Memory.UsedPct
}

// watchGo2rtcLogs polls go2rtc log files every 2s and pushes new lines via SSE.
func (s *Supervisor) watchGo2rtcLogs(ctx context.Context) {
	type tracked struct {
		name string // SSE log name ("go2rtc" or "go2rtc.err")
		path string // filename on disk
		size int64
	}
	files := []tracked{
		{name: "go2rtc", path: "go2rtc.out.log"},
		{name: "go2rtc.err", path: "go2rtc.err.log"},
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		cfg := s.cfg.Get()
		logDir := cfg.Paths.LogDir
		for i := range files {
			f := &files[i]
			fullPath := filepath.Join(logDir, f.path)
			st, err := os.Stat(fullPath)
			if err != nil || st.Size() <= f.size {
				continue
			}
			// Read new bytes.
			data, err := tailBytes(fullPath, st.Size()-f.size)
			if err != nil {
				continue
			}
			f.size = st.Size()
			for _, line := range splitLines(string(data)) {
				if line != "" {
					s.logBus.Send(web.LogEvent{Name: f.name, Line: line})
				}
			}
		}
	}
}

// tailBytes reads the last n bytes from a file.
func tailBytes(path string, n int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if n > st.Size() {
		n = st.Size()
	}
	if _, err := f.Seek(st.Size()-n, 0); err != nil {
		return nil, err
	}
	buf := make([]byte, n)
	_, err = f.Read(buf)
	return buf, err
}

// splitLines splits text on newlines, dropping empty trailing segments.
func splitLines(s string) []string {
	var lines []string
	for len(s) > 0 {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			if s != "" {
				lines = append(lines, s)
			}
			break
		}
		lines = append(lines, s[:i])
		s = s[i+1:]
	}
	return lines
}
