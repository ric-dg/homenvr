// Package config loads, validates and hot-reloads the JSONC configuration.
//
// Mirrors v1's config.py contract: cameras, per-camera video/audio settings,
// recording mode, retention, paths and external tool locations. The schema is
// defined once in this package and is the single source of truth for the CLI,
// control panel and the supervisor.
//
// Loading semantics match v1: the user config (a JSONC file) is merged over
// built-in defaults, missing fields keep their defaults, and each camera entry
// is completed against a per-camera template. Unknown keys are ignored.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Config is the effective, fully-resolved configuration.
type Config struct {
	Go2rtc  Go2rtc   `json:"go2rtc"`
	GPU     GPU      `json:"gpu"`
	Cameras []Camera `json:"cameras"`
	Record  Record   `json:"record"`
	Paths   Paths    `json:"paths"`
	Log     Log      `json:"log"`
	Tools   Tools    `json:"tools"`
	Monitor Monitor  `json:"monitor"`
}

type Go2rtc struct {
	APIPort    int `json:"api_port"`
	RTSPPort   int `json:"rtsp_port"`
	WebRTCPort int `json:"webrtc_port"`
}

type GPU struct {
	Encoders string `json:"encoders"`
}

type Video struct {
	Codec   string `json:"codec"`
	Preset  string `json:"preset"`
	Tune    string `json:"tune,omitempty"`
	RC      string `json:"rc"`
	CQ      int    `json:"cq"`
	CRF     int    `json:"crf"`
	MaxRate string `json:"maxrate"`
	G       int    `json:"g"`
	BF      *int   `json:"bf,omitempty"`
}

type Audio struct {
	Codec   string `json:"codec"`
	Bitrate string `json:"bitrate"`
}

type Live struct {
	Video Video `json:"video"`
	Audio Audio `json:"audio"`
}

type Mic struct {
	Enabled    bool    `json:"enabled"`
	DeviceName string  `json:"device_name"`
	SampleRate int     `json:"sample_rate"`
	Channels   int     `json:"channels"`
	BlockSize  int     `json:"block_size"`
	Gain       float64 `json:"gain"`
	LivePort   int     `json:"live_port"`
	RecPort    int     `json:"rec_port"`
	CtlPort    int     `json:"ctl_port"`
}

type Motion struct {
	Enabled        bool    `json:"enabled"`
	Width          int     `json:"width"`
	Height         int     `json:"height"`
	FPS            int     `json:"fps"`
	Threshold      int     `json:"threshold"`
	MinPixels      int     `json:"min_pixels"`
	BgAlpha        float64 `json:"bg_alpha"`
	PostRollSec    float64 `json:"post_roll_sec"`
	EventCapSec    float64 `json:"event_cap_sec"`
	RestartBackoff float64 `json:"restart_backoff_sec"`
}

type Sound struct {
	Enabled    bool    `json:"enabled"`
	AbsFloor   float64 `json:"abs_floor"`
	Ratio      float64 `json:"ratio"`
	Tau        float64 `json:"tau"`
	HoldBlocks int     `json:"hold_blocks"`
	DropBlocks int     `json:"drop_blocks"`
}

type CamRecord struct {
	Enabled bool  `json:"enabled"`
	Prefix  string `json:"prefix"`
	OutDir  string `json:"out_dir"`
	Video   Video `json:"video"`
	Audio   Audio `json:"audio"`
}

// Camera is one entry of the "cameras" array.
type Camera struct {
	Name       string    `json:"name"`
	Source     string    `json:"source"`
	DeviceName string    `json:"device_name"`
	RTSPURL    string    `json:"rtsp_url"`
	Width      int       `json:"width"`
	Height     int       `json:"height"`
	FPS        int       `json:"fps"`
	RTBufSize  string    `json:"rtbufsize"`
	Live       Live      `json:"live"`
	Mic        Mic       `json:"mic"`
	Motion     Motion    `json:"motion"`
	Sound      Sound     `json:"sound"`
	Record     CamRecord `json:"record"`
}

type Record struct {
	Mode        string `json:"mode"`
	RetainHours int    `json:"retain_hours"`
}

type Paths struct {
	LogDir   string `json:"log_dir"`
	RTSPHost string `json:"rtsp_host"`
}

type Log struct {
	Level string `json:"level"`
	MaxMB int    `json:"max_mb"`
	Keep  int    `json:"keep"`
}

// Tools holds resolved external program locations. Empty means "not found".
type Tools struct {
	FFmpeg string `json:"ffmpeg"`
	Go2rtc string `json:"go2rtc"`
	Python string `json:"python"`
	Pwsh   string `json:"pwsh"`
}

type Monitor struct {
	Enabled       bool   `json:"enabled"`
	IntervalSec   int    `json:"interval_sec"`
	AlertAfterSec int    `json:"alert_after_sec"`
	NtfyTopic     string `json:"ntfy_topic"`
}

