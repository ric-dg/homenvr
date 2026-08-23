package mic

import (
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// AudioDevice is a detected audio input device.
type AudioDevice struct {
	Name   string `json:"name"`
	Source string `json:"source"` // "wasapi" or "ks"
}

// ListAudioDevices returns all active audio capture devices on the system.
// On Windows it enumerates WASAPI endpoints; other platforms return an empty
// list (ffmpeg dshow enumeration is done separately via ListVideoDevices).
func ListAudioDevices() []AudioDevice {
	if runtime.GOOS != "windows" {
		return nil
	}
	return listWasapiDevices()
}

// VideoDevice is a detected video capture device.
type VideoDevice struct {
	Name string `json:"name"`
}

// ListVideoDevices runs ffmpeg -list_devices to enumerate DirectShow video
// sources. Returns an empty slice on failure.
func ListVideoDevices() []VideoDevice {
	ff := "ffmpeg"
	if v, err := exec.LookPath("ffmpeg"); err == nil {
		ff = v
	}
	out, err := exec.Command(ff, "-hide_banner", "-list_devices", "true", "-f", "dshow", "-i", "dummy").CombinedOutput()
	if err != nil {
		return nil
	}
	return parseDshowDevices(string(out))
}

// extractQuoted returns the text between the first and last double quotes, or "".
func extractQuoted(s string) string {
	start := strings.Index(s, `"`)
	end := strings.LastIndex(s, `"`)
	if start < end && start >= 0 {
		return s[start+1 : end]
	}
	return ""
}

// parseDshowDevices extracts video device names from ffmpeg's listing.
// Handles two formats:
//
//	New:  [in#0 @ ...] "Name" (video)
//	Old:  [dshow @ ...] DirectShow video devices
//	      [dshow @ ...]  "USB Video Device"
func parseDshowDevices(output string) []VideoDevice {
	var devs []VideoDevice
	seen := map[string]bool{}
	inVideo := false
	for _, line := range strings.Split(output, "\n") {
		low := strings.ToLower(line)
		// New ffmpeg format: device lines have type tag like (video) or (audio)
		if strings.Contains(line, `"`) && strings.Contains(low, "(video)") {
			name := extractQuoted(line)
			if name != "" && !strings.HasPrefix(name, "@device_") && !seen[name] {
				seen[name] = true
				devs = append(devs, VideoDevice{Name: name})
			}
			continue
		}
		// Old ffmpeg format: track DirectShow section headers
		if strings.Contains(low, "directshow video devices") {
			inVideo = true
			continue
		}
		if strings.Contains(low, "directshow audio devices") {
			inVideo = false
			continue
		}
		if inVideo && strings.Contains(line, `"`) {
			name := extractQuoted(line)
			if name != "" && !strings.HasPrefix(name, "@device_") && !seen[name] {
				seen[name] = true
				devs = append(devs, VideoDevice{Name: name})
			}
		}
	}
	return devs
}

// DeviceFormat reports one resolution/FPS combination a DirectShow device supports.
type DeviceFormat struct {
	Width  int     `json:"width"`
	Height int     `json:"height"`
	MinFPS float64 `json:"min_fps"`
	MaxFPS float64 `json:"max_fps"`
}

// dshowOptionRe matches lines like:
//
//	pixel_format=yuyv422  min s=1280x720 fps=30 max s=1280x720 fps=30
var dshowOptionRe = regexp.MustCompile(
	`min\s+s=(\d+)x(\d+)\s+fps=([\d.]+)\s+max\s+s=(\d+)x(\d+)\s+fps=([\d.]+)`)

// ListDeviceFormats queries ffmpeg for the resolutions and FPS ranges a
// DirectShow video device supports. Returns nil on failure or non-Windows.
func ListDeviceFormats(device string) []DeviceFormat {
	if runtime.GOOS != "windows" || device == "" {
		return nil
	}
	ff := "ffmpeg"
	if v, err := exec.LookPath("ffmpeg"); err == nil {
		ff = v
	}
	out, err := exec.Command(ff, "-hide_banner", "-f", "dshow",
		"-list_options", "true", "-i", "video="+device).CombinedOutput()
	if err != nil {
		return nil
	}
	return parseDshowFormats(string(out))
}

func parseDshowFormats(output string) []DeviceFormat {
	seen := map[string]bool{}
	var formats []DeviceFormat
	for _, line := range strings.Split(output, "\n") {
		m := dshowOptionRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		w1, _ := strconv.Atoi(m[1])
		h1, _ := strconv.Atoi(m[2])
		minFPS, _ := strconv.ParseFloat(m[3], 64)
		w2, _ := strconv.Atoi(m[4])
		h2, _ := strconv.Atoi(m[5])
		maxFPS, _ := strconv.ParseFloat(m[6], 64)
		if w1 != w2 || h1 != h2 {
			continue //跨分辨率行跳过（不应出现）
		}
		key := strconv.Itoa(w1) + "x" + strconv.Itoa(h1)
		if seen[key] {
			// 同分辨率取最大FPS范围
			for i := range formats {
				if formats[i].Width == w1 && formats[i].Height == h1 {
					if minFPS < formats[i].MinFPS {
						formats[i].MinFPS = minFPS
					}
					if maxFPS > formats[i].MaxFPS {
						formats[i].MaxFPS = maxFPS
					}
					break
				}
			}
			continue
		}
		seen[key] = true
		formats = append(formats, DeviceFormat{
			Width: w1, Height: h1, MinFPS: minFPS, MaxFPS: maxFPS,
		})
	}
	return formats
}
