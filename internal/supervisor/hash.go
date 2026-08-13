package supervisor

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"

	"github.com/ric-dg/homenvr/internal/config"
)

// GenHash fingerprints the config subset that affects the go2rtc.yaml file and
// go2rtc's own process: go2rtc ports, gpu encoders, each camera's capture and
// live-encode settings plus mic ports, and the ffmpeg/go2rtc tool paths.
// It mirrors v1 run.ps1 Get-GenHash: a different hash means the yaml must be
// regenerated and go2rtc restarted. Changes outside this set (motion, sound,
// record, log, monitor, mic gain) never restart go2rtc.
func GenHash(cfg *config.Config) string {
	type mic struct {
		Enabled    bool   `json:"enabled"`
		DeviceName string `json:"device_name"`
		SampleRate int    `json:"sample_rate"`
		Channels   int    `json:"channels"`
		BlockSize  int    `json:"block_size"`
		LivePort   int    `json:"live_port"`
		RecPort    int    `json:"rec_port"`
		CtlPort    int    `json:"ctl_port"`
	}
	type cam struct {
		Name       string      `json:"name"`
		Source     string      `json:"source"`
		DeviceName string      `json:"device_name"`
		RTSPURL    string      `json:"rtsp_url"`
		Width      int         `json:"width"`
		Height     int         `json:"height"`
		FPS        int         `json:"fps"`
		RTBufSize  string      `json:"rtbufsize"`
		Live       config.Live `json:"live"`
		Mic        mic         `json:"mic"`
	}
	rel := struct {
		Go2rtc  config.Go2rtc `json:"go2rtc"`
		GPU     config.GPU    `json:"gpu"`
		Cameras []cam         `json:"cameras"`
		Tools   struct {
			FFmpeg string `json:"ffmpeg"`
			Go2rtc string `json:"go2rtc"`
		} `json:"tools"`
	}{
		Go2rtc: cfg.Go2rtc,
		GPU:    cfg.GPU,
		Tools: struct {
			FFmpeg string `json:"ffmpeg"`
			Go2rtc string `json:"go2rtc"`
		}{FFmpeg: cfg.Tools.FFmpeg, Go2rtc: cfg.Tools.Go2rtc},
	}
	for _, c := range cfg.Cameras {
		rel.Cameras = append(rel.Cameras, cam{
			Name: c.Name, Source: c.Source, DeviceName: c.DeviceName,
			RTSPURL: c.RTSPURL, Width: c.Width, Height: c.Height, FPS: c.FPS,
			RTBufSize: c.RTBufSize, Live: c.Live,
			Mic: mic{
				Enabled: c.Mic.Enabled, DeviceName: c.Mic.DeviceName,
				SampleRate: c.Mic.SampleRate, Channels: c.Mic.Channels,
				BlockSize: c.Mic.BlockSize, LivePort: c.Mic.LivePort,
				RecPort: c.Mic.RecPort, CtlPort: c.Mic.CtlPort,
			},
		})
	}
	sum := md5.Sum(mustJSON(rel))
	return hex.EncodeToString(sum[:])
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