// DefaultCamera returns the v1 per-camera template, used to complete any
// partial camera entry.
func DefaultCamera() Camera {
	zero := 0
	return Camera{
		Name: "brio", Source: "dshow", DeviceName: "Brio 100",
		Width: 1920, Height: 1080, FPS: 15, RTBufSize: "64M",
		Live: Live{
			Video: Video{
				Codec: "h264_nvenc", Preset: "p2", Tune: "ll", RC: "vbr",
				CQ: 28, CRF: 24, MaxRate: "4M", G: 15, BF: &zero,
			},
			Audio: Audio{Codec: "aac", Bitrate: "64k"},
		},
		Mic: Mic{
			Enabled: true, DeviceName: "Brio 100",
			SampleRate: 48000, Channels: 1, BlockSize: 960, Gain: 8,
			LivePort: 9010, RecPort: 9011, CtlPort: 9012,
		},
		Motion: Motion{
			Enabled: true, Width: 320, Height: 180, FPS: 10,
			Threshold: 25, MinPixels: 250, BgAlpha: 0.05,
			PostRollSec: 3.0, EventCapSec: 900.0, RestartBackoff: 2.0,
		},
		Sound: Sound{
			Enabled: true, AbsFloor: 250, Ratio: 3.0, Tau: 0.02,
			HoldBlocks: 4, DropBlocks: 30,
		},
		Record: CamRecord{
			Enabled: true, Prefix: "brio", OutDir: "E:\\CCTV",
			Video: Video{
				Codec: "av1_nvenc", Preset: "p5", RC: "vbr",
				CQ: 38, CRF: 34, MaxRate: "3M", G: 30,
			},
			Audio: Audio{Codec: "aac", Bitrate: "64k"},
		},
	}
}

// Defaults returns the v1 built-in defaults before any user config is applied.
func Defaults() Config {
	return Config{
		Go2rtc:  Go2rtc{APIPort: 1984, RTSPPort: 8554, WebRTCPort: 8555},
		GPU:     GPU{Encoders: "auto"},
		Cameras: []Camera{DefaultCamera()},
		Record:  Record{Mode: "event", RetainHours: 72},
		Paths:   Paths{LogDir: "logs", RTSPHost: "127.0.0.1"},
		Log:     Log{Level: "info", MaxMB: 10, Keep: 5},
		Tools:   Tools{},
		Monitor: Monitor{Enabled: true, IntervalSec: 10, AlertAfterSec: 300},
	}
}

// Load reads and merges path (JSONC) over the defaults, completes every camera
// against the template, and resolves tool locations. A missing file yields the
// plain defaults. Parse failures are returned as errors; the config is never
// partially applied.
func Load(path string) (*Config, error) {
	cfg := Defaults()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &cfg, nil
		}
		return nil, err
	}
	if err := unmarshalJSONC(data, &cfg); err != nil {
		return nil, fmt.Errorf("%s parse error: %w", path, err)
	}
	normalizeCameras(&cfg)
	cfg.resolveTools()
	return &cfg, nil
}

func unmarshalJSONC(data []byte, v any) error {
	return json.Unmarshal([]byte(stripComments(string(data))), v)
}

// normalizeCameras replaces the camera slice with fully-completed entries:
// each camera is merged over DefaultCamera, so a bare {"name": "x"} entry
// inherits every template value.
func normalizeCameras(cfg *Config) {
	if len(cfg.Cameras) == 0 {
		cfg.Cameras = []Camera{DefaultCamera()}
	}
	for i := range cfg.Cameras {
		raw, err := json.Marshal(cfg.Cameras[i])
		if err != nil {
			continue
		}
		t := DefaultCamera()
		if err := json.Unmarshal(raw, &t); err != nil {
			continue
		}
		cfg.Cameras[i] = t
	}
}

// Validate reports config problems that would break supervision or recording.
// Load does not call it (matching v1's lenient behavior); use it for the CLI,
// control panel edits and startup sanity checks.
func (c *Config) Validate() error {
	var errs []string
	if len(c.Cameras) == 0 {
		errs = append(errs, "no cameras configured")
	}
	seenNames := map[string]bool{}
	seenPorts := map[int]string{}
	for _, cam := range c.Cameras {
		if cam.Name == "" {
			errs = append(errs, "a camera has an empty name")
		} else if seenNames[cam.Name] {
			errs = append(errs, fmt.Sprintf("duplicate camera name %q", cam.Name))
		}
		seenNames[cam.Name] = true
		if cam.Source != "dshow" && cam.Source != "rtsp" {
			errs = append(errs, fmt.Sprintf("camera %q: source must be \"dshow\" or \"rtsp\"", cam.Name))
		}
		if cam.Source == "dshow" && cam.DeviceName == "" {
			errs = append(errs, fmt.Sprintf("camera %q: device_name is required for dshow", cam.Name))
		}
		if cam.Source == "rtsp" && cam.RTSPURL == "" {
			errs = append(errs, fmt.Sprintf("camera %q: rtsp_url is required for rtsp", cam.Name))
		}
		if cam.Mic.Enabled {
			for _, p := range []int{cam.Mic.LivePort, cam.Mic.RecPort, cam.Mic.CtlPort} {
				if p <= 0 {
					errs = append(errs, fmt.Sprintf("camera %q: mic ports must be positive", cam.Name))
					continue
				}
				if prev, ok := seenPorts[p]; ok {
					errs = append(errs, fmt.Sprintf("mic port %d reused by %q and %q", p, prev, cam.Name))
				} else {
					seenPorts[p] = cam.Name
				}
			}
		}
	}
	switch c.Record.Mode {
	case "event", "continuous", "combined":
	default:
		errs = append(errs, fmt.Sprintf("record.mode %q is invalid (want event|continuous|combined)", c.Record.Mode))
	}
	sort.Strings(errs)
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}
