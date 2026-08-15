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
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
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
	mux.HandleFunc("POST /api/restart", s.handleRestart)
	mux.HandleFunc("POST /api/retention/run", s.handleRunRetention)
	mux.HandleFunc("GET /api/logs", s.handleLogs)
	mux.HandleFunc("POST /api/update", s.handleUpdate)
	s.mux = &http.Server{Handler: mux}
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

// DETACHED_PROCESS (0x00000008) creates the child without a console; not in
// the stdlib syscall package, so defined here. The updater helper must
// outlive the daemon process that spawns it.
const detachedProcessFlag = 0x00000008

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
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | detachedProcessFlag}
	if err := cmd.Start(); err != nil {
		os.Remove(staged)
		os.Remove(script)
		return err
	}
	s.opts.Log.Logf("web: update staged (%d bytes), updater spawned", len(newExe))
	return nil
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
$log = Join-Path (Split-Path $TargetExe -Parent) 'update.log'
function Write-Log([string]$m) {
    $line = "$(Get-Date -Format 'yyyy-MM-dd HH:mm:ss')  $m"
    try { $line | Out-File -FilePath $log -Append -Encoding utf8 } catch { }
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
