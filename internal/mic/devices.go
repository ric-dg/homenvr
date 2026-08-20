package mic

import (
	"os/exec"
	"runtime"
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
