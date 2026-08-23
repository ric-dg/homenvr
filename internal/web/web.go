// Package web serves the control panel: camera status, recordings (with
// Range-served MP4 playback), JSONC config editing with hot-reload, log tails,
// and admin operations (restart, retention run, self-update). It replaces
// v1's Tkinter control_panel.py with an embedded HTTP server (go:embed +
// stdlib net/http + method/path mux), while go2rtc's own web UI on api_port
// covers live view. Admin endpoints work because the daemon runs as the
// service account - no elevation prompts, no desktop needed.
package web

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ric-dg/homenvr/internal/config"
	"github.com/ric-dg/homenvr/internal/mic"
)

// securityHeaders wraps an http.Handler with standard security headers.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; media-src 'self' blob:; connect-src 'self'")
		next.ServeHTTP(w, r)
	})
}

// requestLogger wraps an http.Handler with structured request logging.
func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		if r.URL.Path != "/api/events" { // skip SSE keepalive noise
			slog.Info("web: request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", sw.status,
				"duration", time.Since(start).String(),
				"remote", r.RemoteAddr,
			)
		}
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (sw *statusWriter) WriteHeader(code int) {
	if !sw.wrote {
		sw.status = code
		sw.wrote = true
	}
	sw.ResponseWriter.WriteHeader(code)
}

func (sw *statusWriter) Write(b []byte) (int, error) {
	if !sw.wrote {
		sw.wrote = true
	}
	return sw.ResponseWriter.Write(b)
}

