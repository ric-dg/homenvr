package supervisor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ric-dg/homenvr/internal/config"
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
	go2rtc   *Child
	lastHash string
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
	s := &Supervisor{ConfigPath: cfgPath, YAMLPath: yamlPath, cfg: cf, log: log}
	log.Logf("service starting")
	s.writeYAML()
	return s, nil
}

// Log exposes the service log (used by subsystems for shared logging).
func (s *Supervisor) Log() *ServiceLog { return s.log }

// Cfg returns the current effective config.
func (s *Supervisor) Cfg() *config.Config { return s.cfg.Get() }

// Run supervises until ctx is cancelled. Ticks every 5 seconds, as in v1.
func (s *Supervisor) Run(ctx context.Context) error {
	s.log.Logf("supervisor running (config=%s, yaml=%s)", s.ConfigPath, s.YAMLPath)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		s.tick(ctx)
		select {
		case <-ctx.Done():
			s.Shutdown()
			return ctx.Err()
		case <-ticker.C:
		}
	}
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
}

func (s *Supervisor) startGo2rtc(cfg *config.Config) {
	bin := cfg.Tools.Go2rtc
	if bin == "" {
		s.log.Logf("go2rtc binary not found (tools.go2rtc empty and not on PATH)")
		return
	}
	ch := NewChild("go2rtc", cfg.Paths.LogDir)
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

// Shutdown stops children in order: go2rtc first, then (in later increments)
// feed processes, then the watchdog.
func (s *Supervisor) Shutdown() {
	s.log.Logf("service stopping")
	if s.go2rtc != nil {
		s.go2rtc.Stop()
	}
	s.log.Logf("service stopped")
}
