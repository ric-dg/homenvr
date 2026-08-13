// Package motion provides the two activity gates for event recording:
// a background-subtraction motion detector fed by an ffmpeg rawvideo pipe, and
// a sound trigger fed by the mic feeder's per-block RMS (ctl port). Both
// mirror motion.py: weighted background accumulation, pixel-diff threshold,
// 3x3 elliptical open, then a min-pixel count; and the noise-floor/ratio/hold/
// drop RMS state machine.
package motion

import (
	"fmt"

	"github.com/ric-dg/homenvr/internal/config"
	"github.com/ric-dg/homenvr/internal/proc"
)

// Logger is the subset of logging the detector needs.
type Logger interface {
	Logf(format string, args ...any)
}

// Detector pulls low-res grayscale frames from an ffmpeg rawvideo pipe and
// computes the motion gate. The background model adapts every frame
// (cv2.accumulateWeighted), so it must be warmed up (the first frame only
// initializes it, as in v1).
type Detector struct {
	cfg    *config.File
	name   string
	log    Logger
	ffmpeg string
	logDir string

	proc *proc.Child
	bg   []float32
	th   []bool
	er   []bool
	w, h int
}

// NewDetector creates a detector for camera name. ffmpeg may be "" to fall
// back to PATH.
func NewDetector(cfg *config.File, name string, log Logger, ffmpeg, logDir string) *Detector {
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	return &Detector{cfg: cfg, name: name, log: log, ffmpeg: ffmpeg, logDir: logDir}
}

// Start launches the rawvideo pipe at width/height/fps (v1 spawn_detector),
// resetting the background model. Any previous instance is stopped first.
func (d *Detector) Start(w, h, fps int) error {
	d.Close()
	d.w, d.h = w, h
	d.bg = nil
	cfg := d.cfg.Get()
	cam := cfg.Camera(d.name)
	if cam == nil {
		return fmt.Errorf("camera %q not in config", d.name)
	}
	argv := []string{
		d.ffmpeg, "-hide_banner", "-loglevel", "error",
		"-timeout", "8000000",
		"-rtsp_transport", "tcp", "-rtsp_flags", "prefer_tcp",
		"-i", cfg.CameraURL(*cam),
		"-an",
		"-vf", fmt.Sprintf("scale=%d:%d,fps=%d,format=gray", w, h, fps),
		"-f", "rawvideo", "-pix_fmt", "gray", "pipe:1",
	}
	ch := proc.NewChild("detector-"+d.name, d.logDir)
	if err := ch.StartOpt(argv, proc.Options{Stdout: proc.StdoutPipe}); err != nil {
		return err
	}
	d.proc = ch
	return nil
}

// ReadFrame reads one w*h grayscale frame with a single read (mirroring v1's
// detector.stdout.read(frame_bytes)); a short read is an error so the caller
// restarts the pipe, which is how v1 respawned on detector res/fps changes.
func (d *Detector) ReadFrame() ([]byte, error) {
	if d.proc == nil {
		return nil, fmt.Errorf("detector not started")
	}
	buf := make([]byte, d.w*d.h)
	n, err := d.proc.ReadStdout(buf)
	if err != nil {
		return nil, err
	}
	if n != len(buf) {
		return nil, fmt.Errorf("detector short read: got %d of %d bytes", n, len(buf))
	}
	return buf, nil
}

// Analyze updates the background model with frame and reports whether the
// frame counts as motion. The first frame only seeds the background.
func (d *Detector) Analyze(frame []byte, m config.Motion) bool {
	n := d.w * d.h
	if len(d.bg) == 0 {
		d.bg = make([]float32, n)
		for i, b := range frame {
			d.bg[i] = float32(b)
		}
		return false
	}
	if d.th == nil || len(d.th) != n {
		d.th = make([]bool, n)
		d.er = make([]bool, n)
	}
	bg, th, er := d.bg, d.th, d.er
	alpha := float32(m.BgAlpha)
	oneMinus := 1 - alpha
	thr := float32(m.Threshold)

	for i, b := range frame {
		v := oneMinus*bg[i] + alpha*float32(b)
		bg[i] = v
		df := float32(b) - v
		if df < 0 {
			df = -df
		}
		th[i] = df >= thr
	}

	// Morphological open (erode then dilate) with a 3x3 ellipse kernel
	// [0 1 0; 1 1 1; 0 1 0], interior pixels only (border effects are
	// irrelevant at this resolution).
	w, h := d.w, d.h
	for y := 1; y < h-1; y++ {
		row := y * w
		for x := 1; x < w-1; x++ {
			i := row + x
			er[i] = th[i] && th[i-w] && th[i+w] && th[i-1] && th[i+1]
		}
	}
	count := 0
	for y := 1; y < h-1; y++ {
		row := y * w
		for x := 1; x < w-1; x++ {
			i := row + x
			if er[i] || er[i-w] || er[i+w] || er[i-1] || er[i+1] {
				count++
			}
		}
	}
	return count >= m.MinPixels
}

// Close stops the detector pipe (idempotent).
func (d *Detector) Close() {
	if d.proc != nil {
		d.proc.Stop()
		d.proc = nil
	}
}
