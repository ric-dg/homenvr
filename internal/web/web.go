// Package web serves the control panel: camera status, recordings (with
// Range-served MP4 playback) and JSONC config editing with hot-reload. It
// replaces v1's Tkinter control_panel.py with an embedded HTTP server
// (go:embed + stdlib net/http + method/path mux), while go2rtc's own web UI
// on api_port covers live view.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ric-dg/homenvr/internal/config"
)

//go:embed static
var staticFS embed.FS

// Logger is the subset of logging the server needs.
type Logger interface {
	Logf(format string, args ...any)
}

// ProcInfo describes one supervised process (go2rtc or a camera runner).
type ProcInfo struct {
	Running bool          `json:"running"`
	PID     int           `json:"pid"`
	Uptime  time.Duration `json:"uptime_ns"`
}

// CamInfo is the per-camera runtime state.
type CamInfo struct {
	Name   string `json:"name"`
	Active bool   `json:"active"`
	Mic    bool   `json:"mic"`
	Mode   string `json:"mode"`
}

// Status is the runtime snapshot served at GET /api/status.
type Status struct {
	Version    string        `json:"version"`
	Uptime     time.Duration `json:"uptime_ns"`
	ConfigPath string        `json:"config_path"`
	Mode       string        `json:"mode"`
	Go2rtc     ProcInfo      `json:"go2rtc"`
	LivePort   int           `json:"live_port"`
	Cameras    []CamInfo     `json:"cameras"`
}

// StatusProvider is implemented by the supervisor, which owns the runtime
// state the panel displays.
type StatusProvider interface {
	Status() Status
}

// Options wires the server to its surroundings.
type Options struct {
	ConfigPath string
	Status     StatusProvider
	Log        Logger
	Version    string
	// OnShutdown, when set, is invoked by POST /api/shutdown (stops the
	// service). Nil disables that endpoint.
	OnShutdown func()
}

// Server is the embedded control panel.
type Server struct {
	cfg  *config.File
	opts Options
	mux  *http.Server
}

// New builds the server for the watched config.
func New(cfg *config.File, opts Options) *Server {
	s := &Server{cfg: cfg, opts: opts}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleStatic)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/config", s.handleGetConfig)
	mux.HandleFunc("PUT /api/config", s.handlePutConfig)
	mux.HandleFunc("GET /api/recordings", s.handleListRecordings)
	mux.HandleFunc("GET /api/recordings/{camera}/{file}", s.handleGetRecording)
	mux.HandleFunc("GET /api/recordings/{camera}/combined/{file}", s.handleGetCombinedRecording)
	mux.HandleFunc("POST /api/shutdown", s.handleShutdown)
	s.mux = &http.Server{Handler: mux}
	return s
}

// Start serves the panel on 127.0.0.1:web.port until ctx is cancelled.
func (s *Server) Start(ctx context.Context) error {
	addr := fmt.Sprintf("127.0.0.1:%d", s.cfg.Get().Web.Port)
	s.mux.Addr = addr
	s.opts.Log.Logf("web panel listening on http://%s", addr)
	go func() {
		<-ctx.Done()
		shCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		s.mux.Shutdown(shCtx)
	}()
	err := s.mux.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// ---------------- handlers ----------------

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" || name == "index.html" {
		name = "index.html"
	}
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		http.Error(w, "static assets unavailable", http.StatusInternalServerError)
		return
	}
	http.ServeFileFS(w, r, sub, name)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if s.opts.Status == nil {
		writeErr(w, http.StatusServiceUnavailable, "status provider not wired")
		return
	}
	st := s.opts.Status.Status()
	if st.Version == "" {
		st.Version = s.opts.Version
	}
	writeJSON(w, st)
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(s.opts.ConfigPath)
	if err != nil {
		if os.IsNotExist(err) {
			// No user file yet: show the effective defaults as JSON.
			writeJSON(w, s.cfg.Get())
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/jsonc; charset=utf-8")
	w.Write(data)
}

func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	parsed, err := config.Parse(data)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "parse error: "+err.Error())
		return
	}
	if err := parsed.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid config: "+err.Error())
		return
	}
	if err := writeConfigFile(s.opts.ConfigPath, data); err != nil {
		writeErr(w, http.StatusInternalServerError, "save failed: "+err.Error())
		return
	}
	s.opts.Log.Logf("web: config saved (%d bytes), hot reload picks it up on next tick", len(data))
	writeJSON(w, map[string]string{"ok": "saved"})
}

// writeConfigFile writes the config atomically (temp file + rename) so a
// crash mid-write can never leave a truncated config that the reloader would
// reject.
func writeConfigFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

type Recording struct {
	Camera  string    `json:"camera"`
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

func (s *Server) handleListRecordings(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	var out []Recording
	for _, cam := range cfg.Cameras {
		// The combined dir may hold segments from a previous combined-mode
		// run even when the current mode is event, so always scan it.
		dirs := []string{cam.Record.OutDir, filepath.Join(cam.Record.OutDir, "combined")}
		for _, dir := range dirs {
			entries, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".mp4") {
					continue
				}
				info, err := e.Info()
				if err != nil {
					continue
				}
				out = append(out, Recording{
					Camera:  cam.Name,
					Name:    e.Name(),
					Size:    info.Size(),
					ModTime: info.ModTime(),
				})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModTime.After(out[j].ModTime) })
	writeJSON(w, out)
}

func (s *Server) handleGetRecording(w http.ResponseWriter, r *http.Request) {
	camera := r.PathValue("camera")
	cfg := s.cfg.Get()
	cam := cfg.Camera(camera)
	if cam == nil {
		http.NotFound(w, r)
		return
	}
	s.serveRecording(w, r, cam.Record.OutDir, r.PathValue("file"))
}

func (s *Server) handleGetCombinedRecording(w http.ResponseWriter, r *http.Request) {
	camera := r.PathValue("camera")
	cfg := s.cfg.Get()
	cam := cfg.Camera(camera)
	if cam == nil {
		http.NotFound(w, r)
		return
	}
	s.serveRecording(w, r, filepath.Join(cam.Record.OutDir, "combined"), r.PathValue("file"))
}

// serveRecording opens file as a direct child of dir (no separators allowed,
// so a path can never escape the recordings directory) and streams it with
// Range support for <video> playback.
func (s *Server) serveRecording(w http.ResponseWriter, r *http.Request, dir, file string) {
	if file == "" || strings.ContainsAny(file, `/\`) {
		http.Error(w, "bad file name", http.StatusBadRequest)
		return
	}
	f, err := os.Open(filepath.Join(dir, file))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	http.ServeContent(w, r, file, st.ModTime(), f)
}

func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if s.opts.OnShutdown == nil {
		writeErr(w, http.StatusNotImplemented, "shutdown not wired")
		return
	}
	s.opts.Log.Logf("web: shutdown requested")
	w.WriteHeader(http.StatusNoContent)
	go s.opts.OnShutdown()
}
