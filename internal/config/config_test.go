package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStripComments(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"a": 1} // note`, `{"a": 1} `},
		{"{\n// line\n\"a\": 1\n}", "{\n\n\"a\": 1\n}"},
		{`{"a": /* block */ 1}`, `{"a":  1}`},
		{`{"url": "rtsp://host:8554/cam?rw_timeout=8000000"}`, `{"url": "rtsp://host:8554/cam?rw_timeout=8000000"}`},
		{`{"s": "a//b"}`, `{"s": "a//b"}`},
		{`{"s": "a/*b*/c"}`, `{"s": "a/*b*/c"}`},
		{`{"s": "esc \"// x"}`, `{"s": "esc \"// x"}`},
		{`// only comment`, ``},
		{`/* only block */`, ``},
	}
	for _, c := range cases {
		if got := stripComments(c.in); got != c.want {
			t.Errorf("stripComments(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.jsonc"))
	if err != nil {
		t.Fatalf("Load(missing) = %v", err)
	}
	if cfg.Go2rtc.APIPort != 1984 || cfg.Go2rtc.RTSPPort != 8554 || cfg.Go2rtc.WebRTCPort != 8555 {
		t.Errorf("go2rtc defaults wrong: %+v", cfg.Go2rtc)
	}
	if cfg.Record.Mode != "event" || cfg.Record.RetainHours != 72 {
		t.Errorf("record defaults wrong: %+v", cfg.Record)
	}
	if len(cfg.Cameras) != 1 || cfg.Cameras[0].Name != "brio" {
		t.Errorf("camera defaults wrong: %+v", cfg.Cameras)
	}
	if cfg.Cameras[0].Record.Video.Codec != "av1_nvenc" {
		t.Errorf("record codec default wrong: %q", cfg.Cameras[0].Record.Video.Codec)
	}
	if cfg.Cameras[0].Live.Video.Codec != "h264_nvenc" {
		t.Errorf("live codec default wrong: %q", cfg.Cameras[0].Live.Video.Codec)
	}
}

