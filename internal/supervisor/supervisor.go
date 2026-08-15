package supervisor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

// Supervisor owns the long-running child processes: go2rtc (whose exec
// streams pull in ffmpeg per camera) and, in later increments, the feed
// processes for mic capture, motion gating and recording. This loop mirrors
// v1 run.ps1: reload config, regenerate go2rtc.yaml when the relevant keys
// change (and restart go2rtc), supervise go2rtc, and rotate logs.
type Supervisor struct {
	ConfigPath string
	YAMLPath   string

	log      *ServiceLog
	cfg      *config.File
	go2rtc   *proc.Child
	lastHash string
	started  time.Time

	mu      sync.Mutex // guards runners
	runners map[string]context.CancelFunc
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
	retention.Run(func(msg string) { s.log.Logf("%s", msg) }, dirs, cfg.Record.RetainHours, now)
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
	s.reconcileCameras(cfg)
}

// reconcileCameras starts a runner goroutine for each newly-seen camera and
// cancels runners whose camera disappeared from config. Runners supervise the
// per-camera mic feeder and recording loop; they re-read config internally, so
// only the camera set (add/remove) needs reconciliation here.
func (s *Supervisor) reconcileCameras(cfg *config.Config) {
	seen := map[string]bool{}
	for _, cam := range cfg.Cameras {
		seen[cam.Name] = true
		s.mu.Lock()
		_, ok := s.runners[cam.Name]
		s.mu.Unlock()
		if ok {
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		s.mu.Lock()
		s.runners[cam.Name] = cancel
		s.mu.Unlock()
		s.log.Logf("camera %q runner starting", cam.Name)
		go s.runCamera(cam.Name, ctx)
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
