package motion

import (
	"testing"

	"github.com/ric-dg/homenvr/internal/config"
)

func TestAnalyzeMotionDetection(t *testing.T) {
	d := &Detector{w: 12, h: 12}
	m := config.Motion{Enabled: true, Threshold: 25, MinPixels: 4, BgAlpha: 0.05}

	seed := make([]byte, 12*12)
	if d.Analyze(seed, m) {
		t.Fatal("seed frame should initialize bg, not report motion")
	}

	// Unchanged frame -> no motion.
	if d.Analyze(seed, m) {
		t.Fatal("identical frame reported motion")
	}

	// A 3x3 bright block in the interior survives the morphological open.
	block := make([]byte, 12*12)
	for y := 4; y <= 6; y++ {
		for x := 4; x <= 6; x++ {
			block[y*12+x] = 200
		}
	}
	if !d.Analyze(block, m) {
		t.Fatal("3x3 block should trigger motion")
	}

	// A fresh detector with four isolated pixels: erosion removes them all.
	d2 := &Detector{w: 12, h: 12}
	d2.Analyze(seed, m)
	scatter := make([]byte, 12*12)
	for _, p := range []struct{ x, y int }{{3, 3}, {3, 8}, {8, 3}, {8, 8}} {
		scatter[p.y*12+p.x] = 200
	}
	if d2.Analyze(scatter, m) {
		t.Fatal("isolated pixels should be removed by the open")
	}
}

func TestAnalyzeConvergesOnStaticScene(t *testing.T) {
	d := &Detector{w: 12, h: 12}
	m := config.Motion{Enabled: true, Threshold: 25, MinPixels: 1, BgAlpha: 0.2}
	frame := make([]byte, 12*12)
	for y := 4; y <= 6; y++ {
		for x := 4; x <= 6; x++ {
			frame[y*12+x] = 200
		}
	}
	// First sighting triggers; the background adapts every frame so a static
	// scene eventually stops counting as motion.
	seed := make([]byte, 12*12)
	d.Analyze(seed, m) // seed the background on a blank scene
	if !d.Analyze(frame, m) {
		t.Fatal("first sighting should trigger")
	}
	converged := false
	for i := 0; i < 100 && !converged; i++ {
		converged = !d.Analyze(frame, m)
	}
	if !converged {
		t.Fatal("static scene never converged into the background")
	}
}

func TestSoundTrackerStateMachine(t *testing.T) {
	s := config.DefaultCamera().Sound // hold 4, drop 30, ratio 3.0, abs_floor 250, tau 0.02
	trk := NewSoundTracker()

	// A quiet baseline lets the noise floor (initially 300) adapt down to the
	// ambient level, and never triggers.
	for i := 0; i < 200; i++ {
		if trk.Update(100, s) {
			t.Fatal("quiet baseline triggered")
		}
	}
	if trk.hold != 0 {
		t.Fatalf("quiet baseline accumulated hold = %d", trk.hold)
	}

	// A loud burst jumps the RMS well above max(floor*ratio, abs_floor) and
	// builds hold over 4 blocks (v1 semantics: the floor freezes while
	// triggered, so the burst stays above threshold).
	var trig bool
	for i := 0; i < 4; i++ {
		trig = trk.Update(1000, s)
	}
	if !trig {
		t.Fatalf("burst did not trigger (hold=%d)", trk.hold)
	}

	// Once triggered, drop counts down from 30 while the signal continues.
	for i := 0; i < s.DropBlocks-1; i++ {
		if !trk.Update(1000, s) {
			t.Fatalf("released early at drop=%d", trk.drop)
		}
	}
	if trk.Update(1000, s) {
		t.Fatal("not released after drop_blocks")
	}
}

func TestSoundTrackerFloorFrozenWhileTriggered(t *testing.T) {
	// v1 only adapts the noise floor in the untriggered branch; while
	// triggered, floor must stay put (so a burst above threshold stays
	// triggered for the full drop period).
	s := config.Sound{Enabled: true, AbsFloor: 0, Ratio: 3.0, Tau: 0.02, HoldBlocks: 2, DropBlocks: 5}
	trk := NewSoundTracker()
	for i := 0; i < 2; i++ {
		trk.Update(1000, s)
	}
	floor := trk.noiseFloor
	for i := 0; i < s.DropBlocks-1; i++ {
		trk.Update(1000, s)
	}
	if trk.noiseFloor != floor {
		t.Errorf("noise floor changed while triggered: %v -> %v", floor, trk.noiseFloor)
	}
	if trk.Update(1000, s) {
		t.Fatal("should have released after drop_blocks")
	}
}

func TestSoundTrackerAbsFloor(t *testing.T) {
	s := config.Sound{Enabled: true, AbsFloor: 250, Ratio: 1.2, Tau: 0.02, HoldBlocks: 2, DropBlocks: 3}
	trk := NewSoundTracker()
	// Floor starts at 300 with ratio 1.2 -> threshold 360; quiet signal at 40
	// never reaches the threshold, so it can never trigger.
	for i := 0; i < 20; i++ {
		if trk.Update(40, s) {
			t.Fatal("quiet signal triggered")
		}
	}
	// But a signal above the noise floor by the ratio does trigger.
	var trig bool
	for i := 0; i < 2; i++ {
		trig = trk.Update(1000, s)
	}
	if !trig {
		t.Fatal("strong signal should trigger")
	}
}