func TestLoadMergeAndNormalize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.jsonc")
	// Camera is partial: only name, fps and live.codec are given.
	content := `{
  "go2rtc": { "api_port": 1999 },
  "log": { "keep": 3 },
  "record": { "mode": "continuous" },
  "cameras": [
    { "name": "front", "fps": 30, "live": { "video": { "codec": "hevc_nvenc" } } }
  ]
}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load = %v", err)
	}
	if cfg.Go2rtc.APIPort != 1999 {
		t.Errorf("api_port = %d, want 1999", cfg.Go2rtc.APIPort)
	}
	if cfg.Go2rtc.RTSPPort != 8554 {
		t.Errorf("rtsp_port = %d, want default 8554", cfg.Go2rtc.RTSPPort)
	}
	if cfg.Log.Keep != 3 {
		t.Errorf("log.keep = %d, want 3", cfg.Log.Keep)
	}
	if cfg.Record.Mode != "continuous" {
		t.Errorf("record.mode = %q", cfg.Record.Mode)
	}
	cam := cfg.Cameras[0]
	if len(cfg.Cameras) != 1 || cam.Name != "front" {
		t.Fatalf("cameras = %+v", cfg.Cameras)
	}
	if cam.FPS != 30 {
		t.Errorf("fps = %d, want 30", cam.FPS)
	}
	if cam.Live.Video.Codec != "hevc_nvenc" {
		t.Errorf("live codec = %q", cam.Live.Video.Codec)
	}
	if cam.Live.Video.Tune != "ll" {
		t.Errorf("template tune lost: %q", cam.Live.Video.Tune)
	}
	if cam.Width != 1920 || cam.Height != 1080 {
		t.Errorf("template dims lost: %dx%d", cam.Width, cam.Height)
	}
	if cam.Record.Video.Codec != "av1_nvenc" {
		t.Errorf("record codec = %q, want template av1_nvenc", cam.Record.Video.Codec)
	}
}

func TestLoadParseErrorKeepsNothing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.jsonc")
	if err := os.WriteFile(path, []byte(`{"go2rtc": {`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load with broken JSON should error")
	}
}

func TestValidate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.jsonc")
	bad := `{
  "record": { "mode": "weird" },
  "cameras": [
    { "name": "a", "source": "dshow", "mic": { "enabled": true, "live_port": 1, "rec_port": 1, "ctl_port": 2 } },
    { "name": "a", "source": "vga", "mic": { "enabled": false } }
  ]
}`
	if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load = %v", err)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate should fail on this config")
	} else {
		for _, want := range []string{"mode", "duplicate camera", "port 1 reused", "source must"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("Validate error %q missing %q", err, want)
			}
		}
	}
	// Valid config passes.
	good := `{ "cameras": [{ "name": "x", "source": "dshow", "device_name": "D", "mic": { "enabled": true, "live_port": 10, "rec_port": 11, "ctl_port": 12 } }] }`
	if err := os.WriteFile(path, []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatalf("Load = %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate(good) = %v", err)
	}
}

func TestVideoEncodeArgs(t *testing.T) {
	live := DefaultCamera().Live.Video
	cfg := &Config{GPU: GPU{Encoders: "auto"}, Encoders: EncoderProbe{NVENC: true, CPU: []string{"libx264", "libsvtav1"}}}
	got := VideoEncodeArgs(cfg, live)
	want := []string{"-c:v", "h264_nvenc", "-preset", "p2", "-rc", "vbr", "-cq", "28", "-b:v", "0", "-maxrate", "4M", "-g", "15", "-tune", "ll", "-bf", "0"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("nvenc args = %v, want %v", got, want)
	}

	cfg.GPU.Encoders = "1"
	got = VideoEncodeArgs(cfg, live)
	if !contains(got, "-gpu", "1") {
		t.Errorf("gpu index missing: %v", got)
	}

	rec := DefaultCamera().Record.Video
	cfg.GPU.Encoders = "auto"
	got = VideoEncodeArgs(cfg, rec)
	want = []string{"-c:v", "av1_nvenc", "-preset", "p5", "-rc", "vbr", "-cq", "38", "-b:v", "0", "-maxrate", "3M", "-g", "30"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("record nvenc args = %v, want %v", got, want)
	}

	cpu := Video{Codec: "libx264", Preset: "p5", CRF: 23, G: 30}
	got = VideoEncodeArgs(cfg, cpu)
	want = []string{"-c:v", "libx264", "-preset", "medium", "-crf", "23", "-g", "30"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("cpu args = %v, want %v", got, want)
	}
	if p := cpuPreset("libsvtav1", "p1"); p != "12" {
		t.Errorf("svtav1 p1 = %q, want 12", p)
	}
}

func TestVideoEncodeArgsFallback(t *testing.T) {
	// No NVIDIA hardware: nvenc codecs degrade to their CPU equivalents with
	// the CPU preset mapping, and the GPU flag is never emitted.
	cfg := &Config{GPU: GPU{Encoders: "auto"}, Encoders: EncoderProbe{CPU: []string{"libx264"}}}
	got := VideoEncodeArgs(cfg, DefaultCamera().Live.Video) // h264_nvenc
	want := []string{"-c:v", "libx264", "-preset", "veryfast", "-crf", "24", "-g", "15"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("nvenc->cpu fallback = %v, want %v", got, want)
	}
	if got := VideoEncodeArgs(cfg, DefaultCamera().Record.Video); got[1] != "libx264" {
		t.Errorf("av1_nvenc fallback = %v, want libx264 (no libsvtav1)", got)
	}

	// libsvtav1 missing from the build -> libx264.
	cfg2 := &Config{GPU: GPU{Encoders: "auto"}, Encoders: EncoderProbe{CPU: []string{"libx264"}}}
	if got := VideoEncodeArgs(cfg2, Video{Codec: "libsvtav1", Preset: "p5", CRF: 30, G: 30}); got[1] != "libx264" {
		t.Errorf("missing svtav1 fallback = %v, want libx264", got)
	}

	// Passthrough emits nothing but the copy switch.
	if got := VideoEncodeArgs(cfg, Video{Codec: "copy", Preset: "p5"}); strings.Join(got, " ") != "-c:v copy" {
		t.Errorf("copy args = %v, want only -c:v copy", got)
	}

	// ResolvedCodec is stable even when nothing is probed (Parse output).
	if got := (&Config{}).ResolvedCodec("h264_nvenc"); got != "libx264" {
		t.Errorf("unprobed nvenc resolves to %q, want libx264", got)
	}
}

func TestScanEncoders(t *testing.T) {
	s := ` V....D h264_nvenc         NVIDIA NVENC H.264 encoder
 V....D av1_nvenc          NVIDIA NVENC AV1 encoder
 V....D libsvtav1          SVT-AV1 encoder
 V..... libx264            libx264 H.264
`
	p := scanEncoders(s)
	if !p.NVENC {
		t.Error("NVENC not detected")
	}
	if !hasCodec(p.CPU, "libx264") || !hasCodec(p.CPU, "libsvtav1") {
		t.Errorf("cpu codecs = %v, want libx264 and libsvtav1", p.CPU)
	}
}

func hasCodec(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}

func TestCameraURL(t *testing.T) {
	cfg, _ := Load(filepath.Join(t.TempDir(), "missing.jsonc"))
	got := cfg.CameraURL(cfg.Cameras[0])
	want := "rtsp://127.0.0.1:8554/brio?rw_timeout=8000000"
	if got != want {
		t.Errorf("CameraURL = %q, want %q", got, want)
	}
}

func TestFileRefresh(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.jsonc")
	if err := os.WriteFile(path, []byte(`{"log": {"keep": 2}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	f, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if f.Get().Log.Keep != 2 {
		t.Fatalf("initial keep = %d", f.Get().Log.Keep)
	}
	if changed, _ := f.Refresh(); changed {
		t.Fatal("Refresh reported change with no edit")
	}
	if err := os.WriteFile(path, []byte(`{"log": {"keep": 9}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	changed, err := f.Refresh()
	if err != nil || !changed {
		t.Fatalf("Refresh after edit: changed=%v err=%v", changed, err)
	}
	if f.Get().Log.Keep != 9 {
		t.Errorf("keep = %d, want 9", f.Get().Log.Keep)
	}
	// Broken edit keeps last good config.
	if err := os.WriteFile(path, []byte(`{"log": {`), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	changed, err = f.Refresh()
	if err == nil {
		t.Fatal("Refresh should surface parse error")
	}
	if f.Get().Log.Keep != 9 {
		t.Errorf("broken edit clobbered config: keep = %d, want 9", f.Get().Log.Keep)
	}
}

func contains(s []string, pairs ...string) bool {
	for i := 0; i+1 < len(pairs); i += 2 {
		found := false
		for j := 0; j < len(s)-1; j++ {
			if s[j] == pairs[i] && s[j+1] == pairs[i+1] {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
