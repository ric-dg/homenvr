package mic

import (
	"testing"

	"github.com/ric-dg/homenvr/internal/config"
)

func TestApplyGainAndClip(t *testing.T) {
	in := []int16{0, 1000, 30000, -30000, -32768}
	got := applyGain(in, 8.0)
	want := []int16{0, 8000, 32767, -32768, -32768}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("applyGain(%v)[%d] = %d, want %d", in, i, got[i], want[i])
		}
	}
	// Fractional gain rounds like numpy's cast to int16 (truncation).
	if g := applyGain([]int16{100}, 1.9); g[0] != 190 {
		t.Errorf("applyGain 1.9 = %d, want 190", g[0])
	}
}

func TestBlockRMS(t *testing.T) {
	if r := blockRMS([]int16{0, 0, 0}); r != 0 {
		t.Errorf("rms(silence) = %d, want 0", r)
	}
	if r := blockRMS([]int16{1000, -1000, 1000, -1000}); r != 1000 {
		t.Errorf("rms(±1000) = %d, want 1000", r)
	}
	// Full-scale square wave hits ~32767.
	if r := blockRMS([]int16{32767, 32767, 32767, 32767}); r != 32767 {
		t.Errorf("rms(full scale) = %d, want 32767", r)
	}
}

func TestMicKey(t *testing.T) {
	m := config.Mic{Enabled: true, DeviceName: "Mic", SampleRate: 48000, Channels: 1,
		BlockSize: 960, LivePort: 9010, RecPort: 9011, CtlPort: 9012, Gain: 8}
	base := micKey("ffmpeg.exe", m)

	// Gain hot-reloads per block and must NOT restart the capture.
	m.Gain = 40
	if micKey("ffmpeg.exe", m) != base {
		t.Error("gain change must not change the capture key")
	}

	// Structural changes restart the capture.
	for name, mutate := range map[string]func(){
		"device":    func() { m.DeviceName = "Other" },
		"rate":      func() { m.SampleRate = 44100 },
		"channels":  func() { m.Channels = 2 },
		"block":     func() { m.BlockSize = 480 },
		"live port": func() { m.LivePort = 9000 },
		"rec port":  func() { m.RecPort = 9001 },
		"ctl port":  func() { m.CtlPort = 9002 },
		"disabled":  func() { m.Enabled = false },
	} {
		saved := m
		mutate()
		if micKey("ffmpeg.exe", m) == base {
			t.Errorf("%s change did not change the key", name)
		}
		m = saved
	}

	if micKey("ffmpegA", m) == base {
		t.Error("ffmpeg binary change did not change the key")
	}
}
