package supervisor

import (
	"strings"
	"testing"

	"github.com/ric-dg/homenvr/internal/config"
)

// testConfig builds a single-camera config matching the reference go2rtc.yaml
// that v1 generated on this machine (used for the parity test below).
func testConfig() *config.Config {
	cfg := config.Defaults()
	cfg.Tools.FFmpeg = `C:\ProgramData\scoop\shims\ffmpeg.exe`
	// Match this box: NVIDIA present, so the default h264_nvenc live codec is
	// used rather than degraded to libx264.
	cfg.Encoders = config.EncoderProbe{NVENC: true, CPU: []string{"libx264", "libsvtav1"}}
	return &cfg
}

func TestBuildYAML(t *testing.T) {
	cfg := testConfig()
	got := BuildYAML(cfg)
	want := `log:
  level: info
  format: text

api:
  listen: ":1984"

rtsp:
  listen: ":8554"

webrtc:
  listen: ":8555"

streams:
  brio:
    - exec:C:\ProgramData\scoop\shims\ffmpeg.exe -hide_banner -f dshow -rtbufsize 64M -framerate 15 -video_size 1920x1080 -i "video=Brio 100" -thread_queue_size 512 -f s16le -ar 48000 -ac 1 -i tcp://127.0.0.1:9010 -map 0:v:0 -map 1:a:0 -vf format=yuv420p -c:v h264_nvenc -preset p2 -rc vbr -cq 28 -b:v 0 -maxrate 4M -g 15 -tune ll -bf 0 -c:a aac -b:a 64k -f mpegts -

preload:
  brio: video
`
	if got != want {
		t.Errorf("BuildYAML mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestBuildYAMLRTSPCamera(t *testing.T) {
	cfg := testConfig()
	cfg.Tools.FFmpeg = ""
	cam := config.DefaultCamera()
	cam.Name = "front"
	cam.Source = "rtsp"
	cam.RTSPURL = "rtsp://192.168.1.50:554/stream1"
	cam.Mic.Enabled = false
	cfg.Cameras = []config.Camera{cam}
	got := BuildYAML(cfg)
	if !strings.Contains(got, `exec:ffmpeg -hide_banner -rtsp_transport tcp -i "rtsp://192.168.1.50:554/stream1" -map 0:v:0`) {
		t.Errorf("rtsp camera line wrong:\n%s", got)
	}
	if strings.Contains(got, "-map 1:a:0") {
		t.Errorf("mic disabled but audio map present:\n%s", got)
	}
	if strings.Contains(got, "thread_queue_size") {
		t.Errorf("mic disabled but mic input present:\n%s", got)
	}
}

func TestGenHashSensitiveToRelevantKeys(t *testing.T) {
	cfg := testConfig()
	base := GenHash(cfg)

	cfg.Go2rtc.APIPort = 3000
	if GenHash(cfg) == base {
		t.Error("api_port change did not change hash")
	}
	cfg.Go2rtc.APIPort = 1984

	cfg.Cameras[0].FPS = 20
	if GenHash(cfg) == base {
		t.Error("camera fps change did not change hash")
	}
	cfg.Cameras[0].FPS = 15

	cfg.Cameras[0].Mic.LivePort = 9999
	if GenHash(cfg) == base {
		t.Error("mic live_port change did not change hash")
	}
	cfg.Cameras[0].Mic.LivePort = 9010

	cfg.Cameras[0].Motion.Threshold = 99
	cfg.Cameras[0].Sound.Ratio = 9
	cfg.Cameras[0].Record.Video.CQ = 50
	cfg.Cameras[0].Mic.Gain = 20
	cfg.Log.Keep = 1
	cfg.Record.Mode = "continuous"
	cfg.Monitor.IntervalSec = 1
	if h := GenHash(cfg); h != base {
		t.Errorf("hot-reload-only keys changed the hash: %s -> %s", base, h)
	}
}

func TestGenHashStable(t *testing.T) {
	cfg := testConfig()
	a := GenHash(cfg)
	b := GenHash(cfg)
	if a != b {
		t.Errorf("hash not stable: %s != %s", a, b)
	}
	if len(a) != 32 {
		t.Errorf("hash length = %d, want 32", len(a))
	}
}
