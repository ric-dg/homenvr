package supervisor

import (
	"fmt"
	"strings"

	"github.com/ric-dg/homenvr/internal/config"
)

// BuildYAML renders the go2rtc.yaml file from the effective config, mirroring
// v1 run.ps1 Build-Yaml. go2rtc executes the ffmpeg command per camera; the
// supervisor only manages go2rtc itself and the feed processes.
func BuildYAML(cfg *config.Config) string {
	var b strings.Builder
	b.WriteString("log:\n  level: info\n  format: text\n\n")
	fmt.Fprintf(&b, "api:\n  listen: \":%d\"\n\n", cfg.Go2rtc.APIPort)
	fmt.Fprintf(&b, "rtsp:\n  listen: \":%d\"\n\n", cfg.Go2rtc.RTSPPort)
	fmt.Fprintf(&b, "webrtc:\n  listen: \":%d\"\n\n", cfg.Go2rtc.WebRTCPort)
	b.WriteString("streams:\n")
	for _, cam := range cfg.Cameras {
		ff := cfg.Tools.FFmpeg
		if ff == "" {
			ff = "ffmpeg"
		}
		if strings.Contains(ff, " ") {
			ff = `"` + ff + `"`
		}
		var videoIn string
		if cam.Source == "rtsp" {
			videoIn = `-rtsp_transport tcp -i "` + cam.RTSPURL + `"`
		} else {
			videoIn = fmt.Sprintf(
				`-f dshow -rtbufsize %s -framerate %d -video_size %dx%d -i "video=%s"`,
				cam.RTBufSize, cam.FPS, cam.Width, cam.Height, cam.DeviceName)
		}
		maps := "-map 0:v:0"
		streamAud := ""
		if cam.Mic.Enabled {
			videoIn += fmt.Sprintf(
				" -thread_queue_size 512 -f s16le -ar %d -ac %d -i tcp://127.0.0.1:%d",
				cam.Mic.SampleRate, cam.Mic.Channels, cam.Mic.LivePort)
			maps = "-map 0:v:0 -map 1:a:0"
			streamAud = fmt.Sprintf(" -c:a %s -b:a %s", cam.Live.Audio.Codec, cam.Live.Audio.Bitrate)
		}
		enc := "-vf format=yuv420p " + strings.Join(
			config.VideoEncodeArgs(cam.Live.Video, cfg.GPU.Encoders), " ")
		execLine := fmt.Sprintf("exec:%s -hide_banner %s %s %s%s -f mpegts -",
			ff, videoIn, maps, enc, streamAud)
		fmt.Fprintf(&b, "  %s:\n", cam.Name)
		b.WriteString("    - " + execLine + "\n")
	}
	b.WriteString("\npreload:\n")
	for _, cam := range cfg.Cameras {
		fmt.Fprintf(&b, "  %s: video\n", cam.Name)
	}
	return b.String()
}
