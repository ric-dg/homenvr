package recorder

import (
	"encoding/json"
	"fmt"
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

func TestPreRollEventCmd(t *testing.T) {
	cam := camNoAudio("front")
	cam.Record.PreRollSec = 4
	cam.Record.SegmentSec = 2
	r, _, _ := testRecorder(t, []config.Camera{cam})
	cfg := r.cfg.Get()
	cam = *cfg.Camera("front")
	path := filepath.Join(cam.Record.OutDir, "front-20060102-150405.mp4")
	pre := []string{
		filepath.Join(cam.Record.OutDir, ".preroll-front", "00000000.ts"),
		filepath.Join(cam.Record.OutDir, ".preroll-front", "00000001.ts"),
	}
	got := r.preRollEventCmd(cfg, cam, path, pre, false)
	joined := strings.Join(got, " ")

	for _, want := range []string{
		"-i rtsp://127.0.0.1:8554/front?rw_timeout=8000000",
		"-i " + pre[0],
		"-i " + pre[1],
		"-filter_complex",
		"[1:v:0]setpts=PTS,fps=15[pr0];",
		"[2:v:0]setpts=PTS,fps=15[pr1];",
		"[0:v:0]setpts=PTS,fps=15,drawtext=",
		"[pr0][pr1][vL]concat=n=3:v=1:a=0[vout]",
		"-map [vout]",
		"-c:v av1_nvenc",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("pre-roll event cmd missing %q:\n%v", want, got)
		}
	}
	if strings.Contains(joined, "-vf ") {
		t.Errorf("pre-roll event cmd must not use -vf, got filter_complex:\n%v", got)
	}
	if n := strings.Count(joined, "drawtext"); n != 1 {
		t.Errorf("drawtext should appear once (live branch only), got %d:\n%v", n, got)
	}
}

func TestPreRollEventCmdCopyDegrades(t *testing.T) {
	cam := camNoAudio("front")
	cam.Record.PreRollSec = 4
	cam.Record.Video.Codec = "copy"
	r, _, _ := testRecorder(t, []config.Camera{cam})
	cfg := r.cfg.Get()
	cam = *cfg.Camera("front")
	got := r.preRollEventCmd(cfg, cam,
		filepath.Join(cam.Record.OutDir, "front-20060102-150405.mp4"),
		[]string{"seg.ts"}, false)
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "-c:v libx264") {
		t.Errorf("pre-roll with codec copy should degrade to libx264:\n%v", got)
	}
	if strings.Contains(joined, "-c:v copy") {
		t.Errorf("pre-roll must not stream-copy:\n%v", got)
	}
}

func TestPreRollEventCmdAudioInput(t *testing.T) {
	cam := camNoAudio("front")
	cam.Record.PreRollSec = 4
	cam.Mic.Enabled = true
	cam.Mic.RecPort = 9011
	cam.Mic.SampleRate = 48000
	cam.Mic.Channels = 1
	r, _, _ := testRecorder(t, []config.Camera{cam})
	cfg := r.cfg.Get()
	cam = *cfg.Camera("front")
	pre := []string{"seg0.ts", "seg1.ts"}
	got := r.preRollEventCmd(cfg, cam,
		filepath.Join(cam.Record.OutDir, "front-20060102-150405.mp4"), pre, true)
	joined := strings.Join(got, " ")

	// live(0) + 2 ring inputs(1,2) + mic(3): every -i must be present or the
	// filtergraph's [3:a:0] reference fails to bind.
	wantIn := "-i tcp://127.0.0.1:9011"
	if !strings.Contains(joined, wantIn) {
		t.Errorf("pre-roll event cmd missing mic input %q:\n%v", wantIn, got)
	}
	if n := strings.Count(joined, " -i "); n != 4 {
		t.Errorf("pre-roll event cmd should have 4 inputs (live+2 ring+mic), got %d:\n%v", n, got)
	}
	if !strings.Contains(joined, "[3:a:0]anull[aout]") {
		t.Errorf("pre-roll event cmd should map mic audio via [3:a:0]:\n%v", got)
	}
	if !strings.Contains(joined, "-map [aout]") {
		t.Errorf("pre-roll event cmd should map [aout]:\n%v", got)
	}
}

func TestPreRollRingFiles(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i <= 6; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%08d.ts", i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p := &prerollRing{dir: dir, keep: 4}
	got := p.files()
	want := []string{
		filepath.Join(dir, "00000003.ts"),
		filepath.Join(dir, "00000004.ts"),
		filepath.Join(dir, "00000005.ts"),
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("ring files = %v, want %v", got, want)
	}
	if got := (&prerollRing{}).files(); got != nil {
		t.Errorf("nil ring files = %v, want nil", got)
	}
}

func TestPreRollRingPrune(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i <= 10; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%08d.ts", i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p := &prerollRing{dir: dir, keep: 4}
	p.prune()
	for _, i := range []int{5, 6, 7, 8, 9, 10} {
		if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("%08d.ts", i))); err != nil {
			t.Errorf("segment %d should survive prune: %v", i, err)
		}
	}
	for _, i := range []int{0, 1, 2, 3, 4} {
		if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("%08d.ts", i))); err == nil {
			t.Errorf("segment %d should have been pruned", i)
		}
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
