package config

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
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

// EncoderProbe records which encoders the local ffmpeg build provides,
// discovered once at Load (see probeEncoders). Unknown codecs are assumed
// unavailable, so ResolvedCodec can fall back before ffmpeg errors at runtime.
type EncoderProbe struct {
	NVENC bool
	CPU   []string
}

// probeEncoders asks the resolved ffmpeg which encoders it has. One cheap
// subprocess at startup is all it costs; the result drives per-camera codec
// fallback on machines without NVIDIA hardware.
func (c *Config) probeEncoders() {
	ff := c.Tools.FFmpeg
	if ff == "" {
		ff = "ffmpeg"
	}
	out, err := exec.Command(ff, "-hide_banner", "-encoders").Output()
	if err != nil {
		return
	}
	c.Encoders = scanEncoders(string(out))
}

func scanEncoders(s string) EncoderProbe {
	var p EncoderProbe
	for _, line := range strings.Split(s, "\n") {
		switch {
		case strings.Contains(line, "_nvenc"):
			p.NVENC = true
		case strings.Contains(line, "libx264"):
			p.CPU = append(p.CPU, "libx264")
		case strings.Contains(line, "libsvtav1"):
			p.CPU = append(p.CPU, "libsvtav1")
		}
	}
	return p
}

func (c *Config) hasCPUCodec(codec string) bool {
	for _, s := range c.Encoders.CPU {
		if s == codec {
			return true
		}
	}
	return false
}

// ResolvedCodec returns the encoder to actually use for a configured codec,
// falling back when the local ffmpeg lacks it: any *_nvenc codec on a machine
// without NVIDIA hardware becomes its CPU equivalent (h264/hevc -> libx264,
// av1 -> libsvtav1), and a missing CPU codec (e.g. libsvtav1 on a minimal
// build) becomes libx264. "copy" passes through. Unknown codecs pass through
// so ffmpeg reports the real error. The daemon and the generated go2rtc.yaml
// both use this, so a config that works on one box degrades gracefully on
// another instead of failing at encode time.
func (c *Config) ResolvedCodec(configured string) string {
	switch {
	case configured == "copy":
		return "copy"
	case nvencCodecs[configured]:
		if !c.Encoders.NVENC {
			switch configured {
			case "h264_nvenc", "hevc_nvenc":
				return "libx264"
			case "av1_nvenc":
				if c.hasCPUCodec("libsvtav1") {
					return "libsvtav1"
				}
				return "libx264"
			}
		}
	case cpuCodecs[configured]:
		if !c.hasCPUCodec(configured) {
			return "libx264"
		}
	}
	return configured
}

// VideoEncodeArgs produces the ffmpeg video-encode arguments for one video
// dict (live or record), mirroring v1 config.py video_encode_args but against
// the resolved codec. gpu.encoders of "auto" (or empty) adds no -gpu flag;
// an explicit index selects the GPU. "copy" (stream copy / passthrough, for
// low-RAM SBCs) returns just "-c:v copy" and no rate-control flags.
func VideoEncodeArgs(cfg *Config, v Video) []string {
	codec := cfg.ResolvedCodec(v.Codec)
	args := []string{"-c:v", codec}
	if codec == "copy" {
		return args
	}
	if nvencCodecs[codec] {
		if gpu := cfg.GPU.Encoders; gpu != "" && gpu != "auto" {
			args = append(args, "-gpu", gpu)
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
			"-preset", cpuPreset(codec, v.Preset),
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
