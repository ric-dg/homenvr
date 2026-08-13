package web

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ric-dg/homenvr/internal/config"
)

type tlog struct{ t *testing.T }

func (l tlog) Logf(format string, args ...any) { l.t.Logf(format, args...) }

type fakeStatus struct{}

func (fakeStatus) Status() Status {
	return Status{
		Mode:     "event",
		LivePort: 1984,
		Go2rtc:   ProcInfo{Running: true, PID: 42, Uptime: time.Minute},
		Cameras:  []CamInfo{{Name: "front", Active: true, Mic: true, Mode: "event"}},
	}
}

// testServer writes a config pointing the camera's recordings at a temp dir
// and returns the ready server plus the config path.
func testServer(t *testing.T) (*Server, string, string) {
	t.Helper()
	dir := t.TempDir()
	outDir := filepath.Join(dir, "recs")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.jsonc")
	content := fmt.Sprintf(`{
  "web": { "port": 0 },
  "paths": { "log_dir": %q },
  "cameras": [{
    "name": "front", "source": "dshow", "device_name": "D",
    "mic": { "enabled": false },
    "record": { "enabled": true, "prefix": "front", "out_dir": %q }
  }]
}`, dir, outDir)
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := config.NewFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	return New(f, Options{ConfigPath: cfgPath, Status: fakeStatus{}, Log: tlog{t}, Version: "9.9.9"}), cfgPath, outDir
}

func doReq(t *testing.T, s *Server, method, path string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.mux.Handler.ServeHTTP(rec, req)
	return rec
}

func TestStaticAssets(t *testing.T) {
	s, _, _ := testServer(t)
	if rec := doReq(t, s, "GET", "/", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "HomeNVR") {
		t.Errorf("GET / = %d, body has HomeNVR: %v", rec.Code, strings.Contains(rec.Body.String(), "HomeNVR"))
	}
	if rec := doReq(t, s, "GET", "/app.js", ""); rec.Code != 200 {
		t.Errorf("GET /app.js = %d", rec.Code)
	}
	if rec := doReq(t, s, "GET", "/style.css", ""); rec.Code != 200 {
		t.Errorf("GET /style.css = %d", rec.Code)
	}
	if rec := doReq(t, s, "GET", "/missing", ""); rec.Code != 404 {
		t.Errorf("GET /missing = %d, want 404", rec.Code)
	}
}

func TestStatusEndpoint(t *testing.T) {
	s, _, _ := testServer(t)
	rec := doReq(t, s, "GET", "/api/status", "")
	if rec.Code != 200 {
		t.Fatalf("GET /api/status = %d", rec.Code)
	}
	var st Status
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Version != "9.9.9" {
		t.Errorf("version = %q, want injected 9.9.9", st.Version)
	}
	if !st.Go2rtc.Running || st.Go2rtc.PID != 42 {
		t.Errorf("go2rtc status wrong: %+v", st.Go2rtc)
	}
	if len(st.Cameras) != 1 || st.Cameras[0].Name != "front" {
		t.Errorf("cameras = %+v", st.Cameras)
	}
}

func TestConfigGetPut(t *testing.T) {
	s, cfgPath, _ := testServer(t)

	rec := doReq(t, s, "GET", "/api/config", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"out_dir"`) {
		t.Errorf("GET /api/config = %d, body=%s", rec.Code, rec.Body.String())
	}

	// Valid edit round-trips and lands on disk.
	edit := `{ "web": { "port": 8081 }, "record": { "mode": "continuous" },
  "cameras": [ { "name": "front", "record": { "out_dir": "/tmp/x" } } ] }`
	rec = doReq(t, s, "PUT", "/api/config", edit)
	if rec.Code != 200 {
		t.Fatalf("PUT valid = %d: %s", rec.Code, rec.Body.String())
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"port": 8081`) {
		t.Errorf("saved config missing edit: %s", data)
	}

	// Broken edits are rejected and do not touch the file.
	before, _ := os.ReadFile(cfgPath)
	rec = doReq(t, s, "PUT", "/api/config", `{"go2rtc": {`)
	if rec.Code != 400 {
		t.Errorf("PUT broken JSON = %d, want 400", rec.Code)
	}
	rec = doReq(t, s, "PUT", "/api/config", `{ "record": { "mode": "bogus" } }`)
	if rec.Code != 400 {
		t.Errorf("PUT invalid mode = %d, want 400", rec.Code)
	}
	after, _ := os.ReadFile(cfgPath)
	if string(before) != string(after) {
		t.Error("failed PUT still modified the config file")
	}
}

func TestRecordingsListAndServe(t *testing.T) {
	s, _, outDir := testServer(t)

	// Write a fake MP4 in the camera dir and in a combined subdir.
	video := []byte("0123456789abcdef")
	if err := os.WriteFile(filepath.Join(outDir, "front-motion-20260101-120000.mp4"), video, 0o644); err != nil {
		t.Fatal(err)
	}
	combinedDir := filepath.Join(outDir, "combined")
	if err := os.MkdirAll(combinedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(combinedDir, "combined-20260101-120000.mp4"), video, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "notes.txt"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := doReq(t, s, "GET", "/api/recordings", "")
	if rec.Code != 200 {
		t.Fatalf("GET /api/recordings = %d", rec.Code)
	}
	var list []Recording
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Errorf("recordings = %d, want 2 (mp4 only): %+v", len(list), list)
	}

	// Full download.
	if rec := doReq(t, s, "GET", "/api/recordings/front/front-motion-20260101-120000.mp4", ""); rec.Code != 200 || rec.Body.String() != string(video) {
		t.Errorf("full get = %d, body %q", rec.Code, rec.Body.String())
	}

	// Range request (video scrub) returns 206 with the requested slice.
	req := httptest.NewRequest("GET", "/api/recordings/front/front-motion-20260101-120000.mp4", nil)
	req.Header.Set("Range", "bytes=2-5")
	rec = httptest.NewRecorder()
	s.mux.Handler.ServeHTTP(rec, req)
	if rec.Code != 206 || rec.Body.String() != "2345" {
		t.Errorf("range get = %d, body %q", rec.Code, rec.Body.String())
	}

	// Combined route.
	if rec := doReq(t, s, "GET", "/api/recordings/front/combined/combined-20260101-120000.mp4", ""); rec.Code != 200 {
		t.Errorf("combined get = %d", rec.Code)
	}

	// Traversal attempts are refused.
	for _, path := range []string{
		"/api/recordings/front/..%2F..%2Fsecret.mp4",
		"/api/recordings/front/%5Cwindows%5Cnotepad.exe",
		"/api/recordings/unknown/x.mp4",
	} {
		if rec := doReq(t, s, "GET", path, ""); rec.Code == 200 {
			t.Errorf("traversal %s served, want non-200", path)
		}
	}
}
