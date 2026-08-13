package recorder

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ric-dg/homenvr/internal/config"
)

type tlog struct{ t *testing.T }

func (l tlog) Logf(format string, args ...any) { l.t.Logf(format, args...) }

// testFile writes cfg to a temp file and loads it into a hot-reloading File.
func testFile(t *testing.T, cfg config.Config) *config.File {
	t.Helper()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.jsonc")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := config.NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func testRecorder(t *testing.T, cameras []config.Camera) (*Recorder, *config.File, config.Config) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Cameras = cameras
	f := testFile(t, cfg)
	return New(f, cameras[0].Name, tlog{t}, "ffmpeg.exe", t.TempDir()), f, cfg
}

// camNoAudio is a record-enabled camera with the mic off so the command
// builders never try to probe the (unreachable) audio port.
func camNoAudio(name string) config.Camera {
	c := config.DefaultCamera()
	c.Name = name
	c.Record.Prefix = name
	c.Mic.Enabled = false
	c.Mic.RecPort = 0
	c.Record.OutDir = filepath.Join(string(os.TempDir()), "nvr-test-"+name)
	return c
}

func TestEventCmd(t *testing.T) {
	r, _, _ := testRecorder(t, []config.Camera{camNoAudio("front")})
	cfg := r.cfg.Get()
	cam := cfg.Camera("front")
	path := filepath.Join(cam.Record.OutDir, "front-20060102-150405.mp4")
	got := r.eventCmd(cfg, *cam, path)

	label := "front"
	drawtext := "drawtext=fontfile='C\\:/Windows/Fonts/arial.ttf'" +
		":fontsize=32:fontcolor=white:box=1:boxcolor=black@0.6:boxborderw=8:x=20:y=20" +
		":text='" + label + " %{localtime\\:%Y-%m-%d %H-%M-%S}'"
	want := []string{
		"ffmpeg.exe", "-hide_banner", "-loglevel", "error",
		"-timeout", "8000000",
		"-rtsp_transport", "tcp", "-rtsp_flags", "prefer_tcp",
		"-i", "rtsp://127.0.0.1:8554/front?rw_timeout=8000000",
		"-map", "0:v:0",
		"-vf", drawtext,
		"-c:v", "av1_nvenc", "-preset", "p5", "-rc", "vbr", "-cq", "38",
		"-b:v", "0", "-maxrate", "3M", "-g", "30",
		"-movflags", "frag_keyframe+empty_moov", "-f", "mp4", path,
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("event cmd:\n got %v\nwant %v", got, want)
	}
}

func TestContinuousCmd(t *testing.T) {
	r, _, _ := testRecorder(t, []config.Camera{camNoAudio("front")})
	cfg := r.cfg.Get()
	cam := cfg.Camera("front")
	got := r.continuousCmd(cfg, *cam)
	joined := strings.Join(got, " ")
	for _, want := range []string{
		"-f segment", "-segment_time 600", "-reset_timestamps 1", "-strftime 1",
		filepath.Join(cam.Record.OutDir, "front-%Y%m%d-%H%M%S.mp4"),
		"-vf drawtext", "-c:v av1_nvenc",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("continuous cmd missing %q:\n%v", want, got)
		}
	}
	if _, err := os.Stat(cam.Record.OutDir); err != nil {
		t.Errorf("out dir not created by segmentArgs: %v", err)
	}
}

func TestCombinedCmd(t *testing.T) {
	r, _, _ := testRecorder(t, []config.Camera{
		camNoAudio("front"), camNoAudio("door"),
	})
	cfg := r.cfg.Get()
	argv, owner := r.combinedCmd(cfg)
	if owner != "front" {
		t.Fatalf("combined owner = %q, want front", owner)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{
		"-filter_complex",
		"[0:v:0]scale=960:540,fps=15,format=yuv420p,setsar=1,drawtext=fontfile='C\\:/Windows/Fonts/arial.ttf':fontsize=28:fontcolor=white:box=1:boxcolor=black@0.6:boxborderw=6:x=10:y=10:text='front'[v0]",
		"[1:v:0]scale=960:540,fps=15,format=yuv420p,setsar=1,drawtext=fontfile='C\\:/Windows/Fonts/arial.ttf':fontsize=28:fontcolor=white:box=1:boxcolor=black@0.6:boxborderw=6:x=10:y=10:text='door'[v1]",
		"[v0][v1]xstack=inputs=2:layout=0_0|960_0[vout]",
		"-map [vout]",
		filepath.Join(cfg.Cameras[0].Record.OutDir, "combined", "combined-%Y%m%d-%H%M%S.mp4"),
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("combined cmd missing %q:\n%v", want, argv)
		}
	}
	if strings.Contains(joined, "aout") {
		t.Errorf("combined cmd should not include audio when mics are off:\n%v", argv)
	}
}

func TestEventName(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{1, "front-motion-20060102-150405.mp4"},
		{2, "front-motion-20060102-150405-2.mp4"},
		{9, "front-motion-20060102-150405-9.mp4"},
	}
	for _, c := range cases {
		if got := eventName("front", "motion", "20060102-150405", c.n); got != c.want {
			t.Errorf("eventName(n=%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestOwnerSelection(t *testing.T) {
	cfg := config.Defaults()
	cfg.Cameras = []config.Camera{
		camNoAudio("a"), camNoAudio("b"),
	}
	cfg.Cameras[0].Record.Enabled = false
	cfg.Cameras[1].Record.Enabled = true
	if got := cameraIndex(&cfg, "b"); got != 1 {
		t.Errorf("cameraIndex(b) = %d, want 1", got)
	}
	if got := cameraIndex(&cfg, "missing"); got != 0 {
		t.Errorf("cameraIndex(missing) = %d, want 0", got)
	}
	if got := firstEnabledRecordIndex(&cfg); got != 1 {
		t.Errorf("firstEnabledRecordIndex = %d, want 1", got)
	}
	cfg.Cameras[0].Record.Enabled = true
	if got := firstEnabledRecordIndex(&cfg); got != 0 {
		t.Errorf("firstEnabledRecordIndex = %d, want 0", got)
	}
}
