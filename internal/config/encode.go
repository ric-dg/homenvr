package config

import (
	"fmt"
	"strconv"
)

// NVENC_CODECS and CPU_CODECS, as in v1 config.py.
var nvencCodecs = map[string]bool{
	"h264_nvenc": true,
	"hevc_nvenc": true,
	"av1_nvenc":  true,
}

var cpuCodecs = map[string]bool{
	"libx264":   true,
	"libsvtav1": true,
}

// cpuPresetMap translates the NVENC-style presets (p1 fastest .. p7 slowest)
// to the equivalent CPU encoder preset, so one config value works for any codec.
var cpuPresetMap = map[string]map[string]string{
	"libx264": {
		"p1": "superfast", "p2": "veryfast", "p3": "faster",
		"p4": "fast", "p5": "medium", "p6": "slow", "p7": "veryslow",
	},
	"libsvtav1": {
		"p1": "12", "p2": "10", "p3": "9", "p4": "8",
		"p5": "7", "p6": "5", "p7": "3",
	},
}

func cpuPreset(codec, preset string) string {
	if mapped, ok := cpuPresetMap[codec][preset]; ok {
		return mapped
	}
	return preset
}

// VideoEncodeArgs produces the ffmpeg video-encode arguments for one video
// dict (live or record), mirroring v1 config.py video_encode_args. gpuIndex is
// the "gpu.encoders" value; "auto" (or empty) adds no -gpu flag.
func VideoEncodeArgs(v Video, gpuIndex string) []string {
	args := []string{"-c:v", v.Codec}
	if nvencCodecs[v.Codec] {
		if gpuIndex != "" && gpuIndex != "auto" {
			args = append(args, "-gpu", gpuIndex)
		}
		args = append(args,
			"-preset", v.Preset,
			"-rc", v.RC,
			"-cq", strconv.Itoa(v.CQ),
			"-b:v", "0",
			"-maxrate", v.MaxRate,
			"-g", strconv.Itoa(v.G),
		)
		if v.Tune != "" {
			args = append(args, "-tune", v.Tune)
		}
		if v.BF != nil {
			args = append(args, "-bf", strconv.Itoa(*v.BF))
		}
	} else {
		args = append(args,
			"-preset", cpuPreset(v.Codec, v.Preset),
			"-crf", strconv.Itoa(v.CRF),
			"-g", strconv.Itoa(v.G),
		)
	}
	return args
}

// IsNVENCCodec reports whether codec needs NVENC-style arguments.
func IsNVENCCodec(codec string) bool { return nvencCodecs[codec] }

// IsCPUCodec reports whether codec needs CPU-style arguments.
func IsCPUCodec(codec string) bool { return cpuCodecs[codec] }

// CameraURL returns the RTSP URL the detector/recorder use to reach this
// camera through go2rtc, mirroring v1 config.py camera_url.
func (c *Config) CameraURL(cam Camera) string {
	return fmt.Sprintf("rtsp://%s:%d/%s?rw_timeout=8000000",
		c.Paths.RTSPHost, c.Go2rtc.RTSPPort, cam.Name)
}
