//go:build windows

package mic

import (
	"encoding/binary"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/ric-dg/homenvr/internal/config"
)

// TestKSCapture opens the configured mic through the native WDM-KS backend,
// reads a few blocks and verifies they carry plausible audio (not constant
// silence). Enabled only when MIC_KS_TEST=1 and a KS audio interface matching
// MIC_KS_DEVICE (default "Brio 100") exists, so it never runs on CI or
// foreign machines.
func TestKSCapture(t *testing.T) {
	if os.Getenv("MIC_KS_TEST") == "" {
		t.Skip("set MIC_KS_TEST=1 to run")
	}
	device := os.Getenv("MIC_KS_DEVICE")
	if device == "" {
		device = "Brio 100"
	}
	mic := config.Mic{
		DeviceName: device, SampleRate: 48000, Channels: 1,
		BlockSize: 960, LivePort: 0, RecPort: 0, CtlPort: 0,
	}
	src, err := newKs("test", testLogger{t}, mic)
	if err != nil {
		if strings.Contains(err.Error(), "no KS audio device matches") {
			t.Skipf("device %q not present: %v", device, enumerateKsDevices())
		}
		t.Fatalf("newKs: %v", err)
	}
	defer src.close()

	block := make([]byte, 960*2)
	var peak float64
	for i := 0; i < 40; i++ {
		if err := src.readBlock(block); err != nil {
			t.Fatalf("readBlock: %v", err)
		}
		var sum float64
		for j := 0; j < len(block)/2; j++ {
			v := float64(int16(binary.LittleEndian.Uint16(block[j*2:])))
			sum += v * v
			if math.Abs(v) > peak {
				peak = math.Abs(v)
			}
		}
		if sum/float64(len(block)/2) > 100 {
			t.Logf("block %d: RMS^2=%.0f peak=%.0f (audio present)", i, sum/float64(len(block)/2), peak)
			return
		}
	}
	t.Fatalf("captured 40 blocks of silence (peak=%.0f); gain or device wrong?", peak)
}