func (sw *statusWriter) Flush() {
	if f, ok := sw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

//go:embed static
var staticFS embed.FS

type dirEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"isDir"`
}

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
	System     SystemInfo    `json:"system"`
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
	// ServiceName is the Windows service to stop/start during a self-update.
	// Only used by /api/update. Defaults to "homenvrd".
	ServiceName string
	// OnShutdown, when set, is invoked by POST /api/shutdown (stops the
	// service). Nil disables that endpoint.
	OnShutdown func()
	// OnRestart, when set, is invoked by POST /api/restart. The daemon is
	// expected to exit with a non-zero code so the service manager restarts
	// it. Nil disables that endpoint.
	OnRestart func()
	// OnUpdate, when set, is invoked by POST /api/update after the new
	// binary has been staged and the updater helper spawned. The daemon then
	// exits cleanly; the helper swaps the exe and starts the service. Nil
	// disables that endpoint.
	OnUpdate func()
	// RunRetention, when set, is invoked by POST /api/retention/run. Nil
	// disables that endpoint.
	RunRetention func()
	OnStopAll    func()
	OnStartAll   func()
	// LogBus, if set, receives log lines from the supervisor for SSE push.
	LogBus *LogBroadcaster
}

// Server is the embedded control panel.
type Server struct {
	cfg  *config.File
	opts Options
	mux  *http.Server

	recMu     sync.Mutex
	recCache  []Recording
	recExpiry time.Time

	logBus *LogBroadcaster
}

// New builds the server for the watched config.
func New(cfg *config.File, opts Options) *Server {
	bus := opts.LogBus
	if bus == nil {
		bus = NewLogBroadcaster()
	}
	s := &Server{cfg: cfg, opts: opts, logBus: bus}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleStatic)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/config", s.handleGetConfig)
	mux.HandleFunc("PUT /api/config", s.handlePutConfig)
	mux.HandleFunc("GET /api/recordings", s.handleListRecordings)
	mux.HandleFunc("GET /api/recordings/{camera}/{file}", s.handleGetRecording)
	mux.HandleFunc("GET /api/recordings/{camera}/combined/{file}", s.handleGetCombinedRecording)
	mux.HandleFunc("POST /api/shutdown", s.handleShutdown)
	mux.HandleFunc("POST /api/restart", s.handleRestart)
	mux.HandleFunc("POST /api/retention/run", s.handleRunRetention)
	mux.HandleFunc("GET /api/logs", s.handleLogs)
	mux.HandleFunc("POST /api/update", s.handleUpdate)
	mux.HandleFunc("GET /api/devices", s.handleDevices)
	mux.HandleFunc("GET /api/devices/{name}/formats", s.handleDeviceFormats)
	mux.HandleFunc("GET /api/browse", s.handleBrowse)
	mux.HandleFunc("POST /api/mkdir", s.handleMkdir)
	mux.HandleFunc("POST /api/stop-all", s.handleStopAll)
	mux.HandleFunc("POST /api/start-all", s.handleStartAll)
	mux.HandleFunc("GET /api/mic-calibrate", s.handleMicCalibrate)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("GET /api/health", s.handleHealth)
	s.mux = &http.Server{
		Handler:           requestLogger(securityHeaders(mux)),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return s
}

// Start serves the panel on web.bind:web.port until ctx is cancelled.
func (s *Server) Start(ctx context.Context) error {
	bind := s.cfg.Get().Web.Bind
	if bind == "" {
		bind = "127.0.0.1"
	}
	addr := fmt.Sprintf("%s:%d", bind, s.cfg.Get().Web.Port)
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

// LogBus returns the log broadcaster for pushing log lines via SSE.
func (s *Server) LogBus() *LogBroadcaster { return s.logBus }

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Header already sent; log but can't send error to client.
		slog.Error("web: json encode failed", "error", err)
	}
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
		writeErr(w, http.StatusInternalServerError, "static assets unavailable")
		return
	}
	// index.html gets no-cache so cache-bust query strings work; everything
	// else gets a day of browser cache since only a redeploy changes files.
	if name == "index.html" {
		w.Header().Set("Cache-Control", "no-cache")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=86400")
	}
	// Serve pre-compressed files from build time. Priority: brotli > zstd >
	// gzip — all zero runtime CPU cost.
	accept := r.Header.Get("Accept-Encoding")
	if strings.Contains(accept, "br") {
		if data, err := fs.ReadFile(sub, "compressed/"+name+".br"); err == nil {
			w.Header().Set("Content-Encoding", "br")
			w.Header().Del("Content-Length")
			http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
			return
		}
	}
	if strings.Contains(accept, "zstd") {
		if data, err := fs.ReadFile(sub, "compressed/"+name+".zst"); err == nil {
			w.Header().Set("Content-Encoding", "zstd")
			w.Header().Del("Content-Length")
			http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
			return
		}
	}
	if strings.Contains(accept, "gzip") {
		if data, err := fs.ReadFile(sub, "compressed/"+name+".gz"); err == nil {
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Del("Content-Length")
			http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
			return
		}
	}
	http.ServeFileFS(w, r, sub, name)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{"status": "ok"})
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
	if err := s.cfg.Save(data); err != nil {
		writeErr(w, http.StatusInternalServerError, "save failed: "+err.Error())
		return
	}
	s.opts.Log.Logf("web: config saved (%d bytes), hot reload picks it up on next tick", len(data))
	s.recMu.Lock()
	s.recExpiry = time.Time{}
	s.recMu.Unlock()
	writeJSON(w, map[string]string{"ok": "saved"})
}

// handleDevices returns detected video/audio devices and available ffmpeg
// encoders, so the settings form can show real dropdown options.
func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	type encoderInfo struct {
		Name string `json:"name"`
		Info string `json:"info"`
	}
	type devResp struct {
		Video    []mic.VideoDevice `json:"video"`
		Audio    []mic.AudioDevice `json:"audio"`
		Encoders []encoderInfo     `json:"encoders"`
		NVENC    bool              `json:"nvenc"`
	}
	resp := devResp{
		Video: mic.ListVideoDevices(),
		Audio: mic.ListAudioDevices(),
		NVENC: cfg.Encoders.NVENC,
	}
	nvencInfo := map[string]string{
		"h264_nvenc": "NVIDIA GPU, H.264 — fast, great compatibility",
		"hevc_nvenc": "NVIDIA GPU, HEVC/H.265 — smaller files, 4K-ready",
		"av1_nvenc":  "NVIDIA GPU, AV1 — newest, best compression",
	}
	cpuInfo := map[string]string{
		"libx264":   "CPU, H.264 — universal, works everywhere",
		"libsvtav1": "CPU, AV1 — slow but best quality-per-bit",
	}
	if cfg.Encoders.NVENC {
		for _, name := range []string{"h264_nvenc", "av1_nvenc", "hevc_nvenc"} {
			resp.Encoders = append(resp.Encoders, encoderInfo{Name: name, Info: nvencInfo[name]})
		}
	}
	for _, name := range cfg.Encoders.CPU {
		info := cpuInfo[name]
		if info == "" {
			info = "CPU encoder"
		}
		resp.Encoders = append(resp.Encoders, encoderInfo{Name: name, Info: info})
	}
	writeJSON(w, resp)
}

// handleDeviceFormats returns the resolutions and FPS ranges a video device
// supports, queried live via ffmpeg -list_options. The frontend uses this to
// populate resolution/FPS dropdowns with device-specific values.
func (s *Server) handleDeviceFormats(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeErr(w, http.StatusBadRequest, "device name required")
		return
	}
	formats := mic.ListDeviceFormats(name)
	if formats == nil {
		formats = []mic.DeviceFormat{}
	}
	writeJSON(w, formats)
}

// handleBrowse lists subdirectories of a path for the folder picker.
func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("path")

	// Empty path → list drives directly.
	if dir == "" {
		dirs := listDrives()
		writeJSON(w, map[string]any{"path": "", "dirs": dirs})
		return
	}

	var dirs []dirEntry

	// On Windows, if we're at a root, show drives as the parent and
	// list this drive's contents.
	if isAtRoot(dir) {
		// The parent of a root is the drive list.
		dirs = append(dirs, dirEntry{
			Name:  "..",
			Path:  "",
			IsDir: true,
		})
	} else {
		// Add parent directory entry unless we're at filesystem root.
		parent := filepath.Dir(dir)
		dirs = append(dirs, dirEntry{
			Name:  "..",
			Path:  parent,
			IsDir: true,
		})
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		dirs = append(dirs, dirEntry{
			Name:  name,
			Path:  filepath.Join(dir, name),
			IsDir: true,
		})
	}
	writeJSON(w, map[string]any{"path": dir, "dirs": dirs})
}

func (s *Server) handleMkdir(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("path")
	if dir == "" {
		writeErr(w, http.StatusBadRequest, "path is required")
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]string{"ok": "created", "path": dir})
}

func isAtRoot(dir string) bool {
	cleaned := filepath.Clean(dir)
	vol := filepath.VolumeName(cleaned)
	return cleaned == vol || cleaned == vol+string(filepath.Separator) || cleaned == string(filepath.Separator) || cleaned == ""
}

func listDrives() []dirEntry {
	var dirs []dirEntry
	for _, letter := range "ABCDEFGHIJKLMNOPQRSTUVWXYZ" {
		vol := string(letter) + ":"
		if _, err := os.Stat(vol + "\\"); err == nil {
			dirs = append(dirs, dirEntry{
				Name:  vol + "\\",
				Path:  vol + "\\",
				IsDir: true,
			})
		}
	}
	return dirs
}

func (s *Server) handleStopAll(w http.ResponseWriter, r *http.Request) {
	if s.opts.OnStopAll == nil {
		writeErr(w, http.StatusNotImplemented, "stop-all not available")
		return
	}
	s.opts.OnStopAll()
	writeJSON(w, map[string]string{"ok": "stopped"})
}

func (s *Server) handleStartAll(w http.ResponseWriter, r *http.Request) {
	if s.opts.OnStartAll == nil {
		writeErr(w, http.StatusNotImplemented, "start-all not available")
		return
	}
	s.opts.OnStartAll()
	writeJSON(w, map[string]string{"ok": "started"})
}

type Recording struct {
	Camera  string    `json:"camera"`
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

func (s *Server) handleListRecordings(w http.ResponseWriter, r *http.Request) {
	all := s.listRecordings()

	offset := 0
	limit := len(all)
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			offset = n
		}
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	total := len(all)
	if offset >= total {
		writeJSON(w, map[string]any{"items": []Recording{}, "total": total})
		return
	}
	end := offset + limit
	if end > total {
		end = total
	}
	writeJSON(w, map[string]any{"items": all[offset:end], "total": total})
}

func (s *Server) listRecordings() []Recording {
	s.recMu.Lock()
	if s.recCache != nil && time.Now().Before(s.recExpiry) {
		cached := s.recCache
		s.recMu.Unlock()
		return cached
	}
	s.recMu.Unlock()

	cfg := s.cfg.Get()
	var out []Recording
	for _, cam := range cfg.Cameras {
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

	s.recMu.Lock()
	// Only update if another goroutine didn't beat us to it.
	if s.recCache == nil || time.Now().After(s.recExpiry) {
		s.recCache = out
		s.recExpiry = time.Now().Add(5 * time.Second)
	}
	cached := s.recCache
	s.recMu.Unlock()
	return cached
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

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	if s.opts.OnRestart == nil {
		writeErr(w, http.StatusNotImplemented, "restart not wired")
		return
	}
	s.opts.Log.Logf("web: service restart requested")
	w.WriteHeader(http.StatusNoContent)
	// Give the response time to flush before the process exits.
	go func() {
		time.Sleep(300 * time.Millisecond)
		s.opts.OnRestart()
	}()
}

func (s *Server) handleRunRetention(w http.ResponseWriter, r *http.Request) {
	if s.opts.RunRetention == nil {
		writeErr(w, http.StatusNotImplemented, "retention not wired")
		return
	}
	s.opts.Log.Logf("web: retention run requested")
	s.opts.RunRetention()
	s.recMu.Lock()
	s.recExpiry = time.Time{}
	s.recMu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// logFiles maps an /api/logs name to the file under paths.log_dir. The
// whitelist prevents path traversal through the log name.
var logFiles = map[string]string{
	"service":    "service.log",
	"alert":      "alert.log",
	"go2rtc":     "go2rtc.out.log",
	"go2rtc.err": "go2rtc.err.log",
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	file, ok := logFiles[name]
	if !ok {
		writeErr(w, http.StatusBadRequest, "unknown log name (want service|alert|go2rtc|go2rtc.err)")
		return
	}
	lines := 200
	if v := r.URL.Query().Get("lines"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			lines = n
		}
	}
	data, err := tailLog(filepath.Join(s.cfg.Get().Paths.LogDir, file), lines)
	if err != nil {
		if os.IsNotExist(err) {
			writeErr(w, http.StatusNotFound, "log file not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(data)
}

// maxTailBytes caps how much of a log file is read for a tail request.
const maxTailBytes = 256 << 10

// tailLog returns up to max lines from the end of path, reading at most
// maxTailBytes so a multi-hundred-MB log cannot be pulled across the wire.
func tailLog(path string, max int) ([]byte, error) {
	if max < 1 {
		max = 1
	}
	if max > 2000 {
		max = 2000
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	trimmed := false
	if st.Size() > maxTailBytes {
		if _, err := f.Seek(st.Size()-maxTailBytes, io.SeekStart); err != nil {
			return nil, err
		}
		trimmed = true
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	s := string(data)
	if trimmed {
		// Drop the partial first line that the seek cut into.
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
	}
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil, nil
	}
	ls := strings.Split(s, "\n")
	if len(ls) > max {
		ls = ls[len(ls)-max:]
	}
	return []byte(strings.Join(ls, "\n")), nil
}

// maxUpdateBytes caps the uploaded replacement binary (a Windows exe).
const maxUpdateBytes = 200 << 20

func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	if s.opts.OnUpdate == nil {
		writeErr(w, http.StatusNotImplemented, "update not wired")
		return
	}
	cfg := s.cfg.Get()
	pwsh := cfg.Tools.Pwsh
	if pwsh == "" {
		writeErr(w, http.StatusBadRequest, "pwsh not available - cannot run the updater")
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxUpdateBytes+1))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(data) > maxUpdateBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "binary too large")
		return
	}
	if len(data) < 2 || !bytes.Equal(data[:2], []byte("MZ")) {
		writeErr(w, http.StatusBadRequest, "not a Windows executable (missing MZ header)")
		return
	}
	target, err := os.Executable()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "cannot locate own executable: "+err.Error())
		return
	}
	if err := s.stageUpdate(data, pwsh, target); err != nil {
		writeErr(w, http.StatusInternalServerError, "staging failed: "+err.Error())
		return
	}
	writeJSON(w, map[string]string{"ok": "update staged; service will restart in a few seconds"})
	// Exit the daemon once the response has flushed; the detached updater
	// swaps the exe and starts the service.
	go func() {
		time.Sleep(750 * time.Millisecond)
		s.opts.OnUpdate()
	}()
}

// stageUpdate writes the new exe and the updater script to temp, then spawns
// pwsh detached. The script waits for the daemon to exit, swaps the exe and
// restarts the service; it logs to <exe dir>/update.log.
func (s *Server) stageUpdate(newExe []byte, pwsh, target string) error {
	dir := filepath.Dir(target)
	uid := time.Now().UnixNano()
	staged := filepath.Join(os.TempDir(), fmt.Sprintf("homenvrd.new.%d.exe", uid))
	script := filepath.Join(os.TempDir(), fmt.Sprintf("homenvr-update.%d.ps1", uid))
	updLog := filepath.Join(dir, "update.log")

	if err := os.WriteFile(staged, newExe, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(script, []byte(updateScript), 0o600); err != nil {
		os.Remove(staged)
		return err
	}

	out, err := os.OpenFile(updLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		os.Remove(staged)
		os.Remove(script)
		return err
	}
	defer out.Close()

	svc := s.opts.ServiceName
	if svc == "" {
		svc = "homenvrd"
	}
	cmd := exec.Command(pwsh, "-NoProfile", "-ExecutionPolicy", "Bypass",
		"-File", script, "-NewExe", staged, "-TargetExe", target, "-Svc", svc)
	cmd.Stdout = out
	cmd.Stderr = out
	detachCmd(cmd)
	if err := cmd.Start(); err != nil {
		os.Remove(staged)
		os.Remove(script)
		return err
	}
	s.opts.Log.Logf("web: update staged (%d bytes), updater spawned", len(newExe))
	return nil
}

// handleEvents streams server-sent events (SSE) for live config and recording
// updates. The client connects once and receives events when config is saved
// or recordings are finalized, replacing polling loops.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	done := make(chan struct{})
	defer close(done)
	ch := s.cfg.Subscribe(done)
	recCh := s.cfg.SubscribeRecordings(done)
	logCh := s.logBus.Subscribe(done)
	defer func() { <-done }()

	// Send initial config so the client can hydrate.
	data, _ := json.Marshal(s.cfg.Get())
	fmt.Fprintf(w, "event: config-changed\ndata: %s\n\n", data)
	flusher.Flush()

	ctx := r.Context()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			data, _ := json.Marshal(s.cfg.Get())
			fmt.Fprintf(w, "event: config-changed\ndata: %s\n\n", data)
			flusher.Flush()
		case <-recCh:
			// Invalidate recording cache so next list fetch is fresh.
			s.recMu.Lock()
			s.recExpiry = time.Time{}
			s.recMu.Unlock()
			fmt.Fprintf(w, "event: recordings-changed\ndata: {}\n\n")
			flusher.Flush()
		case evt := <-logCh:
			data, _ := json.Marshal(evt)
			fmt.Fprintf(w, "event: log-line\ndata: %s\n\n", data)
			flusher.Flush()
		case <-ticker.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// updateScript is the detached PowerShell helper that applies a staged
// homenvrd.exe replacement. It is spawned by the daemon (running as SYSTEM),
// waits for the daemon process to exit so the exe is unlocked, swaps the
// binary, and starts the service. Failures are logged to update.log.
const updateScript = `param(
    [Parameter(Mandatory=$true)][string]$NewExe,
    [Parameter(Mandatory=$true)][string]$TargetExe,
    [Parameter(Mandatory=$true)][string]$Svc
)
$ErrorActionPreference = 'Stop'
$exeBase = [IO.Path]::GetFileNameWithoutExtension($TargetExe)
function Write-Log([string]$m) {
    # The daemon passes update.log as this process's stdout, so Write-Output
    # lands in the log. A separate Out-File cannot reopen the file while the
    # inherited handle holds it.
    Write-Output "$(Get-Date -Format 'yyyy-MM-dd HH:mm:ss')  $m"
}
function Test-DaemonRunning {
    return [bool](Get-Process -Name $exeBase -ErrorAction SilentlyContinue)
}
Write-Log 'update helper started'
& sc.exe stop $Svc 2>$null | Out-Null
$deadline = (Get-Date).AddMinutes(2)
while ((Get-Date) -lt $deadline) {
    if (-not (Test-DaemonRunning)) { break }
    Start-Sleep -Milliseconds 500
}
if (Test-DaemonRunning) {
    Write-Log 'update aborted: daemon still running after 2m'
    exit 1
}
$bak = "$TargetExe.bak"
Remove-Item -LiteralPath $bak -Force -ErrorAction SilentlyContinue
$ok = $false
for ($try = 1; $try -le 20 -and -not $ok; $try++) {
    try {
        Move-Item -LiteralPath $TargetExe -Destination $bak -Force -ErrorAction Stop
        Copy-Item -LiteralPath $NewExe -Destination $TargetExe -Force -ErrorAction Stop
        $ok = $true
    } catch {
        Start-Sleep -Milliseconds 500
    }
}
if (-not $ok) {
    Write-Log 'update aborted: could not swap exe'
    exit 1
}
Write-Log 'update swapped, starting service'
& sc.exe start $Svc 2>$null | Out-Null
Remove-Item -LiteralPath $NewExe -Force -ErrorAction SilentlyContinue
Write-Log 'update done'
`
